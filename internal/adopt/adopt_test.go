package adopt_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/adopt"
	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
)

func newAdopter(e *testenv.Env) (*adopt.Adopter, *store.Store, *bytes.Buffer) {
	s := store.New(e.P)
	out := &bytes.Buffer{}
	return adopt.New(e.P, s, out), s, out
}

func TestInitHappyPath(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	e.BuildModernCodexTree()
	e.BuildClaudeJSON()
	ad, s, _ := newAdopter(e)

	items, err := ad.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Run(items); err != nil {
		t.Fatal(err)
	}

	// Both live paths must now be managed symlinks into "default".
	for _, p := range []string{e.P.ClaudeDir, e.P.CodexDir} {
		c, err := linker.Classify(p, e.P.ContextsDir())
		if err != nil || c.Kind != linker.ManagedLink || c.Context != "default" {
			t.Fatalf("%s: %+v, %v", p, c, err)
		}
	}
	// Content travelled.
	if _, err := os.Stat(filepath.Join(e.P.CtxClaudeDir("default"), "settings.json")); err != nil {
		t.Fatal("settings.json did not move into context")
	}
	// Resolving through the symlink still works.
	if _, err := os.ReadFile(filepath.Join(e.P.ClaudeDir, "settings.json")); err != nil {
		t.Fatal("cannot read through managed symlink")
	}
	// claude.json was copied, not moved.
	if _, err := os.Stat(e.P.ClaudeJSON); err != nil {
		t.Fatal("live claude.json disappeared")
	}
	if _, err := os.Stat(e.P.CtxClaudeJSON("default")); err != nil {
		t.Fatal("context claude.json copy missing")
	}
	// Backup exists.
	entries, _ := os.ReadDir(e.P.BackupsDir())
	if len(entries) == 0 {
		t.Fatal("no pre-init backup of claude.json")
	}
	// State recorded.
	st, err := s.Load()
	if err != nil || st.Current != "default" || st.InProgress != nil {
		t.Fatalf("state: %+v, %v", st, err)
	}
}

func TestInitMissingCodex(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	// no codex dir at all, no claude.json
	ad, _, _ := newAdopter(e)

	items, err := ad.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Run(items); err != nil {
		t.Fatal(err)
	}
	c, _ := linker.Classify(e.P.CodexDir, e.P.ContextsDir())
	if c.Kind != linker.ManagedLink {
		t.Fatalf("missing codex should become a managed link to an empty dir, got %v", c.Kind)
	}
	// Stub claude.json written into context.
	data, err := os.ReadFile(e.P.CtxClaudeJSON("default"))
	if err != nil || strings.TrimSpace(string(data)) != "{}" {
		t.Fatalf("claude.json stub: %q, %v", data, err)
	}
}

func TestInitRefusesForeignSymlink(t *testing.T) {
	e := testenv.New(t)
	dotfiles := filepath.Join(e.Root, "dotfiles", "claude")
	if err := os.MkdirAll(dotfiles, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, e.P.ClaudeDir); err != nil {
		t.Fatal(err)
	}
	ad, _, _ := newAdopter(e)
	_, err := ad.Plan()
	if err == nil || !strings.Contains(err.Error(), "Refusing") {
		t.Fatalf("expected foreign-symlink refusal, got %v", err)
	}
}

func TestInitIdempotent(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	e.BuildOldCodexTree()
	e.BuildClaudeJSON()
	ad, _, _ := newAdopter(e)

	items, err := ad.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Run(items); err != nil {
		t.Fatal(err)
	}

	// Second run: plan reports already-managed and run is a no-op.
	ad2, _, out2 := newAdopter(e)
	items2, err := ad2.Plan()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items2 {
		if it.Kind != linker.ManagedLink {
			t.Fatalf("re-plan kind = %v, want managed", it.Kind)
		}
	}
	if err := ad2.Run(items2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2.String(), "already initialized") {
		t.Fatalf("expected already-initialized message, got %q", out2.String())
	}
}

func TestInitOldCodexLayoutMovesUntouched(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	e.BuildOldCodexTree()
	ad, _, _ := newAdopter(e)
	items, _ := ad.Plan()
	if err := ad.Run(items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.P.CtxCodexDir("default"), "config.json")); err != nil {
		t.Fatal("old codex config.json should move untouched")
	}
}

// Crash mid-init: claude moved but codex not, journal set. Re-running init
// must complete the remaining moves.
func TestInitResumeAfterCrash(t *testing.T) {
	e := testenv.New(t)
	e.BuildClaudeTree()
	e.BuildModernCodexTree()
	e.BuildClaudeJSON()
	s := store.New(e.P)

	// Simulate the half-done state: claude already adopted, codex untouched.
	if err := s.ScaffoldContext("default"); err != nil {
		t.Fatal(err)
	}
	os.Remove(e.P.CtxClaudeDir("default"))
	if err := os.Rename(e.P.ClaudeDir, e.P.CtxClaudeDir("default")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.P.CtxClaudeDir("default"), e.P.ClaudeDir); err != nil {
		t.Fatal(err)
	}
	st := &store.State{Current: "default"}
	if err := s.SetJournal(st, &store.Journal{Op: "init", To: "default", Step: "move"}); err != nil {
		t.Fatal(err)
	}

	ad, _, _ := newAdopter(e)
	items, err := ad.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Run(items); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{e.P.ClaudeDir, e.P.CodexDir} {
		c, _ := linker.Classify(p, e.P.ContextsDir())
		if c.Kind != linker.ManagedLink {
			t.Fatalf("%s not managed after resume: %v", p, c.Kind)
		}
	}
	stFinal, _ := s.Load()
	if stFinal.InProgress != nil {
		t.Fatal("journal not cleared after resumed init")
	}
}
