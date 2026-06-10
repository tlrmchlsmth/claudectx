// Package paths resolves every filesystem location claudectx touches.
// It is the only package allowed to read environment variables, so tests
// can construct a Paths pointing anywhere.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

// Paths holds every resolved location. Treat as immutable after FromEnv.
type Paths struct {
	// Home is the claudectx state root (~/.claudectx).
	Home string
	// ClaudeDir is the live Claude Code dir (~/.claude), managed as a symlink.
	ClaudeDir string
	// CodexDir is the live Codex CLI dir (~/.codex), managed as a symlink.
	CodexDir string
	// ClaudeJSON is the live ~/.claude.json, copy-swapped on claude switches.
	ClaudeJSON string
	// KeychainEnabled is false on non-darwin or when CLAUDECTX_NO_KEYCHAIN is set.
	KeychainEnabled bool
	// TerminalClaude / TerminalCodex name the profile each axis of this
	// terminal is pinned to via CLAUDE_CONFIG_DIR / CODEX_HOME exports
	// (see `claudectx env`); "" when that axis follows the global symlink.
	TerminalClaude string
	TerminalCodex  string
	// LegacyTerminalPin is set when a pin points into the pre-v2 contexts/
	// layout — the pin still names a context but the target has moved or
	// will move during migration; commands warn the user to re-eval.
	LegacyTerminalPin bool
}

func FromEnv() Paths {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}

	pick := func(keys []string, def string) string {
		for _, k := range keys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
		return def
	}

	p := Paths{
		Home:            pick([]string{"CLAUDECTX_HOME"}, filepath.Join(home, ".claudectx")),
		ClaudeJSON:      pick([]string{"CLAUDECTX_CLAUDE_JSON"}, filepath.Join(home, ".claude.json")),
		KeychainEnabled: runtime.GOOS == "darwin" && os.Getenv("CLAUDECTX_NO_KEYCHAIN") == "",
	}

	// CLAUDE_CONFIG_DIR / CODEX_HOME pointing inside our profiles (or legacy
	// contexts) tree means this terminal is env-pinned to a profile
	// (claudectx env). Those values then describe the terminal scope, not
	// the globally managed paths.
	resolveLive := func(t tool.Tool, explicit, toolEnv, def string) (string, string, bool) {
		if v := os.Getenv(explicit); v != "" {
			return v, "", false
		}
		if v := os.Getenv(toolEnv); v != "" {
			if name, ok := nameUnder(v, p.ToolProfilesDir(t)); ok {
				return def, name, false
			}
			if name, ok := nameUnder(v, p.LegacyContextsDir()); ok {
				return def, name, true
			}
			return v, "", false
		}
		return def, "", false
	}
	var legacy bool
	p.ClaudeDir, p.TerminalClaude, legacy = resolveLive(tool.Claude,
		"CLAUDECTX_CLAUDE_DIR", "CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	p.LegacyTerminalPin = p.LegacyTerminalPin || legacy
	p.CodexDir, p.TerminalCodex, legacy = resolveLive(tool.Codex,
		"CLAUDECTX_CODEX_DIR", "CODEX_HOME", filepath.Join(home, ".codex"))
	p.LegacyTerminalPin = p.LegacyTerminalPin || legacy
	return p
}

// nameUnder reports the first path component of path relative to root, when
// path lies inside root.
func nameUnder(path, root string) (string, bool) {
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return strings.Split(rel, string(filepath.Separator))[0], true
}

func (p Paths) StateFile() string  { return filepath.Join(p.Home, "state.json") }
func (p Paths) BackupsDir() string { return filepath.Join(p.Home, "backups") }

// LegacyContextsDir is the pre-v2 paired-context root, consulted only by
// migration and doctor.
func (p Paths) LegacyContextsDir() string { return filepath.Join(p.Home, "contexts") }

// Per-tool profile locations (v2 layout). A profile is
// profiles/<tool>/<name>/ containing home/ (the symlink target) and, for
// claude, secrets/.
func (p Paths) ProfilesDir() string { return filepath.Join(p.Home, "profiles") }
func (p Paths) ToolProfilesDir(t tool.Tool) string {
	return filepath.Join(p.ProfilesDir(), string(t))
}
func (p Paths) ProfileDir(t tool.Tool, name string) string {
	return filepath.Join(p.ToolProfilesDir(t), name)
}
func (p Paths) ProfileHome(t tool.Tool, name string) string {
	return filepath.Join(p.ProfileDir(t, name), "home")
}

// ProfileClaudeJSON lives INSIDE the profile's home dir: that is where
// Claude Code itself writes it when CLAUDE_CONFIG_DIR is set
// (terminal-pinned mode), so global copy-swap and `claudectx env` share one
// canonical file.
func (p Paths) ProfileClaudeJSON(name string) string {
	return filepath.Join(p.ProfileHome(tool.Claude, name), ".claude.json")
}
func (p Paths) ProfileSecretsDir(name string) string {
	return filepath.Join(p.ProfileDir(tool.Claude, name), "secrets")
}
func (p Paths) KeychainStash(name string) string {
	return filepath.Join(p.ProfileSecretsDir(name), "claude-keychain.json")
}

// LiveDir is the managed live path for an axis (~/.claude or ~/.codex).
func (p Paths) LiveDir(t tool.Tool) string {
	if t == tool.Claude {
		return p.ClaudeDir
	}
	return p.CodexDir
}

// TerminalPin returns the profile this terminal pins for an axis ("" = none).
func (p Paths) TerminalPin(t tool.Tool) string {
	if t == tool.Claude {
		return p.TerminalClaude
	}
	return p.TerminalCodex
}
