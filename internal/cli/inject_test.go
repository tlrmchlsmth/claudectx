package cli_test

import (
	"archive/tar"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/cli"
	"github.com/tlrmchlsmth/claudectx/internal/keychain"
)

// fakeExec captures every transport command: argv, raw stdin, and (when
// stdin is a tar) the unpacked payload. bin/args/files mirror the last
// call for the single-call inject tests.
type execCall struct {
	bin   string
	args  []string
	stdin []byte
	files map[string][]byte
}

type fakeExec struct {
	calls []execCall
	bin   string
	args  []string
	files map[string][]byte
	fail  error
	// failAt makes fail apply only to the Nth call (1-based); 0 = every call.
	failAt int
	// stdout is written to the call's stdout, keyed by bin (`gh auth token`).
	stdout map[string]string
}

func (f *fakeExec) run(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	if out, ok := f.stdout[name]; ok {
		io.WriteString(stdout, out)
	}
	raw, _ := io.ReadAll(stdin)
	call := execCall{bin: name, args: args, stdin: raw, files: map[string][]byte{}}
	tr := tar.NewReader(strings.NewReader(string(raw)))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(tr)
		call.files[hdr.Name] = data
	}
	f.calls = append(f.calls, call)
	f.bin, f.args, f.files = call.bin, call.args, call.files
	if f.failAt == 0 || len(f.calls) == f.failAt {
		return f.fail
	}
	return nil
}

func injectHarness(t *testing.T) (*harness, *fakeExec) {
	h := initialized(t)
	fe := &fakeExec{}
	h.app.Exec = fe.run
	return h, fe
}

// stashCreds writes a keychain stash with access + refresh tokens for a
// claude profile.
func stashCreds(h *harness, profile string) {
	pw := `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-x","refreshToken":"sk-ant-ort01-x","expiresAt":1893456000000}}`
	stash, _ := json.Marshal(keychain.Credential{
		Service: keychain.Service, Account: "me", Password: pw,
	})
	h.e.WriteFile(h.app.P.KeychainStash(profile), string(stash))
	os.Chmod(h.app.P.KeychainStash(profile), 0o600)
}

func TestInjectPodArgvAndPayload(t *testing.T) {
	h, fe := injectHarness(t)
	out := h.mustRun(t, "inject", "claude", "pod/vllm-0", "-n", "dev", "-c", "main")

	if fe.bin != "kubectl" {
		t.Fatalf("bin = %q, want kubectl", fe.bin)
	}
	want := []string{"exec", "-i", "-n", "dev", "-c", "main", "vllm-0", "--", "sh", "-c"}
	if got := strings.Join(fe.args[:len(want)], " "); got != strings.Join(want, " ") {
		t.Fatalf("argv = %v", fe.args)
	}
	script := fe.args[len(fe.args)-1]
	if !strings.Contains(script, `"$HOME"/.claude`) || !strings.Contains(script, "tar -xf -") {
		t.Errorf("script = %q", script)
	}
	if _, has := fe.files["settings.json"]; !has {
		t.Error("settings.json missing from payload")
	}
	if _, has := fe.files[".credentials.json"]; has {
		t.Error("credentials sent without --with-creds")
	}
	for name := range fe.files {
		if strings.HasPrefix(name, "projects/") || name == "history.jsonl" {
			t.Errorf("host-private file sent: %s", name)
		}
	}
	if !strings.Contains(out, `injected claude profile "default" into pod/vllm-0`) {
		t.Errorf("output: %s", out)
	}
	if !strings.Contains(out, "--with-creds to include") {
		t.Errorf("should mention creds were not included: %s", out)
	}
}

func TestInjectWithCredsStripsRefreshAndWarns(t *testing.T) {
	h, fe := injectHarness(t)
	h.mustRun(t, "create", "claude", "work")
	stashCreds(h, "work")

	out := h.mustRun(t, "inject", "claude", "work", "pod/p", "--with-creds")
	var m map[string]any
	if err := json.Unmarshal(fe.files[".credentials.json"], &m); err != nil {
		t.Fatalf("credentials payload: %v", err)
	}
	oauth := m["claudeAiOauth"].(map[string]any)
	if _, has := oauth["refreshToken"]; has {
		t.Error("refresh token crossed the wire")
	}
	if !strings.Contains(out, "access token only") {
		t.Errorf("output should explain the strip: %s", out)
	}
	if !strings.Contains(h.err.String(), "can read the injected credentials") {
		t.Errorf("missing exec-readers warning: %s", h.err.String())
	}

	h.mustRun(t, "inject", "claude", "work", "pod/p", "--with-refresh-token")
	var m2 map[string]any
	json.Unmarshal(fe.files[".credentials.json"], &m2)
	if _, has := m2["claudeAiOauth"].(map[string]any)["refreshToken"]; !has {
		t.Error("--with-refresh-token should keep the refresh token (and imply --with-creds)")
	}
}

func TestInjectDockerAndCodex(t *testing.T) {
	h, fe := injectHarness(t)
	h.mustRun(t, "inject", "codex", "docker:abc", "--with-creds")
	if fe.bin != "docker" {
		t.Fatalf("bin = %q", fe.bin)
	}
	if got := strings.Join(fe.args[:4], " "); got != "exec -i abc sh" {
		t.Fatalf("argv = %v", fe.args)
	}
	if !strings.Contains(fe.args[len(fe.args)-1], `"$HOME"/.codex`) {
		t.Errorf("script = %q", fe.args[len(fe.args)-1])
	}
	if _, has := fe.files["auth.json"]; !has {
		t.Error("codex auth.json missing with --with-creds")
	}
	for name := range fe.files {
		if strings.HasPrefix(name, "sessions/") {
			t.Errorf("codex session state sent: %s", name)
		}
	}
}

func TestInjectDirTarget(t *testing.T) {
	h, _ := injectHarness(t)
	dst := filepath.Join(h.e.Root, "out")
	h.mustRun(t, "inject", "claude", "dir:"+dst)
	if _, err := os.Stat(filepath.Join(dst, "settings.json")); err != nil {
		t.Fatal("settings.json not materialized")
	}
	if _, err := os.Stat(filepath.Join(dst, "projects")); !os.IsNotExist(err) {
		t.Error("projects/ should not be materialized")
	}
}

func TestInjectCustomDestQuoting(t *testing.T) {
	h, fe := injectHarness(t)
	out := h.mustRun(t, "inject", "claude", "pod/p", "--dest", "/work/o'brien cfg")
	script := fe.args[len(fe.args)-1]
	if !strings.Contains(script, `'/work/o'\''brien cfg'`) {
		t.Errorf("dest not safely quoted: %q", script)
	}
	if !strings.Contains(out, "CLAUDE_CONFIG_DIR=/work/o'brien cfg") {
		t.Errorf("missing env hint: %s", out)
	}
}

func TestInjectArgErrors(t *testing.T) {
	h, _ := injectHarness(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"inject", "claude"}, "usage:"},
		{[]string{"inject", "claude", "somepod"}, "unrecognized target"},
		{[]string{"inject", "claude", "pod/p", "work"}, "the target comes last"},
		{[]string{"inject", "claude", "nope", "pod/p"}, `no such claude profile "nope"`},
		{[]string{"inject", "claude", "docker:x", "-n", "ns"}, "only apply to pod/ targets"},
	}
	for _, c := range cases {
		if code := h.run(t, c.args...); code == 0 {
			t.Errorf("%v should fail", c.args)
		}
		if !strings.Contains(h.err.String(), c.want) {
			t.Errorf("%v: stderr %q should contain %q", c.args, h.err.String(), c.want)
		}
	}
}

func TestInjectNoCredsFoundWarns(t *testing.T) {
	h, fe := injectHarness(t)
	h.mustRun(t, "create", "claude", "empty")
	h.mustRun(t, "inject", "claude", "empty", "pod/p", "--with-creds")
	if !strings.Contains(h.err.String(), "no credentials found") {
		t.Errorf("stderr: %s", h.err.String())
	}
	if _, has := fe.files[".credentials.json"]; has {
		t.Error("no credentials should exist for the empty profile")
	}
}

func TestInjectDryRunRunsNothing(t *testing.T) {
	h, fe := injectHarness(t)
	out := h.mustRun(t, "inject", "claude", "pod/p", "--dry-run")
	if fe.bin != "" {
		t.Error("dry run must not execute the transport")
	}
	if !strings.Contains(out, "would inject") || !strings.Contains(out, "settings.json") {
		t.Errorf("output: %s", out)
	}
}

func TestInjectTransportFailureSurfaces(t *testing.T) {
	h, fe := injectHarness(t)
	fe.fail = io.ErrUnexpectedEOF
	if code := h.run(t, "inject", "claude", "pod/p"); code == 0 {
		t.Fatal("transport failure should fail the command")
	}
	if !strings.Contains(h.err.String(), "needs `sh` and `tar`") {
		t.Errorf("stderr: %s", h.err.String())
	}
}

func TestInjectReservedProfileName(t *testing.T) {
	h := initialized(t)
	if code := h.run(t, "create", "claude", "inject"); code == 0 {
		t.Error("'inject' must be a reserved name")
	}
}

// Sanity: tar payload bytes never appear in argv (secrets stay off the
// command line).
func TestInjectSecretsNeverOnArgv(t *testing.T) {
	h, fe := injectHarness(t)
	h.mustRun(t, "create", "claude", "work")
	stashCreds(h, "work")
	h.mustRun(t, "inject", "claude", "work", "pod/p", "--with-refresh-token")
	for _, arg := range append([]string{fe.bin}, fe.args...) {
		if strings.Contains(arg, "sk-ant-") {
			t.Fatalf("token leaked into argv: %q", arg)
		}
	}
}

var _ cli.CmdRunner = (&fakeExec{}).run
