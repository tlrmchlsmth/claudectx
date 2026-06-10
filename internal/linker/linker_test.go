package linker_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/linker"
)

func TestClassify(t *testing.T) {
	root := t.TempDir()
	contexts := filepath.Join(root, ".claudectx", "contexts")
	ctxClaude := filepath.Join(contexts, "default", "claude")
	if err := os.MkdirAll(ctxClaude, 0o755); err != nil {
		t.Fatal(err)
	}
	foreignTarget := filepath.Join(root, "dotfiles", "claude")
	if err := os.MkdirAll(foreignTarget, 0o755); err != nil {
		t.Fatal(err)
	}

	mk := func(name string, setup func(path string)) string {
		p := filepath.Join(root, name)
		setup(p)
		return p
	}

	cases := []struct {
		name    string
		path    string
		want    linker.Kind
		context string
	}{
		{"missing", filepath.Join(root, "nope"), linker.Missing, ""},
		{"real dir", mk("realdir", func(p string) { os.Mkdir(p, 0o755) }), linker.Real, ""},
		{"managed", mk("managed", func(p string) { os.Symlink(ctxClaude, p) }), linker.ManagedLink, "default"},
		{"foreign", mk("foreign", func(p string) { os.Symlink(foreignTarget, p) }), linker.ForeignLink, ""},
		{"dangling", mk("dangling", func(p string) { os.Symlink(filepath.Join(root, "gone"), p) }), linker.Dangling, ""},
		{"dangling managed", mk("dm", func(p string) {
			os.Symlink(filepath.Join(contexts, "deleted", "claude"), p)
		}), linker.Dangling, "deleted"},
	}
	for _, tc := range cases {
		c, err := linker.Classify(tc.path, contexts)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if c.Kind != tc.want {
			t.Errorf("%s: kind = %v, want %v", tc.name, c.Kind, tc.want)
		}
		if c.Context != tc.context {
			t.Errorf("%s: context = %q, want %q", tc.name, c.Context, tc.context)
		}
	}
}

func TestClassifyRelativeSymlink(t *testing.T) {
	root := t.TempDir()
	contexts := filepath.Join(root, "contexts")
	target := filepath.Join(contexts, "work", "claude")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join("contexts", "work", "claude"), link); err != nil {
		t.Fatal(err)
	}
	c, err := linker.Classify(link, contexts)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != linker.ManagedLink || c.Context != "work" {
		t.Fatalf("relative link: %+v", c)
	}
}

func TestReplace(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	os.MkdirAll(a, 0o755)
	os.MkdirAll(b, 0o755)
	link := filepath.Join(root, "link")

	// Create from nothing.
	if err := linker.Replace(link, a); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(link); got != a {
		t.Fatalf("link -> %s, want %s", got, a)
	}
	// Atomic repoint over an existing symlink.
	if err := linker.Replace(link, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(link); got != b {
		t.Fatalf("link -> %s, want %s", got, b)
	}
	// No temp litter.
	entries, _ := os.ReadDir(root)
	if len(entries) != 3 {
		t.Fatalf("unexpected dir contents: %v", entries)
	}
}
