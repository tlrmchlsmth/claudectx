// Package procs detects running claude / codex processes so switch can warn
// before yanking state out from under them.
package procs

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Proc struct {
	PID  int
	Name string // "claude" or "codex"
}

// FindRunning scans the process table for claude and codex CLIs.
func FindRunning() []Proc {
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return nil
	}
	return parse(string(out))
}

func parse(psOut string) []Proc {
	var found []Proc
	for _, line := range strings.Split(psOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		base := filepath.Base(fields[1])
		// Exact match only: "claude" / "codex" binaries, not e.g. "claudectx"
		// or an editor with "codex" in its path.
		if base == "claude" || base == "codex" {
			found = append(found, Proc{PID: pid, Name: base})
		}
	}
	return found
}
