package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// discoveredBlock is a top-level block found during interactive scanning.
type discoveredBlock struct {
	Path   string
	Type   string
	Labels []string
	Source string
}

func (b discoveredBlock) display() string {
	if len(b.Labels) == 0 {
		return b.Type
	}
	return b.Type + "." + strings.Join(b.Labels, ".")
}

func (b discoveredBlock) signature() string {
	parts := append([]string{b.Type}, b.Labels...)
	return strings.Join(parts, "\x00")
}

// matchKey uniquely identifies a top-level block within a file. Used by
// interactive mode to feed an explicit selection set into the operation
// pipeline.
type matchKey struct {
	Path      string
	Signature string
}

func collectBlocks(paths []string) ([]discoveredBlock, error) {
	var all []discoveredBlock
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		file, diags := hclwrite.ParseConfig(src, path, hcl.InitialPos)
		if diags.HasErrors() {
			return nil, fmt.Errorf("parse %s: %s", path, diags.Error())
		}
		for _, blk := range file.Body().Blocks() {
			var buf bytes.Buffer
			blk.BuildTokens(nil).WriteTo(&buf)

			all = append(all, discoveredBlock{
				Path:   path,
				Type:   blk.Type(),
				Labels: blk.Labels(),
				Source: strings.TrimSpace(buf.String()),
			})
		}
	}
	return all, nil
}

// Styles — lipgloss strips ANSI when stdout isn't a TTY, so these are safe.
var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF5FAF"))
	subtitleStyle = lipgloss.NewStyle().Faint(true)
	cursorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF87D7")).Bold(true)
	checkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#5FFF87")).Bold(true)
	typeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#5FAFFF")).Bold(true)
	labelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD787"))
	pathStyle     = lipgloss.NewStyle().Faint(true).Italic(true)
	helpStyle     = lipgloss.NewStyle().Faint(true)
	matchStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5FAF")).Bold(true)
	emptyStyle    = lipgloss.NewStyle().Faint(true).Italic(true).Padding(1, 2)
	panelStyle    = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#5F5F87")).
			Padding(0, 1)
	previewStyle = panelStyle.
			BorderForeground(lipgloss.Color("#3A3A5C"))
	opBadge = map[string]lipgloss.Style{
		"sort":   lipgloss.NewStyle().Background(lipgloss.Color("#5FAFFF")).Foreground(lipgloss.Color("#000000")).Padding(0, 1).Bold(true),
		"remove": lipgloss.NewStyle().Background(lipgloss.Color("#FF5F5F")).Foreground(lipgloss.Color("#000000")).Padding(0, 1).Bold(true),
		"move":   lipgloss.NewStyle().Background(lipgloss.Color("#FFD787")).Foreground(lipgloss.Color("#000000")).Padding(0, 1).Bold(true),
		"list":   lipgloss.NewStyle().Background(lipgloss.Color("#5FFF87")).Foreground(lipgloss.Color("#000000")).Padding(0, 1).Bold(true),
	}
)

type tuiModel struct {
	blocks    []discoveredBlock
	filtered  []int
	selected  map[int]bool
	cursor    int
	filter    textinput.Model
	width     int
	height    int
	operation string
	confirmed bool
	cancelled bool
}

func newTUIModel(blocks []discoveredBlock, op string) tuiModel {
	ti := textinput.New()
	ti.Placeholder = "iam, google_storage_*, module.network, data.google_*"
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.Focus()

	m := tuiModel{
		blocks:    blocks,
		selected:  make(map[int]bool),
		filter:    ti,
		operation: op,
		width:     100,
		height:    30,
	}
	m.recomputeFilter()
	return m
}

// recomputeFilter rebuilds the filtered index list. The filter matches if
// every whitespace-separated token appears (case-insensitively) somewhere in
// the block's `type.label1.label2` string or its file path.
func (m *tuiModel) recomputeFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	tokens := strings.Fields(q)

	m.filtered = m.filtered[:0]
	for i, b := range m.blocks {
		hay := strings.ToLower(b.display() + " " + b.Path)
		ok := true
		for _, t := range tokens {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m tuiModel) Init() tea.Cmd { return textinput.Blink }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			m.confirmed = true
			return m, tea.Quit
		case "up", "ctrl+p":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "ctrl+n":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
			return m, nil
		case "pgup":
			m.cursor -= 10
			if m.cursor < 0 {
				m.cursor = 0
			}
			return m, nil
		case "pgdown":
			m.cursor += 10
			if m.cursor >= len(m.filtered) {
				m.cursor = len(m.filtered) - 1
			}
			return m, nil
		case "home":
			m.cursor = 0
			return m, nil
		case "end":
			m.cursor = len(m.filtered) - 1
			return m, nil
		case "tab":
			if m.cursor < len(m.filtered) {
				idx := m.filtered[m.cursor]
				if m.selected[idx] {
					delete(m.selected, idx)
				} else {
					m.selected[idx] = true
				}
				if m.cursor < len(m.filtered)-1 {
					m.cursor++
				}
			}
			return m, nil
		case "ctrl+a":
			allSel := len(m.filtered) > 0
			for _, idx := range m.filtered {
				if !m.selected[idx] {
					allSel = false
					break
				}
			}
			for _, idx := range m.filtered {
				if allSel {
					delete(m.selected, idx)
				} else {
					m.selected[idx] = true
				}
			}
			return m, nil
		case "ctrl+x":
			m.selected = make(map[int]bool)
			return m, nil
		}
	}

	var cmd tea.Cmd
	prev := m.filter.Value()
	m.filter, cmd = m.filter.Update(msg)
	if m.filter.Value() != prev {
		m.recomputeFilter()
	}
	return m, cmd
}

func (m tuiModel) View() string {
	var b strings.Builder

	// Header
	badge, ok := opBadge[m.operation]
	if !ok {
		badge = lipgloss.NewStyle().Background(lipgloss.Color("#5F5F87")).Foreground(lipgloss.Color("#FFFFFF")).Padding(0, 1).Bold(true)
	}
	header := lipgloss.JoinHorizontal(
		lipgloss.Center,
		titleStyle.Render("tfhcl"),
		"  ",
		badge.Render(strings.ToUpper(m.operation)),
		"  ",
		subtitleStyle.Render(fmt.Sprintf(
			"%d blocks  •  %s matched  •  %s selected",
			len(m.blocks),
			matchStyle.Render(fmt.Sprintf("%d", len(m.filtered))),
			checkStyle.Render(fmt.Sprintf("%d", len(m.selected))),
		)),
	)
	b.WriteString(header)
	b.WriteString("\n\n")

	filterLabel := lipgloss.NewStyle().Foreground(lipgloss.Color("#5FAFFF")).Bold(true).Render("  filter ▸ ")
	b.WriteString(filterLabel + m.filter.View())
	b.WriteString("\n\n")

	// Layout: left list / right preview.
	chrome := 4 // borders + padding
	listWidth := m.width / 2
	if listWidth < 40 {
		listWidth = m.width - chrome
	}
	previewWidth := m.width - listWidth - chrome
	if previewWidth < 30 {
		previewWidth = 0
	}

	bodyHeight := m.height - 8
	if bodyHeight < 8 {
		bodyHeight = 8
	}

	// Build visible window of the list.
	visibleStart := 0
	rows := bodyHeight
	if m.cursor >= rows {
		visibleStart = m.cursor - rows + 1
	}
	visibleEnd := visibleStart + rows
	if visibleEnd > len(m.filtered) {
		visibleEnd = len(m.filtered)
	}

	var listLines []string
	for i := visibleStart; i < visibleEnd; i++ {
		idx := m.filtered[i]
		blk := m.blocks[idx]

		check := "[ ]"
		if m.selected[idx] {
			check = checkStyle.Render("[x]")
		}
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		labelPart := ""
		if len(blk.Labels) > 0 {
			labelPart = " " + labelStyle.Render(strings.Join(blk.Labels, "."))
		}

		line := fmt.Sprintf("%s%s %s%s", cursor, check, typeStyle.Render(blk.Type), labelPart)
		listLines = append(listLines, line+"  "+pathStyle.Render(blk.Path))
	}

	listContent := strings.Join(listLines, "\n")
	if len(listLines) == 0 {
		listContent = emptyStyle.Render("(no matches — refine your filter or press esc)")
	}
	listPanel := panelStyle.Width(listWidth).Height(bodyHeight).Render(listContent)

	// Preview pane
	var previewBody string
	if m.cursor < len(m.filtered) {
		blk := m.blocks[m.filtered[m.cursor]]
		previewBody = pathStyle.Render(blk.Path) + "\n\n" + blk.Source
	}

	if previewWidth > 0 {
		previewPanel := previewStyle.Width(previewWidth).Height(bodyHeight).Render(previewBody)
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, listPanel, " ", previewPanel))
	} else {
		b.WriteString(listPanel)
	}
	b.WriteString("\n\n")

	help := strings.Join([]string{
		"↑/↓ move",
		"pgup/pgdn jump",
		"tab toggle",
		"^a all",
		"^x clear",
		"enter confirm",
		"esc cancel",
	}, "  ·  ")
	b.WriteString(helpStyle.Render(help))

	return b.String()
}

// runInteractive presents the TUI selector and returns the selected blocks.
// Returns nil with no error when the user cancels or selects nothing.
func runInteractive(cfg Config, paths []string) ([]discoveredBlock, error) {
	blocks, err := collectBlocks(paths)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		fmt.Println("No top-level blocks found.")
		return nil, nil
	}

	m := newTUIModel(blocks, cfg.Operation)
	p := tea.NewProgram(m, tea.WithAltScreen())

	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	fm := final.(tuiModel)
	if fm.cancelled || !fm.confirmed {
		return nil, nil
	}

	out := make([]discoveredBlock, 0, len(fm.selected))
	for idx := range fm.selected {
		out = append(out, blocks[idx])
	}
	return out, nil
}
