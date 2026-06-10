// Package store manages claudectx state: the state.json file (current/previous
// context plus the crash journal) and context directory CRUD.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/tlrmchlsmth/claudectx/internal/paths"
)

// Reserved are subcommand names that can never be context names, so the
// bare-name switch shortcut stays unambiguous.
var Reserved = map[string]bool{
	"list": true, "current": true, "create": true, "delete": true,
	"rename": true, "show": true, "init": true, "translate": true,
	"doctor": true, "switch": true, "version": true, "help": true,
	"env": true, "shell": true, "shell-init": true,
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid context name %q (allowed: letters, digits, '.', '_', '-'; max 64 chars; must start alphanumeric)", name)
	}
	if Reserved[name] {
		return fmt.Errorf("%q is a reserved command name and cannot be a context name", name)
	}
	return nil
}

// Journal records an in-flight multi-step operation so an interrupted run can
// be rolled forward by the next invocation. Every claudectx command checks
// for a non-nil journal at startup and finishes the recorded operation before
// doing anything else.
//
// Contract for journaled operations: each step must be idempotent (recovery
// re-runs from the journaled Step, possibly repeating work that already
// happened) and recovery must reconcile by observing disk state, not by
// trusting memory of what was done.
type Journal struct {
	Op        string `json:"op"`   // "init" | "switch"
	From      string `json:"from"` // switch only
	To        string `json:"to"`
	Step      string `json:"step"`
	StartedAt string `json:"started_at"`
}

type State struct {
	Version    int      `json:"version"`
	Current    string   `json:"current"`
	Previous   string   `json:"previous,omitempty"`
	InProgress *Journal `json:"in_progress"`
}

type Store struct {
	P paths.Paths
}

func New(p paths.Paths) *Store { return &Store{P: p} }

// Initialized reports whether state.json exists.
func (s *Store) Initialized() bool {
	_, err := os.Stat(s.P.StateFile())
	return err == nil
}

func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.P.StateFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("claudectx is not initialized — run `claudectx init` first")
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("corrupt state file %s: %w", s.P.StateFile(), err)
	}
	return &st, nil
}

func (s *Store) Save(st *State) error {
	if st.Version == 0 {
		st.Version = 1
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.P.StateFile(), append(data, '\n'), 0o644)
}

// SetJournal records (or clears, with nil) the in-progress operation.
func (s *Store) SetJournal(st *State, j *Journal) error {
	if j != nil && j.StartedAt == "" {
		j.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	st.InProgress = j
	return s.Save(st)
}

// List returns context names sorted alphabetically.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.P.ContextsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) Exists(name string) bool {
	fi, err := os.Stat(s.P.ContextDir(name))
	return err == nil && fi.IsDir()
}

// ScaffoldContext creates the empty skeleton of a context directory.
func (s *Store) ScaffoldContext(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	for _, d := range []string{
		s.P.CtxClaudeDir(name),
		s.P.CtxCodexDir(name),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return os.MkdirAll(s.P.CtxSecretsDir(name), 0o700)
}

// Trash moves a context dir into backups/ instead of deleting it.
func (s *Store) Trash(name string) (string, error) {
	if err := os.MkdirAll(s.P.BackupsDir(), 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(s.P.BackupsDir(),
		fmt.Sprintf("%s.deleted.%s", name, time.Now().UTC().Format("20060102T150405Z")))
	if err := os.Rename(s.P.ContextDir(name), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// WriteFileAtomic writes data to a temp file in path's directory and renames
// it into place, so readers never observe a partial write.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// CopyFileAtomic copies src to dst with WriteFileAtomic semantics.
func CopyFileAtomic(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return WriteFileAtomic(dst, data, perm)
}
