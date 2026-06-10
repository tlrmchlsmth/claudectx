package doctor_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/adopt"
	"github.com/tlrmchlsmth/claudectx/internal/doctor"
	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
)

func setup(t *testing.T) (*testenv.Env, *store.Store, *doctor.Doctor) {
	t.Helper()
	e := testenv.New(t)
	e.BuildClaudeTree()
	e.BuildModernCodexTree()
	e.BuildClaudeJSON()
	s := store.New(e.P)
	ad := adopt.New(e.P, s, &bytes.Buffer{})
	items, err := ad.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Run(items); err != nil {
		t.Fatal(err)
	}
	return e, s, doctor.New(e.P, s)
}

func severities(fs []doctor.Finding) map[string]int {
	m := map[string]int{}
	for _, f := range fs {
		m[f.Severity]++
	}
	return m
}

func TestHealthySystem(t *testing.T) {
	_, _, d := setup(t)
	out := &bytes.Buffer{}
	if problems := d.Run(out, false); problems != 0 {
		t.Fatalf("healthy system reported %d problems:\n%s", problems, out.String())
	}
}

func TestUninitialized(t *testing.T) {
	e := testenv.New(t)
	d := doctor.New(e.P, store.New(e.P))
	fs := d.Check()
	if len(fs) != 1 || fs[0].Severity != "info" {
		t.Fatalf("findings = %+v", fs)
	}
}

func TestStateLinkDriftFixedByTrustingLinks(t *testing.T) {
	e, s, d := setup(t)
	// Create a second context and point the links at it manually, leaving
	// state.json stale.
	if err := s.ScaffoldContext("work"); err != nil {
		t.Fatal(err)
	}
	linker.Replace(e.P.ClaudeDir, e.P.CtxClaudeDir("work"))
	linker.Replace(e.P.CodexDir, e.P.CtxCodexDir("work"))

	out := &bytes.Buffer{}
	if problems := d.Run(out, true); problems != 0 {
		t.Fatalf("fix run left %d problems:\n%s", problems, out.String())
	}
	st, _ := s.Load()
	if st.Current != "work" {
		t.Fatalf("state not updated to follow links: %q", st.Current)
	}
}

func TestMissingLinkIsFixable(t *testing.T) {
	e, _, d := setup(t)
	os.Remove(e.P.CodexDir)

	out := &bytes.Buffer{}
	if problems := d.Run(out, true); problems != 0 {
		t.Fatalf("fix run left %d problems:\n%s", problems, out.String())
	}
	c, _ := linker.Classify(e.P.CodexDir, e.P.ContextsDir())
	if c.Kind != linker.ManagedLink || c.Context != "default" {
		t.Fatalf("codex link not restored: %+v", c)
	}
}

func TestLoosePermissionsFixed(t *testing.T) {
	e, _, d := setup(t)
	e.WriteFile(e.P.CtxKeychainStash("default"), `{"password":"x"}`)
	os.Chmod(e.P.CtxKeychainStash("default"), 0o644)
	os.Chmod(e.P.ClaudeJSON, 0o644)

	out := &bytes.Buffer{}
	if problems := d.Run(out, true); problems != 0 {
		t.Fatalf("fix run left %d problems:\n%s", problems, out.String())
	}
	for _, p := range []string{e.P.CtxKeychainStash("default"), e.P.ClaudeJSON} {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s perms = %v after fix", p, fi.Mode().Perm())
		}
	}
}

func TestClobberedDirAndOldCodexDetected(t *testing.T) {
	e, s, d := setup(t)
	// Replace the claude symlink with a real dir, as a misbehaving tool would.
	os.Remove(e.P.ClaudeDir)
	os.MkdirAll(e.P.ClaudeDir, 0o755)
	// And make the context's codex layout old-style.
	os.Remove(e.P.CtxCodexDir("default") + "/config.toml")
	e.WriteFile(e.P.CtxCodexDir("default")+"/config.json", "{}")

	fs := d.Check()
	sev := severities(fs)
	if sev["error"] == 0 {
		t.Fatalf("clobbered dir not reported as error: %+v", fs)
	}
	foundOld := false
	for _, f := range fs {
		if f.Severity == "info" && contains(f.Message, "old-style codex layout") {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatalf("old codex layout not detected: %+v", fs)
	}
	_ = s
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
