// Package report holds the shared data model for translation plans and
// doctor findings, plus terminal rendering.
package report

import (
	"fmt"
	"io"
)

type Severity string

const (
	Info Severity = "info"
	Warn Severity = "warn"
	Lost Severity = "lost"
)

// LossNote describes something that did not survive a translation intact.
type LossNote struct {
	Severity Severity
	Artifact string // e.g. "mcp:atlassian", "skill:crashlog", "settings:permissions.allow"
	Message  string
}

type ActionKind string

const (
	Write ActionKind = "write"
	Copy  ActionKind = "copy"
	Skip  ActionKind = "skip"
	Merge ActionKind = "merge"
)

// Action is one planned step of a translation.
type Action struct {
	Kind        ActionKind
	Dst         string
	Description string
	Notes       []LossNote
	// Apply executes the action. nil for Skip actions.
	Apply func() error `json:"-"`
}

// Report is a set of actions grouped by translator.
type Report struct {
	Sections []Section
}

type Section struct {
	Name    string
	Actions []Action
	Notes   []LossNote // section-level notes not tied to one action
}

func (r *Report) Add(s Section) { r.Sections = append(r.Sections, s) }

func (r *Report) Counts() (translated, skipped, lossy int) {
	for _, s := range r.Sections {
		for _, n := range s.Notes {
			if n.Severity != Info {
				lossy++
			}
		}
		for _, a := range s.Actions {
			if a.Kind == Skip {
				skipped++
			} else {
				translated++
			}
			for _, n := range a.Notes {
				if n.Severity != Info {
					lossy++
				}
			}
		}
	}
	return
}

// Render prints the report in a human-readable form.
func (r *Report) Render(w io.Writer, color bool) {
	sev := func(n LossNote) string {
		tag := fmt.Sprintf("[%s]", n.Severity)
		if !color {
			return tag
		}
		switch n.Severity {
		case Lost:
			return "\x1b[31m" + tag + "\x1b[0m"
		case Warn:
			return "\x1b[33m" + tag + "\x1b[0m"
		default:
			return "\x1b[2m" + tag + "\x1b[0m"
		}
	}
	for _, s := range r.Sections {
		if len(s.Actions) == 0 && len(s.Notes) == 0 {
			continue
		}
		fmt.Fprintf(w, "%s:\n", s.Name)
		for _, a := range s.Actions {
			fmt.Fprintf(w, "  %-5s %s", a.Kind, a.Description)
			if a.Dst != "" {
				fmt.Fprintf(w, " -> %s", a.Dst)
			}
			fmt.Fprintln(w)
			for _, n := range a.Notes {
				fmt.Fprintf(w, "        %s %s: %s\n", sev(n), n.Artifact, n.Message)
			}
		}
		for _, n := range s.Notes {
			fmt.Fprintf(w, "  %s %s: %s\n", sev(n), n.Artifact, n.Message)
		}
	}
	t, sk, lo := r.Counts()
	fmt.Fprintf(w, "\n%d translated, %d skipped, %d lossy\n", t, sk, lo)
}
