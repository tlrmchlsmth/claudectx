package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
)

func TestValidateName(t *testing.T) {
	valid := []string{"work", "personal", "client-x", "a", "Foo_bar.2", "x1234567890"}
	for _, n := range valid {
		if err := store.ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{"", "-lead", ".lead", "has space", "a/b", "list", "delete", "switch", "version",
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

	st := &store.State{Current: "default"}
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
	if got.Current != "default" || got.Version != 1 || got.InProgress != nil {
		t.Fatalf("unexpected state: %+v", got)
	}

	if err := s.SetJournal(got, &store.Journal{Op: "switch", From: "default", To: "work", Step: "stash"}); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Load()
	if got2.InProgress == nil || got2.InProgress.To != "work" || got2.InProgress.StartedAt == "" {
		t.Fatalf("journal not persisted: %+v", got2.InProgress)
	}
}

func TestContextCRUD(t *testing.T) {
	e := testenv.New(t)
	s := store.New(e.P)

	names, err := s.List()
	if err != nil || len(names) != 0 {
		t.Fatalf("List on empty = %v, %v", names, err)
	}

	if err := s.ScaffoldContext("work"); err != nil {
		t.Fatal(err)
	}
	if err := s.ScaffoldContext("personal"); err != nil {
		t.Fatal(err)
	}
	names, _ = s.List()
	if len(names) != 2 || names[0] != "personal" || names[1] != "work" {
		t.Fatalf("List = %v", names)
	}
	if !s.Exists("work") || s.Exists("nope") {
		t.Fatal("Exists wrong")
	}

	fi, err := os.Stat(e.P.CtxSecretsDir("work"))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("secrets dir perms = %v, %v", fi.Mode().Perm(), err)
	}

	dst, err := s.Trash("personal")
	if err != nil {
		t.Fatal(err)
	}
	if s.Exists("personal") {
		t.Fatal("personal still exists after Trash")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("trashed dir missing: %v", err)
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
