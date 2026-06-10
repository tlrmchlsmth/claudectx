package frontmatter_test

import (
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/frontmatter"
)

func TestParseAndReassemble(t *testing.T) {
	src := "---\nname: crashlog\ndescription: Diagnose crashes\nallowed-tools: Bash, Read\n---\n\nBody text.\n"
	doc, err := frontmatter.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Get("name") != "crashlog" || doc.Get("description") != "Diagnose crashes" {
		t.Fatalf("fields = %+v", doc.Fields)
	}
	if !doc.Has("allowed-tools") || doc.Has("model") {
		t.Fatal("Has wrong")
	}
	if doc.String() != src {
		t.Fatalf("reassembly not byte-faithful:\n%q\nvs\n%q", src, doc.String())
	}
}

func TestParseNoFrontmatter(t *testing.T) {
	doc, err := frontmatter.Parse("# Just markdown\n")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Fields != nil || doc.Get("name") != "" {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.String() != "# Just markdown\n" {
		t.Fatal("body altered")
	}
}

func TestParseUnterminated(t *testing.T) {
	if _, err := frontmatter.Parse("---\nname: x\nno closing fence"); err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

func TestParseListsAndNesting(t *testing.T) {
	src := "---\nname: s\ndescription: d\ntags: [a, b]\nhooks:\n  pre: echo\n---\nbody"
	doc, err := frontmatter.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Has("tags") || !doc.Has("hooks") {
		t.Fatalf("fields = %+v", doc.Fields)
	}
}
