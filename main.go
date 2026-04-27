package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// stringSlice is a flag.Value that accumulates repeated --select flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// Selector is a parsed --select expression.
//
//	resource.google_project_iam_member.*
//	module.network
//	data.google_iam_policy.admin
type Selector struct {
	BlockType string // resource, module, data, variable, …
	Label1    string // resource type / module name / data source type
	Label2    string // resource name / data source name (optional)
}

func parseSelector(s string) (Selector, error) {
	// Strip surrounding quotes that users may copy from shell examples.
	s = strings.Trim(s, "'\"")
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 1 || parts[0] == "" {
		return Selector{}, fmt.Errorf("invalid selector %q: must be <block>[.<label1>[.<label2>]]", s)
	}
	sel := Selector{BlockType: parts[0]}
	if len(parts) >= 2 {
		sel.Label1 = parts[1]
	}
	if len(parts) >= 3 {
		sel.Label2 = parts[2]
	}
	return sel, nil
}

type Config struct {
	Root          string
	Operation     string
	Recursive     bool
	InPlace       bool
	DryRun        bool
	Backup        bool
	Output        string
	Selects       stringSlice
	MatchBlock    string
	MatchResType  string
	MatchResName  string
	ExcludeDirs   string
	IncludeHidden bool
	Plan          bool
	Interactive   bool

	// matchSet, when non-nil, restricts matching to exactly these (path, block)
	// pairs. Populated by interactive mode.
	matchSet map[matchKey]bool
}

type BlockInfo struct {
	Type   string
	Labels []string
}

func (b BlockInfo) String() string {
	if len(b.Labels) == 0 {
		return b.Type
	}
	return b.Type + "." + strings.Join(b.Labels, ".")
}

type FileChange struct {
	Path        string
	Changed     bool
	Removed     int
	Sorted      bool
	Blocks      []BlockInfo
	BeforeBytes []byte
	AfterBytes  []byte
}

func main() {
	cfg := parseFlags()

	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() Config {
	var cfg Config

	flag.StringVar(&cfg.Root, "root", ".", "Root directory or single .tf file to process")
	flag.StringVar(&cfg.Operation, "op", "sort", "Operation: sort, remove, move")
	flag.BoolVar(&cfg.Recursive, "recursive", true, "Recursively scan directories")
	flag.BoolVar(&cfg.InPlace, "in-place", false, "Write changes back to files")
	flag.BoolVar(&cfg.DryRun, "dry-run", true, "Show what would change without writing")
	flag.BoolVar(&cfg.Backup, "backup", true, "Create .bak files before overwriting")
	flag.StringVar(&cfg.Output, "out", "", "Output file for move operation")
	flag.Var(&cfg.Selects, "select", "Selector expression (repeatable): resource.google_project_iam_member.*, module.network, data.google_iam_policy.admin")
	flag.StringVar(&cfg.MatchBlock, "block", "", "Top-level block type to match, e.g. resource, module, data, variable")
	flag.StringVar(&cfg.MatchResType, "resource-type", "", "Terraform resource type glob, e.g. google_project_iam_*")
	flag.StringVar(&cfg.MatchResName, "resource-name", "", "Terraform resource name glob, e.g. my_service_account")
	flag.StringVar(&cfg.ExcludeDirs, "exclude-dirs", ".git,.terraform", "Comma-separated directory names to skip")
	flag.BoolVar(&cfg.IncludeHidden, "include-hidden", false, "Include hidden files/directories")
	flag.BoolVar(&cfg.Plan, "plan", false, "Emit a markdown change report instead of the default terse output")
	flag.BoolVar(&cfg.Interactive, "interactive", false, "Pick blocks via an interactive TUI (alias: -i)")
	flag.BoolVar(&cfg.Interactive, "i", false, "Shorthand for --interactive")

	flag.Parse()

	return cfg
}

func run(cfg Config) error {
	cfg.Operation = strings.ToLower(strings.TrimSpace(cfg.Operation))

	switch cfg.Operation {
	case "sort", "remove", "move", "list":
	default:
		return fmt.Errorf("unsupported operation %q; use sort, remove, move, or list", cfg.Operation)
	}

	if cfg.Operation == "remove" || cfg.Operation == "move" {
		// When using legacy flags without --select, default block type to "resource".
		if len(cfg.Selects) == 0 && cfg.MatchBlock == "" && !cfg.Interactive {
			cfg.MatchBlock = "resource"
		}
	}

	if cfg.Operation == "move" && cfg.Output == "" {
		return errors.New("--out is required for -op move")
	}

	if cfg.Interactive && cfg.Operation == "sort" {
		return errors.New("--interactive does not apply to -op sort (sort runs file-wide)")
	}

	// Parse and validate all --select expressions up-front.
	selectors, err := parseSelectors(cfg.Selects)
	if err != nil {
		return err
	}

	paths, err := discoverTerraformFiles(cfg)
	if err != nil {
		return err
	}

	if len(paths) == 0 {
		fmt.Println("No .tf files found.")
		return nil
	}

	if cfg.Interactive {
		picked, err := runInteractive(cfg, paths)
		if err != nil {
			return err
		}
		if len(picked) == 0 {
			fmt.Println("No blocks selected.")
			return nil
		}
		cfg.matchSet = make(map[matchKey]bool, len(picked))
		for _, b := range picked {
			cfg.matchSet[matchKey{Path: b.Path, Signature: b.signature()}] = true
		}
		// Selectors and legacy flags are ignored when an explicit set is in play.
		selectors = nil
	}

	if cfg.Operation == "list" {
		return runListOperation(cfg, paths, selectors)
	}

	var movedBlocks []*hclwrite.Block
	var changes []FileChange

	for _, path := range paths {
		change, blocks, err := processFile(path, cfg, selectors)
		if err != nil {
			return err
		}

		if change.Changed {
			changes = append(changes, change)
		}

		if cfg.Operation == "move" {
			movedBlocks = append(movedBlocks, blocks...)
		}
	}

	if cfg.Operation == "move" {
		if err := writeMovedBlocks(cfg, movedBlocks); err != nil {
			return err
		}
	}

	if cfg.Plan {
		printPlanReport(cfg, changes, movedBlocks)
	} else {
		printSummary(cfg, changes, movedBlocks)
	}

	if cfg.DryRun || !cfg.InPlace {
		return nil
	}

	for _, change := range changes {
		if err := writeChange(change, cfg.Backup); err != nil {
			return err
		}
	}

	return nil
}

func parseSelectors(raw []string) ([]Selector, error) {
	out := make([]Selector, 0, len(raw))
	for _, s := range raw {
		sel, err := parseSelector(s)
		if err != nil {
			return nil, err
		}
		out = append(out, sel)
	}
	return out, nil
}

func discoverTerraformFiles(cfg Config) ([]string, error) {
	info, err := os.Stat(cfg.Root)
	if err != nil {
		return nil, err
	}

	if !info.IsDir() {
		if strings.HasSuffix(cfg.Root, ".tf") {
			return []string{cfg.Root}, nil
		}
		return nil, fmt.Errorf("%s is not a .tf file", cfg.Root)
	}

	excluded := map[string]bool{}
	for _, d := range strings.Split(cfg.ExcludeDirs, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			excluded[d] = true
		}
	}

	var paths []string

	err = filepath.WalkDir(cfg.Root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		name := d.Name()

		if d.IsDir() {
			if path != cfg.Root {
				if excluded[name] {
					return filepath.SkipDir
				}

				if !cfg.IncludeHidden && strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
			}

			if !cfg.Recursive && path != cfg.Root {
				return filepath.SkipDir
			}

			return nil
		}

		if !cfg.IncludeHidden && strings.HasPrefix(name, ".") {
			return nil
		}

		if strings.HasSuffix(name, ".tf") {
			paths = append(paths, path)
		}

		return nil
	})

	sort.Strings(paths)
	return paths, err
}

func processFile(path string, cfg Config, selectors []Selector) (FileChange, []*hclwrite.Block, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return FileChange{}, nil, err
	}

	file, diags := hclwrite.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return FileChange{}, nil, fmt.Errorf("failed to parse %s: %s", path, diags.Error())
	}

	body := file.Body()

	var removedOrMoved []*hclwrite.Block
	var blockInfos []BlockInfo

	switch cfg.Operation {
	case "sort":
		sortTopLevelBlocks(body)

	case "remove", "move":
		removedOrMoved, blockInfos = removeMatchingBlocks(body, cfg, selectors, path)
	}

	out := file.Bytes()

	change := FileChange{
		Path:        path,
		Changed:     !bytes.Equal(src, out),
		Removed:     len(removedOrMoved),
		Sorted:      cfg.Operation == "sort",
		Blocks:      blockInfos,
		BeforeBytes: src,
		AfterBytes:  out,
	}

	return change, removedOrMoved, nil
}

func sortTopLevelBlocks(body *hclwrite.Body) {
	blocks := body.Blocks()
	if len(blocks) <= 1 {
		return
	}

	sort.SliceStable(blocks, func(i, j int) bool {
		return blockSortKey(blocks[i]) < blockSortKey(blocks[j])
	})

	attrs := body.Attributes()

	body.Clear()

	attrNames := make([]string, 0, len(attrs))
	for name := range attrs {
		attrNames = append(attrNames, name)
	}
	sort.Strings(attrNames)

	for _, name := range attrNames {
		body.SetAttributeRaw(name, attrs[name].Expr().BuildTokens(nil))
	}

	if len(attrNames) > 0 && len(blocks) > 0 {
		body.AppendNewline()
	}

	for i, block := range blocks {
		body.AppendBlock(block)
		if i != len(blocks)-1 {
			body.AppendNewline()
		}
	}
}

func removeMatchingBlocks(body *hclwrite.Body, cfg Config, selectors []Selector, path string) ([]*hclwrite.Block, []BlockInfo) {
	var removed []*hclwrite.Block
	var infos []BlockInfo

	for _, block := range body.Blocks() {
		if blockMatches(block, cfg, selectors, path) {
			infos = append(infos, BlockInfo{Type: block.Type(), Labels: block.Labels()})
			removed = append(removed, block)
			body.RemoveBlock(block)
		}
	}

	return removed, infos
}

func blockMatches(block *hclwrite.Block, cfg Config, selectors []Selector, path string) bool {
	if cfg.matchSet != nil {
		return cfg.matchSet[matchKey{Path: path, Signature: blockSortKey(block)}]
	}

	if len(selectors) > 0 {
		for _, sel := range selectors {
			if selectorMatches(sel, block) {
				return true
			}
		}
		return false
	}

	// Legacy flag-based matching.
	if cfg.MatchBlock != "" && block.Type() != cfg.MatchBlock {
		return false
	}

	labels := block.Labels()

	if cfg.MatchBlock == "resource" || block.Type() == "resource" {
		if len(labels) < 2 {
			return false
		}

		if cfg.MatchResType != "" && !globMatch(cfg.MatchResType, labels[0]) {
			return false
		}

		if cfg.MatchResName != "" && !globMatch(cfg.MatchResName, labels[1]) {
			return false
		}
	}

	return true
}

func selectorMatches(sel Selector, block *hclwrite.Block) bool {
	if sel.BlockType != "" && !globMatch(sel.BlockType, block.Type()) {
		return false
	}

	labels := block.Labels()

	if sel.Label1 != "" {
		if len(labels) < 1 || !globMatch(sel.Label1, labels[0]) {
			return false
		}
	}

	if sel.Label2 != "" {
		if len(labels) < 2 || !globMatch(sel.Label2, labels[1]) {
			return false
		}
	}

	return true
}

func globMatch(pattern, value string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}

	ok, err := filepath.Match(pattern, value)
	if err == nil {
		return ok
	}

	return pattern == value
}

func blockSortKey(block *hclwrite.Block) string {
	parts := []string{block.Type()}
	parts = append(parts, block.Labels()...)
	return strings.Join(parts, "\x00")
}

func writeMovedBlocks(cfg Config, blocks []*hclwrite.Block) error {
	if len(blocks) == 0 {
		return nil
	}

	sort.SliceStable(blocks, func(i, j int) bool {
		return blockSortKey(blocks[i]) < blockSortKey(blocks[j])
	})

	out := hclwrite.NewEmptyFile()
	body := out.Body()

	for i, block := range blocks {
		body.AppendBlock(block)
		if i != len(blocks)-1 {
			body.AppendNewline()
		}
	}

	if cfg.DryRun || !cfg.InPlace {
		if !cfg.Plan {
			fmt.Printf("\nWould write %d moved block(s) to %s\n", len(blocks), cfg.Output)
		}
		return nil
	}

	if cfg.Backup {
		if _, err := os.Stat(cfg.Output); err == nil {
			if err := copyFile(cfg.Output, cfg.Output+".bak"); err != nil {
				return err
			}
		}
	}

	return os.WriteFile(cfg.Output, out.Bytes(), 0644)
}

func writeChange(change FileChange, backup bool) error {
	if backup {
		if err := os.WriteFile(change.Path+".bak", change.BeforeBytes, 0644); err != nil {
			return err
		}
	}

	return os.WriteFile(change.Path, change.AfterBytes, 0644)
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}

	return os.WriteFile(dstPath, src, 0644)
}

// runListOperation prints every block matched by the current selectors /
// match-set, one per line, in `path<TAB>type.label1.label2` form. Designed for
// piping into other shell tools.
func runListOperation(cfg Config, paths []string, selectors []Selector) error {
	type match struct {
		Path   string
		Type   string
		Labels []string
	}

	var matches []match
	typeCounts := map[string]int{}

	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, diags := hclwrite.ParseConfig(src, path, hcl.InitialPos)
		if diags.HasErrors() {
			return fmt.Errorf("failed to parse %s: %s", path, diags.Error())
		}
		for _, blk := range file.Body().Blocks() {
			if blockMatches(blk, cfg, selectors, path) {
				matches = append(matches, match{Path: path, Type: blk.Type(), Labels: blk.Labels()})
				typeCounts[blk.Type()]++
			}
		}
	}

	if len(matches) == 0 {
		fmt.Fprintln(os.Stderr, "No matching blocks.")
		return nil
	}

	pathStyle := lipgloss.NewStyle().Faint(true)
	typeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#5FAFFF")).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD787"))

	for _, m := range matches {
		labelPart := ""
		if len(m.Labels) > 0 {
			labelPart = "." + labelStyle.Render(strings.Join(m.Labels, "."))
		}
		fmt.Printf("%s\t%s%s\n", pathStyle.Render(m.Path), typeStyle.Render(m.Type), labelPart)
	}

	// Footer with per-type counts goes to stderr so stdout stays a clean,
	// pipe-friendly stream.
	parts := make([]string, 0, len(typeCounts))
	for t, n := range typeCounts {
		parts = append(parts, fmt.Sprintf("%s=%d", t, n))
	}
	sort.Strings(parts)
	fmt.Fprintf(os.Stderr, "\n%d match%s  (%s)\n",
		len(matches), pluralS(len(matches)), strings.Join(parts, ", "))
	return nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

var (
	sumTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF87D7"))
	sumKeyStyle      = lipgloss.NewStyle().Faint(true)
	sumPathStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#5FAFFF"))
	sumNoChangeStyle = lipgloss.NewStyle().Faint(true).Italic(true)
	sumDryRunStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD787")).Italic(true)
	sumOpStyles      = map[string]lipgloss.Style{
		"sort":   lipgloss.NewStyle().Foreground(lipgloss.Color("#5FAFFF")).Bold(true),
		"remove": lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F5F")).Bold(true),
		"move":   lipgloss.NewStyle().Foreground(lipgloss.Color("#FFD787")).Bold(true),
	}
)

func printSummary(cfg Config, changes []FileChange, movedBlocks []*hclwrite.Block) {
	fmt.Println(sumTitleStyle.Render("tfhcl"))
	fmt.Printf("%s %s\n", sumKeyStyle.Render("operation:"), cfg.Operation)
	fmt.Printf("%s %s\n", sumKeyStyle.Render("root:     "), cfg.Root)
	fmt.Printf("%s %t\n", sumKeyStyle.Render("dry-run:  "), cfg.DryRun)
	fmt.Printf("%s %t\n\n", sumKeyStyle.Render("in-place: "), cfg.InPlace)

	if len(changes) == 0 {
		fmt.Println(sumNoChangeStyle.Render("No changes."))
		return
	}

	opTag := func() string {
		s, ok := sumOpStyles[cfg.Operation]
		if !ok {
			return strings.ToUpper(cfg.Operation)
		}
		return s.Render(strings.ToUpper(cfg.Operation))
	}()

	for _, change := range changes {
		switch cfg.Operation {
		case "sort":
			fmt.Printf("  %s  %s\n", opTag, sumPathStyle.Render(change.Path))
		case "remove", "move":
			fmt.Printf("  %s  %s  %s\n",
				opTag,
				sumPathStyle.Render(change.Path),
				sumKeyStyle.Render(fmt.Sprintf("blocks=%d", change.Removed)))
		}
	}

	if cfg.Operation == "move" {
		fmt.Printf("\n%s %d\n", sumKeyStyle.Render("moved block total:"), len(movedBlocks))
		fmt.Printf("%s %s\n", sumKeyStyle.Render("destination:      "), cfg.Output)
	}

	if cfg.DryRun || !cfg.InPlace {
		fmt.Println()
		fmt.Println(sumDryRunStyle.Render("Dry run — no files written. Pass --dry-run=false --in-place=true to apply."))
	}
}

func printPlanReport(cfg Config, changes []FileChange, movedBlocks []*hclwrite.Block) {
	action := strings.ToUpper(cfg.Operation)

	fmt.Printf("# Terraform HCL Change Plan\n\n")
	fmt.Printf("| | |\n")
	fmt.Printf("|---|---|\n")
	fmt.Printf("| **Operation** | `%s` |\n", cfg.Operation)
	fmt.Printf("| **Root** | `%s` |\n", cfg.Root)
	fmt.Printf("| **Date** | %s |\n", time.Now().Format("2006-01-02 15:04:05"))
	if cfg.Operation == "move" && cfg.Output != "" {
		fmt.Printf("| **Destination** | `%s` |\n", cfg.Output)
	}
	if len(cfg.Selects) > 0 {
		quoted := make([]string, len(cfg.Selects))
		for i, s := range cfg.Selects {
			quoted[i] = "`" + s + "`"
		}
		fmt.Printf("| **Selectors** | %s |\n", strings.Join(quoted, ", "))
	}
	fmt.Printf("| **Dry run** | %t |\n", cfg.DryRun)
	fmt.Println()

	if len(changes) == 0 {
		fmt.Println("_No changes detected._")
		return
	}

	fmt.Printf("## Files Affected (%d)\n\n", len(changes))

	totalBlocks := 0

	for _, change := range changes {
		fmt.Printf("### `%s`\n\n", change.Path)

		switch cfg.Operation {
		case "sort":
			fmt.Println("Blocks will be sorted alphabetically by type and label.")

		case "remove", "move":
			if len(change.Blocks) == 0 {
				break
			}

			fmt.Printf("| Block Type | Labels | Action |\n")
			fmt.Printf("|---|---|---|\n")

			for _, b := range change.Blocks {
				labelStr := ""
				if len(b.Labels) > 0 {
					parts := make([]string, len(b.Labels))
					for i, l := range b.Labels {
						parts[i] = "`" + l + "`"
					}
					labelStr = strings.Join(parts, ", ")
				}

				dest := ""
				if cfg.Operation == "move" {
					dest = " → `" + cfg.Output + "`"
				}

				fmt.Printf("| `%s` | %s | %s%s |\n", b.Type, labelStr, action, dest)
				totalBlocks++
			}
		}

		fmt.Println()
	}

	fmt.Println("## Summary")
	fmt.Println()
	fmt.Printf("- **Files affected:** %d\n", len(changes))

	switch cfg.Operation {
	case "sort":
		fmt.Printf("- **Files to sort:** %d\n", len(changes))
	case "remove":
		fmt.Printf("- **Blocks to remove:** %d\n", totalBlocks)
	case "move":
		fmt.Printf("- **Blocks to move:** %d\n", len(movedBlocks))
		fmt.Printf("- **Destination:** `%s`\n", cfg.Output)
	}

	if cfg.DryRun || !cfg.InPlace {
		fmt.Println("\n> **Dry run** — no files written. Pass `--dry-run=false --in-place=true` to apply.")
	}
}
