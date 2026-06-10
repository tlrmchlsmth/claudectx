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
	"github.com/tlrmchlsmth/claudectx/internal/tool"
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
		ProcScan: func(tool.Tool) string { return "" },
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

func TestV1StateGuidance(t *testing.T) {
	h := newHarness(t, false)
	h.e.BuildV1ContextsTree()
	for _, cmd := range [][]string{{"list"}, {"claude", "vertex"}, {"current"}} {
		if code := h.run(t, cmd...); code == 0 {
			t.Fatalf("%v on v1 state should fail", cmd)
		}
		if !strings.Contains(h.err.String(), "claudectx migrate") {
			t.Fatalf("%v should point at migrate: %s", cmd, h.err.String())
		}
	}
}

func TestStatusAndCurrent(t *testing.T) {
	h := initialized(t)

	out := h.mustRun(t)
	if !strings.Contains(out, "claude: default") || !strings.Contains(out, "codex:  default") {
		t.Fatalf("status = %q", out)
	}
	out = h.mustRun(t, "current")
	if !strings.Contains(out, "claude: default") {
		t.Fatalf("current = %q", out)
	}
	if got := strings.TrimSpace(h.mustRun(t, "current", "claude")); got != "default" {
		t.Fatalf("current claude = %q", got)
	}
}

func TestPerToolSwitchAndDash(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "claude", "work")
	h.mustRun(t, "create", "codex", "personal")

	out := h.mustRun(t, "claude", "work")
	if !strings.Contains(out, `Switched claude to "work"`) {
		t.Fatalf("switch output: %q", out)
	}
	// Codex axis untouched.
	if got := strings.TrimSpace(h.mustRun(t, "current", "codex")); got != "default" {
		t.Fatalf("codex current after claude switch = %q", got)
	}

	h.mustRun(t, "codex", "personal")
	if got := strings.TrimSpace(h.mustRun(t, "current", "codex")); got != "personal" {
		t.Fatalf("codex current = %q", got)
	}
	// Claude axis untouched by the codex switch.
	if got := strings.TrimSpace(h.mustRun(t, "current", "claude")); got != "work" {
		t.Fatalf("claude current = %q", got)
	}

	// Per-axis dash.
	out = h.mustRun(t, "claude", "-")
	if !strings.Contains(out, `Switched claude to "default"`) {
		t.Fatalf("claude dash output: %q", out)
	}
	if got := strings.TrimSpace(h.mustRun(t, "current", "codex")); got != "personal" {
		t.Fatalf("claude dash moved codex: %q", got)
	}
	out = h.mustRun(t, "codex", "-")
	if !strings.Contains(out, `Switched codex to "default"`) {
		t.Fatalf("codex dash output: %q", out)
	}
}

func TestBareToolListsProfiles(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "claude", "work")
	out := h.mustRun(t, "claude")
	if !strings.Contains(out, "* default") || !strings.Contains(out, "  work") {
		t.Fatalf("claude list = %q", out)
	}
}

func TestNoBareNameSwitch(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "claude", "work")
	// v1 muscle memory: bare profile name must NOT switch, but should hint.
	if code := h.run(t, "work"); code != 2 {
		t.Fatalf("bare-name switch exit = %d, want 2", code)
	}
	if !strings.Contains(h.err.String(), "claudectx claude work") {
		t.Fatalf("should suggest the per-tool command: %s", h.err.String())
	}
	if got := strings.TrimSpace(h.mustRun(t, "current", "claude")); got != "default" {
		t.Fatal("bare name switched anyway")
	}
	// Bare dash likewise.
	if code := h.run(t, "-"); code != 2 {
		t.Fatalf("bare dash exit = %d", code)
	}
	if !strings.Contains(h.err.String(), "claudectx claude -") {
		t.Fatalf("dash guidance: %s", h.err.String())
	}
}

func TestSwitchToUnknownProfileFails(t *testing.T) {
	h := initialized(t)
	if code := h.run(t, "claude", "nope"); code == 0 {
		t.Fatal("switching to unknown profile should fail")
	}
	if !strings.Contains(h.err.String(), "no such claude profile") {
		t.Fatalf("err = %q", h.err.String())
	}
}

func TestListJSON(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "codex", "personal")
	var parsed map[string]struct {
		Current  string   `json:"current"`
		Profiles []string `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(h.mustRun(t, "list", "--json")), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["claude"].Current != "default" || len(parsed["codex"].Profiles) != 2 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestCreateDefaultsToEmpty(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "claude", "fresh")
	if _, err := os.Stat(filepath.Join(h.e.P.ProfileHome(tool.Claude, "fresh"), "settings.json")); err == nil {
		t.Fatal("bare `create` cloned settings.json; default should be empty")
	}
	// But onboarding flags are still seeded so Claude skips first-run setup.
	data, err := os.ReadFile(h.e.P.ProfileClaudeJSON("fresh"))
	if err != nil || !strings.Contains(string(data), "hasCompletedOnboarding") {
		t.Fatalf("empty profile not seeded: %s, %v", data, err)
	}
	// Codex empty profile: no seeding, just the home dir.
	h.mustRun(t, "create", "codex", "fresh")
	entries, _ := os.ReadDir(h.e.P.ProfileHome(tool.Codex, "fresh"))
	if len(entries) != 0 {
		t.Fatalf("codex empty profile has content: %v", entries)
	}
}

func TestCreateFromCopiesWithoutCredentials(t *testing.T) {
	h := initialized(t)
	// Plant all three credential stores in the sources; none may be cloned.
	h.e.WriteFile(h.e.P.KeychainStash("default"), `{"password":"tok"}`)
	h.e.WriteFile(filepath.Join(h.e.P.ProfileHome(tool.Claude, "default"), ".credentials.json"), `{"token":"oauth"}`)
	// codex default already has auth.json from BuildModernCodexTree.

	h.mustRun(t, "create", "claude", "clone", "--from", "default")
	if _, err := os.Stat(filepath.Join(h.e.P.ProfileHome(tool.Claude, "clone"), "settings.json")); err != nil {
		t.Fatal("clone missing copied settings.json")
	}
	if _, err := os.Stat(h.e.P.KeychainStash("clone")); err == nil {
		t.Fatal("keychain stash was cloned")
	}
	if _, err := os.Stat(filepath.Join(h.e.P.ProfileHome(tool.Claude, "clone"), ".credentials.json")); err == nil {
		t.Fatal("claude .credentials.json was cloned")
	}

	h.mustRun(t, "create", "codex", "clone", "--from", "default")
	if _, err := os.Stat(filepath.Join(h.e.P.ProfileHome(tool.Codex, "clone"), "config.toml")); err != nil {
		t.Fatal("codex clone missing config.toml")
	}
	if _, err := os.Stat(filepath.Join(h.e.P.ProfileHome(tool.Codex, "clone"), "auth.json")); err == nil {
		t.Fatal("codex auth.json (API key) was cloned")
	}

	// `--from` with no value clones the current profile.
	h.mustRun(t, "create", "claude", "clone2", "--from")
	if _, err := os.Stat(filepath.Join(h.e.P.ProfileHome(tool.Claude, "clone2"), "settings.json")); err != nil {
		t.Fatal("`--from` with no value should clone the current profile")
	}
}

func TestCreateRejectsReservedAndDuplicate(t *testing.T) {
	h := initialized(t)
	for _, name := range []string{"delete", "claude", "codex", "migrate"} {
		if code := h.run(t, "create", "claude", name); code == 0 {
			t.Fatalf("reserved name %q accepted", name)
		}
	}
	h.mustRun(t, "create", "claude", "work")
	if code := h.run(t, "create", "claude", "work"); code == 0 {
		t.Fatal("duplicate name accepted")
	}
	// Same name on the OTHER axis is fine — separate namespaces.
	h.mustRun(t, "create", "codex", "work")
}

func TestDeleteGuards(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "claude", "work")

	// Refuses the active profile on that axis.
	if code := h.run(t, "delete", "claude", "default", "--yes"); code == 0 {
		t.Fatal("deleted the active profile")
	}
	// Without --yes and a non-confirming stdin: aborted.
	if code := h.run(t, "delete", "claude", "work"); code == 0 {
		t.Fatal("delete proceeded without confirmation")
	}
	// With --yes: trashed, not erased.
	out := h.mustRun(t, "delete", "claude", "work", "--yes")
	if !strings.Contains(out, "recoverable at") {
		t.Fatalf("delete output: %q", out)
	}
	entries, _ := os.ReadDir(h.e.P.BackupsDir())
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "claude.work.deleted.") {
			found = true
		}
	}
	if !found {
		t.Fatal("trashed profile not in backups/")
	}
}

func TestRenameActiveProfileRelinks(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "rename", "claude", "default", "main")
	if got := strings.TrimSpace(h.mustRun(t, "current", "claude")); got != "main" {
		t.Fatalf("current = %q", got)
	}
	// Link repointed; codex axis untouched.
	target, err := os.Readlink(h.e.P.ClaudeDir)
	if err != nil || !strings.Contains(target, "/claude/main/") {
		t.Fatalf("claude link = %q, %v", target, err)
	}
	if got := strings.TrimSpace(h.mustRun(t, "current", "codex")); got != "default" {
		t.Fatalf("codex current = %q", got)
	}
	// Subsequent switching works.
	h.mustRun(t, "create", "claude", "tmp")
	h.mustRun(t, "claude", "tmp")
	h.mustRun(t, "claude", "main")
}

func TestRenameActiveWithRunningAgents(t *testing.T) {
	h := initialized(t)
	h.app.ProcScan = func(tool.Tool) string { return "2 claude (pids 1, 2)" }
	if code := h.run(t, "rename", "claude", "default", "main"); code == 0 {
		t.Fatal("rename proceeded without confirmation")
	}
	h.mustRun(t, "rename", "claude", "default", "main", "--force")
	if got := strings.TrimSpace(h.mustRun(t, "current", "claude")); got != "main" {
		t.Fatalf("current = %q", got)
	}
}

func TestShowPerTool(t *testing.T) {
	h := initialized(t)
	out := h.mustRun(t, "show", "claude")
	for _, want := range []string{"claude profile: default (current)", "crashlog", "files"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show claude missing %q:\n%s", want, out)
		}
	}
	out = h.mustRun(t, "show", "codex")
	for _, want := range []string{"codex profile: default (current)", "auth.json:   present", "deploy", "search"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show codex missing %q:\n%s", want, out)
		}
	}
	var parsed struct {
		Skills []string `json:"skills"`
	}
	if err := json.Unmarshal([]byte(h.mustRun(t, "show", "claude", "--json")), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Skills) != 2 {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestRunningProcessWarningBlocksWithoutConfirm(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "claude", "work")
	h.app.ProcScan = func(t tool.Tool) string {
		if t == tool.Claude || t == "" {
			return "1 claude (pid 123)"
		}
		return ""
	}
	if code := h.run(t, "claude", "work"); code == 0 {
		t.Fatal("switch proceeded despite running agent and no confirmation")
	}
	// The codex axis doesn't warn for claude processes.
	h.mustRun(t, "create", "codex", "work")
	if code := h.run(t, "codex", "work"); code != 0 {
		t.Fatalf("codex switch blocked by claude process: %s", h.err.String())
	}
	// Confirming proceeds; --force skips entirely.
	h.app.Stdin = strings.NewReader("y\n")
	if code := h.run(t, "claude", "work"); code != 0 {
		t.Fatalf("confirmed switch failed: %s", h.err.String())
	}
	if code := h.run(t, "claude", "default", "--force"); code != 0 {
		t.Fatalf("--force switch failed: %s", h.err.String())
	}
	if strings.Contains(h.err.String(), "switch anyway?") {
		t.Fatal("--force still prompted")
	}
}

func TestInterruptedSwitchRecoversOnNextCommand(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "claude", "work")

	st, err := h.app.S.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.S.SetJournal(st, &store.Journal{Op: "switch", Tool: "claude", From: "default", To: "work", Step: "links"}); err != nil {
		t.Fatal(err)
	}

	// Any command triggers recovery first.
	out := h.mustRun(t, "current", "claude")
	if strings.TrimSpace(out) != "work" {
		t.Fatalf("current after recovery = %q (stderr: %s)", out, h.err.String())
	}
}

func TestEnvCommandPerTool(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "codex", "work")

	out := h.mustRun(t, "env", "codex", "work")
	if !strings.Contains(out, "export CODEX_HOME=") ||
		!strings.Contains(out, filepath.Join("profiles", "codex", "work", "home")) {
		t.Fatalf("env output: %q", out)
	}
	if strings.Contains(out, "CLAUDE_CONFIG_DIR") {
		t.Fatal("codex pin must not touch the claude var")
	}
	out = h.mustRun(t, "env", "claude", "default")
	if !strings.Contains(out, "export CLAUDE_CONFIG_DIR=") {
		t.Fatalf("env claude output: %q", out)
	}
	if code := h.run(t, "env", "codex", "nope"); code == 0 {
		t.Fatal("env for unknown profile succeeded")
	}
	if out := h.mustRun(t, "env", "--unset"); !strings.Contains(out, "unset CLAUDE_CONFIG_DIR CODEX_HOME") {
		t.Fatalf("unset output: %q", out)
	}
	if out := h.mustRun(t, "env", "--unset", "codex"); strings.TrimSpace(out) != "unset CODEX_HOME" {
		t.Fatalf("per-tool unset output: %q", out)
	}
}

func TestTerminalPinnedCurrentAndStatus(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "codex", "work")
	h.app.P.TerminalCodex = "work" // as paths.FromEnv would set in a pinned terminal

	if got := strings.TrimSpace(h.mustRun(t, "current", "codex")); got != "work" {
		t.Fatalf("pinned current = %q, want work", got)
	}
	// The other axis is unaffected.
	if got := strings.TrimSpace(h.mustRun(t, "current", "claude")); got != "default" {
		t.Fatalf("claude current = %q", got)
	}
	out := h.mustRun(t)
	if !strings.Contains(out, "[this terminal: work]") {
		t.Fatalf("status missing pin note:\n%s", out)
	}
}

func TestShellInit(t *testing.T) {
	h := newHarness(t, false)
	out := h.mustRun(t, "shell-init")
	if !strings.Contains(out, "cx()") || !strings.Contains(out, "claude|codex") {
		t.Fatalf("shell-init output: %q", out)
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
}
