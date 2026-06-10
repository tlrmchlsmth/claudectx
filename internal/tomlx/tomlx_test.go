package tomlx_test

import (
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/tlrmchlsmth/claudectx/internal/tomlx"
)

func parse(t *testing.T, doc string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := toml.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("output is not valid TOML: %v\n%s", err, doc)
	}
	return m
}

func TestEmitTableRoundTrips(t *testing.T) {
	block, err := tomlx.EmitTable("mcp_servers.files", map[string]any{
		"command": "npx",
		"args":    []string{"-y", "@scope/pkg"},
		"env":     map[string]string{"ROOT": "/tmp", "A B": `va"l`},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, block)
	servers := m["mcp_servers"].(map[string]any)["files"].(map[string]any)
	if servers["command"] != "npx" {
		t.Fatalf("command = %v", servers["command"])
	}
	env := servers["env"].(map[string]any)
	if env["A B"] != `va"l` {
		t.Fatalf("env round-trip: %v", env)
	}
}

func TestSpliceTableReplacesAndPreservesComments(t *testing.T) {
	doc := `# my precious comment
model = "gpt-5.2-codex"

[mcp_servers.old]
command = "old-cmd"

# trailing section comment
[other]
key = 1
`
	block, _ := tomlx.EmitTable("mcp_servers.old", map[string]any{"command": "new-cmd"})
	out, err := tomlx.SpliceTable(doc, "mcp_servers.old", block)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# my precious comment") {
		t.Fatal("leading comment lost")
	}
	if !strings.Contains(out, "# trailing section comment") {
		t.Fatal("comment after section lost")
	}
	m := parse(t, out)
	cmd := m["mcp_servers"].(map[string]any)["old"].(map[string]any)["command"]
	if cmd != "new-cmd" {
		t.Fatalf("command = %v", cmd)
	}
	if m["other"].(map[string]any)["key"].(int64) != 1 {
		t.Fatal("unrelated table damaged")
	}
}

func TestSpliceTableAppends(t *testing.T) {
	doc := "model = \"x\"\n"
	block, _ := tomlx.EmitTable("mcp_servers.new", map[string]any{"command": "c"})
	out, err := tomlx.SpliceTable(doc, "mcp_servers.new", block)
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, out)
	if m["model"] != "x" {
		t.Fatal("existing key lost")
	}
	if m["mcp_servers"].(map[string]any)["new"].(map[string]any)["command"] != "c" {
		t.Fatal("appended table missing")
	}
	// Empty doc works too.
	out2, err := tomlx.SpliceTable("", "mcp_servers.new", block)
	if err != nil {
		t.Fatal(err)
	}
	parse(t, out2)
}

func TestSetTopLevel(t *testing.T) {
	doc := `# comment
approval_policy = "on-request"

[table]
k = "v"
`
	out, err := tomlx.SetTopLevel(doc, "approval_policy", "never")
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, out)
	if m["approval_policy"] != "never" {
		t.Fatalf("approval_policy = %v", m["approval_policy"])
	}

	// New key lands above the first table, not inside it.
	out2, err := tomlx.SetTopLevel(doc, "sandbox_mode", "workspace-write")
	if err != nil {
		t.Fatal(err)
	}
	m2 := parse(t, out2)
	if m2["sandbox_mode"] != "workspace-write" {
		t.Fatalf("sandbox_mode = %v (must be top-level, not in [table])", m2["sandbox_mode"])
	}
	if m2["table"].(map[string]any)["k"] != "v" {
		t.Fatal("existing table damaged")
	}

	// Empty doc.
	out3, err := tomlx.SetTopLevel("", "model", "o3")
	if err != nil {
		t.Fatal(err)
	}
	if parse(t, out3)["model"] != "o3" {
		t.Fatal("set on empty doc failed")
	}
}

func TestEmitValueEscapes(t *testing.T) {
	v, err := tomlx.EmitValue("line1\nline2\t\"quoted\" \\slash")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := toml.Unmarshal([]byte("k = "+v), &m); err != nil {
		t.Fatalf("escaped string invalid: %v", err)
	}
	if m["k"] != "line1\nline2\t\"quoted\" \\slash" {
		t.Fatalf("round-trip = %q", m["k"])
	}
}
