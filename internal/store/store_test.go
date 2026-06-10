package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

func TestValidateName(t *testing.T) {
	valid := []string{"work", "personal", "client-x", "a", "Foo_bar.2", "x1234567890"}
	for _, n := range valid {
		if err := store.ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", "-lead", ".lead", "has space", "a/b", "list", "delete", "switch", "version",
		"claude", "codex", "migrate",
		"waytoolongname" + string(make([]byte, 64))}
	for _, n := range invalid {
		if err := store.ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	e := testenv.New(t)
	s := store.New(e.P)

	if s.Initialized() {
		t.Fatal("fresh env reports initialized")
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("Load on uninitialized env should error")
	}

	st := &store.State{
		Claude: store.AxisState{Current: "vertex"},
		Codex:  store.AxisState{Current: "work"},
	}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	if !s.Initialized() {
		t.Fatal("not initialized after Save")
	}

	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Claude.Current != "vertex" || got.Codex.Current != "work" || got.Version != 2 || got.InProgress != nil {
		t.Fatalf("unexpected state: %+v", got)
	}
	if got.Axis(tool.Claude).Current != "vertex" || got.Axis(tool.Codex).Current != "work" {
		t.Fatal("Axis accessor wrong")
	}

	if err := s.SetJournal(got, &store.Journal{Op: "switch", Tool: "claude", From: "vertex", To: "personal", Step: "stash"}); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Load()
	if got2.InProgress == nil || got2.InProgress.To != "personal" || got2.InProgress.Tool != "claude" || got2.InProgress.StartedAt == "" {
		t.Fatalf("journal not persisted: %+v", got2.InProgress)
	}
}

func TestLoadDetectsV1State(t *testing.T) {
	e := testenv.New(t)
	e.WriteFile(e.P.StateFile(), `{"version":1,"current":"claude-vertex","previous":"codex-work","in_progress":null}`)
	s := store.New(e.P)

	if _, err := s.Load(); !errors.Is(err, store.ErrV1State) {
		t.Fatalf("Load on v1 state = %v, want ErrV1State", err)
	}
	// LoadV1 reads the legacy shape.
	v1, err := s.LoadV1()
	if err != nil || v1.Current != "claude-vertex" || v1.Previous != "codex-work" {
		t.Fatalf("LoadV1 = %+v, %v", v1, err)
	}
}

func TestProfileCRUD(t *testing.T) {
	e := testenv.New(t)
	s := store.New(e.P)

	for _, tl := range tool.All {
		names, err := s.List(tl)
		if err != nil || len(names) != 0 {
			t.Fatalf("List(%s) on empty = %v, %v", tl, names, err)
		}
	}

	if err := s.ScaffoldProfile(tool.Claude, "vertex"); err != nil {
		t.Fatal(err)
	}
	if err := s.ScaffoldProfile(tool.Claude, "personal"); err != nil {
		t.Fatal(err)
	}
	if err := s.ScaffoldProfile(tool.Codex, "work"); err != nil {
		t.Fatal(err)
	}

	names, _ := s.List(tool.Claude)
	if len(names) != 2 || names[0] != "personal" || names[1] != "vertex" {
		t.Fatalf("List(claude) = %v", names)
	}
	names, _ = s.List(tool.Codex)
	if len(names) != 1 || names[0] != "work" {
		t.Fatalf("List(codex) = %v", names)
	}
	// Axes are independent namespaces.
	if !s.Exists(tool.Claude, "vertex") || s.Exists(tool.Codex, "vertex") {
		t.Fatal("Exists not per-tool")
	}

	// Claude profiles get a secrets dir; codex profiles don't.
	fi, err := os.Stat(e.P.ProfileSecretsDir("vertex"))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("claude secrets dir: %v, %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(e.P.ProfileDir(tool.Codex, "work"), "secrets")); err == nil {
		t.Fatal("codex profile should have no secrets dir")
	}

	dst, err := s.Trash(tool.Claude, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if s.Exists(tool.Claude, "personal") {
		t.Fatal("personal still exists after Trash")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("trashed dir missing: %v", err)
	}
	if filepath.Base(dst)[:7] != "claude." {
		t.Fatalf("trash name should be tool-prefixed: %s", dst)
	}
}

func TestWriteFileAtomicPerms(t *testing.T) {
	e := testenv.New(t)
	path := filepath.Join(e.Root, "secret.json")
	if err := store.WriteFileAtomic(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perms = %v, want 0600", fi.Mode().Perm())
	}
	// Overwrite keeps working and leaves no temp litter.
	if err := store.WriteFileAtomic(path, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(e.Root)
	for _, en := range entries {
		if len(en.Name()) > 4 && en.Name()[:4] == ".tmp" {
			t.Fatalf("temp file left behind: %s", en.Name())
		}
	}
}
