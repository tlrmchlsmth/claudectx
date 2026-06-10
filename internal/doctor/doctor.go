// Package doctor implements `claudectx doctor`: read-only health checks over
// everything claudectx manages, with an optional --fix for the safe subset.
//
// It exists because the managed state can drift in ways no single command
// notices: a tool replaces the ~/.claude symlink with a real directory, a
// crashed switch leaves a stale journal, secrets lose their 0600 perms, or
// state.json disagrees with where the symlinks actually point. When state
// and links disagree, the links win — they are what Claude/Codex actually
// read — and --fix rewrites state.json to match, never the other way around.
//
// Each check returns a Finding{Severity, Message, Fix}; Fix is nil for
// problems that need a human (e.g. a foreign symlink we refuse to touch).
package doctor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
	"github.com/tlrmchlsmth/claudectx/internal/store"
)

type Finding struct {
	Severity string // "ok", "info", "warn", "error"
	Message  string
	// Fix repairs the finding; nil when not auto-fixable.
	Fix func() error
}

type Doctor struct {
	P paths.Paths
	S *store.Store
}

func New(p paths.Paths, s *store.Store) *Doctor { return &Doctor{P: p, S: s} }

func (d *Doctor) Check() []Finding {
	var fs []Finding

	if !d.S.Initialized() {
		return []Finding{{Severity: "info", Message: "not initialized — run `claudectx init`"}}
	}
	st, err := d.S.Load()
	if err != nil {
		return []Finding{{Severity: "error", Message: err.Error()}}
	}

	if st.InProgress != nil {
		fs = append(fs, Finding{
			Severity: "warn",
			Message: fmt.Sprintf("interrupted %q operation in journal (started %s) — any command will trigger recovery",
				st.InProgress.Op, st.InProgress.StartedAt),
		})
	}

	if !d.S.Exists(st.Current) {
		fs = append(fs, Finding{Severity: "error",
			Message: fmt.Sprintf("state says current context is %q but contexts/%s does not exist", st.Current, st.Current)})
	}

	// Links are ground truth; state.json follows them.
	linkCtx := map[string]string{}
	for _, item := range []struct{ live, label string }{
		{d.P.ClaudeDir, "claude"},
		{d.P.CodexDir, "codex"},
	} {
		c, err := linker.Classify(item.live, d.P.ContextsDir())
		if err != nil {
			fs = append(fs, Finding{Severity: "error", Message: fmt.Sprintf("%s: %v", item.live, err)})
			continue
		}
		switch c.Kind {
		case linker.ManagedLink:
			linkCtx[item.label] = c.Context
			if c.Context != st.Current {
				ctx := c.Context
				fs = append(fs, Finding{
					Severity: "warn",
					Message: fmt.Sprintf("%s points at context %q but state says %q (links win)",
						item.live, c.Context, st.Current),
					Fix: func() error {
						st.Previous = st.Current
						st.Current = ctx
						return d.S.Save(st)
					},
				})
			} else {
				fs = append(fs, Finding{Severity: "ok",
					Message: fmt.Sprintf("%s -> context %q", item.live, c.Context)})
			}
		case linker.Real:
			fs = append(fs, Finding{Severity: "error",
				Message: fmt.Sprintf("%s is a real directory — something replaced the managed symlink (a tool may have done a directory-level rewrite); back it up and re-run `claudectx init`", item.live)})
		case linker.Dangling:
			fs = append(fs, Finding{Severity: "error",
				Message: fmt.Sprintf("%s is a dangling symlink to %s", item.live, c.Target)})
		case linker.ForeignLink:
			fs = append(fs, Finding{Severity: "error",
				Message: fmt.Sprintf("%s is a foreign symlink to %s — claudectx will not touch it", item.live, c.Target)})
		case linker.Missing:
			live := item.live
			target := filepath.Join(d.P.ContextDir(st.Current), item.label)
			fs = append(fs, Finding{
				Severity: "warn",
				Message:  fmt.Sprintf("%s is missing — should link to %s", live, target),
				Fix:      func() error { return linker.Replace(live, target) },
			})
		}
	}

	// claude.json: presence and permissions.
	if fi, err := os.Stat(d.P.ClaudeJSON); err != nil {
		fs = append(fs, Finding{Severity: "warn",
			Message: fmt.Sprintf("%s missing — Claude Code will recreate it; context copy will repopulate on next switch", d.P.ClaudeJSON)})
	} else if fi.Mode().Perm()&0o077 != 0 {
		path := d.P.ClaudeJSON
		fs = append(fs, Finding{
			Severity: "warn",
			Message:  fmt.Sprintf("%s is group/world readable (%v)", path, fi.Mode().Perm()),
			Fix:      func() error { return os.Chmod(path, 0o600) },
		})
	}

	// Secrets permissions.
	names, _ := d.S.List()
	for _, name := range names {
		stash := d.P.CtxKeychainStash(name)
		if fi, err := os.Stat(stash); err == nil && fi.Mode().Perm()&0o077 != 0 {
			p := stash
			fs = append(fs, Finding{
				Severity: "warn",
				Message:  fmt.Sprintf("%s is group/world readable (%v)", p, fi.Mode().Perm()),
				Fix:      func() error { return os.Chmod(p, 0o600) },
			})
		}
		// Old codex layout: pre-rust CLI files without a config.toml.
		codexDir := d.P.CtxCodexDir(name)
		if fileExists(filepath.Join(codexDir, "config.json")) && !fileExists(filepath.Join(codexDir, "config.toml")) {
			fs = append(fs, Finding{Severity: "info",
				Message: fmt.Sprintf("context %q has an old-style codex layout (config.json, no config.toml) — `claudectx translate claude-to-codex --context %s` can generate modern config", name, name)})
		}
	}

	// Stale trash.
	if entries, err := os.ReadDir(d.P.BackupsDir()); err == nil {
		cutoff := time.Now().AddDate(0, 0, -30)
		for _, e := range entries {
			info, err := e.Info()
			if err == nil && info.ModTime().Before(cutoff) {
				fs = append(fs, Finding{Severity: "info",
					Message: fmt.Sprintf("backups/%s is older than 30 days — safe to remove manually", e.Name())})
			}
		}
	}

	return fs
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Run prints findings and optionally applies fixes. Returns the number of
// unfixed problems (warn/error).
func (d *Doctor) Run(w io.Writer, fix bool) int {
	findings := d.Check()
	problems := 0
	for _, f := range findings {
		tag := f.Severity
		fmt.Fprintf(w, "[%s] %s\n", tag, f.Message)
		if f.Severity == "ok" || f.Severity == "info" {
			continue
		}
		if fix && f.Fix != nil {
			if err := f.Fix(); err != nil {
				fmt.Fprintf(w, "  fix failed: %v\n", err)
				problems++
			} else {
				fmt.Fprintf(w, "  fixed\n")
			}
			continue
		}
		if f.Fix != nil {
			fmt.Fprintf(w, "  (fixable with --fix)\n")
		}
		problems++
	}
	if problems == 0 {
		fmt.Fprintln(w, "all good")
	}
	return problems
}
