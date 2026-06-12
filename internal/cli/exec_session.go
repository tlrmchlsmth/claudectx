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

	"github.com/tlrmchlsmth/claudectx/internal/extras"
	"github.com/tlrmchlsmth/claudectx/internal/snapshot"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

const execSessionUsage = `usage: claudectx exec <claude|codex> [profile] <pod/NAME|docker:NAME|podman:NAME>
          [-n <namespace>] [-c <container>] [--extras gh,kube] [-- <command>...]

Syncs the profile's config into the container, then opens an exec session
with the credential held only in that session's environment
(CLAUDE_CODE_OAUTH_TOKEN / OPENAI_API_KEY) — nothing credential-shaped is
left on the container filesystem. The command defaults to the tool itself.

--extras adds host credentials for other tools to the session: gh (GH_TOKEN/
GITHUB_TOKEN in env, same session-only delivery) and kube (the host
kubeconfig, installed as a file at ~/.kube/config).`

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
	extrasSpec, args := flagValue(args, "--extras")
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
	// Resolve extras eagerly — a missing gh login or kubeconfig should fail
	// before anything touches the container.
	var extraEnv [][2]string
	var extraFiles []snapshot.Entry
	if extrasSpec != "" {
		providers, err := extras.Parse(extrasSpec, a.extrasDeps())
		if err != nil {
			return err
		}
		if extraEnv, err = extras.EnvVars(providers); err != nil {
			return err
		}
		if extraFiles, err = extras.FileEntries(providers); err != nil {
			return err
		}
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

	// 1b. File-form extras (kubeconfig) land under $HOME — config files,
	// not session secrets; the env-form extras stay session-only below.
	if err := a.sendExtraFiles(extraFiles, tgt, ns, ctr); err != nil {
		return err
	}

	// 2. Resolve credentials to env-var form: the tool's own, then extras.
	envVar, token, expires, err := snapshot.EnvCredential(a.P, a.KC, t, name, current)
	if err != nil {
		return err
	}
	var env [][2]string
	if token != "" {
		env = append(env, [2]string{envVar, token})
	}
	env = append(env, extraEnv...)
	tty := a.isTTY() && a.stdinIsTTY()

	if token == "" {
		fmt.Fprintf(a.Stderr, "note: %s profile %q has no env-deliverable credential — session is config-only (fine for Vertex/API-key-in-settings profiles)\n", t, name)
	}
	if len(env) == 0 {
		bin, argv := tgt.shArgv(ns, ctr, tty, command...)
		return sessionErr(a.runner()(a.Stdin, a.Stdout, a.Stderr, bin, argv...))
	}
	if token != "" && !expires.IsZero() {
		if d := time.Until(expires); d <= 0 {
			fmt.Fprintf(a.Stderr, "warning: the access token is ALREADY EXPIRED — log in again on this machine first\n")
		}
	}
	names := make([]string, len(env))
	for i, kv := range env {
		names[i] = kv[0]
	}
	line := fmt.Sprintf("session credentials: %s in env only", strings.Join(names, ", "))
	if token != "" && !expires.IsZero() {
		if d := time.Until(expires); d > 0 {
			line += fmt.Sprintf(" (%s expires in %s)", envVar, d.Round(time.Minute))
		}
	}
	fmt.Fprintln(a.Stderr, line)

	// 3. Hand the credentials over via a 0600 tmpfs file the session
	// consumes and removes before the command starts; it exists for the
	// instant between the two execs and never appears on any argv.
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	var handoff strings.Builder
	for _, kv := range env {
		handoff.WriteString(kv[0] + "=" + shQuote(kv[1]) + "\n")
	}
	fname := ".claudectx-" + hex.EncodeToString(suffix)
	locate := `d=/dev/shm; [ -d "$d" ] && [ -w "$d" ] || d=/tmp; f="$d/` + fname + `"`
	writeScript := `umask 077; ` + locate + `; cat > "$f"`
	runScript := locate + `; set -a; . "$f" 2>/dev/null; set +a; rm -f -- "$f"; exec "$@"`
	cleanupScript := locate + `; rm -f -- "$f"`

	bin, argv = tgt.shArgv(ns, ctr, false, "sh", "-c", writeScript)
	if err := a.runner()(strings.NewReader(handoff.String()), a.Stderr, a.Stderr, bin, argv...); err != nil {
		return fmt.Errorf("credential handoff via %s failed: %w", bin, err)
	}
	remote := append([]string{"sh", "-c", runScript, "sh"}, command...)
	bin, argv = tgt.shArgv(ns, ctr, tty, remote...)
	err = a.runner()(a.Stdin, a.Stdout, a.Stderr, bin, argv...)
	if err != nil {
		// The session may have died before consuming the handoff file —
		// best-effort removal so no credentials linger in the container.
		bin, argv := tgt.shArgv(ns, ctr, false, "sh", "-c", cleanupScript)
		a.runner()(strings.NewReader(""), a.Stderr, a.Stderr, bin, argv...)
	}
	return sessionErr(err)
}

// shQuote single-quotes a value for a sourced sh file.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sendExtraFiles delivers file-form extras in one $HOME-rooted tar (a
// kubeconfig doesn't belong in the tool's config dir).
func (a *App) sendExtraFiles(entries []snapshot.Entry, tgt injectTarget, ns, ctr string) error {
	if len(entries) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if err := snapshot.WriteEntriesTar(&buf, entries); err != nil {
		return err
	}
	bin, argv := tgt.shArgv(ns, ctr, false, "sh", "-c", `exec tar -xf - -C "$HOME"`)
	if err := a.runner()(&buf, a.Stderr, a.Stderr, bin, argv...); err != nil {
		return fmt.Errorf("extras delivery via %s failed: %w", bin, err)
	}
	for _, e := range entries {
		fmt.Fprintf(a.Stderr, "extras: installed ~/%s\n", e.Rel)
	}
	return nil
}

// extrasDeps wires the extras providers to the app's command runner.
func (a *App) extrasDeps() extras.Deps {
	home, _ := os.UserHomeDir()
	return extras.Deps{
		Output: func(name string, args ...string) (string, error) {
			var out bytes.Buffer
			err := a.runner()(strings.NewReader(""), &out, a.Stderr, name, args...)
			return out.String(), err
		},
		Getenv:   os.Getenv,
		UserHome: home,
	}
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
