package cli

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/tlrmchlsmth/claudectx/internal/snapshot"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

// CmdRunner executes an external command with the given stdin, streaming
// its output to the given writers. Seam for tests.
type CmdRunner func(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error

func defaultRunner(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func (a *App) runner() CmdRunner {
	if a.Exec != nil {
		return a.Exec
	}
	return defaultRunner
}

const injectUsage = `usage: claudectx inject <claude|codex> [profile] <target>
          [-n <namespace>] [-c <container>] [--dest <path>]
          [--with-creds] [--with-refresh-token] [--dry-run]

targets:
  pod/<name>       kubernetes pod (via kubectl exec)
  docker:<name>    docker container (via docker exec)
  podman:<name>    podman container (via podman exec)
  dir:<path>       local directory (for mounts / devcontainers)`

// injectTarget is a parsed delivery destination.
type injectTarget struct {
	kind string // "pod", "docker", "podman", "dir"
	name string
}

func (t injectTarget) String() string {
	if t.kind == "pod" {
		return "pod/" + t.name
	}
	return t.kind + ":" + t.name
}

func parseInjectTarget(s string) (injectTarget, bool) {
	// pod follows kubectl's pod/NAME spelling (pod:NAME also accepted);
	// the rest are colon-prefixed so dir: paths can contain slashes.
	for _, prefix := range []string{"pod/", "pod:"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok && rest != "" {
			return injectTarget{kind: "pod", name: rest}, true
		}
	}
	for _, kind := range []string{"docker", "podman", "dir"} {
		if rest, ok := strings.CutPrefix(s, kind+":"); ok && rest != "" {
			return injectTarget{kind: kind, name: rest}, true
		}
	}
	return injectTarget{}, false
}

// cmdInject snapshots a profile and lands it in a container's config dir.
// Secrets travel only inside the exec stream — never argv, never local
// files (except an explicit dir: target). See docs/design/inject.md.
func (a *App) cmdInject(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	ns, args := flagValue(args, "--namespace")
	if ns == "" {
		ns, args = flagValue(args, "-n")
	}
	ctr, args := flagValue(args, "--container")
	if ctr == "" {
		ctr, args = flagValue(args, "-c")
	}
	dest, args := flagValue(args, "--dest")
	withRefresh := hasFlag(args, "--with-refresh-token")
	withCreds := hasFlag(args, "--with-creds") || withRefresh
	dryRun := hasFlag(args, "--dry-run")

	var positional []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
		}
	}
	if len(positional) < 2 || len(positional) > 3 {
		return fmt.Errorf("%s", injectUsage)
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
	if !ok {
		if alt, ok := parseInjectTarget(positional[1]); ok && len(positional) == 3 {
			return fmt.Errorf("the target comes last: claudectx inject %s %s %s", t, positional[2], alt)
		}
		return fmt.Errorf("unrecognized target %q\n\n%s", positional[len(positional)-1], injectUsage)
	}
	if !a.S.Exists(t, name) {
		return fmt.Errorf("no such %s profile %q", t, name)
	}
	if (ns != "" || ctr != "") && tgt.kind != "pod" {
		return fmt.Errorf("-n/-c only apply to pod/ targets")
	}
	if withRefresh && t == tool.Codex {
		fmt.Fprintln(a.Stderr, "note: --with-refresh-token only affects claude; codex auth.json travels whole under --with-creds")
	}

	current := name == st.Axis(t).Current
	snap, err := snapshot.Build(a.P, a.KC, t, name, current, snapshot.Options{
		WithCreds:        withCreds,
		WithRefreshToken: withRefresh,
	})
	if err != nil {
		return err
	}
	if withCreds && !snap.Cred.Included {
		fmt.Fprintf(a.Stderr, "warning: no credentials found for %s profile %q — injecting config only\n", t, name)
	}

	if dryRun {
		fmt.Fprintf(a.Stdout, "would inject %s profile %q into %s (%s):\n", t, name, tgt, destLabel(t, dest))
		for _, e := range snap.Entries {
			fmt.Fprintf(a.Stdout, "  %s\n", e.Rel)
		}
		a.reportInject(snap, withCreds)
		return nil
	}

	if tgt.kind == "dir" {
		if snap.Cred.Included {
			fmt.Fprintf(a.Stderr, "warning: writing credentials to local disk at %s — keep it out of images and version control\n", tgt.name)
		}
		if err := snap.WriteDir(tgt.name); err != nil {
			return err
		}
	} else {
		if snap.Cred.Included {
			fmt.Fprintf(a.Stderr, "warning: anyone who can exec into %s can read the injected credentials\n", tgt)
		}
		var buf bytes.Buffer
		if err := snap.WriteTar(&buf); err != nil {
			return err
		}
		bin, argv := tgt.execArgv(ns, ctr, remoteScript(t, dest))
		if err := a.runner()(&buf, a.Stderr, a.Stderr, bin, argv...); err != nil {
			return fmt.Errorf("%s %s failed: %w (the target needs `sh` and `tar`)", bin, argv[0], err)
		}
	}

	fmt.Fprintf(a.Stdout, "injected %s profile %q into %s (%s, %d files, %s)\n",
		t, name, tgt, destLabel(t, dest), len(snap.Entries), humanBytes(snap.TotalBytes()))
	a.reportInject(snap, withCreds)
	if dest != "" {
		envVar := "CLAUDE_CONFIG_DIR"
		if t == tool.Codex {
			envVar = "CODEX_HOME"
		}
		fmt.Fprintf(a.Stdout, "  non-default destination: run %s with %s=%s\n", t, envVar, dest)
	}
	return nil
}

// reportInject prints the credential and exclusion summary.
func (a *App) reportInject(snap *snapshot.Snapshot, withCreds bool) {
	switch {
	case snap.Cred.Included && snap.Cred.RefreshStripped:
		line := "  credentials: access token only (refresh token stays on this machine"
		if !snap.Cred.ExpiresAt.IsZero() {
			if d := time.Until(snap.Cred.ExpiresAt); d > 0 {
				line += fmt.Sprintf("; expires in %s — re-run inject to refresh", d.Round(time.Minute))
			} else {
				line += "; ALREADY EXPIRED — log in again on this machine first"
			}
		}
		fmt.Fprintln(a.Stdout, line+")")
	case snap.Cred.Included:
		fmt.Fprintln(a.Stdout, "  credentials: included")
	case !withCreds:
		fmt.Fprintln(a.Stdout, "  credentials: not included (--with-creds to include)")
	}
	if len(snap.Skipped) > 0 {
		fmt.Fprintf(a.Stdout, "  skipped host-private state: %s\n", strings.Join(snap.Skipped, ", "))
	}
}

// remoteScript builds the in-container extraction command. The default
// destination is the tool's own default config dir, so the tool works with
// no env var; $HOME is expanded by the remote shell. An explicit --dest is
// single-quoted so the remote shell takes it literally.
func remoteScript(t tool.Tool, dest string) string {
	destExpr := `"$HOME"/.claude`
	if t == tool.Codex {
		destExpr = `"$HOME"/.codex`
	}
	if dest != "" {
		destExpr = "'" + strings.ReplaceAll(dest, "'", `'\''`) + "'"
	}
	return fmt.Sprintf(`mkdir -p -- %s && exec tar -xf - -C %s`, destExpr, destExpr)
}

func destLabel(t tool.Tool, dest string) string {
	if dest != "" {
		return dest
	}
	if t == tool.Codex {
		return "~/.codex"
	}
	return "~/.claude"
}

// execArgv builds the runtime command that receives the tar on stdin.
func (t injectTarget) execArgv(ns, ctr, script string) (string, []string) {
	if t.kind == "pod" {
		argv := []string{"exec", "-i"}
		if ns != "" {
			argv = append(argv, "-n", ns)
		}
		if ctr != "" {
			argv = append(argv, "-c", ctr)
		}
		argv = append(argv, t.name, "--", "sh", "-c", script)
		return "kubectl", argv
	}
	return t.kind, []string{"exec", "-i", t.name, "sh", "-c", script}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
