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
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
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
	if errors.Is(err, store.ErrV1State) {
		return []Finding{{Severity: "error",
			Message: "state is the v1 paired-context layout — run `claudectx migrate` to upgrade to per-tool profiles"}}
	}
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

	if d.P.LegacyTerminalPin {
		fs = append(fs, Finding{Severity: "warn",
			Message: "this terminal is pinned into the old contexts/ layout (stale CLAUDE_CONFIG_DIR/CODEX_HOME) — re-run `eval \"$(claudectx env ...)\"` or `cx off`"})
	}

	for _, t := range tool.All {
		axis := st.Axis(t)
		if !d.S.Exists(t, axis.Current) {
			fs = append(fs, Finding{Severity: "error",
				Message: fmt.Sprintf("state says current %s profile is %q but profiles/%s/%s does not exist", t, axis.Current, t, axis.Current)})
		}

		// Links are ground truth; state.json follows them.
		live := d.P.LiveDir(t)
		c, err := linker.Classify(live, d.P.ToolProfilesDir(t))
		if err != nil {
			fs = append(fs, Finding{Severity: "error", Message: fmt.Sprintf("%s: %v", live, err)})
			continue
		}
		switch c.Kind {
		case linker.ManagedLink:
			if c.Context != axis.Current {
				name, ax := c.Context, axis
				fs = append(fs, Finding{
					Severity: "warn",
					Message: fmt.Sprintf("%s points at %s profile %q but state says %q (links win)",
						live, t, c.Context, axis.Current),
					Fix: func() error {
						ax.Previous = ax.Current
						ax.Current = name
						return d.S.Save(st)
					},
				})
			} else {
				fs = append(fs, Finding{Severity: "ok",
					Message: fmt.Sprintf("%s -> %s profile %q", live, t, c.Context)})
			}
		case linker.Real:
			fs = append(fs, Finding{Severity: "error",
				Message: fmt.Sprintf("%s is a real directory — something replaced the managed symlink (a tool may have done a directory-level rewrite); back it up and re-run `claudectx init`", live)})
		case linker.Dangling:
			fs = append(fs, Finding{Severity: "error",
				Message: fmt.Sprintf("%s is a dangling symlink to %s", live, c.Target)})
		case linker.ForeignLink:
			fs = append(fs, Finding{Severity: "error",
				Message: fmt.Sprintf("%s is a foreign symlink to %s — claudectx will not touch it", live, c.Target)})
		case linker.Missing:
			liveDir := live
			target := d.P.ProfileHome(t, axis.Current)
			fs = append(fs, Finding{
				Severity: "warn",
				Message:  fmt.Sprintf("%s is missing — should link to %s", liveDir, target),
				Fix:      func() error { return linker.Replace(liveDir, target) },
			})
		}
	}

	// Leftover v1 layout next to a v2 state means a migration finished but
	// something recreated contexts/ — or a partial manual restore.
	if _, err := os.Stat(d.P.LegacyContextsDir()); err == nil {
		fs = append(fs, Finding{Severity: "warn",
			Message: fmt.Sprintf("legacy %s exists alongside the v2 layout — inspect and remove or re-run `claudectx migrate`", d.P.LegacyContextsDir())})
	}

	// claude.json: presence and permissions.
	if fi, err := os.Stat(d.P.ClaudeJSON); err != nil {
		fs = append(fs, Finding{Severity: "warn",
			Message: fmt.Sprintf("%s missing — Claude Code will recreate it; profile copy will repopulate on next switch", d.P.ClaudeJSON)})
	} else if fi.Mode().Perm()&0o077 != 0 {
		path := d.P.ClaudeJSON
		fs = append(fs, Finding{
			Severity: "warn",
			Message:  fmt.Sprintf("%s is group/world readable (%v)", path, fi.Mode().Perm()),
			Fix:      func() error { return os.Chmod(path, 0o600) },
		})
	}

	// Secrets permissions (claude axis).
	names, _ := d.S.List(tool.Claude)
	for _, name := range names {
		stash := d.P.KeychainStash(name)
		if fi, err := os.Stat(stash); err == nil && fi.Mode().Perm()&0o077 != 0 {
			p := stash
			fs = append(fs, Finding{
				Severity: "warn",
				Message:  fmt.Sprintf("%s is group/world readable (%v)", p, fi.Mode().Perm()),
				Fix:      func() error { return os.Chmod(p, 0o600) },
			})
		}
	}

	// Old codex layout inside codex profiles: pre-rust CLI files without a
	// config.toml.
	codexNames, _ := d.S.List(tool.Codex)
	for _, name := range codexNames {
		home := d.P.ProfileHome(tool.Codex, name)
		if fileExists(home+"/config.json") && !fileExists(home+"/config.toml") {
			fs = append(fs, Finding{Severity: "info",
				Message: fmt.Sprintf("codex profile %q has an old-style layout (config.json, no config.toml) — `claudectx translate claude-to-codex --codex %s` can generate modern config", name, name)})
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
		fmt.Fprintf(w, "[%s] %s\n", f.Severity, f.Message)
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
