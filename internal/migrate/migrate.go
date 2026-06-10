// Package migrate upgrades a v1 paired-context layout
// (contexts/<name>/{claude,codex,secrets}) to the v2 per-tool profile layout
// (profiles/<tool>/<name>/home). It runs while the managed tools may be live,
// so the live symlinks are repointed immediately after each axis's move, and
// every step is idempotent-by-observation so an interrupted migration rolls
// forward on the next claudectx invocation.
package migrate

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

// Step names, in execution order. backup happens before the journal exists
// (it is what makes manual bail-out possible), so it has no step name.
const (
	StepClaudeAxis = "claude_axis" // move live claude half + repoint ~/.claude
	StepCodexAxis  = "codex_axis"  // move live codex half + repoint ~/.codex
	StepMoveRest   = "move_rest"   // move all remaining context halves
	StepFinalize   = "finalize"    // currents, previous, sweep remnants
)

var steps = []string{StepClaudeAxis, StepCodexAxis, StepMoveRest, StepFinalize}

type Migrator struct {
	P   paths.Paths
	S   *store.Store
	Out io.Writer
}

func New(p paths.Paths, s *store.Store, out io.Writer) *Migrator {
	return &Migrator{P: p, S: s, Out: out}
}

// Plan validates that migration can run and describes what it will do.
// Returns (nil, nil) when the state is already v2 (message printed).
type Plan struct {
	V1         store.V1State
	ClaudeFrom string // context the ~/.claude link points at ("" if missing)
	CodexFrom  string
	Contexts   []string // every context dir found under contexts/
}

func (m *Migrator) Plan() (*Plan, error) {
	if !m.S.Initialized() {
		return nil, fmt.Errorf("nothing to migrate — claudectx is not initialized (run `claudectx init`)")
	}
	v1, err := m.S.LoadV1()
	if err != nil {
		return nil, err
	}
	if v1.Version >= 2 {
		fmt.Fprintln(m.Out, "already on the v2 per-tool profile layout — nothing to do")
		return nil, nil
	}
	if v1.InProgress != nil {
		return nil, fmt.Errorf("the v1 state has an interrupted %q operation — recover it with claudectx v0.1.x first, then re-run migrate", v1.InProgress.Op)
	}

	plan := &Plan{V1: *v1}

	// Links are ground truth for what is live, not state.json.
	for _, item := range []struct {
		t    tool.Tool
		dest *string
	}{
		{tool.Claude, &plan.ClaudeFrom},
		{tool.Codex, &plan.CodexFrom},
	} {
		live := m.P.LiveDir(item.t)
		c, err := linker.Classify(live, m.P.LegacyContextsDir())
		if err != nil {
			return nil, err
		}
		switch c.Kind {
		case linker.ManagedLink:
			*item.dest = c.Context
		case linker.Missing:
			*item.dest = ""
		default:
			return nil, fmt.Errorf("%s is a %s — expected a claudectx v1 symlink; run `claudectx doctor` (with v0.1.x if needed) before migrating", live, c.Kind)
		}
	}

	entries, err := os.ReadDir(m.P.LegacyContextsDir())
	if err != nil {
		return nil, fmt.Errorf("reading v1 contexts: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if store.Reserved[e.Name()] {
			return nil, fmt.Errorf("v1 context %q collides with a v2 reserved name — rename it with claudectx v0.1.x (`claudectx rename %s <other>`) and re-run migrate", e.Name(), e.Name())
		}
		plan.Contexts = append(plan.Contexts, e.Name())
	}
	sort.Strings(plan.Contexts)
	return plan, nil
}

// PrintPlan describes the migration in concrete moves.
func (m *Migrator) PrintPlan(plan *Plan) {
	fmt.Fprintln(m.Out, "migrate will convert the v1 paired-context layout to per-tool profiles:")
	for _, name := range plan.Contexts {
		for _, t := range tool.All {
			src := filepath.Join(m.P.LegacyContextsDir(), name, string(t))
			if empty, err := dirEmpty(src); err == nil {
				if empty {
					fmt.Fprintf(m.Out, "  %-52s -> (empty, skipped)\n", src)
				} else {
					fmt.Fprintf(m.Out, "  %-52s -> %s\n", src, m.P.ProfileHome(t, name))
				}
			}
		}
		secrets := filepath.Join(m.P.LegacyContextsDir(), name, "secrets")
		if empty, err := dirEmpty(secrets); err == nil && !empty {
			fmt.Fprintf(m.Out, "  %-52s -> %s\n", secrets, m.P.ProfileSecretsDir(name))
		}
	}
	fmt.Fprintf(m.Out, "  live links: ~claude -> profiles/claude/%s, ~codex -> profiles/codex/%s\n",
		orNone(plan.ClaudeFrom), orNone(plan.CodexFrom))
	fmt.Fprintf(m.Out, "  leftovers (incl. any stale root claude.json) -> %s/contexts.v1.<ts>/\n", m.P.BackupsDir())
	fmt.Fprintf(m.Out, "  state backup -> %s/state.v1.<ts>.json\n", m.P.BackupsDir())
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// Run executes the migration described by plan.
func (m *Migrator) Run(plan *Plan) error {
	// Insurance before anything moves; this plus the untouched contexts tree
	// is the manual bail-out path.
	ts := time.Now().UTC().Format("20060102T150405Z")
	if err := os.MkdirAll(m.P.BackupsDir(), 0o755); err != nil {
		return err
	}
	if err := store.CopyFileAtomic(m.P.StateFile(),
		filepath.Join(m.P.BackupsDir(), fmt.Sprintf("state.v1.%s.json", ts)), 0o644); err != nil {
		return fmt.Errorf("backing up v1 state: %w", err)
	}

	st := &store.State{}
	j := &store.Journal{
		Op:   "migrate",
		Step: StepClaudeAxis,
		Migrate: &store.MigrateInfo{
			ClaudeFrom: plan.ClaudeFrom,
			CodexFrom:  plan.CodexFrom,
			V1Current:  plan.V1.Current,
			V1Previous: plan.V1.Previous,
		},
	}
	if err := m.S.SetJournal(st, j); err != nil {
		return err
	}
	return m.run(st, j, StepClaudeAxis)
}

// Recover rolls an interrupted migration forward from its journaled step.
func (m *Migrator) Recover(st *store.State) error {
	j := st.InProgress
	if j == nil || j.Op != "migrate" || j.Migrate == nil {
		return fmt.Errorf("no migration journal to recover")
	}
	fmt.Fprintf(m.Out, "recovering interrupted v1->v2 migration (from step %q)\n", j.Step)
	return m.run(st, j, j.Step)
}

func (m *Migrator) run(st *store.State, j *store.Journal, fromStep string) error {
	start := 0
	for i, s := range steps {
		if s == fromStep {
			start = i
			break
		}
	}
	for _, stepName := range steps[start:] {
		j.Step = stepName
		if err := m.S.SetJournal(st, j); err != nil {
			return err
		}
		var err error
		switch stepName {
		case StepClaudeAxis:
			err = m.migrateLiveAxis(tool.Claude, j.Migrate.ClaudeFrom)
		case StepCodexAxis:
			err = m.migrateLiveAxis(tool.Codex, j.Migrate.CodexFrom)
		case StepMoveRest:
			err = m.moveRest()
		case StepFinalize:
			err = m.finalize(st, j)
		}
		if err != nil {
			return fmt.Errorf("migrate step %q failed (re-run any claudectx command to resume): %w", stepName, err)
		}
	}
	return nil
}

// migrateLiveAxis moves the context half the live link points at, then
// repoints the link immediately — the dangle window for a running tool is
// just these two operations.
func (m *Migrator) migrateLiveAxis(t tool.Tool, from string) error {
	live := m.P.LiveDir(t)
	if from == "" {
		// v1 had no live link for this axis (never the case on a healthy
		// install, but Missing is tolerated in preflight).
		return nil
	}
	dest := m.P.ProfileHome(t, from)
	src := filepath.Join(m.P.LegacyContextsDir(), from, string(t))
	if err := m.moveHalf(src, dest); err != nil {
		return err
	}
	// Recovery-safe: if the link already points into profiles, this is a
	// re-run and Replace is a no-op repoint to the same target.
	if err := linker.Replace(live, dest); err != nil {
		return err
	}
	// secrets travel with the claude axis so the live profile is complete
	// (stash included) as early as possible.
	if t == tool.Claude {
		return m.moveSecrets(from)
	}
	return nil
}

// moveRest migrates every remaining context half that has content.
func (m *Migrator) moveRest() error {
	entries, err := os.ReadDir(m.P.LegacyContextsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil // already swept (re-run after finalize crash)
	}
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		for _, t := range tool.All {
			src := filepath.Join(m.P.LegacyContextsDir(), name, string(t))
			if err := m.moveHalf(src, m.P.ProfileHome(t, name)); err != nil {
				return err
			}
		}
		if err := m.moveSecrets(name); err != nil {
			return err
		}
	}
	return nil
}

// moveHalf renames src to dest unless src is gone (already moved) or empty
// (no profile is created for empty halves). Idempotent.
func (m *Migrator) moveHalf(src, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return nil // already moved by a previous (interrupted) run
	}
	empty, err := dirEmpty(src)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if empty {
		return nil // skip: an empty half does not become a profile
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.Rename(src, dest)
}

// moveSecrets carries a context's keychain stash into the claude profile.
func (m *Migrator) moveSecrets(name string) error {
	src := filepath.Join(m.P.LegacyContextsDir(), name, "secrets")
	dest := m.P.ProfileSecretsDir(name)
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	empty, err := dirEmpty(src)
	if errors.Is(err, os.ErrNotExist) || (err == nil && empty) {
		return nil
	}
	if err != nil {
		return err
	}
	// Secrets only make sense next to a claude profile; create the profile
	// dir if the claude half was empty (edge case).
	if err := os.MkdirAll(m.P.ProfileDir(tool.Claude, name), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dest); err != nil {
		return err
	}
	if err := os.Chmod(dest, 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(filepath.Join(dest, "claude-keychain.json")); err == nil && fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(filepath.Join(dest, "claude-keychain.json"), 0o600)
	}
	return nil
}

func (m *Migrator) finalize(st *store.State, j *store.Journal) error {
	mi := j.Migrate

	// Currents preserve v1 semantics exactly: each axis stays on whatever
	// the live link pointed at. Previous maps over where the profile exists.
	for _, item := range []struct {
		t    tool.Tool
		from string
	}{
		{tool.Claude, mi.ClaudeFrom},
		{tool.Codex, mi.CodexFrom},
	} {
		axis := st.Axis(item.t)
		axis.Current = item.from
		if axis.Current == "" {
			// No live link existed; fall back to any migrated profile.
			if names, _ := m.S.List(item.t); len(names) > 0 {
				axis.Current = names[0]
			}
		}
		if mi.V1Previous != "" && m.S.Exists(item.t, mi.V1Previous) && mi.V1Previous != axis.Current {
			axis.Previous = mi.V1Previous
		}
	}

	// Sweep the remnant contexts tree (empty halves, stale root-level
	// claude.json copies) into backups in one atomic rename. MkdirAll here
	// too: recovery may run on a machine where backups/ never existed.
	if _, err := os.Stat(m.P.LegacyContextsDir()); err == nil {
		if err := os.MkdirAll(m.P.BackupsDir(), 0o755); err != nil {
			return err
		}
		dst := filepath.Join(m.P.BackupsDir(),
			fmt.Sprintf("contexts.v1.%s", time.Now().UTC().Format("20060102T150405Z")))
		if err := os.Rename(m.P.LegacyContextsDir(), dst); err != nil {
			return err
		}
	}

	st.InProgress = nil
	if err := m.S.Save(st); err != nil {
		return err
	}

	fmt.Fprintln(m.Out, "migrated to per-tool profiles:")
	for _, t := range tool.All {
		names, _ := m.S.List(t)
		fmt.Fprintf(m.Out, "  %-7s current=%q  profiles: %v\n", t, st.Axis(t).Current, names)
	}
	fmt.Fprintf(m.Out, "v1 leftovers are in %s (keep until you've verified everything)\n", m.P.BackupsDir())
	fmt.Fprintln(m.Out, "note: `previous` was mapped per-axis from the v1 value where a matching profile exists")
	fmt.Fprintln(m.Out, "note: any terminal pinned via `claudectx env` before migration must re-run `eval \"$(claudectx env ...)\"`")
	// If the current codex profile is logged out but another one holds
	// credentials, the user almost certainly wants to switch to it.
	if !fileExists(filepath.Join(m.P.ProfileHome(tool.Codex, st.Codex.Current), "auth.json")) {
		names, _ := m.S.List(tool.Codex)
		for _, n := range names {
			if n != st.Codex.Current && fileExists(filepath.Join(m.P.ProfileHome(tool.Codex, n), "auth.json")) {
				fmt.Fprintf(m.Out, "tip: codex profile %q holds credentials — run: claudectx codex %s\n", n, n)
				break
			}
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dirEmpty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
