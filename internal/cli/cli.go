// Package cli implements the claudectx command surface: per-tool profile
// switching plus explicit management subcommands.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tlrmchlsmth/claudectx/internal/adopt"
	"github.com/tlrmchlsmth/claudectx/internal/keychain"
	"github.com/tlrmchlsmth/claudectx/internal/migrate"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/switcher"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

const usage = `claudectx — per-tool profiles for Claude Code and Codex CLI

Usage:
  claudectx                       status: both tools' current profiles
  claudectx claude <name> [--force] [--no-keychain]
                                  switch the Claude profile
  claudectx codex <name> [--force]
                                  switch the Codex profile
  claudectx claude - / codex -    switch that tool to its previous profile
  claudectx claude / codex        list that tool's profiles
  claudectx list [--json]         list both tools' profiles
  claudectx current [claude|codex]
  claudectx show <claude|codex> [name] [--json]
  claudectx create <claude|codex> <name> [--from [<profile>]]
                                  empty by default; --from <p> clones a
                                  profile (--from alone clones the current)
  claudectx delete <claude|codex> <name> [--yes]
  claudectx rename <claude|codex> <old> <new> [--force]
  claudectx init [--yes]          adopt existing ~/.claude + ~/.codex
  claudectx migrate [--yes]       upgrade a v1 paired-context layout
  eval "$(claudectx env <claude|codex> <name>)"
                                  pin THIS terminal's tool to a profile
  claudectx env --unset [<tool>]  unpin one or both tools
  claudectx shell [--claude <p>] [--codex <p>]
                                  subshell with the given pins
  claudectx shell-init            print a 'cx' helper for your shell rc
  claudectx completion <bash|zsh|fish>
                                  print a tab-completion script
  claudectx inject <claude|codex> [profile] <pod/NAME|docker:NAME|podman:NAME|dir:PATH>
            [-n <namespace>] [-c <container>] [--dest <path>]
            [--with-creds] [--with-refresh-token] [--dry-run]
                                  copy a profile into a container's config dir
  claudectx exec <claude|codex> [profile] <pod/NAME|docker:NAME|podman:NAME>
            [-n <namespace>] [-c <container>] [-- <command>...]
                                  session in the container: config synced, the
                                  credential only in the session's env — never
                                  on the container filesystem
  claudectx translate <claude-to-codex|codex-to-claude>
            [--claude <profile>] [--codex <profile>]
            [--only instructions,skills,mcp,settings]
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
	// ProcScan reports running agent processes for a tool ("" = all;
	// testable seam).
	ProcScan func(t tool.Tool) string
	// Exec runs external commands for inject transports (kubectl, docker);
	// nil means the real default (testable seam).
	Exec CmdRunner
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
	// except for commands that must work pre-init or pre-migration, and the
	// completion plumbing, which must stay fast and side-effect-free.
	switch cmd {
	case "init", "help", "version", "migrate", "completion", "__complete":
	default:
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
	case "status":
		err = a.cmdStatus()
	case "claude":
		err = a.cmdTool(tool.Claude, rest)
	case "codex":
		err = a.cmdTool(tool.Codex, rest)
	case "list":
		err = a.cmdList(rest)
	case "current":
		err = a.cmdCurrent(rest)
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
	case "migrate":
		err = a.cmdMigrate(rest)
	case "translate":
		err = a.cmdTranslate(rest)
	case "doctor":
		err = a.cmdDoctor(rest)
	case "inject":
		err = a.cmdInject(rest)
	case "exec":
		err = a.cmdExecSession(rest)
	case "env":
		err = a.cmdEnv(rest)
	case "shell":
		err = a.cmdShell(rest)
	case "shell-init":
		err = a.cmdShellInit()
	case "completion":
		err = a.cmdCompletion(rest)
	case "__complete":
		err = a.cmdComplete(rest)
	default:
		a.unknownCommand(cmd)
		return 2
	}

	// A session command's own exit status passes through silently — that
	// failure belongs to the command the user ran, not to claudectx.
	var ec exitCodeError
	if errors.As(err, &ec) {
		return ec.code
	}
	if errors.Is(err, store.ErrV1State) {
		fmt.Fprintf(a.Stderr, "claudectx: %v\n", err)
		return 1
	}
	if err != nil {
		fmt.Fprintf(a.Stderr, "claudectx: %v\n", err)
		return 1
	}
	return 0
}

// dispatch maps raw args to (command, remaining args). Switching is always
// per-tool ("claude"/"codex" subcommands); there is no bare-name switch.
func dispatch(args []string) (string, []string) {
	if len(args) == 0 {
		return "status", nil
	}
	first := args[0]
	switch {
	case first == "-h" || first == "--help" || first == "help":
		return "help", nil
	case store.Reserved[first]:
		return first, args[1:]
	case first == "-":
		return first, args[1:] // per-tool guidance in unknownCommand
	case strings.HasPrefix(first, "-"):
		return "status", args
	default:
		return first, args[1:] // unknown — handled with guidance in Run
	}
}

// unknownCommand explains the per-tool surface, suggesting a switch command
// when the arg names an existing profile (old muscle memory from v1).
func (a *App) unknownCommand(arg string) {
	for _, t := range tool.All {
		if a.S.Exists(t, arg) {
			fmt.Fprintf(a.Stderr, "claudectx: switching is per-tool now — did you mean `claudectx %s %s`?\n", t, arg)
			return
		}
	}
	if arg == "-" {
		fmt.Fprintf(a.Stderr, "claudectx: '-' is per-tool now: `claudectx claude -` or `claudectx codex -`\n")
		return
	}
	fmt.Fprintf(a.Stderr, "unknown command %q\n\n%s", arg, usage)
}

// cmdTool handles `claudectx <tool> ...`: bare = list, "-" = previous,
// otherwise switch.
func (a *App) cmdTool(t tool.Tool, args []string) error {
	var name string
	for _, arg := range args {
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			name = arg
			break
		}
	}
	if name == "" {
		return a.listTool(t)
	}
	force := hasFlag(args, "--force") || hasFlag(args, "-f")
	noKeychain := hasFlag(args, "--no-keychain")
	return a.cmdSwitch(t, name, force, noKeychain)
}

// recoverIfNeeded rolls forward an interrupted journaled operation. Returns
// (exitCode, true) when the command must not proceed.
func (a *App) recoverIfNeeded() (int, bool) {
	if !a.S.Initialized() {
		return 0, false // pre-init commands give their own guidance
	}
	st, err := a.S.Load()
	if errors.Is(err, store.ErrV1State) {
		return 0, false // per-command Load surfaces the migrate guidance
	}
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
	case "migrate":
		m := migrate.New(a.P, a.S, a.Stderr)
		if err := m.Recover(st); err != nil {
			fmt.Fprintf(a.Stderr, "claudectx: migration recovery failed: %v\n", err)
			return 1, true
		}
	case "init":
		fmt.Fprintln(a.Stderr, "claudectx: a previous `init` was interrupted — run `claudectx init` to resume")
		return 1, true
	}
	return 0, false
}

func (a *App) cmdSwitch(t tool.Tool, name string, force, noKeychain bool) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	axis := st.Axis(t)
	if name == "-" {
		if axis.Previous == "" {
			return fmt.Errorf("no previous %s profile", t)
		}
		name = axis.Previous
	}

	if pin := a.P.TerminalPin(t); pin != "" {
		fmt.Fprintf(a.Stderr, "note: this terminal pins %s to %q via env — the global switch won't affect it (`cx off` to unpin)\n", t, pin)
	}
	if procs := a.ProcScan(t); len(procs) > 0 && !force {
		fmt.Fprintf(a.Stderr, "warning: running processes are using the current %s profile: %s\n", t, procs)
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
	err = sw.Switch(t, name)
	if errors.Is(err, switcher.ErrSameProfile) {
		fmt.Fprintf(a.Stdout, "%s is already on %q\n", t, name)
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
	if a.S.Initialized() {
		if _, err := a.S.Load(); errors.Is(err, store.ErrV1State) {
			return err
		}
	}
	ad := adopt.New(a.P, a.S, a.Stdout)
	items, err := ad.Plan()
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, "init will:")
	for _, it := range items {
		fmt.Fprintf(a.Stdout, "  %-40s (%s): %s\n", it.Live, it.Kind, it.Action)
	}
	fmt.Fprintf(a.Stdout, "  %-40s copy into claude profile (live file stays, copy-swapped on switch)\n", a.P.ClaudeJSON)
	if !yes && !a.confirm("proceed?") {
		return fmt.Errorf("aborted")
	}
	return ad.Run(items)
}

func (a *App) cmdMigrate(args []string) error {
	yes := hasFlag(args, "--yes") || hasFlag(args, "-y")
	m := migrate.New(a.P, a.S, a.Stdout)
	plan, err := m.Plan()
	if err != nil {
		return err
	}
	if plan == nil {
		return nil // already v2; Plan printed the message
	}
	m.PrintPlan(plan)
	if ps := a.ProcScan(""); ps != "" {
		fmt.Fprintf(a.Stderr, "note: running agents detected (%s). Migration uses atomic renames; open files keep working, but new opens during the (sub-millisecond) relink window could fail.\n", ps)
	}
	if !yes && !a.confirm("migrate now?") {
		return fmt.Errorf("aborted")
	}
	return m.Run(plan)
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
