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
