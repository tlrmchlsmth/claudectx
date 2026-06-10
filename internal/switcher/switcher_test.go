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
)

// world sets up an initialized env with contexts "default" and "work".
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

	// Second context with distinguishable content.
	if err := s.ScaffoldContext("work"); err != nil {
		t.Fatal(err)
	}
	e.WriteFile(filepath.Join(e.P.CtxClaudeDir("work"), "settings.json"), `{"model":"work-model"}`)
	e.WriteFile(e.P.CtxClaudeJSON("work"), `{"work":true}`)

	kc := &keychain.Fake{Cred: &keychain.Credential{
		Service: keychain.Service, Account: "default@example.com", Password: "tok-default",
	}, FailOn: map[string]error{}}

	return &world{e: e, s: s, kc: kc, sw: switcher.New(e.P, s, kc, &bytes.Buffer{})}
}

func (w *world) assertOn(t *testing.T, ctx string) {
	t.Helper()
	st, err := w.s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Current != ctx {
		t.Fatalf("state current = %q, want %q", st.Current, ctx)
	}
	if st.InProgress != nil {
		t.Fatalf("journal not cleared: %+v", st.InProgress)
	}
	for _, p := range []string{w.e.P.ClaudeDir, w.e.P.CodexDir} {
		c, _ := linker.Classify(p, w.e.P.ContextsDir())
		if c.Kind != linker.ManagedLink || c.Context != ctx {
			t.Fatalf("%s -> %+v, want managed link to %q", p, c, ctx)
		}
	}
}

func TestSwitchHappyPath(t *testing.T) {
	w := setup(t)

	if err := w.sw.Switch("work"); err != nil {
		t.Fatal(err)
	}
	w.assertOn(t, "work")

	// Outgoing context captured the live claude.json and the keychain cred.
	var stash keychain.Credential
	data, err := os.ReadFile(w.e.P.CtxKeychainStash("default"))
	if err != nil {
		t.Fatal("no keychain stash for outgoing context")
	}
	if fi, _ := os.Stat(w.e.P.CtxKeychainStash("default")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("stash perms = %v", fi.Mode().Perm())
	}
	json.Unmarshal(data, &stash)
	if stash.Password != "tok-default" {
		t.Fatalf("stash = %+v", stash)
	}

	// Incoming context's claude.json went live.
	live, _ := os.ReadFile(w.e.P.ClaudeJSON)
	if !strings.Contains(string(live), `"work"`) {
		t.Fatalf("live claude.json = %s", live)
	}

	// Target had no stash: keychain item must be deleted (no token leak).
	if w.kc.Cred != nil {
		t.Fatal("keychain item leaked into context without stored credentials")
	}

	// Reading through the live symlink shows work's settings.
	settings, _ := os.ReadFile(filepath.Join(w.e.P.ClaudeDir, "settings.json"))
	if !strings.Contains(string(settings), "work-model") {
		t.Fatalf("settings through link = %s", settings)
	}
}

func TestSwitchBackRestoresCredentials(t *testing.T) {
	w := setup(t)
	if err := w.sw.Switch("work"); err != nil {
		t.Fatal(err)
	}
	// Log in as a different account in "work".
	w.kc.Cred = &keychain.Credential{Service: keychain.Service, Account: "work@example.com", Password: "tok-work"}

	if err := w.sw.Switch("default"); err != nil {
		t.Fatal(err)
	}
	w.assertOn(t, "default")
	if w.kc.Cred == nil || w.kc.Cred.Password != "tok-default" {
		t.Fatalf("default credentials not restored: %+v", w.kc.Cred)
	}
	// And work's stash now holds the work token.
	data, _ := os.ReadFile(w.e.P.CtxKeychainStash("work"))
	if !strings.Contains(string(data), "tok-work") {
		t.Fatalf("work stash = %s", data)
	}
}

func TestSwitchPreflight(t *testing.T) {
	w := setup(t)
	if err := w.sw.Switch("default"); !errors.Is(err, switcher.ErrSameContext) {
		t.Fatalf("same-context switch: %v", err)
	}
	if err := w.sw.Switch("missing"); err == nil || !strings.Contains(err.Error(), "no such context") {
		t.Fatalf("missing context: %v", err)
	}
	// Clobber the claude link with a real dir: switch must refuse.
	os.Remove(w.e.P.ClaudeDir)
	os.MkdirAll(w.e.P.ClaudeDir, 0o755)
	if err := w.sw.Switch("work"); err == nil || !strings.Contains(err.Error(), "doctor") {
		t.Fatalf("clobbered link: %v", err)
	}
}

// Crash injection: fail at each journaled step, then clear the failure and
// assert recovery converges to the target.
func TestSwitchCrashRecoveryConverges(t *testing.T) {
	for _, failStep := range []string{"read", "delete"} {
		t.Run("keychain_"+failStep, func(t *testing.T) {
			w := setup(t)
			w.kc.FailOn[failStep] = errors.New("injected keychain failure")

			err := w.sw.Switch("work")
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
			w.assertOn(t, "work")
		})
	}
}

// Simulated power-loss after the links step: journal says keychain_in is
// next, links already point at the target. Recovery must finish the job
// without redoing damage.
func TestSwitchRecoveryFromLinksStep(t *testing.T) {
	w := setup(t)

	// Manually do steps 1-3 the way a crashed switcher would have.
	st, _ := w.s.Load()
	if err := w.s.SetJournal(st, &store.Journal{Op: "switch", From: "default", To: "work", Step: switcher.StepLinks}); err != nil {
		t.Fatal(err)
	}
	if err := linker.Replace(w.e.P.ClaudeDir, w.e.P.CtxClaudeDir("work")); err != nil {
		t.Fatal(err)
	}
	// codex link intentionally NOT repointed: mixed state.

	st, _ = w.s.Load()
	if err := w.sw.Recover(st); err != nil {
		t.Fatal(err)
	}
	w.assertOn(t, "work")
}

// Recovery is idempotent: running it twice (e.g. two concurrent shells both
// noticing the journal) must not corrupt anything.
func TestRecoveryIdempotent(t *testing.T) {
	w := setup(t)
	st, _ := w.s.Load()
	if err := w.s.SetJournal(st, &store.Journal{Op: "switch", From: "default", To: "work", Step: switcher.StepStash}); err != nil {
		t.Fatal(err)
	}
	st, _ = w.s.Load()
	if err := w.sw.Recover(st); err != nil {
		t.Fatal(err)
	}
	w.assertOn(t, "work")
	// Second recovery attempt: journal is gone, so it must refuse gracefully.
	st, _ = w.s.Load()
	if err := w.sw.Recover(st); err == nil {
		t.Fatal("recover with no journal should error, not re-run")
	}
	w.assertOn(t, "work")
}

func TestSwitchWithKeychainDisabled(t *testing.T) {
	w := setup(t)
	w.e.P.KeychainEnabled = false
	sw := switcher.New(w.e.P, w.s, keychain.Null{}, &bytes.Buffer{})
	if err := sw.Switch("work"); err != nil {
		t.Fatal(err)
	}
	// Null backend: no stash written for outgoing context.
	if _, err := os.Stat(w.e.P.CtxKeychainStash("default")); err == nil {
		t.Fatal("stash file written despite Null keychain")
	}
}
