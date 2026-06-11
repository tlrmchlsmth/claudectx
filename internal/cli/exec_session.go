package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tlrmchlsmth/claudectx/internal/snapshot"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

const execSessionUsage = `usage: claudectx exec <claude|codex> [profile] <pod/NAME|docker:NAME|podman:NAME>
          [-n <namespace>] [-c <container>] [-- <command>...]

Syncs the profile's config into the container, then opens an exec session
with the credential held only in that session's environment
(CLAUDE_CODE_OAUTH_TOKEN / OPENAI_API_KEY) — nothing credential-shaped is
left on the container filesystem. The command defaults to the tool itself.`

// exitCodeError carries the session command's exit status up through Run
// without claudectx adding error noise — `claudectx exec ... -- claude -p`
// must be scriptable.
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("session exited with status %d", e.code) }

// cmdExecSession is the session-scoped sibling of inject: same config
// snapshot, but the credential travels as an env var assembled inside the
// session, not as a file. The token transits the container only via stdin
// and a 0600 tmpfs handoff file that the session consumes and removes
// before the command starts.
func (a *App) cmdExecSession(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	var command []string
	for i, arg := range args {
		if arg == "--" {
			command = args[i+1:]
			args = args[:i]
			break
		}
	}
	ns, args := flagValue(args, "--namespace")
	if ns == "" {
		ns, args = flagValue(args, "-n")
	}
	ctr, args := flagValue(args, "--container")
	if ctr == "" {
		ctr, args = flagValue(args, "-c")
	}
	var positional []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
		}
	}
	if len(positional) < 2 || len(positional) > 3 {
		return fmt.Errorf("%s", execSessionUsage)
	}
	t, err := tool.Parse(positional[0])
	if err != nil {
		return err
	}
	name := st.Axis(t).Current
	if len(positional) == 3 {
		name = positional[1]
	}
	tgt, ok := parseInjectTarget(positional[len(positional)-1])
	if !ok || tgt.kind == "dir" {
		return fmt.Errorf("exec needs a running container target (pod/NAME, docker:NAME, podman:NAME)\n\n%s", execSessionUsage)
	}
	if !a.S.Exists(t, name) {
		return fmt.Errorf("no such %s profile %q", t, name)
	}
	if (ns != "" || ctr != "") && tgt.kind != "pod" {
		return fmt.Errorf("-n/-c only apply to pod/ targets")
	}
	if len(command) == 0 {
		command = []string{string(t)}
	}
	current := name == st.Axis(t).Current

	// 1. Sync config — never credentials; that is the point of exec.
	snap, err := snapshot.Build(a.P, a.KC, t, name, current, snapshot.Options{})
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := snap.WriteTar(&buf); err != nil {
		return err
	}
	bin, argv := tgt.shArgv(ns, ctr, false, "sh", "-c", remoteScript(t, ""))
	if err := a.runner()(&buf, a.Stderr, a.Stderr, bin, argv...); err != nil {
		return fmt.Errorf("config sync via %s failed: %w (the target needs `sh` and `tar`)", bin, err)
	}

	// 2. Resolve the credential to env-var form.
	envVar, token, expires, err := snapshot.EnvCredential(a.P, a.KC, t, name, current)
	if err != nil {
		return err
	}
	tty := a.isTTY() && a.stdinIsTTY()

	if token == "" {
		fmt.Fprintf(a.Stderr, "note: %s profile %q has no env-deliverable credential — session is config-only (fine for Vertex/API-key-in-settings profiles)\n", t, name)
		bin, argv := tgt.shArgv(ns, ctr, tty, command...)
		return sessionErr(a.runner()(a.Stdin, a.Stdout, a.Stderr, bin, argv...))
	}
	if !expires.IsZero() {
		if d := time.Until(expires); d <= 0 {
			fmt.Fprintf(a.Stderr, "warning: the access token is ALREADY EXPIRED — log in again on this machine first\n")
		} else {
			fmt.Fprintf(a.Stderr, "session credential: %s in env only (expires in %s)\n", envVar, d.Round(time.Minute))
		}
	} else {
		fmt.Fprintf(a.Stderr, "session credential: %s in env only\n", envVar)
	}

	// 3. Hand the token over via a 0600 tmpfs file the session consumes
	// and removes before the command starts; it exists for the instant
	// between the two execs and never appears on any argv.
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	fname := ".claudectx-" + hex.EncodeToString(suffix)
	locate := `d=/dev/shm; [ -d "$d" ] && [ -w "$d" ] || d=/tmp; f="$d/` + fname + `"`
	writeScript := `umask 077; ` + locate + `; cat > "$f"`
	runScript := locate + `; ` + envVar + `=$(cat "$f" 2>/dev/null); export ` + envVar + `; rm -f -- "$f"; exec "$@"`
	cleanupScript := locate + `; rm -f -- "$f"`

	bin, argv = tgt.shArgv(ns, ctr, false, "sh", "-c", writeScript)
	if err := a.runner()(strings.NewReader(token), a.Stderr, a.Stderr, bin, argv...); err != nil {
		return fmt.Errorf("credential handoff via %s failed: %w", bin, err)
	}
	remote := append([]string{"sh", "-c", runScript, "sh"}, command...)
	bin, argv = tgt.shArgv(ns, ctr, tty, remote...)
	err = a.runner()(a.Stdin, a.Stdout, a.Stderr, bin, argv...)
	if err != nil {
		// The session may have died before consuming the handoff file —
		// best-effort removal so no token lingers in the container.
		bin, argv := tgt.shArgv(ns, ctr, false, "sh", "-c", cleanupScript)
		a.runner()(strings.NewReader(""), a.Stderr, a.Stderr, bin, argv...)
	}
	return sessionErr(err)
}

// sessionErr converts the session command's own exit status into a silent
// exit code; anything else (kubectl missing, connection refused) stays a
// real error.
func sessionErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitCodeError{code: ee.ExitCode()}
	}
	return err
}

func (a *App) stdinIsTTY() bool {
	f, ok := a.Stdin.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// shArgv builds the runtime exec argv around an arbitrary remote command
// vector. tty adds -t for interactive sessions.
func (t injectTarget) shArgv(ns, ctr string, tty bool, remote ...string) (string, []string) {
	if t.kind == "pod" {
		argv := []string{"exec", "-i"}
		if tty {
			argv = append(argv, "-t")
		}
		if ns != "" {
			argv = append(argv, "-n", ns)
		}
		if ctr != "" {
			argv = append(argv, "-c", ctr)
		}
		argv = append(argv, t.name, "--")
		return "kubectl", append(argv, remote...)
	}
	argv := []string{"exec", "-i"}
	if tty {
		argv = append(argv, "-t")
	}
	return t.kind, append(append(argv, t.name), remote...)
}
