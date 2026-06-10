package translate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/tlrmchlsmth/claudectx/internal/frontmatter"
	"github.com/tlrmchlsmth/claudectx/internal/fsx"
	"github.com/tlrmchlsmth/claudectx/internal/report"
)

// claudeOnlyFields are SKILL.md frontmatter keys Claude Code understands but
// Codex ignores. The Agent Skills standard says unknown fields are ignored,
// so they are kept (stripping would be more destructive than the warning).
var claudeOnlyFields = []string{"allowed-tools", "context", "agent", "model", "hooks"}

// planSkills copies skill directories between the two tools. Both use the
// Agent Skills standard (skills/<name>/SKILL.md), so this is mostly a copy
// plus frontmatter validation.
func planSkills(ctx Context, opts Options) (report.Section, error) {
	sec := report.Section{Name: "skills"}

	var srcRoot, dstRoot string
	if opts.Direction == ClaudeToCodex {
		srcRoot = filepath.Join(ctx.ClaudeDir, "skills")
		dstRoot = filepath.Join(ctx.CodexDir, "skills")
	} else {
		srcRoot = filepath.Join(ctx.CodexDir, "skills")
		dstRoot = filepath.Join(ctx.ClaudeDir, "skills")
	}

	entries, err := os.ReadDir(srcRoot)
	if os.IsNotExist(err) {
		sec.Notes = append(sec.Notes, report.LossNote{
			Severity: report.Info, Artifact: "skills",
			Message: "no skills directory in source — nothing to translate",
		})
		return sec, nil
	}
	if err != nil {
		return sec, err
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		srcDir := filepath.Join(srcRoot, name)
		dstDir := filepath.Join(dstRoot, name)
		skillFile := filepath.Join(srcDir, "SKILL.md")

		data, err := os.ReadFile(skillFile)
		if err != nil {
			sec.Notes = append(sec.Notes, report.LossNote{
				Severity: report.Info, Artifact: "skill:" + name,
				Message: "directory has no readable SKILL.md — skipped (not a skill)",
			})
			continue
		}

		var notes []report.LossNote
		doc, err := frontmatter.Parse(string(data))
		if err != nil {
			notes = append(notes, report.LossNote{
				Severity: report.Warn, Artifact: "skill:" + name,
				Message: fmt.Sprintf("frontmatter could not be parsed (%v) — copied as-is", err),
			})
		} else {
			if doc.Get("name") == "" || doc.Get("description") == "" {
				notes = append(notes, report.LossNote{
					Severity: report.Warn, Artifact: "skill:" + name,
					Message: "frontmatter is missing required name/description — both tools may refuse to load it",
				})
			}
			if opts.Direction == ClaudeToCodex {
				for _, f := range claudeOnlyFields {
					if doc.Has(f) {
						notes = append(notes, report.LossNote{
							Severity: report.Warn, Artifact: "skill:" + name,
							Message: fmt.Sprintf("frontmatter field %q is Claude-specific; Codex will ignore it (kept)", f),
						})
					}
				}
			}
		}

		if _, err := os.Stat(dstDir); err == nil && !opts.Force {
			sec.Actions = append(sec.Actions, report.Action{
				Kind: report.Skip, Dst: dstDir,
				Description: fmt.Sprintf("skill %q", name),
				Notes: append(notes, report.LossNote{
					Severity: report.Info, Artifact: "skill:" + name,
					Message: "already exists at destination — use --force to overwrite",
				}),
			})
			continue
		}

		src, dst := srcDir, dstDir
		sec.Actions = append(sec.Actions, report.Action{
			Kind: report.Copy, Dst: dstDir,
			Description: fmt.Sprintf("skill %q", name),
			Notes:       notes,
			Apply: func() error {
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				return fsx.CopyTree(src, dst, nil)
			},
		})
	}

	// Plugin-provided skills live elsewhere and are intentionally untouched.
	if opts.Direction == ClaudeToCodex {
		if entries, err := os.ReadDir(filepath.Join(ctx.ClaudeDir, "plugins")); err == nil && len(entries) > 0 {
			sec.Notes = append(sec.Notes, report.LossNote{
				Severity: report.Info, Artifact: "plugins",
				Message: "plugin-provided skills are not translated (only skills/ is)",
			})
		}
	}
	return sec, nil
}
