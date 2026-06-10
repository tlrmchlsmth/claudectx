package translate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/tlrmchlsmth/claudectx/internal/report"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
	"github.com/tlrmchlsmth/claudectx/internal/translate"
)

// ctxFromEnv builds a translate.Context over a fabricated context dir
// (translation always operates inside a context, never on live paths).
func ctxFromEnv(t *testing.T, e *testenv.Env) translate.Context {
	t.Helper()
	return translate.Context{
		Name:       "test",
		ClaudeDir:  e.P.ClaudeDir,
		CodexDir:   e.P.CodexDir,
		ClaudeJSON: e.P.ClaudeJSON,
	}
}

func notesFor(rep *report.Report, section string) []report.LossNote {
	var notes []report.LossNote
	for _, s := range rep.Sections {
		if s.Name != section {
			continue
		}
		notes = append(notes, s.Notes...)
		for _, a := range s.Actions {
			notes = append(notes, a.Notes...)
		}
	}
	return notes
}

func hasNote(notes []report.LossNote, sev report.Severity, substr string) bool {
	for _, n := range notes {
		if n.Severity == sev && strings.Contains(n.Artifact+" "+n.Message, substr) {
			return true
		}
	}
	return false
}

func run(t *testing.T, ctx translate.Context, opts translate.Options) *report.Report {
	t.Helper()
	rep, err := translate.Run(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestInstructionsClaudeToCodex(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	ctx := ctxFromEnv(t, e)

	run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex,
		Only: map[string]bool{"instructions": true}, InlineImports: true})

	agents, err := os.ReadFile(filepath.Join(e.P.CodexDir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "Be terse.") {
		t.Fatalf("AGENTS.md = %s", agents)
	}
}

func TestInstructionsInlinesImports(t *testing.T) {
	e := testenv.New(t)
	e.WriteFile(filepath.Join(e.P.ClaudeDir, "extra.md"), "Extra rules here.\n")
	e.WriteFile(filepath.Join(e.P.ClaudeDir, "CLAUDE.md"), "# Rules\n\n@extra.md\n\n@missing.md\n")
	ctx := ctxFromEnv(t, e)

	rep := run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex,
		Only: map[string]bool{"instructions": true}, InlineImports: true})

	agents, _ := os.ReadFile(filepath.Join(e.P.CodexDir, "AGENTS.md"))
	out := string(agents)
	if !strings.Contains(out, "inlined from @extra.md by claudectx") || !strings.Contains(out, "Extra rules here.") {
		t.Fatalf("import not inlined:\n%s", out)
	}
	if !strings.Contains(out, "@missing.md") {
		t.Fatal("unresolvable import line should be kept verbatim")
	}
	notes := notesFor(rep, "instructions")
	if !hasNote(notes, report.Warn, "missing.md") {
		t.Fatalf("missing import should warn: %+v", notes)
	}
}

func TestInstructionsRefusesSymlinkedDestination(t *testing.T) {
	e := testenv.New(t)
	e.BuildModernCodexTree()
	// CLAUDE.md is a symlink into dotfiles (like the real machine).
	e.WriteFile(filepath.Join(e.Root, "dotfiles", "CLAUDE.md"), "dotfiles content\n")
	os.MkdirAll(e.P.ClaudeDir, 0o755)
	os.Symlink(filepath.Join(e.Root, "dotfiles", "CLAUDE.md"), filepath.Join(e.P.ClaudeDir, "CLAUDE.md"))
	ctx := ctxFromEnv(t, e)

	rep := run(t, ctx, translate.Options{Direction: translate.CodexToClaude,
		Only: map[string]bool{"instructions": true}, Force: true})

	// Even with --force the symlink must not be overwritten.
	data, _ := os.ReadFile(filepath.Join(e.Root, "dotfiles", "CLAUDE.md"))
	if string(data) != "dotfiles content\n" {
		t.Fatalf("dotfiles file was clobbered: %s", data)
	}
	notes := notesFor(rep, "instructions")
	if !hasNote(notes, report.Warn, "symlink") {
		t.Fatalf("expected symlink warning: %+v", notes)
	}
}

func TestInstructionsSkipWithoutForce(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	e.WriteFile(filepath.Join(e.P.CodexDir, "AGENTS.md"), "different existing content\n")
	ctx := ctxFromEnv(t, e)

	rep := run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex,
		Only: map[string]bool{"instructions": true}, InlineImports: true})
	agents, _ := os.ReadFile(filepath.Join(e.P.CodexDir, "AGENTS.md"))
	if string(agents) != "different existing content\n" {
		t.Fatal("existing AGENTS.md overwritten without --force")
	}
	if !hasNote(notesFor(rep, "instructions"), report.Warn, "--force") {
		t.Fatal("expected a force-needed warning")
	}

	// With force it overwrites.
	run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex,
		Only: map[string]bool{"instructions": true}, InlineImports: true, Force: true})
	agents, _ = os.ReadFile(filepath.Join(e.P.CodexDir, "AGENTS.md"))
	if !strings.Contains(string(agents), "Be terse.") {
		t.Fatal("--force did not overwrite")
	}
}

func TestSkillsCopyAndWarnings(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	ctx := ctxFromEnv(t, e)

	rep := run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex,
		Only: map[string]bool{"skills": true}})

	// Both skills copied, support files included.
	for _, p := range []string{
		filepath.Join(e.P.CodexDir, "skills", "crashlog", "SKILL.md"),
		filepath.Join(e.P.CodexDir, "skills", "plain", "SKILL.md"),
		filepath.Join(e.P.CodexDir, "skills", "plain", "helper.sh"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s", p)
		}
	}
	// allowed-tools is kept but warned about.
	copied, _ := os.ReadFile(filepath.Join(e.P.CodexDir, "skills", "crashlog", "SKILL.md"))
	if !strings.Contains(string(copied), "allowed-tools") {
		t.Fatal("Claude-only frontmatter was stripped; expected keep-but-warn")
	}
	notes := notesFor(rep, "skills")
	if !hasNote(notes, report.Warn, "allowed-tools") {
		t.Fatalf("expected allowed-tools warning: %+v", notes)
	}
}

func TestSkillsRoundTripIdempotent(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	ctx := ctxFromEnv(t, e)

	original, _ := os.ReadFile(filepath.Join(e.P.ClaudeDir, "skills", "plain", "SKILL.md"))

	run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex, Only: map[string]bool{"skills": true}})
	// Wipe claude's copy, translate back.
	os.RemoveAll(filepath.Join(e.P.ClaudeDir, "skills", "plain"))
	run(t, ctx, translate.Options{Direction: translate.CodexToClaude, Only: map[string]bool{"skills": true}})

	back, err := os.ReadFile(filepath.Join(e.P.ClaudeDir, "skills", "plain", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != string(original) {
		t.Fatalf("round-trip not byte-identical:\n%q\nvs\n%q", original, back)
	}
}

func TestMCPClaudeToCodex(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeJSON()
	e.BuildModernCodexTree() // existing config.toml with comments + a server
	ctx := ctxFromEnv(t, e)

	rep := run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex,
		Only: map[string]bool{"mcp": true}})

	data, _ := os.ReadFile(filepath.Join(e.P.CodexDir, "config.toml"))
	doc := string(data)
	if !strings.Contains(doc, "# my codex config") {
		t.Fatal("user comment destroyed")
	}
	var parsed struct {
		Model      string                    `toml:"model"`
		MCPServers map[string]map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("config.toml invalid after merge: %v", err)
	}
	if parsed.Model != "gpt-5.2-codex" {
		t.Fatal("unrelated key lost")
	}
	files := parsed.MCPServers["files"]
	if files["command"] != "npx" {
		t.Fatalf("files server = %+v", files)
	}
	if _, ok := parsed.MCPServers["search"]; !ok {
		t.Fatal("pre-existing codex server lost")
	}
	// http server must NOT be written; it is reported lost with a snippet.
	if _, ok := parsed.MCPServers["atlassian"]; ok {
		t.Fatal("http server written despite version-dependent support")
	}
	notes := notesFor(rep, "mcp")
	if !hasNote(notes, report.Lost, "atlassian") {
		t.Fatalf("http server should be a lost note: %+v", notes)
	}
}

func TestMCPCodexToClaude(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeJSON() // has files + atlassian already
	e.BuildModernCodexTree()
	// Add a codex-only key to test the dropped-key note.
	e.WriteFile(filepath.Join(e.P.CodexDir, "config.toml"),
		"[mcp_servers.search]\ncommand = \"search-mcp\"\nargs = [\"--fast\"]\nstartup_timeout_ms = 5000\n\n[mcp_servers.files]\ncommand = \"other\"\n")
	ctx := ctxFromEnv(t, e)

	rep := run(t, ctx, translate.Options{Direction: translate.CodexToClaude,
		Only: map[string]bool{"mcp": true}})

	data, _ := os.ReadFile(e.P.ClaudeJSON)
	var cj map[string]json.RawMessage
	if err := json.Unmarshal(data, &cj); err != nil {
		t.Fatalf("claude.json invalid: %v", err)
	}
	// Untouched top-level keys survive.
	if _, ok := cj["projects"]; !ok {
		t.Fatal("projects key lost from claude.json")
	}
	var servers map[string]map[string]any
	json.Unmarshal(cj["mcpServers"], &servers)
	if servers["search"]["command"] != "search-mcp" {
		t.Fatalf("search not merged: %+v", servers)
	}
	// files existed already -> skipped without force.
	if servers["files"]["command"] != "npx" {
		t.Fatalf("existing files server overwritten without --force: %+v", servers["files"])
	}
	notes := notesFor(rep, "mcp")
	if !hasNote(notes, report.Info, "startup_timeout_ms") {
		t.Fatalf("expected dropped-key note: %+v", notes)
	}
}

func TestMCPStdioRoundTrip(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeJSON()
	ctx := ctxFromEnv(t, e)

	// claude -> codex
	run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex, Only: map[string]bool{"mcp": true}})
	// wipe claude's mcpServers, translate back
	e.WriteFile(e.P.ClaudeJSON, `{"projects":{}}`)
	run(t, ctx, translate.Options{Direction: translate.CodexToClaude, Only: map[string]bool{"mcp": true}})

	data, _ := os.ReadFile(e.P.ClaudeJSON)
	var cj struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cj); err != nil {
		t.Fatal(err)
	}
	files := cj.MCPServers["files"]
	if files.Command != "npx" || len(files.Args) != 2 || files.Env["ROOT"] != "/tmp" {
		t.Fatalf("stdio server did not round-trip: %+v", files)
	}
}

func TestSettingsClaudeToCodex(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree() // acceptEdits + allow/deny rules + model
	e.BuildModernCodexTree()
	ctx := ctxFromEnv(t, e)

	rep := run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex,
		Only: map[string]bool{"settings": true}})

	data, _ := os.ReadFile(filepath.Join(e.P.CodexDir, "config.toml"))
	var parsed struct {
		ApprovalPolicy string `toml:"approval_policy"`
		SandboxMode    string `toml:"sandbox_mode"`
		Model          string `toml:"model"`
	}
	if err := toml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ApprovalPolicy != "on-failure" || parsed.SandboxMode != "workspace-write" {
		t.Fatalf("mapped policy = %+v", parsed)
	}
	if parsed.Model != "gpt-5.2-codex" {
		t.Fatal("codex model overwritten — models must never cross vendors")
	}
	notes := notesFor(rep, "settings")
	if !hasNote(notes, report.Lost, "2 allow rule") {
		t.Fatalf("allow rules loss not counted: %+v", notes)
	}
	if !hasNote(notes, report.Lost, "1 deny rule") {
		t.Fatalf("deny rules loss not counted: %+v", notes)
	}
	if !hasNote(notes, report.Lost, "claude-opus-4-6") {
		t.Fatalf("model loss not reported: %+v", notes)
	}
}

func TestSettingsCodexToClaude(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	e.BuildModernCodexTree() // approval_policy = "on-failure"
	ctx := ctxFromEnv(t, e)

	run(t, ctx, translate.Options{Direction: translate.CodexToClaude,
		Only: map[string]bool{"settings": true}})

	data, _ := os.ReadFile(filepath.Join(e.P.ClaudeDir, "settings.json"))
	var s struct {
		Model       string `json:"model"`
		Permissions struct {
			DefaultMode string   `json:"defaultMode"`
			Allow       []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if s.Permissions.DefaultMode != "acceptEdits" {
		t.Fatalf("defaultMode = %q", s.Permissions.DefaultMode)
	}
	// Merge must not clobber other settings.
	if s.Model != "claude-opus-4-6" || len(s.Permissions.Allow) != 2 {
		t.Fatalf("settings.json damaged by merge: %+v", s)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	e.BuildClaudeJSON()
	ctx := ctxFromEnv(t, e)

	rep := run(t, ctx, translate.Options{Direction: translate.ClaudeToCodex,
		DryRun: true, InlineImports: true})

	if _, err := os.Stat(e.P.CodexDir); err == nil {
		entries, _ := os.ReadDir(e.P.CodexDir)
		if len(entries) > 0 {
			t.Fatalf("dry run created files: %v", entries)
		}
	}
	translated, _, _ := rep.Counts()
	if translated == 0 {
		t.Fatal("dry run should still report planned actions")
	}
}

func TestMissingSourcesAreInfoNotError(t *testing.T) {
	e := testenv.New(t)
	// Entirely empty context: every translator should no-op with info notes.
	os.MkdirAll(e.P.ClaudeDir, 0o755)
	os.MkdirAll(e.P.CodexDir, 0o755)
	ctx := ctxFromEnv(t, e)
	for _, dir := range []translate.Direction{translate.ClaudeToCodex, translate.CodexToClaude} {
		rep := run(t, ctx, translate.Options{Direction: dir, InlineImports: true})
		_, _, lossy := rep.Counts()
		if lossy != 0 {
			t.Fatalf("%s on empty context: %d lossy", dir, lossy)
		}
	}
}
