// Package testenv builds throwaway fake-home environments for tests.
package testenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/paths"
)

// Env is a fully isolated claudectx world rooted in a temp dir.
type Env struct {
	T    *testing.T
	Root string
	P    paths.Paths
}

// New creates the fake home. The claude/codex dirs are NOT created — use the
// builders so each test states what exists.
func New(t *testing.T) *Env {
	t.Helper()
	root := t.TempDir()
	return &Env{
		T:    t,
		Root: root,
		P: paths.Paths{
			Home:            filepath.Join(root, ".claudectx"),
			ClaudeDir:       filepath.Join(root, ".claude"),
			CodexDir:        filepath.Join(root, ".codex"),
			ClaudeJSON:      filepath.Join(root, ".claude.json"),
			KeychainEnabled: false,
		},
	}
}

func (e *Env) WriteFile(path, content string) {
	e.T.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.T.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.T.Fatal(err)
	}
}

// BuildClaudeTree fabricates a realistic ~/.claude with settings, CLAUDE.md,
// and a couple of skills (one with Claude-only frontmatter).
func (e *Env) BuildClaudeTree() {
	e.T.Helper()
	d := e.P.ClaudeDir
	settings := map[string]any{
		"model": "claude-opus-4-6",
		"permissions": map[string]any{
			"defaultMode": "acceptEdits",
			"allow":       []string{"Bash(git status)", "Bash(go test:*)"},
			"deny":        []string{"Read(.env)"},
		},
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	e.WriteFile(filepath.Join(d, "settings.json"), string(data))
	e.WriteFile(filepath.Join(d, "CLAUDE.md"), "# Global rules\n\nBe terse.\n")
	e.WriteFile(filepath.Join(d, "history.jsonl"), "{}\n")
	e.WriteFile(filepath.Join(d, "skills", "crashlog", "SKILL.md"),
		"---\nname: crashlog\ndescription: Diagnose crashes\nallowed-tools: Bash, Read\n---\n\nDo the thing.\n")
	e.WriteFile(filepath.Join(d, "skills", "plain", "SKILL.md"),
		"---\nname: plain\ndescription: A standard skill\n---\n\nPlain body.\n")
	e.WriteFile(filepath.Join(d, "skills", "plain", "helper.sh"), "echo hi\n")
}

// BuildClaudeJSON writes a live ~/.claude.json with stdio + http MCP servers.
func (e *Env) BuildClaudeJSON() {
	e.T.Helper()
	cj := map[string]any{
		"hasCompletedOnboarding": true,
		"lastOnboardingVersion":  "2.1.0",
		"installMethod":          "brew",
		"autoUpdates":            true,
		"projects":               map[string]any{"/tmp/p": map[string]any{"allowedTools": []string{}}},
		"mcpServers": map[string]any{
			"files": map[string]any{
				"command": "npx",
				"args":    []string{"-y", "@modelcontextprotocol/server-filesystem"},
				"env":     map[string]string{"ROOT": "/tmp"},
			},
			"atlassian": map[string]any{
				"type": "http",
				"url":  "https://mcp.atlassian.com/v1/sse",
			},
		},
	}
	data, _ := json.MarshalIndent(cj, "", "  ")
	e.WriteFile(e.P.ClaudeJSON, string(data))
	os.Chmod(e.P.ClaudeJSON, 0o600)
}

// BuildModernCodexTree fabricates a modern ~/.codex (config.toml, AGENTS.md,
// auth.json, one skill).
func (e *Env) BuildModernCodexTree() {
	e.T.Helper()
	d := e.P.CodexDir
	e.WriteFile(filepath.Join(d, "config.toml"),
		"# my codex config\nmodel = \"gpt-5.2-codex\"\napproval_policy = \"on-failure\"\n\n[mcp_servers.search]\ncommand = \"search-mcp\"\nargs = [\"--fast\"]\n")
	e.WriteFile(filepath.Join(d, "AGENTS.md"), "# Codex rules\n\nPrefer rg over grep.\n")
	e.WriteFile(filepath.Join(d, "auth.json"), `{"OPENAI_API_KEY":"sk-test"}`)
	e.WriteFile(filepath.Join(d, "skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: Deploy the app\n---\n\nRun deploy.\n")
}

// BuildOldCodexTree fabricates the pre-rust codex CLI layout.
func (e *Env) BuildOldCodexTree() {
	e.T.Helper()
	d := e.P.CodexDir
	e.WriteFile(filepath.Join(d, "config.json"), `{"model":""}`)
	e.WriteFile(filepath.Join(d, "instructions.md"), "")
}

// BuildV1ContextsTree fabricates a v1 paired-context installation mirroring
// the real machine this tool was developed on:
//
//   - context "claude-vertex": real Claude content (CLAUDE.md, settings,
//     skills, in-dir .claude.json) PLUS a stale root-level claude.json from
//     an older layout; a junk-ish but non-empty codex half; a keychain stash.
//   - context "codex-work": junk claude half; codex half holding auth.json
//     (the precious API key); an EMPTY secrets dir.
//   - live symlinks for both tools -> claude-vertex; v1 state.json.
func (e *Env) BuildV1ContextsTree() {
	e.T.Helper()
	ctxs := filepath.Join(e.P.Home, "contexts")

	// claude-vertex: the real Claude setup.
	cv := filepath.Join(ctxs, "claude-vertex")
	e.WriteFile(filepath.Join(cv, "claude", "CLAUDE.md"), "# Global rules\n\nBe terse.\n")
	e.WriteFile(filepath.Join(cv, "claude", "settings.json"),
		`{"model":"claude-opus-4-6","permissions":{"defaultMode":"acceptEdits"}}`)
	e.WriteFile(filepath.Join(cv, "claude", "skills", "crashlog", "SKILL.md"),
		"---\nname: crashlog\ndescription: Diagnose crashes\n---\n\nbody\n")
	e.WriteFile(filepath.Join(cv, "claude", ".claude.json"),
		`{"hasCompletedOnboarding":true,"mcpServers":{"files":{"command":"npx"}}}`)
	e.WriteFile(filepath.Join(cv, "claude.json"), `{"stale":"root level copy from old layout"}`)
	e.WriteFile(filepath.Join(cv, "codex", "log", "codex.log"), "ran logged out\n")
	e.WriteFile(filepath.Join(cv, "secrets", "claude-keychain.json"),
		`{"service":"Claude Code-credentials","account":"me@example.com","password":"tok-personal"}`)
	os.Chmod(filepath.Join(cv, "secrets", "claude-keychain.json"), 0o600)
	os.Chmod(filepath.Join(cv, "secrets"), 0o700)

	// codex-work: junk claude half, codex half with the API key.
	cw := filepath.Join(ctxs, "codex-work")
	e.WriteFile(filepath.Join(cw, "claude", ".claude.json"), `{"hasCompletedOnboarding":true}`)
	e.WriteFile(filepath.Join(cw, "claude", "shell-snapshots", "snap.sh"), "")
	e.WriteFile(filepath.Join(cw, "codex", "auth.json"), `{"OPENAI_API_KEY":"sk-work"}`)
	os.Chmod(filepath.Join(cw, "codex", "auth.json"), 0o600)
	e.WriteFile(filepath.Join(cw, "codex", "log", "codex.log"), "")
	if err := os.MkdirAll(filepath.Join(cw, "secrets"), 0o700); err != nil {
		e.T.Fatal(err)
	}

	// Live links + v1 state.
	if err := os.Symlink(filepath.Join(cv, "claude"), e.P.ClaudeDir); err != nil {
		e.T.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(cv, "codex"), e.P.CodexDir); err != nil {
		e.T.Fatal(err)
	}
	e.WriteFile(e.P.ClaudeJSON, `{"hasCompletedOnboarding":true,"mcpServers":{"files":{"command":"npx"}}}`)
	os.Chmod(e.P.ClaudeJSON, 0o600)
	e.WriteFile(filepath.Join(e.P.Home, "state.json"),
		`{"version":1,"current":"claude-vertex","previous":"codex-work","in_progress":null}`)
}
