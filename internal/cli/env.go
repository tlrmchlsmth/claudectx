package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

func envVarFor(t tool.Tool) string {
	if t == tool.Claude {
		return "CLAUDE_CONFIG_DIR"
	}
	return "CODEX_HOME"
}

// cmdEnv prints eval-able exports that pin the calling terminal's tool to a
// profile via CLAUDE_CONFIG_DIR / CODEX_HOME (both tools honor these
// natively). Usage:
//
//	eval "$(claudectx env claude work)"
//	eval "$(claudectx env --unset)"        # both tools
//	eval "$(claudectx env --unset codex)"  # one tool
func (a *App) cmdEnv(args []string) error {
	if hasFlag(args, "--unset") || hasFlag(args, "-u") {
		for _, arg := range args {
			if t, err := tool.Parse(arg); err == nil {
				fmt.Fprintf(a.Stdout, "unset %s\n", envVarFor(t))
				return nil
			}
		}
		fmt.Fprintln(a.Stdout, "unset CLAUDE_CONFIG_DIR CODEX_HOME")
		return nil
	}
	var positional []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
		}
	}
	if len(positional) != 2 {
		return fmt.Errorf("usage: eval \"$(claudectx env <claude|codex> <name>)\"  (or env --unset [<tool>])")
	}
	t, err := tool.Parse(positional[0])
	if err != nil {
		return err
	}
	name := positional[1]
	if _, err := a.S.Load(); err != nil {
		return err
	}
	if !a.S.Exists(t, name) {
		return fmt.Errorf("no such %s profile %q (see `claudectx %s`)", t, name, t)
	}
	fmt.Fprintf(a.Stdout, "export %s=%q\n", envVarFor(t), a.P.ProfileHome(t, name))
	if t == tool.Claude {
		// Informational, safe under eval. The keychain is per-user, not
		// per-terminal, so pinned terminals share the global Claude login.
		fmt.Fprintf(a.Stdout, "# terminal claude pinned to %q (Claude OAuth login stays global)\n", name)
	} else {
		fmt.Fprintf(a.Stdout, "# terminal codex pinned to %q\n", name)
	}
	return nil
}

// cmdShell launches a subshell with the given per-tool pins. Exiting the
// shell returns to the previous scope.
func (a *App) cmdShell(args []string) error {
	claudeProfile, args := flagValue(args, "--claude")
	codexProfile, _ := flagValue(args, "--codex")
	if claudeProfile == "" && codexProfile == "" {
		return fmt.Errorf("usage: claudectx shell [--claude <profile>] [--codex <profile>] (at least one)")
	}
	if _, err := a.S.Load(); err != nil {
		return err
	}
	env := os.Environ()
	var desc []string
	if claudeProfile != "" {
		if !a.S.Exists(tool.Claude, claudeProfile) {
			return fmt.Errorf("no such claude profile %q", claudeProfile)
		}
		env = append(env, "CLAUDE_CONFIG_DIR="+a.P.ProfileHome(tool.Claude, claudeProfile))
		desc = append(desc, "claude="+claudeProfile)
	}
	if codexProfile != "" {
		if !a.S.Exists(tool.Codex, codexProfile) {
			return fmt.Errorf("no such codex profile %q", codexProfile)
		}
		env = append(env, "CODEX_HOME="+a.P.ProfileHome(tool.Codex, codexProfile))
		desc = append(desc, "codex="+codexProfile)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Fprintf(a.Stderr, "spawning %s pinned to %s (exit to leave)\n", shell, strings.Join(desc, ", "))
	return cmd.Run()
}

const shellInit = `# claudectx shell integration — add to your shell rc:
#   eval "$(claudectx shell-init)"
cx() {
  case "$1" in
    "")            claudectx ;;
    off)           eval "$(claudectx env --unset)" && echo "terminal unpinned (following global profiles)" ;;
    claude|codex)  eval "$(claudectx env "$1" "$2")" && echo "terminal $1 pinned to $2" ;;
    *)             echo "usage: cx [claude|codex <profile> | off]" >&2; return 2 ;;
  esac
}
`

// cmdShellInit prints a `cx` helper: `cx claude work` pins this terminal's
// Claude, `cx codex work` its Codex, `cx off` unpins both, bare `cx` shows
// status.
func (a *App) cmdShellInit() error {
	fmt.Fprint(a.Stdout, shellInit)
	return nil
}
