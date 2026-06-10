package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// cmdEnv prints eval-able exports that pin the calling terminal to a context
// via CLAUDE_CONFIG_DIR / CODEX_HOME (both tools honor these natively).
// Usage: eval "$(claudectx env work)"   /   eval "$(claudectx env --unset)"
func (a *App) cmdEnv(args []string) error {
	if hasFlag(args, "--unset") || hasFlag(args, "-u") {
		fmt.Fprintln(a.Stdout, "unset CLAUDE_CONFIG_DIR CODEX_HOME")
		return nil
	}
	var name string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			name = arg
			break
		}
	}
	if name == "" {
		return fmt.Errorf("usage: eval \"$(claudectx env <name>)\"  (or --unset)")
	}
	if _, err := a.S.Load(); err != nil {
		return err
	}
	if !a.S.Exists(name) {
		return fmt.Errorf("no such context %q (see `claudectx list`)", name)
	}
	fmt.Fprintf(a.Stdout, "export CLAUDE_CONFIG_DIR=%q\n", a.P.CtxClaudeDir(name))
	fmt.Fprintf(a.Stdout, "export CODEX_HOME=%q\n", a.P.CtxCodexDir(name))
	// Informational, safe under eval. The keychain is per-user, not
	// per-terminal, so env-pinned terminals share the global Claude login.
	fmt.Fprintf(a.Stdout, "# terminal pinned to context %q (Claude OAuth login stays global)\n", name)
	return nil
}

// cmdShell launches a subshell pinned to a context. Exiting the shell
// returns to the previous scope.
func (a *App) cmdShell(args []string) error {
	var name string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			name = arg
			break
		}
	}
	if name == "" {
		return fmt.Errorf("usage: claudectx shell <name>")
	}
	if _, err := a.S.Load(); err != nil {
		return err
	}
	if !a.S.Exists(name) {
		return fmt.Errorf("no such context %q (see `claudectx list`)", name)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(),
		"CLAUDE_CONFIG_DIR="+a.P.CtxClaudeDir(name),
		"CODEX_HOME="+a.P.CtxCodexDir(name),
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	fmt.Fprintf(a.Stderr, "spawning %s pinned to context %q (exit to leave)\n", shell, name)
	return cmd.Run()
}

const shellInit = `# claudectx shell integration — add to your shell rc:
#   eval "$(claudectx shell-init)"
cx() {
  case "$1" in
    "")  claudectx list ;;
    off) eval "$(claudectx env --unset)" && echo "terminal unpinned (following global context)" ;;
    *)   eval "$(claudectx env "$1")" && echo "terminal pinned to $1" ;;
  esac
}
`

// cmdShellInit prints a `cx` helper: `cx work` pins the current terminal,
// `cx off` unpins, `cx` lists.
func (a *App) cmdShellInit() error {
	fmt.Fprint(a.Stdout, shellInit)
	return nil
}
