// Package cli implements the claudectx command surface: kubectx-style
// bare-name dispatch plus explicit subcommands.
package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tlrmchlsmth/claudectx/internal/adopt"
	"github.com/tlrmchlsmth/claudectx/internal/keychain"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/switcher"
)

const usage = `claudectx — manage paired Claude Code + Codex CLI contexts

Usage:
  claudectx                      list contexts (current marked)
  claudectx <name> [--force] [--no-keychain]
                                 switch to context (--force skips the
                                 running-process check)
  claudectx -                    switch to previous context
  claudectx list [--json]
  claudectx current
  claudectx show [name] [--json]
  claudectx create <name> [--from <ctx>] [--empty]
  claudectx delete <name> [--yes]
  claudectx rename <old> <new>
  claudectx init [--yes]
  eval "$(claudectx env <name>)"  pin THIS terminal to a context (--unset to unpin)
  claudectx shell <name>          subshell pinned to a context
  claudectx shell-init            print a 'cx' helper function for your shell rc
  claudectx translate <claude-to-codex|codex-to-claude>
            [--context <name>] [--only instructions,skills,mcp,settings]
            [--dry-run] [--force] [--no-inline-imports]
  claudectx doctor [--fix]
  claudectx version

Environment:
  CLAUDECTX_HOME           state root (default ~/.claudectx)
  CLAUDECTX_CLAUDE_DIR     managed Claude dir (default $CLAUDE_CONFIG_DIR or ~/.claude)
  CLAUDECTX_CODEX_DIR      managed Codex dir (default $CODEX_HOME or ~/.codex)
  CLAUDECTX_CLAUDE_JSON    managed ~/.claude.json path
  CLAUDECTX_NO_KEYCHAIN    set to disable macOS keychain handling
`

// App carries shared dependencies into command implementations.
type App struct {
	P      paths.Paths
	S      *store.Store
	KC     keychain.Backend
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	// Version is stamped from main.
	Version string
	// ProcScan reports running agent processes (testable seam).
	ProcScan func() string
}

func NewApp(p paths.Paths, version string) *App {
	var kc keychain.Backend = keychain.Null{}
	if p.KeychainEnabled {
		kc = keychain.Mac{}
	}
	return &App{
		P: p, S: store.New(p), KC: kc,
		Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin,
		Version:  version,
		ProcScan: runningAgents,
	}
}

// Run dispatches and returns a process exit code.
func (a *App) Run(args []string) int {
	cmd, rest := dispatch(args)

	// Roll forward any interrupted operation before doing anything else —
	// except for commands that must work pre-init.
	if cmd != "init" && cmd != "help" && cmd != "version" {
		if code, fatal := a.recoverIfNeeded(); fatal {
			return code
		}
	}

	var err error
	switch cmd {
	case "help":
		fmt.Fprint(a.Stdout, usage)
	case "version":
		fmt.Fprintf(a.Stdout, "claudectx %s\n", a.Version)
	case "list":
		err = a.cmdList(rest)
	case "current":
		err = a.cmdCurrent()
	case "show":
		err = a.cmdShow(rest)
	case "create":
		err = a.cmdCreate(rest)
	case "delete":
		err = a.cmdDelete(rest)
	case "rename":
		err = a.cmdRename(rest)
	case "init":
		err = a.cmdInit(rest)
	case "translate":
		err = a.cmdTranslate(rest)
	case "doctor":
		err = a.cmdDoctor(rest)
	case "switch":
		err = a.cmdSwitchArgs(rest)
	case "env":
		err = a.cmdEnv(rest)
	case "shell":
		err = a.cmdShell(rest)
	case "shell-init":
		err = a.cmdShellInit()
	default:
		fmt.Fprintf(a.Stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}

	if err != nil {
		fmt.Fprintf(a.Stderr, "claudectx: %v\n", err)
		return 1
	}
	return 0
}

// dispatch maps raw args to (command, remaining args). A bare first arg that
// is not a known command is a switch target; "-" means previous.
func dispatch(args []string) (string, []string) {
	if len(args) == 0 {
		return "list", nil
	}
	first := args[0]
	switch {
	case first == "-h" || first == "--help" || first == "help":
		return "help", nil
	case first == "-":
		return "switch", args
	case store.Reserved[first]:
		return first, args[1:]
	case strings.HasPrefix(first, "-"):
		return "list", args // flags like --json fall through to list
	default:
		return "switch", args
	}
}

// recoverIfNeeded rolls forward an interrupted journaled operation. Returns
// (exitCode, true) when the command must not proceed.
func (a *App) recoverIfNeeded() (int, bool) {
	if !a.S.Initialized() {
		return 0, false // pre-init commands give their own guidance
	}
	st, err := a.S.Load()
	if err != nil {
		fmt.Fprintf(a.Stderr, "claudectx: %v\n", err)
		return 1, true
	}
	if st.InProgress == nil {
		return 0, false
	}
	switch st.InProgress.Op {
	case "switch":
		sw := switcher.New(a.P, a.S, a.KC, a.Stderr)
		if err := sw.Recover(st); err != nil {
			fmt.Fprintf(a.Stderr, "claudectx: recovery failed: %v\n", err)
			return 1, true
		}
	case "init":
		fmt.Fprintln(a.Stderr, "claudectx: a previous `init` was interrupted — run `claudectx init` to resume")
		return 1, true
	}
	return 0, false
}

// cmdSwitchArgs parses `<name> [--force] [--no-keychain]` (name may be "-").
func (a *App) cmdSwitchArgs(args []string) error {
	force := hasFlag(args, "--force") || hasFlag(args, "-f")
	noKeychain := hasFlag(args, "--no-keychain")
	name := ""
	for _, arg := range args {
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			name = arg
			break
		}
	}
	if name == "" {
		return fmt.Errorf("usage: claudectx <name> [--force] [--no-keychain]")
	}
	return a.cmdSwitch(name, force, noKeychain)
}

func (a *App) cmdSwitch(name string, force, noKeychain bool) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	if name == "-" {
		if st.Previous == "" {
			return fmt.Errorf("no previous context")
		}
		name = st.Previous
	}

	if a.P.TerminalContext != "" {
		fmt.Fprintf(a.Stderr, "note: this terminal is pinned to %q via CLAUDE_CONFIG_DIR — the global switch won't affect it (`cx off` to unpin)\n",
			a.P.TerminalContext)
	}
	if procs := a.ProcScan(); len(procs) > 0 && !force {
		fmt.Fprintf(a.Stderr, "warning: running processes are using the current context: %s\n", procs)
		if !a.confirm("switch anyway?") {
			return fmt.Errorf("aborted (use --force to skip this check)")
		}
	}

	p, kc := a.P, a.KC
	if noKeychain {
		p.KeychainEnabled = false
		kc = keychain.Null{}
	}
	sw := switcher.New(p, a.S, kc, a.Stdout)
	err = sw.Switch(name)
	if err == switcher.ErrSameContext {
		fmt.Fprintf(a.Stdout, "already on %q\n", name)
		return nil
	}
	return err
}

// confirm prompts on stderr and reads a y/N answer. Non-TTY stdin answers no.
func (a *App) confirm(prompt string) bool {
	fmt.Fprintf(a.Stderr, "%s [y/N] ", prompt)
	r := bufio.NewReader(a.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func (a *App) cmdInit(args []string) error {
	yes := hasFlag(args, "--yes") || hasFlag(args, "-y")
	ad := adopt.New(a.P, a.S, a.Stdout)
	items, err := ad.Plan()
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, "init will:")
	for _, it := range items {
		fmt.Fprintf(a.Stdout, "  %-40s (%s): %s\n", it.Live, it.Kind, it.Action)
	}
	fmt.Fprintf(a.Stdout, "  %-40s copy into context (live file stays, copy-swapped on switch)\n", a.P.ClaudeJSON)
	if !yes && !a.confirm("proceed?") {
		return fmt.Errorf("aborted")
	}
	return ad.Run(items)
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// flagValue extracts "--name value" or "--name=value"; returns remaining args.
func flagValue(args []string, name string) (string, []string) {
	var out []string
	val := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == name && i+1 < len(args) {
			val = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(arg, name+"=") {
			val = strings.TrimPrefix(arg, name+"=")
			continue
		}
		out = append(out, arg)
	}
	return val, out
}
