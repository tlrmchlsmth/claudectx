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
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

func newAdopter(e *testenv.Env) (*adopt.Adopter, *store.Store, *bytes.Buffer) {
	s := store.New(e.P)
	out := &bytes.Buffer{}
	return adopt.New(e.P, s, out), s, out
}

func assertManaged(t *testing.T, e *testenv.Env, tl tool.Tool, profile string) {
	t.Helper()
	c, err := linker.Classify(e.P.LiveDir(tl), e.P.ToolProfilesDir(tl))
	if err != nil || c.Kind != linker.ManagedLink || c.Context != profile {
		t.Fatalf("%s live: %+v, %v", tl, c, err)
	}
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

	// Both live paths must now be managed symlinks into per-tool "default".
	assertManaged(t, e, tool.Claude, adopt.DefaultProfile)
	assertManaged(t, e, tool.Codex, adopt.DefaultProfile)

	// Content travelled to the per-tool homes.
	if _, err := os.Stat(filepath.Join(e.P.ProfileHome(tool.Claude, "default"), "settings.json")); err != nil {
		t.Fatal("settings.json did not move into the claude profile")
	}
	if _, err := os.Stat(filepath.Join(e.P.ProfileHome(tool.Codex, "default"), "auth.json")); err != nil {
		t.Fatal("auth.json did not move into the codex profile")
	}
	// Resolving through the symlink still works.
	if _, err := os.ReadFile(filepath.Join(e.P.ClaudeDir, "settings.json")); err != nil {
		t.Fatal("cannot read through managed symlink")
	}
	// claude.json was copied, not moved.
	if _, err := os.Stat(e.P.ClaudeJSON); err != nil {
		t.Fatal("live claude.json disappeared")
	}
	if _, err := os.Stat(e.P.ProfileClaudeJSON("default")); err != nil {
		t.Fatal("profile claude.json copy missing")
	}
	// Backup exists.
	entries, _ := os.ReadDir(e.P.BackupsDir())
	if len(entries) == 0 {
		t.Fatal("no pre-init backup of claude.json")
	}
	// State recorded for both axes.
	st, err := s.Load()
	if err != nil || st.Claude.Current != "default" || st.Codex.Current != "default" || st.InProgress != nil {
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
	assertManaged(t, e, tool.Codex, adopt.DefaultProfile)
	// Stub claude.json written into the claude profile.
	data, err := os.ReadFile(e.P.ProfileClaudeJSON("default"))
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

func TestInitRefusesV1State(t *testing.T) {
	e := testenv.New(t)
	e.BuildV1ContextsTree()
	ad, _, _ := newAdopter(e)
	// Plan classifies the live links: they point into contexts/, which is
	// foreign relative to the profiles root — init must not adopt them.
	if _, err := ad.Plan(); err == nil {
		t.Fatal("init on a v1 install should refuse (migrate is the path)")
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
	if err := s.ScaffoldProfile(tool.Claude, adopt.DefaultProfile); err != nil {
		t.Fatal(err)
	}
	os.Remove(e.P.ProfileHome(tool.Claude, "default"))
	if err := os.Rename(e.P.ClaudeDir, e.P.ProfileHome(tool.Claude, "default")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.P.ProfileHome(tool.Claude, "default"), e.P.ClaudeDir); err != nil {
		t.Fatal(err)
	}
	st := &store.State{Claude: store.AxisState{Current: "default"}, Codex: store.AxisState{Current: "default"}}
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
	assertManaged(t, e, tool.Claude, "default")
	assertManaged(t, e, tool.Codex, "default")
	stFinal, _ := s.Load()
	if stFinal.InProgress != nil {
		t.Fatal("journal not cleared after resumed init")
	}
}
