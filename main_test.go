package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// parseFirstBlock parses src and returns its single top-level block.
func parseFirstBlock(t *testing.T, src string) *hclwrite.Block {
	t.Helper()
	f, diags := hclwrite.ParseConfig([]byte(src), "test.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags)
	}
	blocks := f.Body().Blocks()
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	return blocks[0]
}

// captureStdout runs fn with os.Stdout redirected and returns the captured
// output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	<-done
	os.Stdout = orig
	return buf.String()
}

func TestParseSelector(t *testing.T) {
	tests := []struct {
		in      string
		want    Selector
		wantErr bool
	}{
		{"resource.google_project_iam_member.*", Selector{"resource", "google_project_iam_member", "*"}, false},
		{"module.network", Selector{"module", "network", ""}, false},
		{"data.google_iam_policy.admin", Selector{"data", "google_iam_policy", "admin"}, false},
		{"locals", Selector{"locals", "", ""}, false},
		{"'resource.foo.*'", Selector{"resource", "foo", "*"}, false},
		{"\"data.foo.bar\"", Selector{"data", "foo", "bar"}, false},
		{"", Selector{}, true},
		{".missing-block-type", Selector{}, true},
	}
	for _, tt := range tests {
		got, err := parseSelector(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseSelector(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseSelector(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParseSelectors_Aggregates(t *testing.T) {
	got, err := parseSelectors([]string{"resource.aws_*.*", "module.network"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].BlockType != "resource" || got[1].BlockType != "module" {
		t.Errorf("unexpected: %+v", got)
	}

	if _, err := parseSelectors([]string{"valid.thing", ""}); err == nil {
		t.Error("expected error from empty selector")
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, value string
		want           bool
	}{
		{"", "anything", true},
		{"*", "anything", true},
		{"google_*", "google_storage_bucket", true},
		{"google_*", "aws_s3_bucket", false},
		{"exact", "exact", true},
		{"exact", "different", false},
		{"foo?", "foob", true},
		{"foo?", "foobar", false},
	}
	for _, tt := range tests {
		if got := globMatch(tt.pattern, tt.value); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.value, got, tt.want)
		}
	}
}

func TestSelectorMatches(t *testing.T) {
	block := parseFirstBlock(t, `resource "google_storage_bucket" "assets" {
  name = "x"
}`)

	tests := []struct {
		name string
		sel  Selector
		want bool
	}{
		{"exact", Selector{"resource", "google_storage_bucket", "assets"}, true},
		{"glob both", Selector{"resource", "google_*", "*"}, true},
		{"glob type, wrong name", Selector{"resource", "google_*", "other"}, false},
		{"wrong block type", Selector{"data", "google_storage_bucket", "assets"}, false},
		{"only type", Selector{"resource", "", ""}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectorMatches(tt.sel, block); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBlockMatches_LegacyFlags(t *testing.T) {
	block := parseFirstBlock(t, `resource "google_storage_bucket" "assets" {}`)
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"type+typeglob", Config{MatchBlock: "resource", MatchResType: "google_*"}, true},
		{"type+typeglob no match", Config{MatchBlock: "resource", MatchResType: "aws_*"}, false},
		{"wrong type", Config{MatchBlock: "module"}, false},
		{"name match", Config{MatchBlock: "resource", MatchResName: "assets"}, true},
		{"name no match", Config{MatchBlock: "resource", MatchResName: "other"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blockMatches(block, tt.cfg, nil, "test.tf"); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBlockMatches_Selectors(t *testing.T) {
	block := parseFirstBlock(t, `module "network" { source = "./x" }`)
	if !blockMatches(block, Config{}, []Selector{{"module", "network", ""}}, "test.tf") {
		t.Error("expected match")
	}
	if blockMatches(block, Config{}, []Selector{{"resource", "*", "*"}}, "test.tf") {
		t.Error("expected no match (wrong block type)")
	}
}

func TestBlockMatches_MatchSetTakesPrecedence(t *testing.T) {
	block := parseFirstBlock(t, `resource "aws_s3_bucket" "data" {}`)
	sig := blockSortKey(block)

	cfg := Config{
		// Legacy flags would otherwise match every resource — but matchSet wins.
		MatchBlock: "resource",
		matchSet: map[matchKey]bool{
			{Path: "test.tf", Signature: sig}: true,
		},
	}
	if !blockMatches(block, cfg, nil, "test.tf") {
		t.Error("expected match from matchSet")
	}
	if blockMatches(block, cfg, nil, "other.tf") {
		t.Error("expected no match (different path)")
	}
}

func TestSortTopLevelBlocks(t *testing.T) {
	src := `module "z" {
  source = "./z"
}

resource "aws_s3_bucket" "b" {}

module "a" {
  source = "./a"
}
`
	f, diags := hclwrite.ParseConfig([]byte(src), "t.tf", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags)
	}
	sortTopLevelBlocks(f.Body())
	out := string(f.Bytes())

	aIdx := strings.Index(out, `module "a"`)
	zIdx := strings.Index(out, `module "z"`)
	rIdx := strings.Index(out, `resource "aws_s3_bucket"`)
	if aIdx == -1 || zIdx == -1 || rIdx == -1 {
		t.Fatalf("missing block in output:\n%s", out)
	}
	if !(aIdx < zIdx && zIdx < rIdx) {
		t.Errorf("unexpected order (a=%d z=%d r=%d):\n%s", aIdx, zIdx, rIdx, out)
	}
}

func TestSortTopLevelBlocks_NoOpSingleBlock(t *testing.T) {
	src := `resource "aws_s3_bucket" "only" {}`
	f, _ := hclwrite.ParseConfig([]byte(src), "t.tf", hcl.InitialPos)
	before := string(f.Bytes())
	sortTopLevelBlocks(f.Body())
	if string(f.Bytes()) != before {
		t.Error("single-block file should be unchanged")
	}
}

func TestRemoveMatchingBlocks_BySelector(t *testing.T) {
	src := `resource "aws_s3_bucket" "a" {}
resource "google_project_iam_member" "admin" {}
module "net" {}
`
	f, _ := hclwrite.ParseConfig([]byte(src), "t.tf", hcl.InitialPos)
	body := f.Body()
	sels := []Selector{{"resource", "google_*", "*"}}

	removed, infos := removeMatchingBlocks(body, Config{}, sels, "t.tf")
	if len(removed) != 1 {
		t.Fatalf("want 1 removed, got %d", len(removed))
	}
	if infos[0].Type != "resource" || infos[0].Labels[0] != "google_project_iam_member" {
		t.Errorf("unexpected info: %+v", infos[0])
	}

	out := string(f.Bytes())
	if strings.Contains(out, "google_project_iam_member") {
		t.Errorf("removed block still present:\n%s", out)
	}
	if !strings.Contains(out, "aws_s3_bucket") {
		t.Error("aws_s3_bucket should be untouched")
	}
	if !strings.Contains(out, `module "net"`) {
		t.Error("module should be untouched")
	}
}

func TestDiscoverTerraformFiles(t *testing.T) {
	dir := t.TempDir()

	mustWrite := func(rel string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("a.tf")
	mustWrite("b.tf")
	mustWrite("nested/c.tf")
	mustWrite("nested/d.txt")
	mustWrite(".terraform/e.tf")
	mustWrite(".hidden/f.tf")
	mustWrite(".g.tf")

	cfg := Config{Root: dir, Recursive: true, ExcludeDirs: ".git,.terraform"}
	paths, err := discoverTerraformFiles(cfg)
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, _ := filepath.Rel(dir, p)
		got = append(got, filepath.ToSlash(rel))
	}
	sort.Strings(got)

	want := []string{"a.tf", "b.tf", "nested/c.tf"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDiscoverTerraformFiles_NonRecursive(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.tf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "b.tf"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	paths, err := discoverTerraformFiles(Config{Root: dir, Recursive: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "a.tf") {
		t.Errorf("expected only a.tf, got %v", paths)
	}
}

func TestDiscoverTerraformFiles_SingleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	paths, err := discoverTerraformFiles(Config{Root: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != path {
		t.Errorf("got %v, want [%s]", paths, path)
	}
}

func TestProcessFile_RemoveSelector(t *testing.T) {
	src := `resource "aws_s3_bucket" "a" {}
resource "google_project_iam_member" "admin" {}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Operation: "remove"}
	sels := []Selector{{"resource", "google_*", "*"}}

	change, _, err := processFile(path, cfg, sels)
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed {
		t.Error("expected Changed=true")
	}
	if change.Removed != 1 {
		t.Errorf("Removed=%d, want 1", change.Removed)
	}
	if strings.Contains(string(change.AfterBytes), "google_project_iam_member") {
		t.Errorf("removed block still present:\n%s", change.AfterBytes)
	}
}

func TestProcessFile_NoChange(t *testing.T) {
	src := `resource "aws_s3_bucket" "a" {}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Operation: "remove"}
	sels := []Selector{{"resource", "google_*", "*"}}

	change, _, err := processFile(path, cfg, sels)
	if err != nil {
		t.Fatal(err)
	}
	if change.Changed {
		t.Error("expected no change")
	}
	if change.Removed != 0 {
		t.Errorf("Removed=%d, want 0", change.Removed)
	}
}

func TestProcessFile_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.tf")
	if err := os.WriteFile(path, []byte("this is { not valid hcl"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := processFile(path, Config{Operation: "sort"}, nil); err == nil {
		t.Error("expected parse error")
	}
}

func TestRunListOperation(t *testing.T) {
	src := `resource "aws_s3_bucket" "a" {}
module "net" {}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runListOperation(Config{}, []string{path}, []Selector{{"resource", "*", "*"}}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "aws_s3_bucket") {
		t.Errorf("missing resource line:\n%s", out)
	}
	if strings.Contains(out, `module.net`) {
		t.Errorf("module should not appear (filtered out):\n%s", out)
	}
}

func TestPluralS(t *testing.T) {
	if pluralS(1) != "" {
		t.Error("pluralS(1) should be empty")
	}
	if pluralS(0) != "es" {
		t.Error("pluralS(0) should be es")
	}
	if pluralS(2) != "es" {
		t.Error("pluralS(2) should be es")
	}
}

func TestRun_RejectsBadOperation(t *testing.T) {
	if err := run(Config{Operation: "delete"}); err == nil {
		t.Error("expected error for unknown op")
	}
}

func TestRun_MoveRequiresOut(t *testing.T) {
	if err := run(Config{Operation: "move"}); err == nil {
		t.Error("expected error when --out missing")
	}
}

func TestRun_InteractiveSortRejected(t *testing.T) {
	if err := run(Config{Operation: "sort", Interactive: true}); err == nil {
		t.Error("expected error: --interactive incompatible with sort")
	}
}

func TestPrintPlanReport_Move(t *testing.T) {
	cfg := Config{
		Operation: "move",
		Root:      "/repo",
		Output:    "/repo/data.tf",
		Selects:   []string{"data.*"},
		DryRun:    true,
		Plan:      true,
	}
	changes := []FileChange{
		{
			Path:    "/repo/main.tf",
			Changed: true,
			Removed: 1,
			Blocks: []BlockInfo{
				{Type: "data", Labels: []string{"google_iam_policy", "admin"}},
			},
		},
	}

	out := captureStdout(t, func() {
		printPlanReport(cfg, changes, nil)
	})

	for _, want := range []string{
		"# Terraform HCL Change Plan",
		"`move`",
		"## Files Affected (1)",
		"`/repo/main.tf`",
		"`data`",
		"`google_iam_policy`",
		"MOVE",
		"## Summary",
		"Dry run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan report missing %q:\n%s", want, out)
		}
	}
}

func TestPrintPlanReport_NoChanges(t *testing.T) {
	out := captureStdout(t, func() {
		printPlanReport(Config{Operation: "remove", Plan: true}, nil, nil)
	})
	if !strings.Contains(out, "No changes detected") {
		t.Errorf("expected 'No changes detected':\n%s", out)
	}
}

func TestWriteChange_WithBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	original := []byte("resource \"a\" \"b\" {}\n")
	updated := []byte("resource \"x\" \"y\" {}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	change := FileChange{
		Path:        path,
		BeforeBytes: original,
		AfterBytes:  updated,
	}
	if err := writeChange(change, true); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, updated) {
		t.Errorf("file not updated; got %q", got)
	}

	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("expected .bak to exist: %v", err)
	}
	if !bytes.Equal(bak, original) {
		t.Errorf(".bak does not match original; got %q", bak)
	}
}

func TestWriteChange_NoBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(path, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}

	change := FileChange{
		Path:        path,
		BeforeBytes: []byte("orig"),
		AfterBytes:  []byte("new"),
	}
	if err := writeChange(change, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Error("expected no .bak file")
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	dst := filepath.Join(dir, "b")
	content := []byte("hello\nworld\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("copy mismatch: %q vs %q", got, content)
	}
}

func TestWriteMovedBlocks_DryRun(t *testing.T) {
	dir := t.TempDir()
	src := `resource "a" "b" {}` + "\n"
	f, _ := hclwrite.ParseConfig([]byte(src), "t.tf", hcl.InitialPos)
	blocks := f.Body().Blocks()

	cfg := Config{
		Operation: "move",
		Output:    filepath.Join(dir, "out.tf"),
		DryRun:    true,
	}
	out := captureStdout(t, func() {
		if err := writeMovedBlocks(cfg, blocks); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Would write") {
		t.Errorf("expected dry-run announcement:\n%s", out)
	}
	if _, err := os.Stat(cfg.Output); !os.IsNotExist(err) {
		t.Error("dry-run should not write the destination file")
	}
}

func TestWriteMovedBlocks_RealWrite(t *testing.T) {
	dir := t.TempDir()
	src := `resource "a" "b" {}` + "\n" + `module "m" {}` + "\n"
	f, _ := hclwrite.ParseConfig([]byte(src), "t.tf", hcl.InitialPos)
	blocks := f.Body().Blocks()

	dest := filepath.Join(dir, "out.tf")
	if err := os.WriteFile(dest, []byte("# preexisting\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Operation: "move",
		Output:    dest,
		DryRun:    false,
		InPlace:   true,
		Backup:    true,
	}
	if err := writeMovedBlocks(cfg, blocks); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `resource "a" "b"`) {
		t.Errorf("destination missing resource block:\n%s", got)
	}

	bak, err := os.ReadFile(dest + ".bak")
	if err != nil {
		t.Fatalf("expected .bak: %v", err)
	}
	if !strings.Contains(string(bak), "# preexisting") {
		t.Errorf(".bak should contain original content:\n%s", bak)
	}
}

func TestRun_SortHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.tf")
	src := `module "z" { source = "./z" }
module "a" { source = "./a" }
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Root:        dir,
		Operation:   "sort",
		Recursive:   true,
		DryRun:      true,
		ExcludeDirs: ".git,.terraform",
	}
	out := captureStdout(t, func() {
		if err := run(cfg); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "SORT") {
		t.Errorf("expected SORT in output:\n%s", out)
	}
}
