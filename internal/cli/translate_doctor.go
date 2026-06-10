package cli

import (
	"fmt"
	"strings"

	"github.com/tlrmchlsmth/claudectx/internal/doctor"
	"github.com/tlrmchlsmth/claudectx/internal/translate"
)

func (a *App) cmdTranslate(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}

	ctxName, args := flagValue(args, "--context")
	only, args := flagValue(args, "--only")
	dryRun := hasFlag(args, "--dry-run")
	force := hasFlag(args, "--force")
	noInline := hasFlag(args, "--no-inline-imports")

	var dirArg string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			dirArg = arg
			break
		}
	}
	if dirArg == "" {
		return fmt.Errorf("usage: claudectx translate <claude-to-codex|codex-to-claude> [--context <name>] [--only %s] [--dry-run] [--force] [--no-inline-imports]",
			strings.Join(translate.TranslatorNames(), ","))
	}
	dir, err := translate.ParseDirection(dirArg)
	if err != nil {
		return err
	}

	if ctxName == "" {
		ctxName = st.Current
	}
	if !a.S.Exists(ctxName) {
		return fmt.Errorf("no such context %q", ctxName)
	}

	var onlySet map[string]bool
	if only != "" {
		onlySet = map[string]bool{}
		valid := map[string]bool{}
		for _, n := range translate.TranslatorNames() {
			valid[n] = true
		}
		for _, n := range strings.Split(only, ",") {
			n = strings.TrimSpace(n)
			if !valid[n] {
				return fmt.Errorf("unknown translator %q (valid: %s)", n, strings.Join(translate.TranslatorNames(), ", "))
			}
			onlySet[n] = true
		}
	}

	// Translating the current context: capture the freshest live claude.json
	// first, since MCP translation reads the context copy.
	if ctxName == st.Current && !dryRun {
		if err := a.captureLiveClaudeJSON(ctxName); err != nil {
			return err
		}
	}

	tctx := translate.Context{
		Name:       ctxName,
		ClaudeDir:  a.P.CtxClaudeDir(ctxName),
		CodexDir:   a.P.CtxCodexDir(ctxName),
		ClaudeJSON: a.P.CtxClaudeJSON(ctxName),
	}
	rep, err := translate.Run(tctx, translate.Options{
		Direction:     dir,
		Only:          onlySet,
		DryRun:        dryRun,
		Force:         force,
		InlineImports: !noInline,
	})
	if rep != nil {
		if dryRun {
			fmt.Fprintln(a.Stdout, "dry run — nothing written")
		}
		rep.Render(a.Stdout, a.isTTY())
	}
	if err == nil && ctxName == st.Current && !dryRun && dir == translate.CodexToClaude {
		// mcp codex->claude edits the context's claude.json; push it live so
		// Claude Code sees it without an extra switch.
		if err := a.pushContextClaudeJSON(ctxName); err != nil {
			return err
		}
	}
	return err
}

func (a *App) cmdDoctor(args []string) error {
	fix := hasFlag(args, "--fix")
	d := doctor.New(a.P, a.S)
	problems := d.Run(a.Stdout, fix)
	if problems > 0 {
		return fmt.Errorf("%d problem(s) found", problems)
	}
	return nil
}
