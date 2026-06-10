package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tlrmchlsmth/claudectx/internal/cli"
	"github.com/tlrmchlsmth/claudectx/internal/keychain"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
)

type harness struct {
	e   *testenv.Env
	app *cli.App
	out *bytes.Buffer
	err *bytes.Buffer
}

func newHarness(t *testing.T, build bool) *harness {
	t.Helper()
	e := testenv.New(t)
	if build {
		e.BuildClaudeTree()
		e.BuildModernCodexTree()
		e.BuildClaudeJSON()
	}
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	app := &cli.App{
		P: e.P, S: store.New(e.P),
		KC:     &keychain.Fake{FailOn: map[string]error{}},
		Stdout: out, Stderr: errb, Stdin: strings.NewReader(""),
		Version:  "test",
		ProcScan: func() string { return "" },
	}
	return &harness{e: e, app: app, out: out, err: errb}
}

func (h *harness) run(t *testing.T, args ...string) int {
	t.Helper()
	h.out.Reset()
	h.err.Reset()
	return h.app.Run(args)
}

func (h *harness) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	if code := h.run(t, args...); code != 0 {
		t.Fatalf("claudectx %v exited %d: %s", args, code, h.err.String())
	}
	return h.out.String()
}

func initialized(t *testing.T) *harness {
	h := newHarness(t, true)
	h.mustRun(t, "init", "--yes")
	return h
}

func TestUninitializedGuidance(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run(t, "list"); code == 0 {
		t.Fatal("list before init should fail")
	}
	if !strings.Contains(h.err.String(), "claudectx init") {
		t.Fatalf("error should point at init: %s", h.err.String())
	}
}

func TestInitListCurrentFlow(t *testing.T) {
	h := initialized(t)

	out := h.mustRun(t, "list")
	if !strings.Contains(out, "* default") {
		t.Fatalf("list = %q", out)
	}
	if got := strings.TrimSpace(h.mustRun(t, "current")); got != "default" {
		t.Fatalf("current = %q", got)
	}
}

func TestCreateSwitchBareNameAndDash(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "work", "--empty")

	// Bare-name dispatch.
	out := h.mustRun(t, "work")
	if !strings.Contains(out, `Switched to "work"`) {
		t.Fatalf("switch output: %q", out)
	}
	if got := strings.TrimSpace(h.mustRun(t, "current")); got != "work" {
		t.Fatalf("current = %q", got)
	}
	// Empty context seeded onboarding flags from the old live claude.json.
	live, _ := os.ReadFile(h.e.P.ClaudeJSON)
	var lj map[string]any
	json.Unmarshal(live, &lj)
	if lj["hasCompletedOnboarding"] != true {
		t.Fatalf("onboarding seed missing: %s", live)
	}

	// Dash returns to previous.
	out = h.mustRun(t, "-")
	if !strings.Contains(out, `Switched to "default"`) {
		t.Fatalf("dash output: %q", out)
	}
	// Dash again bounces back.
	h.mustRun(t, "-")
	if got := strings.TrimSpace(h.mustRun(t, "current")); got != "work" {
		t.Fatalf("after double dash current = %q", got)
	}
}

func TestSwitchToUnknownNameFails(t *testing.T) {
	h := initialized(t)
	if code := h.run(t, "nope"); code == 0 {
		t.Fatal("switching to unknown context should fail")
	}
	if !strings.Contains(h.err.String(), "no such context") {
		t.Fatalf("err = %q", h.err.String())
	}
}

func TestCreateFromCopiesWithoutSecrets(t *testing.T) {
	h := initialized(t)
	// Plant all three credential stores in the source; none may be cloned.
	h.e.WriteFile(h.e.P.CtxKeychainStash("default"), `{"password":"tok"}`)
	codexAuth := filepath.Join(h.e.P.CtxCodexDir("default"), "auth.json")
	h.e.WriteFile(codexAuth, `{"OPENAI_API_KEY":"sk-secret"}`)
	claudeCreds := filepath.Join(h.e.P.CtxClaudeDir("default"), ".credentials.json")
	h.e.WriteFile(claudeCreds, `{"token":"oauth"}`)

	h.mustRun(t, "create", "clone", "--from", "default")
	if _, err := os.Stat(filepath.Join(h.e.P.CtxClaudeDir("clone"), "settings.json")); err != nil {
		t.Fatal("clone missing copied settings.json")
	}
	if _, err := os.Stat(h.e.P.CtxKeychainStash("clone")); err == nil {
		t.Fatal("secrets were copied into clone")
	}
	if _, err := os.Stat(filepath.Join(h.e.P.CtxCodexDir("clone"), "auth.json")); err == nil {
		t.Fatal("codex auth.json (API key) was copied into clone")
	}
	if _, err := os.Stat(filepath.Join(h.e.P.CtxClaudeDir("clone"), ".credentials.json")); err == nil {
		t.Fatal("claude .credentials.json was copied into clone")
	}
	// Internal symlinks preserved: make one and re-copy.
	os.Symlink("/tmp", filepath.Join(h.e.P.CtxClaudeDir("default"), "linky"))
	h.mustRun(t, "create", "clone2")
	fi, err := os.Lstat(filepath.Join(h.e.P.CtxClaudeDir("clone2"), "linky"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("internal symlink not preserved as symlink")
	}
}

func TestCreateRejectsReservedAndDuplicate(t *testing.T) {
	h := initialized(t)
	if code := h.run(t, "create", "delete"); code == 0 {
		t.Fatal("reserved name accepted")
	}
	h.mustRun(t, "create", "work", "--empty")
	if code := h.run(t, "create", "work", "--empty"); code == 0 {
		t.Fatal("duplicate name accepted")
	}
}

func TestDeleteGuards(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "work", "--empty")

	// Refuses the active context.
	if code := h.run(t, "delete", "default", "--yes"); code == 0 {
		t.Fatal("deleted the active context")
	}
	// Without --yes and a non-confirming stdin: aborted.
	if code := h.run(t, "delete", "work"); code == 0 {
		t.Fatal("delete proceeded without confirmation")
	}
	// With --yes: trashed, not erased.
	out := h.mustRun(t, "delete", "work", "--yes")
	if !strings.Contains(out, "recoverable at") {
		t.Fatalf("delete output: %q", out)
	}
	entries, _ := os.ReadDir(h.e.P.BackupsDir())
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "work.deleted.") {
			found = true
		}
	}
	if !found {
		t.Fatal("trashed context not in backups/")
	}
}

func TestRenameActiveContextRelinks(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "rename", "default", "main")
	if got := strings.TrimSpace(h.mustRun(t, "current")); got != "main" {
		t.Fatalf("current = %q", got)
	}
	// Links must already point at the renamed dir (switch would catch drift).
	target, err := os.Readlink(h.e.P.ClaudeDir)
	if err != nil || !strings.Contains(target, "/main/") {
		t.Fatalf("claude link = %q, %v", target, err)
	}
	// And a subsequent switch works.
	h.mustRun(t, "create", "tmp", "--empty")
	h.mustRun(t, "tmp")
	h.mustRun(t, "main")
}

func TestShowSummaries(t *testing.T) {
	h := initialized(t)
	out := h.mustRun(t, "show")
	for _, want := range []string{"context: default (current)", "crashlog", "atlassian", "auth.json:   present"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}
	// JSON mode parses and carries skills.
	var parsed struct {
		ClaudeSkills []string `json:"claude_skills"`
		MCPServers   []string `json:"mcp_servers"`
	}
	if err := json.Unmarshal([]byte(h.mustRun(t, "show", "--json")), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.ClaudeSkills) != 2 || len(parsed.MCPServers) != 2 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestRunningProcessWarningBlocksWithoutConfirm(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "work", "--empty")
	h.app.ProcScan = func() string { return "claude(pid 123)" }
	if code := h.run(t, "work"); code == 0 {
		t.Fatal("switch proceeded despite running agent and no confirmation")
	}
	if !strings.Contains(h.err.String(), "claude(pid 123)") {
		t.Fatalf("warning missing: %s", h.err.String())
	}
	// Confirming proceeds.
	h.app.Stdin = strings.NewReader("y\n")
	if code := h.run(t, "work"); code != 0 {
		t.Fatalf("confirmed switch failed: %s", h.err.String())
	}
	// --force after a bare name skips the prompt entirely.
	if code := h.run(t, "default", "--force"); code != 0 {
		t.Fatalf("--force switch failed: %s", h.err.String())
	}
	if strings.Contains(h.err.String(), "switch anyway?") {
		t.Fatal("--force still prompted")
	}
}

func TestInterruptedSwitchRecoversOnNextCommand(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "work", "--empty")

	// Plant a mid-switch journal as if we crashed at the links step.
	st, err := h.app.S.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.S.SetJournal(st, &store.Journal{Op: "switch", From: "default", To: "work", Step: "links"}); err != nil {
		t.Fatal(err)
	}

	// Any command triggers recovery first.
	out := h.mustRun(t, "current")
	if strings.TrimSpace(out) != "work" {
		t.Fatalf("current after recovery = %q (stderr: %s)", out, h.err.String())
	}
}

func TestEnvCommand(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "work", "--empty")

	out := h.mustRun(t, "env", "work")
	if !strings.Contains(out, "export CLAUDE_CONFIG_DIR=") ||
		!strings.Contains(out, filepath.Join("contexts", "work", "claude")) {
		t.Fatalf("env output: %q", out)
	}
	if !strings.Contains(out, "export CODEX_HOME=") {
		t.Fatalf("env output missing CODEX_HOME: %q", out)
	}
	// Unknown context fails.
	if code := h.run(t, "env", "nope"); code == 0 {
		t.Fatal("env for unknown context succeeded")
	}
	// Unset prints an unset line.
	if out := h.mustRun(t, "env", "--unset"); !strings.Contains(out, "unset CLAUDE_CONFIG_DIR CODEX_HOME") {
		t.Fatalf("unset output: %q", out)
	}
}

func TestTerminalPinnedCurrentAndList(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "work", "--empty")
	h.app.P.TerminalContext = "work" // as paths.FromEnv would set in a pinned terminal

	if got := strings.TrimSpace(h.mustRun(t, "current")); got != "work" {
		t.Fatalf("pinned current = %q, want work", got)
	}
	out := h.mustRun(t, "list")
	if !strings.Contains(out, "work  (this terminal)") {
		t.Fatalf("list missing terminal marker:\n%s", out)
	}
	if !strings.Contains(out, `pinned to "work"`) {
		t.Fatalf("list missing pin explanation:\n%s", out)
	}
}

func TestEnvIsReservedName(t *testing.T) {
	h := initialized(t)
	for _, name := range []string{"env", "shell", "shell-init"} {
		if code := h.run(t, "create", name, "--empty"); code == 0 {
			t.Fatalf("reserved name %q accepted as context", name)
		}
	}
}

func TestShellInit(t *testing.T) {
	h := newHarness(t, false)
	out := h.mustRun(t, "shell-init")
	if !strings.Contains(out, "cx()") || !strings.Contains(out, "claudectx env") {
		t.Fatalf("shell-init output: %q", out)
	}
}

func TestRenameActiveWithRunningAgents(t *testing.T) {
	h := initialized(t)
	h.app.ProcScan = func() string { return "2 claude (pids 1, 2)" }
	// Non-confirming stdin: aborted.
	if code := h.run(t, "rename", "default", "main"); code == 0 {
		t.Fatal("rename proceeded without confirmation")
	}
	// --force skips the check.
	h.mustRun(t, "rename", "default", "main", "--force")
	if got := strings.TrimSpace(h.mustRun(t, "current")); got != "main" {
		t.Fatalf("current = %q", got)
	}
}

func TestVersionAndHelp(t *testing.T) {
	h := newHarness(t, false)
	if out := h.mustRun(t, "version"); !strings.Contains(out, "claudectx test") {
		t.Fatalf("version = %q", out)
	}
	if out := h.mustRun(t, "--help"); !strings.Contains(out, "Usage:") {
		t.Fatalf("help = %q", out)
	}
	if code := h.run(t); code != 0 {
		// no args pre-init: list fails with guidance, which is acceptable
		if !strings.Contains(h.err.String(), "init") {
			t.Fatalf("bare run pre-init: %s", h.err.String())
		}
	}
}
