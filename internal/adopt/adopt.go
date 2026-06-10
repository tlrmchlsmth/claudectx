// Package adopt implements `claudectx init` for fresh installs: moving the
// user's existing ~/.claude and ~/.codex into per-tool "default" profiles
// and symlinking back.
package adopt

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tlrmchlsmth/claudectx/internal/fsx"
	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

const DefaultProfile = "default"

type Adopter struct {
	P   paths.Paths
	S   *store.Store
	Out io.Writer
}

func New(p paths.Paths, s *store.Store, out io.Writer) *Adopter {
	return &Adopter{P: p, S: s, Out: out}
}

// PlanItem describes what init will do to one managed path.
type PlanItem struct {
	Tool   tool.Tool
	Live   string
	Dest   string
	Kind   linker.Kind
	Action string // human description
}

// Plan classifies the managed paths and describes the adoption. It returns an
// error if any path is in a state we refuse to touch (foreign symlink).
func (a *Adopter) Plan() ([]PlanItem, error) {
	var items []PlanItem
	for _, t := range tool.All {
		items = append(items, PlanItem{
			Tool: t,
			Live: a.P.LiveDir(t),
			Dest: a.P.ProfileHome(t, DefaultProfile),
		})
	}
	for i := range items {
		c, err := linker.Classify(items[i].Live, a.P.ToolProfilesDir(items[i].Tool))
		if err != nil {
			return nil, err
		}
		items[i].Kind = c.Kind
		switch c.Kind {
		case linker.Real:
			items[i].Action = fmt.Sprintf("move into %s profile %q and symlink back", items[i].Tool, DefaultProfile)
		case linker.Missing:
			items[i].Action = fmt.Sprintf("create empty %s profile %q and symlink", items[i].Tool, DefaultProfile)
		case linker.ManagedLink:
			items[i].Action = "already managed — leave as is"
		case linker.ForeignLink:
			return nil, fmt.Errorf(
				"%s is a symlink to %s, which claudectx does not manage.\n"+
					"Refusing to move it. If you want claudectx to own this path, remove the symlink (after moving its target's content somewhere safe) and re-run init",
				items[i].Live, c.Target)
		case linker.Dangling:
			return nil, fmt.Errorf("%s is a dangling symlink — remove it and re-run init", items[i].Live)
		}
	}
	return items, nil
}

// Run executes the adoption. Plan must have been shown/confirmed by the caller.
func (a *Adopter) Run(items []PlanItem) error {
	if a.S.Initialized() {
		st, err := a.S.Load()
		if err == nil && st.InProgress == nil {
			fmt.Fprintln(a.Out, "already initialized")
			return nil
		}
		if errors.Is(err, store.ErrV1State) {
			return err
		}
	}

	if err := os.MkdirAll(a.P.BackupsDir(), 0o755); err != nil {
		return err
	}
	for _, t := range tool.All {
		if err := a.S.ScaffoldProfile(t, DefaultProfile); err != nil {
			return err
		}
	}

	// Cheap insurance before we start touching things.
	if _, err := os.Stat(a.P.ClaudeJSON); err == nil {
		backup := filepath.Join(a.P.BackupsDir(),
			fmt.Sprintf("claude.json.%s", time.Now().UTC().Format("20060102T150405Z")))
		if err := store.CopyFileAtomic(a.P.ClaudeJSON, backup, 0o600); err != nil {
			return fmt.Errorf("backing up %s: %w", a.P.ClaudeJSON, err)
		}
	}

	st := &store.State{
		Claude: store.AxisState{Current: DefaultProfile},
		Codex:  store.AxisState{Current: DefaultProfile},
	}
	j := &store.Journal{Op: "init", To: DefaultProfile, Step: "move"}
	if err := a.S.SetJournal(st, j); err != nil {
		return err
	}

	for _, item := range items {
		if err := a.adoptOne(item); err != nil {
			return fmt.Errorf("adopting %s: %w (re-run `claudectx init` to resume)", item.Live, err)
		}
	}

	// Copy (not move) the live claude.json into the claude profile: the live
	// file stays in place and is copy-swapped on every claude switch.
	if _, err := os.Stat(a.P.ClaudeJSON); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(a.P.ProfileClaudeJSON(DefaultProfile)); errors.Is(err, os.ErrNotExist) {
			if err := store.WriteFileAtomic(a.P.ProfileClaudeJSON(DefaultProfile), []byte("{}\n"), 0o600); err != nil {
				return err
			}
		}
	} else {
		if err := store.CopyFileAtomic(a.P.ClaudeJSON, a.P.ProfileClaudeJSON(DefaultProfile), 0o600); err != nil {
			return err
		}
	}

	st.InProgress = nil
	if err := a.S.Save(st); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Initialized: claude and codex profiles %q now hold your state\n", DefaultProfile)
	return nil
}

// adoptOne is idempotent: re-running after a crash skips completed moves.
func (a *Adopter) adoptOne(item PlanItem) error {
	c, err := linker.Classify(item.Live, a.P.ToolProfilesDir(item.Tool))
	if err != nil {
		return err
	}
	switch c.Kind {
	case linker.ManagedLink:
		return nil // done in a previous (interrupted) run
	case linker.Real:
		// The profile skeleton created an empty dest dir; remove it so the
		// rename can land. Refuse if dest already has real content (a crashed
		// half-state doctor should look at, not something to clobber).
		if entries, err := os.ReadDir(item.Dest); err == nil && len(entries) > 0 {
			return fmt.Errorf("destination %s already has content — run `claudectx doctor`", item.Dest)
		}
		os.Remove(item.Dest)
		if err := os.Rename(item.Live, item.Dest); err != nil {
			if !errors.Is(err, syscall.EXDEV) {
				return err
			}
			// Cross-device home layout: copy, verify, then remove the source.
			if err := fsx.CopyTree(item.Live, item.Dest, nil); err != nil {
				return err
			}
			if err := os.RemoveAll(item.Live); err != nil {
				return err
			}
		}
	case linker.Missing:
		if err := os.MkdirAll(item.Dest, 0o755); err != nil {
			return err
		}
	}
	return linker.Replace(item.Live, item.Dest)
}
