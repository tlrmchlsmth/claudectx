// Package translate converts agent state between Claude Code and Codex CLI
// formats, inside a single context directory. It never touches the live
// ~/.claude or ~/.codex paths directly.
package translate

import (
	"fmt"

	"github.com/tlrmchlsmth/claudectx/internal/report"
)

type Direction string

const (
	ClaudeToCodex Direction = "claude-to-codex"
	CodexToClaude Direction = "codex-to-claude"
)

func ParseDirection(s string) (Direction, error) {
	switch Direction(s) {
	case ClaudeToCodex, CodexToClaude:
		return Direction(s), nil
	}
	return "", fmt.Errorf("direction must be %q or %q", ClaudeToCodex, CodexToClaude)
}

// Context locates one context's artifacts on disk.
type Context struct {
	Name       string
	ClaudeDir  string
	CodexDir   string
	ClaudeJSON string // the context's claude.json copy
}

type Options struct {
	Direction Direction
	// Only filters translators by name ("instructions", "skills", "mcp",
	// "settings"); empty means all.
	Only          map[string]bool
	DryRun        bool
	Force         bool
	InlineImports bool
}

type translator struct {
	name string
	plan func(Context, Options) (report.Section, error)
}

var translators = []translator{
	{"instructions", planInstructions},
	{"skills", planSkills},
	{"mcp", planMCP},
	{"settings", planSettings},
}

// TranslatorNames lists valid --only values in execution order.
func TranslatorNames() []string {
	names := make([]string, len(translators))
	for i, t := range translators {
		names[i] = t.name
	}
	return names
}

// Run plans every enabled translator and, unless DryRun, applies the
// resulting actions. Planning the whole report before applying anything
// keeps dry-run output identical to a real run.
func Run(ctx Context, opts Options) (*report.Report, error) {
	rep := &report.Report{}
	for _, t := range translators {
		if len(opts.Only) > 0 && !opts.Only[t.name] {
			continue
		}
		section, err := t.plan(ctx, opts)
		if err != nil {
			return rep, fmt.Errorf("%s: %w", t.name, err)
		}
		rep.Add(section)
	}
	if opts.DryRun {
		return rep, nil
	}
	for _, s := range rep.Sections {
		for _, a := range s.Actions {
			if a.Apply == nil {
				continue
			}
			if err := a.Apply(); err != nil {
				return rep, fmt.Errorf("%s: applying %q: %w", s.Name, a.Description, err)
			}
		}
	}
	return rep, nil
}
