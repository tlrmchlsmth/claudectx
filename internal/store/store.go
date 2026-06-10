// Package store manages claudectx state: the state.json file (per-axis
// current/previous plus the crash journal) and profile directory CRUD.
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
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

// Reserved are subcommand names that can never be profile names, so command
// dispatch stays unambiguous.
var Reserved = map[string]bool{
	"list": true, "current": true, "create": true, "delete": true,
	"rename": true, "show": true, "init": true, "translate": true,
	"doctor": true, "switch": true, "version": true, "help": true,
	"env": true, "shell": true, "shell-init": true,
	"claude": true, "codex": true, "migrate": true,
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid profile name %q (allowed: letters, digits, '.', '_', '-'; max 64 chars; must start alphanumeric)", name)
	}
	if Reserved[name] {
		return fmt.Errorf("%q is a reserved command name and cannot be a profile name", name)
	}
	return nil
}

// MigrateInfo pins down what a v1->v2 migration is moving, so recovery
// never has to re-derive it from links that move mid-operation.
type MigrateInfo struct {
	ClaudeFrom string `json:"claude_from"` // v1 context ~/.claude pointed at
	CodexFrom  string `json:"codex_from"`  // v1 context ~/.codex pointed at
	V1Current  string `json:"v1_current"`
	V1Previous string `json:"v1_previous"`
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
	Op        string       `json:"op"`             // "init" | "switch" | "migrate"
	Tool      string       `json:"tool,omitempty"` // axis for "switch"; "" for whole-system ops
	From      string       `json:"from,omitempty"`
	To        string       `json:"to,omitempty"`
	Step      string       `json:"step"`
	Migrate   *MigrateInfo `json:"migrate,omitempty"`
	StartedAt string       `json:"started_at"`
}

// AxisState is one tool's switch state.
type AxisState struct {
	Current  string `json:"current"`
	Previous string `json:"previous,omitempty"`
}

type State struct {
	Version    int       `json:"version"`
	Claude     AxisState `json:"claude"`
	Codex      AxisState `json:"codex"`
	InProgress *Journal  `json:"in_progress"`
}

// Axis returns the mutable state for one tool.
func (st *State) Axis(t tool.Tool) *AxisState {
	if t == tool.Claude {
		return &st.Claude
	}
	return &st.Codex
}

// ErrV1State means state.json is the pre-profiles paired-context schema;
// callers direct the user to `claudectx migrate`.
var ErrV1State = errors.New("state file is the v1 paired-context layout — run `claudectx migrate` to upgrade to per-tool profiles")

type Store struct {
	P paths.Paths
}

func New(p paths.Paths) *Store { return &Store{P: p} }

// Initialized reports whether state.json exists.
func (s *Store) Initialized() bool {
	_, err := os.Stat(s.P.StateFile())
	return err == nil
}

// V1State is the legacy schema, read only by migration.
type V1State struct {
	Version    int      `json:"version"`
	Current    string   `json:"current"`
	Previous   string   `json:"previous"`
	InProgress *Journal `json:"in_progress"`
}

// LoadV1 reads the raw legacy state. It does not validate the version.
func (s *Store) LoadV1() (*V1State, error) {
	data, err := os.ReadFile(s.P.StateFile())
	if err != nil {
		return nil, err
	}
	var st V1State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("corrupt state file %s: %w", s.P.StateFile(), err)
	}
	return &st, nil
}

func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.P.StateFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("claudectx is not initialized — run `claudectx init` first")
	}
	if err != nil {
		return nil, err
	}
	var ver struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &ver); err != nil {
		return nil, fmt.Errorf("corrupt state file %s: %w", s.P.StateFile(), err)
	}
	if ver.Version < 2 {
		return nil, ErrV1State
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("corrupt state file %s: %w", s.P.StateFile(), err)
	}
	return &st, nil
}

func (s *Store) Save(st *State) error {
	st.Version = 2
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

// List returns profile names for one tool, sorted alphabetically.
func (s *Store) List(t tool.Tool) ([]string, error) {
	entries, err := os.ReadDir(s.P.ToolProfilesDir(t))
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

func (s *Store) Exists(t tool.Tool, name string) bool {
	fi, err := os.Stat(s.P.ProfileDir(t, name))
	return err == nil && fi.IsDir()
}

// ScaffoldProfile creates the empty skeleton of a profile.
func (s *Store) ScaffoldProfile(t tool.Tool, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(s.P.ProfileHome(t, name), 0o755); err != nil {
		return err
	}
	if t == tool.Claude {
		return os.MkdirAll(s.P.ProfileSecretsDir(name), 0o700)
	}
	return nil
}

// Trash moves a profile dir into backups/ instead of deleting it.
func (s *Store) Trash(t tool.Tool, name string) (string, error) {
	if err := os.MkdirAll(s.P.BackupsDir(), 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(s.P.BackupsDir(),
		fmt.Sprintf("%s.%s.deleted.%s", t, name, time.Now().UTC().Format("20060102T150405Z")))
	if err := os.Rename(s.P.ProfileDir(t, name), dst); err != nil {
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
