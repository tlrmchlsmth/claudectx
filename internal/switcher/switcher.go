// Package switcher implements the journaled context switch and its crash
// recovery. Every step is idempotent; an interrupted switch is rolled
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
)

// Step names recorded in the journal, in execution order.
const (
	StepStash         = "stash"
	StepClaudeJSONOut = "claude_json_out"
	StepLinks         = "links"
	StepClaudeJSONIn  = "claude_json_in"
	StepKeychainIn    = "keychain_in"
)

var stepOrder = []string{StepStash, StepClaudeJSONOut, StepLinks, StepClaudeJSONIn, StepKeychainIn}

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
	ErrSameContext = errors.New("already on that context")
)

// Switch moves from the current context to target, starting at the first step.
func (sw *Switcher) Switch(target string) error {
	st, err := sw.S.Load()
	if err != nil {
		return err
	}
	if st.InProgress != nil {
		return fmt.Errorf("an interrupted %q operation needs recovery — run any claudectx command to recover first", st.InProgress.Op)
	}
	if target == st.Current {
		return ErrSameContext
	}
	if !sw.S.Exists(target) {
		return fmt.Errorf("no such context %q (see `claudectx list`)", target)
	}
	for _, live := range []string{sw.P.ClaudeDir, sw.P.CodexDir} {
		c, err := linker.Classify(live, sw.P.ContextsDir())
		if err != nil {
			return err
		}
		if c.Kind != linker.ManagedLink {
			return fmt.Errorf("%s is a %s, expected a claudectx-managed symlink — run `claudectx doctor`", live, c.Kind)
		}
	}

	j := &store.Journal{Op: "switch", From: st.Current, To: target, Step: StepStash}
	if err := sw.S.SetJournal(st, j); err != nil {
		return err
	}
	return sw.run(st, j, StepStash)
}

// Recover rolls an interrupted operation forward from its journaled step.
func (sw *Switcher) Recover(st *store.State) error {
	j := st.InProgress
	if j == nil || j.Op != "switch" {
		return fmt.Errorf("no switch journal to recover")
	}
	fmt.Fprintf(sw.Out, "recovering interrupted switch %s -> %s (from step %q)\n", j.From, j.To, j.Step)
	return sw.run(st, j, j.Step)
}

func (sw *Switcher) run(st *store.State, j *store.Journal, fromStep string) error {
	start := 0
	for i, s := range stepOrder {
		if s == fromStep {
			start = i
			break
		}
	}
	for _, stepName := range stepOrder[start:] {
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
			err = sw.repointLinks(j.To)
		case StepClaudeJSONIn:
			err = sw.claudeJSONIn(j.To)
		case StepKeychainIn:
			err = sw.keychainIn(j.To)
		}
		if err != nil {
			return fmt.Errorf("switch step %q failed (will be retried on next run): %w", stepName, err)
		}
	}

	st.Previous = j.From
	st.Current = j.To
	st.InProgress = nil
	if err := sw.S.Save(st); err != nil {
		return err
	}
	kcMark := "✓"
	if !sw.P.KeychainEnabled {
		kcMark = "skipped"
	}
	fmt.Fprintf(sw.Out, "Switched to %q (claude ✓ codex ✓ keychain %s)\n", j.To, kcMark)
	return nil
}

// stashOut saves the live keychain credential into the outgoing context.
// If no credential exists, a stale stash is removed so it can't resurrect
// an old token later.
func (sw *Switcher) stashOut(outgoing string) error {
	cred, err := sw.KC.Read()
	if errors.Is(err, keychain.ErrNotFound) {
		os.Remove(sw.P.CtxKeychainStash(outgoing))
		return nil
	}
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(sw.P.CtxSecretsDir(outgoing), 0o700); err != nil {
		return err
	}
	return store.WriteFileAtomic(sw.P.CtxKeychainStash(outgoing), data, 0o600)
}

func (sw *Switcher) claudeJSONOut(outgoing string) error {
	err := store.CopyFileAtomic(sw.P.ClaudeJSON, sw.P.CtxClaudeJSON(outgoing), 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil // no live claude.json yet — nothing to capture
	}
	return err
}

func (sw *Switcher) repointLinks(target string) error {
	if err := linker.Replace(sw.P.ClaudeDir, sw.P.CtxClaudeDir(target)); err != nil {
		return err
	}
	return linker.Replace(sw.P.CodexDir, sw.P.CtxCodexDir(target))
}

func (sw *Switcher) claudeJSONIn(target string) error {
	src := sw.P.CtxClaudeJSON(target)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return store.WriteFileAtomic(sw.P.ClaudeJSON, []byte("{}\n"), 0o600)
	}
	return store.CopyFileAtomic(src, sw.P.ClaudeJSON, 0o600)
}

// keychainIn restores the target context's credential, or deletes the live
// keychain item when the target has none — otherwise the previous context's
// token would silently leak into the new one.
func (sw *Switcher) keychainIn(target string) error {
	data, err := os.ReadFile(sw.P.CtxKeychainStash(target))
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
