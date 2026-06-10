// Package tool defines the axis type shared by every layer: which managed
// tool (Claude Code or Codex CLI) an operation applies to. It is a leaf
// package so paths, store, switcher, and cli can all import it without
// cycles.
package tool

import "fmt"

type Tool string

const (
	Claude Tool = "claude"
	Codex  Tool = "codex"
)

// All lists the axes in display order.
var All = []Tool{Claude, Codex}

func Parse(s string) (Tool, error) {
	switch Tool(s) {
	case Claude, Codex:
		return Tool(s), nil
	}
	return "", fmt.Errorf("unknown tool %q (expected %q or %q)", s, Claude, Codex)
}
