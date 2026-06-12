package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kubeconfigFixture points KUBECONFIG at a real temp file and returns its
// content.
func kubeconfigFixture(t *testing.T) string {
	t.Helper()
	content := "apiVersion: v1\nclusters: []\n"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	return content
}

func TestExecExtrasGhTokenInHandoff(t *testing.T) {
	h, fe := injectHarness(t)
	fe.stdout = map[string]string{"gh": "ghp_test123\n"}
	h.mustRun(t, "create", "claude", "work")
	stashCreds(h, "work")

	h.mustRun(t, "exec", "claude", "work", "podman:dev", "--extras", "gh")
	// calls: gh auth token, config sync, handoff, session
	var ghCall, handoff *execCall
	for i := range fe.calls {
		c := &fe.calls[i]
		if c.bin == "gh" {
			ghCall = c
		}
		if strings.Contains(strings.Join(c.args, " "), "umask 077") {
			handoff = c
		}
	}
	if ghCall == nil {
		t.Fatal("gh auth token was never invoked")
	}
	if got := strings.Join(ghCall.args, " "); got != "auth token" {
		t.Errorf("gh argv = %q", got)
	}
	if handoff == nil {
		t.Fatal("no handoff call")
	}
	in := string(handoff.stdin)
	for _, want := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN='sk-ant-oat01-x'\n",
		"GH_TOKEN='ghp_test123'\n",
		"GITHUB_TOKEN='ghp_test123'\n",
	} {
		if !strings.Contains(in, want) {
			t.Errorf("handoff stdin missing %q:\n%s", want, in)
		}
	}
	// The gh token must never appear on any argv.
	for _, c := range fe.calls {
		for _, arg := range c.args {
			if strings.Contains(arg, "ghp_") {
				t.Fatalf("gh token leaked into argv: %q", arg)
			}
		}
	}
	if !strings.Contains(h.err.String(), "GH_TOKEN") {
		t.Errorf("stderr should name the extra env vars: %s", h.err.String())
	}
}

func TestExecExtrasKubeFileDelivery(t *testing.T) {
	h, fe := injectHarness(t)
	content := kubeconfigFixture(t)
	h.mustRun(t, "create", "claude", "work")
	stashCreds(h, "work")

	h.mustRun(t, "exec", "claude", "work", "podman:dev", "--extras", "kube")
	var fileCall *execCall
	for i := range fe.calls {
		c := &fe.calls[i]
		if _, has := c.files[".kube/config"]; has {
			fileCall = c
		}
	}
	if fileCall == nil {
		t.Fatal("kubeconfig never delivered")
	}
	if string(fileCall.files[".kube/config"]) != content {
		t.Errorf("kubeconfig content mismatch")
	}
	script := fileCall.args[len(fileCall.args)-1]
	if !strings.Contains(script, `-C "$HOME"`) {
		t.Errorf("extras tar must extract at $HOME: %q", script)
	}
	if !strings.Contains(h.err.String(), "installed ~/.kube/config") {
		t.Errorf("stderr: %s", h.err.String())
	}
}

func TestExecExtrasFailBeforeAnyTransport(t *testing.T) {
	h, fe := injectHarness(t)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	if code := h.run(t, "exec", "claude", "podman:dev", "--extras", "kube"); code == 0 {
		t.Fatal("missing kubeconfig should fail")
	}
	if len(fe.calls) != 0 {
		t.Errorf("no transport call should happen before extras resolve, got %d", len(fe.calls))
	}
	if !strings.Contains(h.err.String(), "no kubeconfig") {
		t.Errorf("stderr: %s", h.err.String())
	}
}

func TestExecExtrasUnknownName(t *testing.T) {
	h, _ := injectHarness(t)
	if code := h.run(t, "exec", "claude", "podman:dev", "--extras", "aws"); code == 0 {
		t.Fatal("unknown extra should fail")
	}
	if !strings.Contains(h.err.String(), `unknown extra "aws"`) {
		t.Errorf("stderr: %s", h.err.String())
	}
}

func TestInjectExtrasKube(t *testing.T) {
	h, fe := injectHarness(t)
	kubeconfigFixture(t)

	h.mustRun(t, "inject", "claude", "podman:dev", "--extras", "kube")
	last := fe.calls[len(fe.calls)-1]
	if _, has := last.files[".kube/config"]; !has {
		t.Fatalf("second tar should carry .kube/config: %v", last.args)
	}
	if !strings.Contains(last.args[len(last.args)-1], `-C "$HOME"`) {
		t.Errorf("extras tar must extract at $HOME: %v", last.args)
	}
	// The profile tar must NOT contain the kubeconfig (separate roots).
	if _, has := fe.calls[0].files[".kube/config"]; has {
		t.Error("kubeconfig leaked into the config-dir tar")
	}
}

func TestInjectExtrasGhIsEnvOnlyNote(t *testing.T) {
	h, fe := injectHarness(t)
	fe.stdout = map[string]string{"gh": "ghp_x\n"}
	h.mustRun(t, "inject", "claude", "podman:dev", "--extras", "gh")
	if !strings.Contains(h.err.String(), "env-only") {
		t.Errorf("stderr should note gh is env-only: %s", h.err.String())
	}
	// gh delivers nothing on the inject path: exactly one transport call.
	if len(fe.calls) != 1 {
		t.Errorf("want 1 transport call (config only), got %d", len(fe.calls))
	}
}

func TestInjectExtrasRejectsDirTarget(t *testing.T) {
	h, _ := injectHarness(t)
	dst := filepath.Join(h.e.Root, "out")
	if code := h.run(t, "inject", "claude", "dir:"+dst, "--extras", "kube"); code == 0 {
		t.Fatal("dir target with extras should fail")
	}
	if !strings.Contains(h.err.String(), "doesn't apply to dir:") {
		t.Errorf("stderr: %s", h.err.String())
	}
}

func TestInjectExtrasDryRunListsFiles(t *testing.T) {
	h, fe := injectHarness(t)
	kubeconfigFixture(t)
	out := h.mustRun(t, "inject", "claude", "podman:dev", "--extras", "kube", "--dry-run")
	if !strings.Contains(out, "~/.kube/config (extra)") {
		t.Errorf("dry run should list the extra: %s", out)
	}
	if len(fe.calls) != 0 {
		t.Error("dry run must not execute the transport")
	}
}
