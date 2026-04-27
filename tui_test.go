package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoveredBlock_Display(t *testing.T) {
	tests := []struct {
		b    discoveredBlock
		want string
	}{
		{discoveredBlock{Type: "resource", Labels: []string{"google_storage_bucket", "assets"}}, "resource.google_storage_bucket.assets"},
		{discoveredBlock{Type: "module", Labels: []string{"network"}}, "module.network"},
		{discoveredBlock{Type: "locals"}, "locals"},
	}
	for _, tt := range tests {
		if got := tt.b.display(); got != tt.want {
			t.Errorf("display() = %q, want %q", got, tt.want)
		}
	}
}

func TestDiscoveredBlock_Signature(t *testing.T) {
	a := discoveredBlock{Type: "resource", Labels: []string{"x", "y"}}
	b := discoveredBlock{Type: "resource", Labels: []string{"x", "y"}}
	c := discoveredBlock{Type: "resource", Labels: []string{"x", "z"}}

	if a.signature() != b.signature() {
		t.Error("identical blocks should have equal signatures")
	}
	if a.signature() == c.signature() {
		t.Error("different labels should produce different signatures")
	}
}

func TestCollectBlocks(t *testing.T) {
	src := `resource "aws_s3_bucket" "a" {
  bucket = "x"
}

module "net" {
  source = "./x"
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	blocks, err := collectBlocks([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	if blocks[0].Type != "resource" || blocks[0].Labels[0] != "aws_s3_bucket" {
		t.Errorf("unexpected first block: %+v", blocks[0])
	}
	if blocks[1].Type != "module" || blocks[1].Labels[0] != "net" {
		t.Errorf("unexpected second block: %+v", blocks[1])
	}
	if blocks[0].Source == "" || blocks[1].Source == "" {
		t.Error("expected Source to be populated")
	}
}

func TestCollectBlocks_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.tf")
	if err := os.WriteFile(path, []byte("this is { not valid hcl"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := collectBlocks([]string{path}); err == nil {
		t.Error("expected parse error")
	}
}

func TestTUIModel_InitialFilter(t *testing.T) {
	blocks := []discoveredBlock{
		{Path: "a.tf", Type: "resource", Labels: []string{"aws_s3_bucket", "data"}},
		{Path: "b.tf", Type: "module", Labels: []string{"network"}},
	}
	m := newTUIModel(blocks, "remove")
	if len(m.filtered) != 2 {
		t.Errorf("initial filter should match all; got %d", len(m.filtered))
	}
	if m.cursor != 0 {
		t.Errorf("cursor should start at 0, got %d", m.cursor)
	}
}

func TestTUIModel_Filter(t *testing.T) {
	blocks := []discoveredBlock{
		{Path: "a.tf", Type: "resource", Labels: []string{"aws_s3_bucket", "data"}},
		{Path: "a.tf", Type: "resource", Labels: []string{"google_storage_bucket", "assets"}},
		{Path: "b.tf", Type: "module", Labels: []string{"network"}},
	}
	m := newTUIModel(blocks, "remove")

	m.filter.SetValue("google")
	m.recomputeFilter()
	if len(m.filtered) != 1 {
		t.Fatalf("google filter: got %d matches, want 1", len(m.filtered))
	}
	if blocks[m.filtered[0]].Labels[0] != "google_storage_bucket" {
		t.Errorf("unexpected match: %+v", blocks[m.filtered[0]])
	}

	// Multi-token: AND across tokens.
	m.filter.SetValue("module network")
	m.recomputeFilter()
	if len(m.filtered) != 1 || blocks[m.filtered[0]].Type != "module" {
		t.Errorf("multi-token filter failed: %+v", m.filtered)
	}

	// Token may match the path.
	m.filter.SetValue("b.tf")
	m.recomputeFilter()
	if len(m.filtered) != 1 || blocks[m.filtered[0]].Path != "b.tf" {
		t.Errorf("path filter failed: %+v", m.filtered)
	}

	// No matches.
	m.filter.SetValue("nonexistent_xyz")
	m.recomputeFilter()
	if len(m.filtered) != 0 {
		t.Errorf("expected empty, got %d", len(m.filtered))
	}
}

func TestTUIModel_FilterClampsCursor(t *testing.T) {
	blocks := []discoveredBlock{
		{Path: "a.tf", Type: "resource", Labels: []string{"a", "b"}},
		{Path: "a.tf", Type: "resource", Labels: []string{"c", "d"}},
		{Path: "a.tf", Type: "resource", Labels: []string{"e", "f"}},
	}
	m := newTUIModel(blocks, "remove")
	m.cursor = 2

	// Filter down so the cursor's prior position would be out of range.
	m.filter.SetValue("a") // matches only first block (label "a")
	m.recomputeFilter()
	if m.cursor >= len(m.filtered) {
		t.Errorf("cursor not clamped: cursor=%d, filtered=%d", m.cursor, len(m.filtered))
	}
}
