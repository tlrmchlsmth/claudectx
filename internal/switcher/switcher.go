// Package switcher implements the journaled per-axis profile switch and its
// crash recovery. Every step is idempotent; an interrupted switch is rolled
// forward to the journaled target by the next invocation.
package switcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/tlrmchlsmth/claudectx/internal/keychain"
	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

// Step names recorded in the journal, in execution order.
//
// The claude-axis order is what makes an interrupted switch recoverable:
//
//  1. stash / claude_json_out capture the OUTGOING profile's volatile state
//     (keychain token, live ~/.claude.json). Nothing has moved yet, so a
//     failure here aborts cleanly with the system untouched.
//  2. links is the commit point: the axis symlink repoints to the target
//     atomically (rename(2)).
//  3. claude_json_in / keychain_in install the INCOMING profile's state.
//     They only read from the (already captured) target profile, so they
//     can be replayed any number of times.
//
// The codex axis has no out-of-dir state — auth.json lives inside the
// symlinked dir — so its switch is the links step alone.
//
// Every step must stay idempotent: recovery re-runs from the journaled step,
// and two racing recoveries must converge on the same end state.
const (
	StepStash         = "stash"
	StepClaudeJSONOut = "claude_json_out"
	StepLinks         = "links"
	StepClaudeJSONIn  = "claude_json_in"
	StepKeychainIn    = "keychain_in"
)

var (
	stepsClaude = []string{StepStash, StepClaudeJSONOut, StepLinks, StepClaudeJSONIn, StepKeychainIn}
	stepsCodex  = []string{StepLinks}
)

func stepsFor(t tool.Tool) []string {
	if t == tool.Claude {
		return stepsClaude
	}
	return stepsCodex
}

type Switcher struct {
	P  paths.Paths
	S  *store.Store
	KC keychain.Backend
	// Out receives progress / advisory messages.
	Out io.Writer
}

func New(p paths.Paths, s *store.Store, kc keychain.Backend, out io.Writer) *Switcher {
	return &Switcher{P: p, S: s, KC: kc, Out: out}
}

// Preflight errors that callers turn into user guidance.
var (
	ErrSameProfile = errors.New("already on that profile")
)

// Switch moves one axis from its current profile to target.
func (sw *Switcher) Switch(t tool.Tool, target string) error {
	st, err := sw.S.Load()
	if err != nil {
		return err
	}
	if st.InProgress != nil {
		return fmt.Errorf("an interrupted %q operation needs recovery — run any claudectx command to recover first", st.InProgress.Op)
	}
	axis := st.Axis(t)
	if target == axis.Current {
		return ErrSameProfile
	}
	if !sw.S.Exists(t, target) {
		return fmt.Errorf("no such %s profile %q (see `claudectx %s`)", t, target, t)
	}
	live := sw.P.LiveDir(t)
	c, err := linker.Classify(live, sw.P.ToolProfilesDir(t))
	if err != nil {
		return err
	}
	if c.Kind != linker.ManagedLink {
		return fmt.Errorf("%s is a %s, expected a claudectx-managed symlink — run `claudectx doctor`", live, c.Kind)
	}

	j := &store.Journal{Op: "switch", Tool: string(t), From: axis.Current, To: target, Step: stepsFor(t)[0]}
	if err := sw.S.SetJournal(st, j); err != nil {
		return err
	}
	return sw.run(st, j, j.Step)
}

// Recover rolls an interrupted switch forward from its journaled step.
func (sw *Switcher) Recover(st *store.State) error {
	j := st.InProgress
	if j == nil || j.Op != "switch" {
		return fmt.Errorf("no switch journal to recover")
	}
	t, err := tool.Parse(j.Tool)
	if err != nil {
		return fmt.Errorf("corrupt switch journal: %w", err)
	}
	fmt.Fprintf(sw.Out, "recovering interrupted %s switch %s -> %s (from step %q)\n", t, j.From, j.To, j.Step)
	return sw.run(st, j, j.Step)
}

func (sw *Switcher) run(st *store.State, j *store.Journal, fromStep string) error {
	t, err := tool.Parse(j.Tool)
	if err != nil {
		return fmt.Errorf("corrupt switch journal: %w", err)
	}
	steps := stepsFor(t)
	start := 0
	for i, s := range steps {
		if s == fromStep {
			start = i
			break
		}
	}
	for _, stepName := range steps[start:] {
		j.Step = stepName
		if err := sw.S.SetJournal(st, j); err != nil {
			return err
		}
		var err error
		switch stepName {
		case StepStash:
			err = sw.stashOut(j.From)
		case StepClaudeJSONOut:
			err = sw.claudeJSONOut(j.From)
		case StepLinks:
			err = linker.Replace(sw.P.LiveDir(t), sw.P.ProfileHome(t, j.To))
		case StepClaudeJSONIn:
			err = sw.claudeJSONIn(j.To)
		case StepKeychainIn:
			err = sw.keychainIn(j.To)
		}
		if err != nil {
			return fmt.Errorf("switch step %q failed (will be retried on next run): %w", stepName, err)
		}
	}

	axis := st.Axis(t)
	axis.Previous = j.From
	axis.Current = j.To
	st.InProgress = nil
	if err := sw.S.Save(st); err != nil {
		return err
	}
	if t == tool.Claude {
		kcMark := "✓"
		if !sw.P.KeychainEnabled {
			kcMark = "skipped"
		}
		fmt.Fprintf(sw.Out, "Switched claude to %q (keychain %s)\n", j.To, kcMark)
	} else {
		fmt.Fprintf(sw.Out, "Switched codex to %q\n", j.To)
	}
	return nil
}

// stashOut saves the live keychain credential into the outgoing profile.
// If no credential exists, a stale stash is removed so it can't resurrect
// an old token later.
func (sw *Switcher) stashOut(outgoing string) error {
	cred, err := sw.KC.Read()
	if errors.Is(err, keychain.ErrNotFound) {
		os.Remove(sw.P.KeychainStash(outgoing))
		return nil
	}
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(sw.P.ProfileSecretsDir(outgoing), 0o700); err != nil {
		return err
	}
	return store.WriteFileAtomic(sw.P.KeychainStash(outgoing), data, 0o600)
}

func (sw *Switcher) claudeJSONOut(outgoing string) error {
	err := store.CopyFileAtomic(sw.P.ClaudeJSON, sw.P.ProfileClaudeJSON(outgoing), 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil // no live claude.json yet — nothing to capture
	}
	return err
}

func (sw *Switcher) claudeJSONIn(target string) error {
	src := sw.P.ProfileClaudeJSON(target)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return store.WriteFileAtomic(sw.P.ClaudeJSON, []byte("{}\n"), 0o600)
	}
	return store.CopyFileAtomic(src, sw.P.ClaudeJSON, 0o600)
}

// keychainIn restores the target profile's credential, or deletes the live
// keychain item when the target has none — otherwise the previous profile's
// token would silently leak into the new one.
func (sw *Switcher) keychainIn(target string) error {
	data, err := os.ReadFile(sw.P.KeychainStash(target))
	if errors.Is(err, os.ErrNotExist) {
		if err := sw.KC.Delete(); err != nil {
			return err
		}
		if sw.P.KeychainEnabled {
			fmt.Fprintf(sw.Out, "no stored Claude credentials in %q — run `claude` and log in\n", target)
		}
		return nil
	}
	if err != nil {
		return err
	}
	var cred keychain.Credential
	if err := json.Unmarshal(data, &cred); err != nil {
		return fmt.Errorf("corrupt keychain stash for %q: %w", target, err)
	}
	return sw.KC.Write(cred)
}
