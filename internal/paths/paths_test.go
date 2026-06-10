package paths_test

import (
	"path/filepath"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/paths"
)

func TestFromEnvTerminalScope(t *testing.T) {
	home := t.TempDir()
	cxHome := filepath.Join(home, ".claudectx")
	t.Setenv("CLAUDECTX_HOME", cxHome)
	t.Setenv("CLAUDECTX_CLAUDE_DIR", "")
	t.Setenv("CLAUDECTX_CODEX_DIR", "")
	t.Setenv("CLAUDECTX_CLAUDE_JSON", "")

	// Tool env vars pointing into our contexts dir = terminal pinned.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(cxHome, "contexts", "work", "claude"))
	t.Setenv("CODEX_HOME", filepath.Join(cxHome, "contexts", "work", "codex"))
	p := paths.FromEnv()
	if p.TerminalContext != "work" {
		t.Fatalf("TerminalContext = %q, want work", p.TerminalContext)
	}
	// Managed paths fall back to defaults, not the pinned dirs.
	if filepath.Base(p.ClaudeDir) != ".claude" || filepath.Base(p.CodexDir) != ".codex" {
		t.Fatalf("managed dirs polluted by pin: %q %q", p.ClaudeDir, p.CodexDir)
	}

	// A foreign CLAUDE_CONFIG_DIR (not inside contexts) is honored as the
	// managed path and is NOT a terminal pin.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "custom-claude"))
	t.Setenv("CODEX_HOME", "")
	p = paths.FromEnv()
	if p.TerminalContext != "" {
		t.Fatalf("foreign config dir misdetected as pin: %q", p.TerminalContext)
	}
	if p.ClaudeDir != filepath.Join(home, "custom-claude") {
		t.Fatalf("ClaudeDir = %q", p.ClaudeDir)
	}
}

func TestCtxClaudeJSONInsideClaudeDir(t *testing.T) {
	p := paths.Paths{Home: "/x/.claudectx"}
	want := "/x/.claudectx/contexts/work/claude/.claude.json"
	if got := p.CtxClaudeJSON("work"); got != want {
		t.Fatalf("CtxClaudeJSON = %q, want %q (must match Claude Code's CLAUDE_CONFIG_DIR layout)", got, want)
	}
}
