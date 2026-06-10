package cli

import (
	"fmt"
	"strings"

	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

// commands lists the user-facing subcommands in completion order.
var commands = []string{
	"claude", "codex", "list", "current", "show", "create", "delete",
	"rename", "init", "migrate", "translate", "doctor", "env", "shell",
	"shell-init", "completion", "version", "help",
}

const bashCompletion = `# bash completion for claudectx — add to your shell rc:
#   eval "$(claudectx completion bash)"
_claudectx() {
  local cur=${COMP_WORDS[COMP_CWORD]}
  local candidates
  candidates=$(claudectx __complete "${COMP_WORDS[@]:1:COMP_CWORD}" 2>/dev/null)
  COMPREPLY=()
  while IFS='' read -r line; do
    [[ -n $line ]] && COMPREPLY+=("$line")
  done < <(compgen -W "$candidates" -- "$cur")
}
complete -F _claudectx claudectx
`

const zshCompletion = `#compdef claudectx
# zsh completion for claudectx — either add to your shell rc (after compinit):
#   eval "$(claudectx completion zsh)"
# or install as a file named _claudectx somewhere in your $fpath.
_claudectx() {
  local -a candidates
  candidates=(${(f)"$(claudectx __complete "${(@)words[2,CURRENT]}" 2>/dev/null)"})
  (( ${#candidates} )) && compadd -- "${candidates[@]}"
}
if [[ ${zsh_eval_context[-1]} == loadautofunc ]]; then
  _claudectx "$@"
else
  compdef _claudectx claudectx
fi
`

const fishCompletion = `# fish completion for claudectx — add to your config or save as
# ~/.config/fish/completions/claudectx.fish
complete -c claudectx -f -a '(claudectx __complete (commandline -opc)[2..] (commandline -ct) 2>/dev/null)'
`

// cmdCompletion prints the tab-completion script for one shell. The scripts
// delegate candidate generation to the hidden `__complete` command so
// profile names stay live without regenerating anything.
func (a *App) cmdCompletion(args []string) error {
	var shell string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			shell = arg
			break
		}
	}
	switch shell {
	case "bash":
		fmt.Fprint(a.Stdout, bashCompletion)
	case "zsh":
		fmt.Fprint(a.Stdout, zshCompletion)
	case "fish":
		fmt.Fprint(a.Stdout, fishCompletion)
	default:
		return fmt.Errorf("usage: claudectx completion <bash|zsh|fish>")
	}
	return nil
}

// cmdComplete is the hidden plumbing behind the shell scripts: it receives
// every word typed after "claudectx" (the last one being the in-progress
// word, possibly empty) and prints one candidate per line. Prefix filtering
// is the shell's job. It never fails — no candidates is an empty answer.
func (a *App) cmdComplete(words []string) error {
	if len(words) == 0 {
		words = []string{""}
	}
	done, cur := words[:len(words)-1], words[len(words)-1]
	for _, c := range a.completeCandidates(done, cur) {
		fmt.Fprintln(a.Stdout, c)
	}
	return nil
}

func (a *App) completeCandidates(done []string, cur string) []string {
	if len(done) == 0 {
		return commands
	}
	cmd, rest := done[0], done[1:]
	prev := done[len(done)-1]
	pos := positionals(rest)
	wantFlags := strings.HasPrefix(cur, "-") && cur != "-"

	// Flag-value completion comes first: it applies regardless of position.
	switch prev {
	case "--from":
		if t, ok := firstTool(pos); ok {
			return a.profileNames(t)
		}
		return nil
	case "--claude":
		return a.profileNames(tool.Claude)
	case "--codex":
		return a.profileNames(tool.Codex)
	}

	switch cmd {
	case "claude", "codex":
		t := tool.Tool(cmd)
		if wantFlags {
			if t == tool.Claude {
				return []string{"--force", "--no-keychain"}
			}
			return []string{"--force"}
		}
		if len(pos) == 0 {
			return append(a.profileNames(t), "-")
		}
	case "current":
		if len(pos) == 0 {
			return tools()
		}
	case "show":
		if wantFlags {
			return []string{"--json"}
		}
		return a.toolThenProfile(pos)
	case "create":
		if wantFlags {
			return []string{"--from", "--empty"}
		}
		if len(pos) == 0 {
			return tools()
		}
	case "delete":
		if wantFlags {
			return []string{"--yes"}
		}
		return a.toolThenProfile(pos)
	case "rename":
		if wantFlags {
			return []string{"--force"}
		}
		return a.toolThenProfile(pos)
	case "list":
		if wantFlags {
			return []string{"--json"}
		}
	case "env":
		if wantFlags {
			return []string{"--unset"}
		}
		if len(pos) == 0 {
			return tools()
		}
		if len(pos) == 1 && !hasFlag(rest, "--unset") && !hasFlag(rest, "-u") {
			if t, ok := firstTool(pos); ok {
				return a.profileNames(t)
			}
		}
	case "shell":
		if wantFlags {
			return []string{"--claude", "--codex"}
		}
	case "translate":
		if wantFlags {
			return []string{"--claude", "--codex", "--only", "--dry-run", "--force", "--no-inline-imports"}
		}
		if len(pos) == 0 {
			return []string{"claude-to-codex", "codex-to-claude"}
		}
	case "doctor":
		if wantFlags {
			return []string{"--fix"}
		}
	case "init", "migrate":
		if wantFlags {
			return []string{"--yes"}
		}
	case "completion":
		if len(pos) == 0 {
			return []string{"bash", "zsh", "fish"}
		}
	}
	return nil
}

// toolThenProfile completes the common `<claude|codex> <name>` shape.
func (a *App) toolThenProfile(pos []string) []string {
	if len(pos) == 0 {
		return tools()
	}
	if len(pos) == 1 {
		if t, ok := firstTool(pos); ok {
			return a.profileNames(t)
		}
	}
	return nil
}

// profileNames lists one tool's profiles, swallowing errors — completion
// must stay silent when the store is missing or broken.
func (a *App) profileNames(t tool.Tool) []string {
	names, err := a.S.List(t)
	if err != nil {
		return nil
	}
	return names
}

func tools() []string {
	out := make([]string, len(tool.All))
	for i, t := range tool.All {
		out[i] = string(t)
	}
	return out
}

func firstTool(pos []string) (tool.Tool, bool) {
	for _, p := range pos {
		if t, err := tool.Parse(p); err == nil {
			return t, true
		}
	}
	return "", false
}

// positionals filters out flags (and "-" stays, since it names the previous
// profile).
func positionals(args []string) []string {
	var out []string
	for _, arg := range args {
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			out = append(out, arg)
		}
	}
	return out
}
