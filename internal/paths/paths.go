// Package paths resolves every filesystem location claudectx touches.
// It is the only package allowed to read environment variables, so tests
// can construct a Paths pointing anywhere.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Paths holds every resolved location. Treat as immutable after FromEnv.
type Paths struct {
	// Home is the claudectx state root (~/.claudectx).
	Home string
	// ClaudeDir is the live Claude Code dir (~/.claude), managed as a symlink.
	ClaudeDir string
	// CodexDir is the live Codex CLI dir (~/.codex), managed as a symlink.
	CodexDir string
	// ClaudeJSON is the live ~/.claude.json, copy-swapped on switch.
	ClaudeJSON string
	// KeychainEnabled is false on non-darwin or when CLAUDECTX_NO_KEYCHAIN is set.
	KeychainEnabled bool
	// TerminalContext is the context this terminal is pinned to via
	// CLAUDE_CONFIG_DIR/CODEX_HOME exports (see `claudectx env`); "" when the
	// terminal follows the global symlinks.
	TerminalContext string
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

	// CLAUDE_CONFIG_DIR / CODEX_HOME pointing inside our contexts dir means
	// this terminal is env-pinned to a context (claudectx env). Those values
	// then describe the terminal scope, not the globally managed paths.
	resolveLive := func(explicit, toolEnv, def string) string {
		if v := os.Getenv(explicit); v != "" {
			return v
		}
		if v := os.Getenv(toolEnv); v != "" {
			if name, ok := managedContext(v, p.ContextsDir()); ok {
				if p.TerminalContext == "" {
					p.TerminalContext = name
				}
				return def
			}
			return v
		}
		return def
	}
	p.ClaudeDir = resolveLive("CLAUDECTX_CLAUDE_DIR", "CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	p.CodexDir = resolveLive("CLAUDECTX_CODEX_DIR", "CODEX_HOME", filepath.Join(home, ".codex"))
	return p
}

// managedContext reports the context name when path lies inside contextsDir.
func managedContext(path, contextsDir string) (string, bool) {
	rel, err := filepath.Rel(contextsDir, filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return strings.Split(rel, string(filepath.Separator))[0], true
}

func (p Paths) StateFile() string   { return filepath.Join(p.Home, "state.json") }
func (p Paths) ContextsDir() string { return filepath.Join(p.Home, "contexts") }
func (p Paths) BackupsDir() string  { return filepath.Join(p.Home, "backups") }

func (p Paths) ContextDir(name string) string { return filepath.Join(p.ContextsDir(), name) }

// Per-context locations.
func (p Paths) CtxClaudeDir(name string) string { return filepath.Join(p.ContextDir(name), "claude") }
func (p Paths) CtxCodexDir(name string) string  { return filepath.Join(p.ContextDir(name), "codex") }
// CtxClaudeJSON lives INSIDE the context's claude dir: that is where Claude
// Code itself writes it when CLAUDE_CONFIG_DIR is set (terminal-scoped mode),
// so global copy-swap and `claudectx env` share one canonical file.
func (p Paths) CtxClaudeJSON(name string) string {
	return filepath.Join(p.CtxClaudeDir(name), ".claude.json")
}
func (p Paths) CtxSecretsDir(name string) string {
	return filepath.Join(p.ContextDir(name), "secrets")
}
func (p Paths) CtxKeychainStash(name string) string {
	return filepath.Join(p.CtxSecretsDir(name), "claude-keychain.json")
}
