package paths_test

import (
	"path/filepath"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/paths"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

func clearEnv(t *testing.T) {
	for _, k := range []string{"CLAUDECTX_CLAUDE_DIR", "CLAUDECTX_CODEX_DIR", "CLAUDECTX_CLAUDE_JSON",
		"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		t.Setenv(k, "")
	}
}

func TestFromEnvPerToolTerminalPins(t *testing.T) {
	home := t.TempDir()
	cxHome := filepath.Join(home, ".claudectx")
	t.Setenv("CLAUDECTX_HOME", cxHome)
	clearEnv(t)

	// Each tool's env var pinning its own axis, independently.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(cxHome, "profiles", "claude", "vertex", "home"))
	p := paths.FromEnv()
	if p.TerminalClaude != "vertex" || p.TerminalCodex != "" {
		t.Fatalf("claude-only pin: %q / %q", p.TerminalClaude, p.TerminalCodex)
	}
	if filepath.Base(p.ClaudeDir) != ".claude" {
		t.Fatalf("managed dir polluted by pin: %q", p.ClaudeDir)
	}

	t.Setenv("CODEX_HOME", filepath.Join(cxHome, "profiles", "codex", "work", "home"))
	p = paths.FromEnv()
	if p.TerminalClaude != "vertex" || p.TerminalCodex != "work" {
		t.Fatalf("both pins: %q / %q", p.TerminalClaude, p.TerminalCodex)
	}
	if p.TerminalPin(tool.Codex) != "work" {
		t.Fatalf("TerminalPin = %q", p.TerminalPin(tool.Codex))
	}
	if p.LegacyTerminalPin {
		t.Fatal("profiles pin misflagged as legacy")
	}

	// A pin into the OLD contexts/ layout is detected as a stale legacy pin.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(cxHome, "contexts", "old-ctx", "claude"))
	p = paths.FromEnv()
	if p.TerminalClaude != "old-ctx" || !p.LegacyTerminalPin {
		t.Fatalf("legacy pin: %q legacy=%v", p.TerminalClaude, p.LegacyTerminalPin)
	}

	// A foreign CLAUDE_CONFIG_DIR (outside claudectx) is honored as the
	// managed path and is NOT a pin.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "custom-claude"))
	t.Setenv("CODEX_HOME", "")
	p = paths.FromEnv()
	if p.TerminalClaude != "" {
		t.Fatalf("foreign config dir misdetected as pin: %q", p.TerminalClaude)
	}
	if p.ClaudeDir != filepath.Join(home, "custom-claude") {
		t.Fatalf("ClaudeDir = %q", p.ClaudeDir)
	}
}

func TestProfilePathShapes(t *testing.T) {
	p := paths.Paths{Home: "/x/.claudectx"}
	cases := map[string]string{
		p.ToolProfilesDir(tool.Claude):       "/x/.claudectx/profiles/claude",
		p.ProfileHome(tool.Claude, "vertex"): "/x/.claudectx/profiles/claude/vertex/home",
		p.ProfileHome(tool.Codex, "work"):    "/x/.claudectx/profiles/codex/work/home",
		// .claude.json lives INSIDE home/ to match Claude Code's
		// CLAUDE_CONFIG_DIR layout.
		p.ProfileClaudeJSON("vertex"):  "/x/.claudectx/profiles/claude/vertex/home/.claude.json",
		p.KeychainStash("vertex"):      "/x/.claudectx/profiles/claude/vertex/secrets/claude-keychain.json",
		p.LegacyContextsDir():          "/x/.claudectx/contexts",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}
