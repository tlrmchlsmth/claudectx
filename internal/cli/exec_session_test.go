package cli_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestExecSessionTokenHandoff(t *testing.T) {
	h, fe := injectHarness(t)
	h.mustRun(t, "create", "claude", "work", "--from")
	stashCreds(h, "work")

	h.mustRun(t, "exec", "claude", "work", "pod/p", "-n", "dev")
	if len(fe.calls) != 3 {
		t.Fatalf("want 3 transport calls (config, handoff, session), got %d", len(fe.calls))
	}
	cfg, handoff, session := fe.calls[0], fe.calls[1], fe.calls[2]

	if _, has := cfg.files["settings.json"]; !has {
		t.Error("config sync missing settings.json")
	}
	if _, has := cfg.files[".credentials.json"]; has {
		t.Error("exec must never write credentials to the container fs")
	}

	if string(handoff.stdin) != "CLAUDE_CODE_OAUTH_TOKEN='sk-ant-oat01-x'\n" {
		t.Errorf("handoff stdin = %q, want a sourceable KEY='value' line", handoff.stdin)
	}
	script := handoff.args[len(handoff.args)-1]
	if !strings.Contains(script, "umask 077") || !strings.Contains(script, "/dev/shm") {
		t.Errorf("handoff script = %q", script)
	}

	sess := strings.Join(session.args, " ")
	if !strings.Contains(sess, `set -a; . "$f"`) || !strings.Contains(sess, "rm -f") {
		t.Errorf("session script should source-export the handoff file and remove it: %s", sess)
	}
	if session.args[len(session.args)-1] != "claude" {
		t.Errorf("default command should be the tool itself: %v", session.args)
	}
	if !strings.Contains(sess, "-n dev") {
		t.Errorf("namespace missing: %s", sess)
	}

	// The token must never appear on any argv, of any call.
	for _, c := range fe.calls {
		for _, arg := range c.args {
			if strings.Contains(arg, "sk-ant-") {
				t.Fatalf("token leaked into argv: %q", arg)
			}
		}
	}
	if !strings.Contains(h.err.String(), "CLAUDE_CODE_OAUTH_TOKEN in env only") {
		t.Errorf("stderr: %s", h.err.String())
	}
}

func TestExecSessionCustomCommand(t *testing.T) {
	h, fe := injectHarness(t)
	h.mustRun(t, "create", "claude", "work")
	stashCreds(h, "work")
	h.mustRun(t, "exec", "claude", "work", "docker:dev", "--", "bash", "-l")
	session := fe.calls[len(fe.calls)-1]
	got := strings.Join(session.args, " ")
	if !strings.HasSuffix(got, "bash -l") {
		t.Errorf("session argv = %v", session.args)
	}
	if session.bin != "docker" {
		t.Errorf("bin = %q", session.bin)
	}
}

func TestExecSessionConfigOnlyWithoutCredential(t *testing.T) {
	h, fe := injectHarness(t)
	// The default profile has no stash/credential — e.g. a Vertex profile.
	h.mustRun(t, "exec", "claude", "pod/p")
	if len(fe.calls) != 2 {
		t.Fatalf("want 2 calls (config, direct session), got %d", len(fe.calls))
	}
	session := fe.calls[1]
	sess := strings.Join(session.args, " ")
	if strings.Contains(sess, "export") || strings.Contains(sess, "/dev/shm") {
		t.Errorf("config-only session should run the command directly: %s", sess)
	}
	if session.args[len(session.args)-1] != "claude" {
		t.Errorf("argv = %v", session.args)
	}
	if !strings.Contains(h.err.String(), "config-only") {
		t.Errorf("stderr: %s", h.err.String())
	}
}

func TestExecSessionCodexAPIKey(t *testing.T) {
	h, fe := injectHarness(t)
	h.mustRun(t, "exec", "codex", "podman:dev")
	// default codex profile carries auth.json with OPENAI_API_KEY (testenv)
	handoff := fe.calls[1]
	if string(handoff.stdin) != "OPENAI_API_KEY='sk-test'\n" {
		t.Errorf("handoff stdin = %q, want a sourceable KEY='value' line", handoff.stdin)
	}
	session := fe.calls[2]
	if !strings.Contains(strings.Join(session.args, " "), `set -a; . "$f"`) {
		t.Errorf("session argv = %v", session.args)
	}
	cfg := fe.calls[0]
	if _, has := cfg.files["auth.json"]; has {
		t.Error("auth.json must not land on the container fs")
	}
}

func TestExecSessionRejectsDirTarget(t *testing.T) {
	h, _ := injectHarness(t)
	if code := h.run(t, "exec", "claude", "dir:/tmp/x"); code == 0 {
		t.Fatal("dir: target should be rejected")
	}
	if !strings.Contains(h.err.String(), "running container") {
		t.Errorf("stderr: %s", h.err.String())
	}
}

func TestExecSessionExitCodePassthrough(t *testing.T) {
	h, fe := injectHarness(t)
	fe.fail = exec.Command("false").Run() // a real *exec.ExitError, code 1
	fe.failAt = 2                         // the session call (no credential → config, session)
	code := h.run(t, "exec", "claude", "pod/p")
	if code != 1 {
		t.Fatalf("exit code = %d, want the session's own status 1", code)
	}
	if strings.Contains(h.err.String(), "claudectx:") {
		t.Errorf("session exit status should pass through silently, got: %s", h.err.String())
	}
}
