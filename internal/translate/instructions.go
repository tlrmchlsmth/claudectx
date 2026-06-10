package translate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tlrmchlsmth/claudectx/internal/report"
	"github.com/tlrmchlsmth/claudectx/internal/store"
)

// planInstructions maps CLAUDE.md <-> AGENTS.md (the global instruction
// files of each tool).
func planInstructions(ctx Context, opts Options) (report.Section, error) {
	sec := report.Section{Name: "instructions"}

	var srcPath, dstPath, srcLabel, dstLabel string
	if opts.Direction == ClaudeToCodex {
		srcPath = filepath.Join(ctx.ClaudeDir, "CLAUDE.md")
		dstPath = filepath.Join(ctx.CodexDir, "AGENTS.md")
		srcLabel, dstLabel = "CLAUDE.md", "AGENTS.md"
	} else {
		srcPath = filepath.Join(ctx.CodexDir, "AGENTS.md")
		dstPath = filepath.Join(ctx.ClaudeDir, "CLAUDE.md")
		srcLabel, dstLabel = "AGENTS.md", "CLAUDE.md"
	}

	content, err := os.ReadFile(srcPath)
	if errors.Is(err, os.ErrNotExist) {
		sec.Notes = append(sec.Notes, report.LossNote{
			Severity: report.Info, Artifact: srcLabel,
			Message: "not present in this context — nothing to translate",
		})
		return sec, nil
	}
	if err != nil {
		return sec, err
	}

	var notes []report.LossNote
	if fi, err := os.Lstat(srcPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(srcPath)
		notes = append(notes, report.LossNote{
			Severity: report.Info, Artifact: srcLabel,
			Message: fmt.Sprintf("source is a symlink to %s; output is a flattened regular file", target),
		})
	}

	out := string(content)
	if opts.Direction == ClaudeToCodex {
		var importNotes []report.LossNote
		out, importNotes = processImports(out, ctx.ClaudeDir, srcPath, opts.InlineImports)
		notes = append(notes, importNotes...)
	}

	// Never overwrite a symlinked destination — it likely points into the
	// user's dotfiles and a write would follow the link.
	if fi, err := os.Lstat(dstPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(dstPath)
		sec.Actions = append(sec.Actions, report.Action{
			Kind: report.Skip, Dst: dstPath,
			Description: fmt.Sprintf("%s -> %s", srcLabel, dstLabel),
			Notes: append(notes, report.LossNote{
				Severity: report.Warn, Artifact: dstLabel,
				Message: fmt.Sprintf("destination is a symlink to %s — not overwriting; edit the link target instead", target),
			}),
		})
		return sec, nil
	}

	if existing, err := os.ReadFile(dstPath); err == nil && !opts.Force {
		if string(existing) == out {
			sec.Actions = append(sec.Actions, report.Action{
				Kind: report.Skip, Dst: dstPath,
				Description: fmt.Sprintf("%s -> %s (already identical)", srcLabel, dstLabel),
				Notes:       notes,
			})
			return sec, nil
		}
		sec.Actions = append(sec.Actions, report.Action{
			Kind: report.Skip, Dst: dstPath,
			Description: fmt.Sprintf("%s -> %s", srcLabel, dstLabel),
			Notes: append(notes, report.LossNote{
				Severity: report.Warn, Artifact: dstLabel,
				Message: "destination exists with different content — use --force to overwrite",
			}),
		})
		return sec, nil
	}

	final := out
	sec.Actions = append(sec.Actions, report.Action{
		Kind: report.Write, Dst: dstPath,
		Description: fmt.Sprintf("%s -> %s", srcLabel, dstLabel),
		Notes:       notes,
		Apply: func() error {
			return store.WriteFileAtomic(dstPath, []byte(final), 0o644)
		},
	})
	return sec, nil
}

// processImports handles Claude's `@path` memory-import lines, which Codex
// does not support. When inline is true the referenced files are embedded
// with markers; otherwise the lines are kept verbatim with a warning.
func processImports(content, claudeDir, srcPath string, inline bool) (string, []report.LossNote) {
	var notes []report.LossNote
	lines := strings.Split(content, "\n")
	var out []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "@") || len(trimmed) < 2 || strings.ContainsAny(trimmed, " \t") {
			out = append(out, line)
			continue
		}
		ref := strings.TrimPrefix(trimmed, "@")
		resolved, err := resolveImport(ref, claudeDir, srcPath)
		if err != nil {
			notes = append(notes, report.LossNote{
				Severity: report.Warn, Artifact: "import:" + ref,
				Message: "Codex has no import mechanism and the file could not be resolved — line kept verbatim",
			})
			out = append(out, line)
			continue
		}
		if !inline {
			notes = append(notes, report.LossNote{
				Severity: report.Warn, Artifact: "import:" + ref,
				Message: "Codex has no import mechanism — line kept verbatim (--no-inline-imports)",
			})
			out = append(out, line)
			continue
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			notes = append(notes, report.LossNote{
				Severity: report.Warn, Artifact: "import:" + ref,
				Message: fmt.Sprintf("could not read %s — line kept verbatim", resolved),
			})
			out = append(out, line)
			continue
		}
		out = append(out, fmt.Sprintf("<!-- inlined from @%s by claudectx -->", ref))
		out = append(out, strings.TrimRight(string(data), "\n"))
		out = append(out, fmt.Sprintf("<!-- end of @%s -->", ref))
		notes = append(notes, report.LossNote{
			Severity: report.Info, Artifact: "import:" + ref,
			Message: "inlined (Codex has no import mechanism)",
		})
	}
	return strings.Join(out, "\n"), notes
}

func resolveImport(ref, claudeDir, srcPath string) (string, error) {
	var candidates []string
	switch {
	case strings.HasPrefix(ref, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		candidates = []string{filepath.Join(home, ref[2:])}
	case filepath.IsAbs(ref):
		candidates = []string{ref}
	default:
		// Relative imports resolve against the file's directory; when the
		// file is a symlink, also try its resolved location.
		candidates = []string{filepath.Join(claudeDir, ref)}
		if resolved, err := filepath.EvalSymlinks(srcPath); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(resolved), ref))
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("not found")
}
