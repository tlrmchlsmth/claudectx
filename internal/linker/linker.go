// Package linker classifies the managed paths (~/.claude, ~/.codex) and
// performs atomic symlink replacement.
package linker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Kind int

const (
	Missing Kind = iota
	Real         // a real directory (or file) — not a symlink
	ManagedLink  // symlink pointing inside our contexts dir
	ForeignLink  // symlink pointing somewhere else (e.g. dotfiles) — hands off
	Dangling     // symlink whose target does not exist
)

func (k Kind) String() string {
	switch k {
	case Missing:
		return "missing"
	case Real:
		return "real"
	case ManagedLink:
		return "managed symlink"
	case ForeignLink:
		return "foreign symlink"
	case Dangling:
		return "dangling symlink"
	}
	return "unknown"
}

type Classification struct {
	Kind   Kind
	Target string // resolved symlink target (absolute), if a symlink
	// Context is the context name the link points into, for ManagedLink.
	Context string
}

// Classify inspects path relative to contextsDir (the managed root).
func Classify(path, contextsDir string) (Classification, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Classification{Kind: Missing}, nil
	}
	if err != nil {
		return Classification{}, err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return Classification{Kind: Real}, nil
	}

	target, err := os.Readlink(path)
	if err != nil {
		return Classification{}, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	target = filepath.Clean(target)

	c := Classification{Target: target}
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		c.Kind = Dangling
		// A dangling link into our contexts dir is still "ours"; report the
		// context name so doctor can explain it.
		if name, ok := contextOf(target, contextsDir); ok {
			c.Context = name
		}
		return c, nil
	}
	if name, ok := contextOf(target, contextsDir); ok {
		c.Kind = ManagedLink
		c.Context = name
		return c, nil
	}
	c.Kind = ForeignLink
	return c, nil
}

// contextOf extracts the context name from a target like
// <contextsDir>/<name>/claude. Returns false if target is outside contextsDir.
func contextOf(target, contextsDir string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(contextsDir), target)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return parts[0], true
}

// Replace atomically points `path` at `target`: it creates a temporary
// symlink next to the destination and renames it over path. rename(2)
// replaces an existing symlink atomically.
func Replace(path, target string) error {
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".claudectx-link-%d", os.Getpid()))
	os.Remove(tmp) // stale leftover from a crashed run
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
