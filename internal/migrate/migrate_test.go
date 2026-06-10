package migrate_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/migrate"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

func newMigrator(e *testenv.Env) (*migrate.Migrator, *store.Store, *bytes.Buffer) {
	s := store.New(e.P)
	out := &bytes.Buffer{}
	return migrate.New(e.P, s, out), s, out
}

// assertMigrated checks the full expected end state for the real-machine
// fixture (BuildV1ContextsTree).
func assertMigrated(t *testing.T, e *testenv.Env, s *store.Store) {
	t.Helper()
	st, err := s.Load()
	if err != nil {
		t.Fatalf("post-migration Load: %v", err)
	}
	if st.InProgress != nil {
		t.Fatalf("journal not cleared: %+v", st.InProgress)
	}
	// Currents preserve v1 semantics: both axes stay on claude-vertex.
	if st.Claude.Current != "claude-vertex" || st.Codex.Current != "claude-vertex" {
		t.Fatalf("currents = %+v / %+v", st.Claude, st.Codex)
	}
	// Previous mapped from v1 where the profile exists (codex-work on both).
	if st.Claude.Previous != "codex-work" || st.Codex.Previous != "codex-work" {
		t.Fatalf("previous = %q / %q", st.Claude.Previous, st.Codex.Previous)
	}

	// Live links point into the v2 layout.
	for _, tl := range tool.All {
		c, err := linker.Classify(e.P.LiveDir(tl), e.P.ToolProfilesDir(tl))
		if err != nil || c.Kind != linker.ManagedLink || c.Context != "claude-vertex" {
			t.Fatalf("%s live link: %+v, %v", tl, c, err)
		}
	}

	// The real Claude content arrived, in-dir claude.json included.
	if _, err := os.Stat(filepath.Join(e.P.ProfileHome(tool.Claude, "claude-vertex"), "CLAUDE.md")); err != nil {
		t.Fatal("CLAUDE.md missing from migrated claude profile")
	}
	if _, err := os.Stat(e.P.ProfileClaudeJSON("claude-vertex")); err != nil {
		t.Fatal("in-dir .claude.json missing from migrated claude profile")
	}
	// The precious keychain stash travelled with correct perms.
	fi, err := os.Stat(e.P.KeychainStash("claude-vertex"))
	if err != nil {
		t.Fatal("keychain stash missing after migration")
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("stash perms = %v", fi.Mode().Perm())
	}
	// The precious work API key travelled.
	data, err := os.ReadFile(filepath.Join(e.P.ProfileHome(tool.Codex, "codex-work"), "auth.json"))
	if err != nil || !strings.Contains(string(data), "sk-work") {
		t.Fatalf("work API key: %s, %v", data, err)
	}
	// The junk halves became profiles too (kept, not judged).
	if !store.New(e.P).Exists(tool.Claude, "codex-work") {
		t.Fatal("non-empty claude half of codex-work should become a profile")
	}
	if !store.New(e.P).Exists(tool.Codex, "claude-vertex") {
		t.Fatal("non-empty codex half of claude-vertex should become a profile")
	}

	// contexts/ is gone; remnants (incl. the stale root claude.json) are in
	// backups.
	if _, err := os.Stat(e.P.LegacyContextsDir()); err == nil {
		t.Fatal("legacy contexts/ still present")
	}
	entries, _ := os.ReadDir(e.P.BackupsDir())
	var hasStateBackup, hasRemnant bool
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), "state.v1.") {
			hasStateBackup = true
		}
		if strings.HasPrefix(en.Name(), "contexts.v1.") {
			hasRemnant = true
			stale := filepath.Join(e.P.BackupsDir(), en.Name(), "claude-vertex", "claude.json")
			if _, err := os.Stat(stale); err != nil {
				t.Fatal("stale root-level claude.json should land in the remnant backup")
			}
		}
	}
	if !hasStateBackup || !hasRemnant {
		t.Fatalf("backups incomplete: state=%v remnant=%v", hasStateBackup, hasRemnant)
	}
}

func TestMigrateHappyPath(t *testing.T) {
	e := testenv.New(t)
	e.BuildV1ContextsTree()
	m, s, out := newMigrator(e)

	plan, err := m.Plan()
	if err != nil {
		t.Fatal(err)
	}
	m.PrintPlan(plan)
	if err := m.Run(plan); err != nil {
		t.Fatal(err)
	}
	assertMigrated(t, e, s)

	// The logged-out-current tip points at the profile holding credentials.
	if !strings.Contains(out.String(), "claudectx codex codex-work") {
		t.Fatalf("missing codex credentials tip:\n%s", out.String())
	}
}

func TestMigrateSkipsEmptyHalves(t *testing.T) {
	e := testenv.New(t)
	e.BuildV1ContextsTree()
	// Add a context with an empty codex half and an empty secrets dir.
	ctx := filepath.Join(e.P.LegacyContextsDir(), "lonely")
	e.WriteFile(filepath.Join(ctx, "claude", "CLAUDE.md"), "x\n")
	os.MkdirAll(filepath.Join(ctx, "codex"), 0o755)
	os.MkdirAll(filepath.Join(ctx, "secrets"), 0o700)

	m, s, _ := newMigrator(e)
	plan, err := m.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Run(plan); err != nil {
		t.Fatal(err)
	}
	if !s.Exists(tool.Claude, "lonely") {
		t.Fatal("non-empty claude half should migrate")
	}
	if s.Exists(tool.Codex, "lonely") {
		t.Fatal("empty codex half should not become a profile")
	}
	if _, err := os.Stat(e.P.ProfileSecretsDir("lonely")); err == nil {
		t.Fatal("empty secrets dir should not migrate")
	}
}

func TestMigrateRefusesV1Journal(t *testing.T) {
	e := testenv.New(t)
	e.BuildV1ContextsTree()
	e.WriteFile(e.P.StateFile(),
		`{"version":1,"current":"claude-vertex","previous":"","in_progress":{"op":"switch","from":"a","to":"b","step":"links"}}`)
	m, _, _ := newMigrator(e)
	if _, err := m.Plan(); err == nil || !strings.Contains(err.Error(), "v0.1.x") {
		t.Fatalf("expected v1-journal refusal, got %v", err)
	}
}

func TestMigrateRefusesReservedContextName(t *testing.T) {
	e := testenv.New(t)
	e.BuildV1ContextsTree()
	e.WriteFile(filepath.Join(e.P.LegacyContextsDir(), "codex", "claude", "x"), "")
	m, _, _ := newMigrator(e)
	if _, err := m.Plan(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved-name refusal, got %v", err)
	}
}

func TestMigrateNoOpOnV2(t *testing.T) {
	e := testenv.New(t)
	s := store.New(e.P)
	if err := s.Save(&store.State{Claude: store.AxisState{Current: "x"}, Codex: store.AxisState{Current: "y"}}); err != nil {
		t.Fatal(err)
	}
	m, _, out := newMigrator(e)
	plan, err := m.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan != nil {
		t.Fatal("v2 state should produce a nil plan")
	}
	if !strings.Contains(out.String(), "already on the v2") {
		t.Fatalf("output: %q", out.String())
	}
}

// Crash injection: interrupt after each journaled step boundary, then
// recover and assert the full end state.
func TestMigrateCrashRecoveryConverges(t *testing.T) {
	// We simulate "crashed after completing step X" by running the real
	// migration to completion in a scratch env is not possible per-step, so
	// instead: run the real Run(), then for each step, rebuild a fresh env,
	// manually perform the work of the steps BEFORE the crash point exactly
	// as the migrator would, set the journal to the crash step, and recover.
	cases := []struct {
		name    string
		prepare func(e *testenv.Env, m *migrate.Migrator, s *store.Store)
		step    string
	}{
		{
			// Crashed immediately after the journal was written: nothing moved.
			name:    "before_claude_axis",
			step:    migrate.StepClaudeAxis,
			prepare: func(e *testenv.Env, m *migrate.Migrator, s *store.Store) {},
		},
		{
			// Crashed in the worst window: claude half renamed but the live
			// link NOT yet repointed — ~/.claude dangles.
			name: "dangling_claude_link",
			step: migrate.StepClaudeAxis,
			prepare: func(e *testenv.Env, m *migrate.Migrator, s *store.Store) {
				src := filepath.Join(e.P.LegacyContextsDir(), "claude-vertex", "claude")
				dest := e.P.ProfileHome(tool.Claude, "claude-vertex")
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					panic(err)
				}
				if err := os.Rename(src, dest); err != nil {
					panic(err)
				}
				// ~/.claude still points at the old (now gone) path.
			},
		},
		{
			// Crashed after the claude axis completed, before codex.
			name: "before_codex_axis",
			step: migrate.StepCodexAxis,
			prepare: func(e *testenv.Env, m *migrate.Migrator, s *store.Store) {
				src := filepath.Join(e.P.LegacyContextsDir(), "claude-vertex", "claude")
				dest := e.P.ProfileHome(tool.Claude, "claude-vertex")
				os.MkdirAll(filepath.Dir(dest), 0o755)
				if err := os.Rename(src, dest); err != nil {
					panic(err)
				}
				if err := linker.Replace(e.P.ClaudeDir, dest); err != nil {
					panic(err)
				}
				secSrc := filepath.Join(e.P.LegacyContextsDir(), "claude-vertex", "secrets")
				if err := os.Rename(secSrc, e.P.ProfileSecretsDir("claude-vertex")); err != nil {
					panic(err)
				}
			},
		},
		{
			// Crashed right before the final sweep.
			name: "before_finalize",
			step: migrate.StepFinalize,
			prepare: func(e *testenv.Env, m *migrate.Migrator, s *store.Store) {
				// Perform claude+codex axes and move_rest manually via the
				// same primitive: rename each non-empty half.
				for _, mv := range [][2]string{
					{filepath.Join(e.P.LegacyContextsDir(), "claude-vertex", "claude"), e.P.ProfileHome(tool.Claude, "claude-vertex")},
					{filepath.Join(e.P.LegacyContextsDir(), "claude-vertex", "codex"), e.P.ProfileHome(tool.Codex, "claude-vertex")},
					{filepath.Join(e.P.LegacyContextsDir(), "codex-work", "claude"), e.P.ProfileHome(tool.Claude, "codex-work")},
					{filepath.Join(e.P.LegacyContextsDir(), "codex-work", "codex"), e.P.ProfileHome(tool.Codex, "codex-work")},
					{filepath.Join(e.P.LegacyContextsDir(), "claude-vertex", "secrets"), e.P.ProfileSecretsDir("claude-vertex")},
				} {
					os.MkdirAll(filepath.Dir(mv[1]), 0o755)
					if err := os.Rename(mv[0], mv[1]); err != nil {
						panic(err)
					}
				}
				linker.Replace(e.P.ClaudeDir, e.P.ProfileHome(tool.Claude, "claude-vertex"))
				linker.Replace(e.P.CodexDir, e.P.ProfileHome(tool.Codex, "claude-vertex"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testenv.New(t)
			e.BuildV1ContextsTree()
			m, s, _ := newMigrator(e)

			tc.prepare(e, m, s)

			// Plant the journal exactly as a crash at tc.step would leave it.
			// Run() always writes the state backup BEFORE the journal, so any
			// real crash-with-journal scenario has it.
			if err := store.CopyFileAtomic(e.P.StateFile(),
				filepath.Join(e.P.BackupsDir(), "state.v1.test.json"), 0o644); err != nil {
				t.Fatal(err)
			}
			st := &store.State{}
			j := &store.Journal{
				Op: "migrate", Step: tc.step,
				Migrate: &store.MigrateInfo{
					ClaudeFrom: "claude-vertex", CodexFrom: "claude-vertex",
					V1Current: "claude-vertex", V1Previous: "codex-work",
				},
			}
			if err := s.SetJournal(st, j); err != nil {
				t.Fatal(err)
			}

			st, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Recover(st); err != nil {
				t.Fatalf("recovery: %v", err)
			}
			assertMigrated(t, e, s)
		})
	}
}

// Double recovery must not corrupt anything: the second attempt sees no
// journal and refuses.
func TestMigrateDoubleRecovery(t *testing.T) {
	e := testenv.New(t)
	e.BuildV1ContextsTree()
	m, s, _ := newMigrator(e)
	plan, err := m.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Run(plan); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Load()
	if err := m.Recover(st); err == nil {
		t.Fatal("recover with no journal should refuse")
	}
	assertMigrated(t, e, s)
}
