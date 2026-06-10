package doctor_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/adopt"
	"github.com/tlrmchlsmth/claudectx/internal/doctor"
	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
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

func TestV1StateDetected(t *testing.T) {
	e := testenv.New(t)
	e.BuildV1ContextsTree()
	d := doctor.New(e.P, store.New(e.P))
	fs := d.Check()
	if len(fs) != 1 || fs[0].Severity != "error" || !contains(fs[0].Message, "claudectx migrate") {
		t.Fatalf("v1 findings = %+v", fs)
	}
}

func TestStateLinkDriftFixedByTrustingLinks(t *testing.T) {
	e, s, d := setup(t)
	// Point ONE axis's link at a second profile, leaving state stale: the
	// fix must update just that axis.
	if err := s.ScaffoldProfile(tool.Claude, "work"); err != nil {
		t.Fatal(err)
	}
	linker.Replace(e.P.ClaudeDir, e.P.ProfileHome(tool.Claude, "work"))

	out := &bytes.Buffer{}
	if problems := d.Run(out, true); problems != 0 {
		t.Fatalf("fix run left %d problems:\n%s", problems, out.String())
	}
	st, _ := s.Load()
	if st.Claude.Current != "work" {
		t.Fatalf("claude state not updated to follow link: %q", st.Claude.Current)
	}
	if st.Codex.Current != "default" {
		t.Fatalf("codex axis should be untouched: %q", st.Codex.Current)
	}
}

func TestMissingLinkIsFixable(t *testing.T) {
	e, _, d := setup(t)
	os.Remove(e.P.CodexDir)

	out := &bytes.Buffer{}
	if problems := d.Run(out, true); problems != 0 {
		t.Fatalf("fix run left %d problems:\n%s", problems, out.String())
	}
	c, _ := linker.Classify(e.P.CodexDir, e.P.ToolProfilesDir(tool.Codex))
	if c.Kind != linker.ManagedLink || c.Context != "default" {
		t.Fatalf("codex link not restored: %+v", c)
	}
}

func TestLoosePermissionsFixed(t *testing.T) {
	e, _, d := setup(t)
	e.WriteFile(e.P.KeychainStash("default"), `{"password":"x"}`)
	os.Chmod(e.P.KeychainStash("default"), 0o644)
	os.Chmod(e.P.ClaudeJSON, 0o644)

	out := &bytes.Buffer{}
	if problems := d.Run(out, true); problems != 0 {
		t.Fatalf("fix run left %d problems:\n%s", problems, out.String())
	}
	for _, p := range []string{e.P.KeychainStash("default"), e.P.ClaudeJSON} {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s perms = %v after fix", p, fi.Mode().Perm())
		}
	}
}

func TestClobberedDirAndOldCodexDetected(t *testing.T) {
	e, _, d := setup(t)
	// Replace the claude symlink with a real dir, as a misbehaving tool would.
	os.Remove(e.P.ClaudeDir)
	os.MkdirAll(e.P.ClaudeDir, 0o755)
	// And make the codex profile's layout old-style.
	home := e.P.ProfileHome(tool.Codex, "default")
	os.Remove(filepath.Join(home, "config.toml"))
	e.WriteFile(filepath.Join(home, "config.json"), "{}")

	fs := d.Check()
	var hasError, foundOld bool
	for _, f := range fs {
		if f.Severity == "error" {
			hasError = true
		}
		if f.Severity == "info" && contains(f.Message, "old-style layout") {
			foundOld = true
		}
	}
	if !hasError {
		t.Fatalf("clobbered dir not reported as error: %+v", fs)
	}
	if !foundOld {
		t.Fatalf("old codex layout not detected: %+v", fs)
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
