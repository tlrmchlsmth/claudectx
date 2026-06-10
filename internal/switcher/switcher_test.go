package switcher_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/adopt"
	"github.com/tlrmchlsmth/claudectx/internal/keychain"
	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/switcher"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

// world sets up an initialized env with extra profiles on both axes.
type world struct {
	e  *testenv.Env
	s  *store.Store
	kc *keychain.Fake
	sw *switcher.Switcher
}

func setup(t *testing.T) *world {
	t.Helper()
	e := testenv.New(t)
	e.BuildClaudeTree()
	e.BuildModernCodexTree()
	e.BuildClaudeJSON()
	// Keychain handling on in tests, via the fake.
	e.P.KeychainEnabled = true

	s := store.New(e.P)
	ad := adopt.New(e.P, s, &bytes.Buffer{})
	items, err := ad.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.Run(items); err != nil {
		t.Fatal(err)
	}

	// Second profile per axis with distinguishable content.
	if err := s.ScaffoldProfile(tool.Claude, "work"); err != nil {
		t.Fatal(err)
	}
	e.WriteFile(filepath.Join(e.P.ProfileHome(tool.Claude, "work"), "settings.json"), `{"model":"work-model"}`)
	e.WriteFile(e.P.ProfileClaudeJSON("work"), `{"work":true}`)
	if err := s.ScaffoldProfile(tool.Codex, "personal"); err != nil {
		t.Fatal(err)
	}
	e.WriteFile(filepath.Join(e.P.ProfileHome(tool.Codex, "personal"), "config.toml"), `model = "personal-model"`)

	kc := &keychain.Fake{Cred: &keychain.Credential{
		Service: keychain.Service, Account: "default@example.com", Password: "tok-default",
	}, FailOn: map[string]error{}}

	return &world{e: e, s: s, kc: kc, sw: switcher.New(e.P, s, kc, &bytes.Buffer{})}
}

func (w *world) assertAxis(t *testing.T, tl tool.Tool, profile string) {
	t.Helper()
	st, err := w.s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Axis(tl).Current != profile {
		t.Fatalf("%s current = %q, want %q", tl, st.Axis(tl).Current, profile)
	}
	if st.InProgress != nil {
		t.Fatalf("journal not cleared: %+v", st.InProgress)
	}
	c, _ := linker.Classify(w.e.P.LiveDir(tl), w.e.P.ToolProfilesDir(tl))
	if c.Kind != linker.ManagedLink || c.Context != profile {
		t.Fatalf("%s link -> %+v, want managed link to %q", tl, c, profile)
	}
}

func TestClaudeSwitchHappyPath(t *testing.T) {
	w := setup(t)

	if err := w.sw.Switch(tool.Claude, "work"); err != nil {
		t.Fatal(err)
	}
	w.assertAxis(t, tool.Claude, "work")

	// Outgoing profile captured the live claude.json and the keychain cred.
	var stash keychain.Credential
	data, err := os.ReadFile(w.e.P.KeychainStash("default"))
	if err != nil {
		t.Fatal("no keychain stash for outgoing profile")
	}
	if fi, _ := os.Stat(w.e.P.KeychainStash("default")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("stash perms = %v", fi.Mode().Perm())
	}
	json.Unmarshal(data, &stash)
	if stash.Password != "tok-default" {
		t.Fatalf("stash = %+v", stash)
	}

	// Incoming profile's claude.json went live.
	live, _ := os.ReadFile(w.e.P.ClaudeJSON)
	if !strings.Contains(string(live), `"work"`) {
		t.Fatalf("live claude.json = %s", live)
	}

	// Target had no stash: keychain item must be deleted (no token leak).
	if w.kc.Cred != nil {
		t.Fatal("keychain item leaked into profile without stored credentials")
	}

	// Reading through the live symlink shows work's settings.
	settings, _ := os.ReadFile(filepath.Join(w.e.P.ClaudeDir, "settings.json"))
	if !strings.Contains(string(settings), "work-model") {
		t.Fatalf("settings through link = %s", settings)
	}
}

// The whole point of v2: switching one axis must not touch the other.
func TestAxisIndependence(t *testing.T) {
	w := setup(t)
	codexBefore, err := os.Readlink(w.e.P.CodexDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.sw.Switch(tool.Claude, "work"); err != nil {
		t.Fatal(err)
	}
	codexAfter, _ := os.Readlink(w.e.P.CodexDir)
	if codexAfter != codexBefore {
		t.Fatalf("claude switch moved the codex link: %q -> %q", codexBefore, codexAfter)
	}
	st, _ := w.s.Load()
	if st.Codex.Current != "default" {
		t.Fatalf("claude switch changed codex state: %+v", st.Codex)
	}

	// And the reverse: codex switch leaves claude alone (incl. keychain).
	w.kc.Cred = &keychain.Credential{Service: keychain.Service, Account: "x", Password: "tok-x"}
	claudeBefore, _ := os.Readlink(w.e.P.ClaudeDir)
	liveBefore, _ := os.ReadFile(w.e.P.ClaudeJSON)
	if err := w.sw.Switch(tool.Codex, "personal"); err != nil {
		t.Fatal(err)
	}
	w.assertAxis(t, tool.Codex, "personal")
	claudeAfter, _ := os.Readlink(w.e.P.ClaudeDir)
	liveAfter, _ := os.ReadFile(w.e.P.ClaudeJSON)
	if claudeAfter != claudeBefore {
		t.Fatal("codex switch moved the claude link")
	}
	if string(liveAfter) != string(liveBefore) {
		t.Fatal("codex switch rewrote the live claude.json")
	}
	if w.kc.Cred == nil || w.kc.Cred.Password != "tok-x" {
		t.Fatalf("codex switch touched the keychain: %+v", w.kc.Cred)
	}
}

func TestCodexSwitchCarriesAuth(t *testing.T) {
	w := setup(t)
	// default's codex auth.json came from BuildModernCodexTree via adopt.
	if err := w.sw.Switch(tool.Codex, "personal"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.e.P.CodexDir, "auth.json")); err == nil {
		t.Fatal("personal profile should have no auth.json through the link")
	}
	if err := w.sw.Switch(tool.Codex, "default"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(w.e.P.CodexDir, "auth.json"))
	if err != nil || !strings.Contains(string(data), "sk-test") {
		t.Fatalf("default's auth.json not visible after switch back: %s, %v", data, err)
	}
}

func TestClaudeSwitchBackRestoresCredentials(t *testing.T) {
	w := setup(t)
	if err := w.sw.Switch(tool.Claude, "work"); err != nil {
		t.Fatal(err)
	}
	// Log in as a different account in "work".
	w.kc.Cred = &keychain.Credential{Service: keychain.Service, Account: "work@example.com", Password: "tok-work"}

	if err := w.sw.Switch(tool.Claude, "default"); err != nil {
		t.Fatal(err)
	}
	w.assertAxis(t, tool.Claude, "default")
	if w.kc.Cred == nil || w.kc.Cred.Password != "tok-default" {
		t.Fatalf("default credentials not restored: %+v", w.kc.Cred)
	}
	// And work's stash now holds the work token.
	data, _ := os.ReadFile(w.e.P.KeychainStash("work"))
	if !strings.Contains(string(data), "tok-work") {
		t.Fatalf("work stash = %s", data)
	}
}

func TestSwitchPreflight(t *testing.T) {
	w := setup(t)
	if err := w.sw.Switch(tool.Claude, "default"); !errors.Is(err, switcher.ErrSameProfile) {
		t.Fatalf("same-profile switch: %v", err)
	}
	if err := w.sw.Switch(tool.Claude, "missing"); err == nil || !strings.Contains(err.Error(), "no such claude profile") {
		t.Fatalf("missing profile: %v", err)
	}
	// "work" exists on claude but not codex — axes are separate namespaces.
	if err := w.sw.Switch(tool.Codex, "work"); err == nil || !strings.Contains(err.Error(), "no such codex profile") {
		t.Fatalf("cross-axis profile: %v", err)
	}
	// Clobber the claude link with a real dir: switch must refuse.
	os.Remove(w.e.P.ClaudeDir)
	os.MkdirAll(w.e.P.ClaudeDir, 0o755)
	if err := w.sw.Switch(tool.Claude, "work"); err == nil || !strings.Contains(err.Error(), "doctor") {
		t.Fatalf("clobbered link: %v", err)
	}
	// But the codex axis is unaffected by claude's clobbered link.
	if err := w.sw.Switch(tool.Codex, "personal"); err != nil {
		t.Fatalf("codex switch should not depend on the claude link: %v", err)
	}
}

// Crash injection: fail at keychain steps, then clear the failure and
// assert recovery converges to the target.
func TestClaudeSwitchCrashRecoveryConverges(t *testing.T) {
	for _, failStep := range []string{"read", "delete"} {
		t.Run("keychain_"+failStep, func(t *testing.T) {
			w := setup(t)
			w.kc.FailOn[failStep] = errors.New("injected keychain failure")

			err := w.sw.Switch(tool.Claude, "work")
			if err == nil {
				t.Fatal("expected injected failure")
			}
			st, _ := w.s.Load()
			if st.InProgress == nil {
				t.Fatal("journal should remain set after mid-switch failure")
			}

			// "Reboot": clear the failure, run recovery.
			delete(w.kc.FailOn, failStep)
			st, _ = w.s.Load()
			if err := w.sw.Recover(st); err != nil {
				t.Fatal(err)
			}
			w.assertAxis(t, tool.Claude, "work")
		})
	}
}

// Simulated power-loss after the links step: journal says keychain_in is
// next. Recovery must finish the job.
func TestClaudeRecoveryFromLinksStep(t *testing.T) {
	w := setup(t)

	st, _ := w.s.Load()
	if err := w.s.SetJournal(st, &store.Journal{Op: "switch", Tool: "claude", From: "default", To: "work", Step: switcher.StepLinks}); err != nil {
		t.Fatal(err)
	}
	st, _ = w.s.Load()
	if err := w.sw.Recover(st); err != nil {
		t.Fatal(err)
	}
	w.assertAxis(t, tool.Claude, "work")
}

// A codex switch interrupted before its single (links) step ran.
func TestCodexRecovery(t *testing.T) {
	w := setup(t)
	st, _ := w.s.Load()
	if err := w.s.SetJournal(st, &store.Journal{Op: "switch", Tool: "codex", From: "default", To: "personal", Step: switcher.StepLinks}); err != nil {
		t.Fatal(err)
	}
	st, _ = w.s.Load()
	if err := w.sw.Recover(st); err != nil {
		t.Fatal(err)
	}
	w.assertAxis(t, tool.Codex, "personal")
	// claude untouched.
	w.assertAxis(t, tool.Claude, "default")
}

// Recovery is idempotent: a second attempt (no journal left) must refuse
// gracefully without corrupting anything.
func TestRecoveryIdempotent(t *testing.T) {
	w := setup(t)
	st, _ := w.s.Load()
	if err := w.s.SetJournal(st, &store.Journal{Op: "switch", Tool: "claude", From: "default", To: "work", Step: switcher.StepStash}); err != nil {
		t.Fatal(err)
	}
	st, _ = w.s.Load()
	if err := w.sw.Recover(st); err != nil {
		t.Fatal(err)
	}
	w.assertAxis(t, tool.Claude, "work")
	st, _ = w.s.Load()
	if err := w.sw.Recover(st); err == nil {
		t.Fatal("recover with no journal should error, not re-run")
	}
	w.assertAxis(t, tool.Claude, "work")
}

func TestSwitchWithKeychainDisabled(t *testing.T) {
	w := setup(t)
	w.e.P.KeychainEnabled = false
	sw := switcher.New(w.e.P, w.s, keychain.Null{}, &bytes.Buffer{})
	if err := sw.Switch(tool.Claude, "work"); err != nil {
		t.Fatal(err)
	}
	// Null backend: no stash written for outgoing profile.
	if _, err := os.Stat(w.e.P.KeychainStash("default")); err == nil {
		t.Fatal("stash file written despite Null keychain")
	}
}
