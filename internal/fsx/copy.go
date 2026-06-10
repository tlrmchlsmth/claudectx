// Package fsx holds small filesystem helpers shared across packages.
package fsx

import (
	"io/fs"
	"os"
	"path/filepath"
)

// CopyTree recursively copies src to dst, preserving symlinks as symlinks
// and file permissions. skip (optional) receives the path relative to src;
// returning true skips that entry (and its subtree).
func CopyTree(src, dst string, skip func(rel string) bool) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel != "." && skip != nil && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			os.Remove(target)
			return os.Symlink(linkTarget, target)
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		default:
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		}
	})
}
