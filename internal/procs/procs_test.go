package procs

import "testing"

func TestParse(t *testing.T) {
	psOut := `  123 /usr/local/bin/claude
  456 /opt/homebrew/bin/codex
  789 /usr/local/bin/claudectx
 1011 /Applications/Visual Studio Code.app/codex-helper
 1213 vim
garbage line
`
	got := parse(psOut)
	if len(got) != 2 {
		t.Fatalf("parse = %+v, want exactly claude+codex", got)
	}
	if got[0].PID != 123 || got[0].Name != "claude" {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].PID != 456 || got[1].Name != "codex" {
		t.Fatalf("second = %+v", got[1])
	}
}
