package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tlrmchlsmth/claudectx/internal/doctor"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
	"github.com/tlrmchlsmth/claudectx/internal/translate"
)

func (a *App) cmdTranslate(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}

	claudeProfile, args := flagValue(args, "--claude")
	codexProfile, args := flagValue(args, "--codex")
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
		return fmt.Errorf("usage: claudectx translate <claude-to-codex|codex-to-claude> [--claude <profile>] [--codex <profile>] [--only %s] [--dry-run] [--force] [--no-inline-imports]",
			strings.Join(translate.TranslatorNames(), ","))
	}
	dir, err := translate.ParseDirection(dirArg)
	if err != nil {
		return err
	}

	if claudeProfile == "" {
		claudeProfile = st.Claude.Current
	}
	if codexProfile == "" {
		codexProfile = st.Codex.Current
	}
	if !a.S.Exists(tool.Claude, claudeProfile) {
		return fmt.Errorf("no such claude profile %q", claudeProfile)
	}
	if !a.S.Exists(tool.Codex, codexProfile) {
		return fmt.Errorf("no such codex profile %q", codexProfile)
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

	// Translating the current claude profile: capture the freshest live
	// claude.json first, since MCP translation reads the profile copy.
	claudeIsCurrent := claudeProfile == st.Claude.Current
	if claudeIsCurrent && !dryRun {
		if err := a.captureLiveClaudeJSON(claudeProfile); err != nil {
			return err
		}
	}

	tctx := translate.Context{
		Name:       fmt.Sprintf("claude:%s -> codex:%s", claudeProfile, codexProfile),
		ClaudeDir:  a.P.ProfileHome(tool.Claude, claudeProfile),
		CodexDir:   a.P.ProfileHome(tool.Codex, codexProfile),
		ClaudeJSON: a.P.ProfileClaudeJSON(claudeProfile),
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
	if err == nil && claudeIsCurrent && !dryRun && dir == translate.CodexToClaude {
		// mcp codex->claude edits the profile's claude.json; push it live so
		// Claude Code sees it without an extra switch.
		if err := a.pushProfileClaudeJSON(claudeProfile); err != nil {
			return err
		}
	}
	return err
}

// captureLiveClaudeJSON refreshes the profile's claude.json copy from the
// live file (used before translating the current claude profile).
func (a *App) captureLiveClaudeJSON(name string) error {
	err := store.CopyFileAtomic(a.P.ClaudeJSON, a.P.ProfileClaudeJSON(name), 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// pushProfileClaudeJSON propagates the profile's claude.json back to the
// live file (used after a translation edits the current profile's copy).
func (a *App) pushProfileClaudeJSON(name string) error {
	err := store.CopyFileAtomic(a.P.ProfileClaudeJSON(name), a.P.ClaudeJSON, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
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
