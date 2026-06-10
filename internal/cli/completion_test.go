package cli_test

import (
	"strings"
	"testing"
)

// complete runs the hidden __complete plumbing and returns the candidates.
func complete(t *testing.T, h *harness, words ...string) []string {
	t.Helper()
	out := h.mustRun(t, append([]string{"__complete"}, words...)...)
	if out == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(out, "\n"), "\n")
}

func TestCompleteCommands(t *testing.T) {
	h := initialized(t)
	got := complete(t, h, "")
	for _, want := range []string{"claude", "codex", "create", "translate", "completion"} {
		if !contains(got, want) {
			t.Errorf("command completion missing %q: %v", want, got)
		}
	}
	if contains(got, "__complete") {
		t.Errorf("hidden command leaked into completion: %v", got)
	}
}

func TestCompleteProfiles(t *testing.T) {
	h := initialized(t)
	h.mustRun(t, "create", "claude", "work")

	got := complete(t, h, "claude", "")
	for _, want := range []string{"default", "work", "-"} {
		if !contains(got, want) {
			t.Errorf("claude completion missing %q: %v", want, got)
		}
	}

	// codex has no "work" profile.
	got = complete(t, h, "codex", "")
	if contains(got, "work") {
		t.Errorf("codex completion should not list claude profiles: %v", got)
	}

	// `show <tool> <name>` and flag-value completion.
	if got = complete(t, h, "show", ""); !contains(got, "claude") {
		t.Errorf("show completion should offer tools: %v", got)
	}
	if got = complete(t, h, "show", "claude", ""); !contains(got, "work") {
		t.Errorf("show claude completion should offer profiles: %v", got)
	}
	if got = complete(t, h, "create", "claude", "new", "--from", ""); !contains(got, "work") {
		t.Errorf("--from completion should offer profiles: %v", got)
	}
	if got = complete(t, h, "shell", "--claude", ""); !contains(got, "work") {
		t.Errorf("shell --claude completion should offer profiles: %v", got)
	}
}

func TestCompleteFlags(t *testing.T) {
	h := initialized(t)
	if got := complete(t, h, "claude", "--"); !contains(got, "--force") || !contains(got, "--no-keychain") {
		t.Errorf("claude flag completion: %v", got)
	}
	if got := complete(t, h, "codex", "--"); contains(got, "--no-keychain") {
		t.Errorf("--no-keychain is claude-only: %v", got)
	}
	if got := complete(t, h, "list", "--"); !contains(got, "--json") {
		t.Errorf("list flag completion: %v", got)
	}
}

func TestCompleteNeverFails(t *testing.T) {
	// Pre-init, mid-word garbage, unknown commands — all exit 0, no output
	// requirements.
	h := newHarness(t, false)
	for _, words := range [][]string{{}, {"claude", ""}, {"bogus", "x", ""}} {
		if code := h.run(t, append([]string{"__complete"}, words...)...); code != 0 {
			t.Errorf("__complete %v exited %d: %s", words, code, h.err.String())
		}
	}
}

func TestCompletionScripts(t *testing.T) {
	h := newHarness(t, false)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out := h.mustRun(t, "completion", shell)
		if !strings.Contains(out, "__complete") {
			t.Errorf("%s script should call __complete:\n%s", shell, out)
		}
	}
	if code := h.run(t, "completion"); code == 0 {
		t.Error("completion without a shell should fail")
	}
	if code := h.run(t, "completion", "powershell"); code == 0 {
		t.Error("completion for an unsupported shell should fail")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
