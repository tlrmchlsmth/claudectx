package snapshot_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tlrmchlsmth/claudectx/internal/keychain"
	"github.com/tlrmchlsmth/claudectx/internal/snapshot"
	"github.com/tlrmchlsmth/claudectx/internal/testenv"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

// buildClaudeProfile fabricates profiles/claude/<name> with config, noise,
// and a keychain stash carrying refresh tokens.
func buildClaudeProfile(e *testenv.Env, name string) {
	home := e.P.ProfileHome(tool.Claude, name)
	e.WriteFile(filepath.Join(home, "settings.json"), `{"model":"claude-opus-4-6"}`)
	e.WriteFile(filepath.Join(home, "CLAUDE.md"), "# rules\n")
	e.WriteFile(filepath.Join(home, "skills", "crashlog", "SKILL.md"), "---\nname: crashlog\n---\nbody\n")
	e.WriteFile(filepath.Join(home, ".claude.json"),
		`{"hasCompletedOnboarding":true,"projects":{"/host/path":{"history":["secret prompt"]}},"mcpServers":{"files":{"command":"npx"}}}`)
	e.WriteFile(filepath.Join(home, "projects", "p1", "transcript.jsonl"), "{}\n")
	e.WriteFile(filepath.Join(home, "todos", "t.json"), "{}\n")
	e.WriteFile(filepath.Join(home, "history.jsonl"), "{}\n")

	pw := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  "sk-ant-oat01-access",
			"refreshToken": "sk-ant-ort01-refresh",
			"expiresAt":    float64(1893456000000), // 2030-01-01
			"scopes":       []string{"user:inference"},
		},
		"mcpOAuth": map[string]any{
			"linear": map[string]any{
				"accessToken":  "mcp-access",
				"refreshToken": "mcp-refresh",
			},
		},
	}
	pwData, _ := json.Marshal(pw)
	stash, _ := json.Marshal(keychain.Credential{
		Service: keychain.Service, Account: "me@example.com", Password: string(pwData),
	})
	e.WriteFile(e.P.KeychainStash(name), string(stash))
	os.Chmod(e.P.KeychainStash(name), 0o600)
}

func entryMap(s *snapshot.Snapshot) map[string]snapshot.Entry {
	m := map[string]snapshot.Entry{}
	for _, e := range s.Entries {
		m[e.Rel] = e
	}
	return m
}

func TestBuildClaudeExcludesNoiseAndSlimsClaudeJSON(t *testing.T) {
	e := testenv.New(t)
	buildClaudeProfile(e, "work")

	s, err := snapshot.Build(e.P, keychain.Null{}, tool.Claude, "work", false, snapshot.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := entryMap(s)
	for _, want := range []string{"settings.json", "CLAUDE.md", "skills/crashlog/SKILL.md", ".claude.json"} {
		if _, ok := m[want]; !ok {
			t.Errorf("missing entry %s", want)
		}
	}
	for rel := range m {
		for _, bad := range []string{"projects/", "todos/", "history.jsonl", ".credentials.json"} {
			if rel == bad || len(rel) > len(bad) && rel[:len(bad)] == bad {
				t.Errorf("excluded path leaked into snapshot: %s", rel)
			}
		}
	}
	var cj map[string]any
	if err := json.Unmarshal(m[".claude.json"].Data, &cj); err != nil {
		t.Fatal(err)
	}
	if _, has := cj["projects"]; has {
		t.Error("projects key should be stripped from .claude.json")
	}
	if _, has := cj["mcpServers"]; !has {
		t.Error("mcpServers should survive in .claude.json")
	}
	if m[".claude.json"].Mode.Perm() != 0o600 {
		t.Errorf(".claude.json mode = %o, want 600", m[".claude.json"].Mode.Perm())
	}
	if s.Cred.Included {
		t.Error("credentials must be opt-in")
	}
	for _, sk := range s.Skipped {
		if sk == ".claude.json" || sk == ".credentials.json" {
			t.Errorf("special-cased file reported as skipped: %s", sk)
		}
	}
	if len(s.Skipped) != 3 {
		t.Errorf("Skipped = %v, want projects, todos, history.jsonl", s.Skipped)
	}
}

func TestBuildClaudeCredsStripRefreshByDefault(t *testing.T) {
	e := testenv.New(t)
	buildClaudeProfile(e, "work")

	s, err := snapshot.Build(e.P, keychain.Null{}, tool.Claude, "work", false,
		snapshot.Options{WithCreds: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Cred.Included || s.Cred.Source != "stash" {
		t.Fatalf("cred status = %+v, want included from stash", s.Cred)
	}
	if !s.Cred.RefreshStripped {
		t.Error("refresh token should be stripped by default")
	}
	if want := time.UnixMilli(1893456000000); !s.Cred.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", s.Cred.ExpiresAt, want)
	}
	cred := entryMap(s)[".credentials.json"]
	if cred.Mode.Perm() != 0o600 {
		t.Errorf("credentials mode = %o, want 600", cred.Mode.Perm())
	}
	var m map[string]any
	if err := json.Unmarshal(cred.Data, &m); err != nil {
		t.Fatal(err)
	}
	oauth := m["claudeAiOauth"].(map[string]any)
	if _, has := oauth["refreshToken"]; has {
		t.Error("claudeAiOauth.refreshToken leaked into snapshot")
	}
	if oauth["accessToken"] != "sk-ant-oat01-access" {
		t.Error("access token should survive")
	}
	linear := m["mcpOAuth"].(map[string]any)["linear"].(map[string]any)
	if _, has := linear["refreshToken"]; has {
		t.Error("mcpOAuth refreshToken leaked into snapshot")
	}
}

func TestBuildClaudeWithRefreshTokenKeepsIt(t *testing.T) {
	e := testenv.New(t)
	buildClaudeProfile(e, "work")

	s, err := snapshot.Build(e.P, keychain.Null{}, tool.Claude, "work", false,
		snapshot.Options{WithCreds: true, WithRefreshToken: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Cred.RefreshStripped {
		t.Error("RefreshStripped should be false with WithRefreshToken")
	}
	var m map[string]any
	if err := json.Unmarshal(entryMap(s)[".credentials.json"].Data, &m); err != nil {
		t.Fatal(err)
	}
	if m["claudeAiOauth"].(map[string]any)["refreshToken"] != "sk-ant-ort01-refresh" {
		t.Error("refresh token should be kept")
	}
}

func TestBuildCurrentProfilePrefersLiveKeychainAndClaudeJSON(t *testing.T) {
	e := testenv.New(t)
	buildClaudeProfile(e, "work")
	// Live ~/.claude.json is fresher than the profile copy.
	e.WriteFile(e.P.ClaudeJSON, `{"hasCompletedOnboarding":true,"fresh":true,"projects":{"/x":{}}}`)
	e.P.KeychainEnabled = true
	kc := &keychain.Fake{Cred: &keychain.Credential{
		Service: keychain.Service, Account: "me",
		Password: `{"claudeAiOauth":{"accessToken":"live-token","refreshToken":"live-refresh","expiresAt":1893456000000}}`,
	}}

	s, err := snapshot.Build(e.P, kc, tool.Claude, "work", true,
		snapshot.Options{WithCreds: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Cred.Source != "keychain" {
		t.Fatalf("cred source = %q, want keychain (live token beats the stash for the current profile)", s.Cred.Source)
	}
	m := entryMap(s)
	var cj map[string]any
	json.Unmarshal(m[".claude.json"].Data, &cj)
	if cj["fresh"] != true {
		t.Error("current profile should snapshot the live ~/.claude.json")
	}
	if _, has := cj["projects"]; has {
		t.Error("projects key should be stripped from the live claude.json too")
	}
	var cred map[string]any
	json.Unmarshal(m[".credentials.json"].Data, &cred)
	if cred["claudeAiOauth"].(map[string]any)["accessToken"] != "live-token" {
		t.Error("should carry the live keychain token")
	}
}

func TestBuildClaudeLinuxFileCredential(t *testing.T) {
	e := testenv.New(t)
	home := e.P.ProfileHome(tool.Claude, "lin")
	e.WriteFile(filepath.Join(home, "settings.json"), `{}`)
	e.WriteFile(filepath.Join(home, ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"file-token","refreshToken":"file-refresh"}}`)

	s, err := snapshot.Build(e.P, keychain.Null{}, tool.Claude, "lin", false,
		snapshot.Options{WithCreds: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Cred.Source != "file" || !s.Cred.RefreshStripped {
		t.Fatalf("cred status = %+v, want stripped file credential", s.Cred)
	}
	// Without WithCreds the in-dir credential must not ride along via the walk.
	s2, err := snapshot.Build(e.P, keychain.Null{}, tool.Claude, "lin", false, snapshot.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := entryMap(s2)[".credentials.json"]; has {
		t.Error(".credentials.json leaked into a no-creds snapshot")
	}
}

func TestBuildCodex(t *testing.T) {
	e := testenv.New(t)
	home := e.P.ProfileHome(tool.Codex, "work")
	e.WriteFile(filepath.Join(home, "config.toml"), "model = \"gpt-5.2-codex\"\n")
	e.WriteFile(filepath.Join(home, "AGENTS.md"), "# rules\n")
	e.WriteFile(filepath.Join(home, "auth.json"), `{"OPENAI_API_KEY":"sk-test"}`)
	e.WriteFile(filepath.Join(home, "sessions", "2026", "rollout.jsonl"), "{}\n")

	s, err := snapshot.Build(e.P, keychain.Null{}, tool.Codex, "work", false, snapshot.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := entryMap(s)
	if _, has := m["auth.json"]; has {
		t.Error("auth.json must be opt-in")
	}
	if _, has := m["config.toml"]; !has {
		t.Error("config.toml missing")
	}
	for rel := range m {
		if len(rel) >= 9 && rel[:9] == "sessions/" {
			t.Errorf("sessions leaked: %s", rel)
		}
	}

	s, err = snapshot.Build(e.P, keychain.Null{}, tool.Codex, "work", false,
		snapshot.Options{WithCreds: true})
	if err != nil {
		t.Fatal(err)
	}
	cred, has := entryMap(s)["auth.json"]
	if !has || cred.Mode.Perm() != 0o600 {
		t.Fatalf("auth.json should be included at 0600 with creds, got %+v", cred)
	}
	if !s.Cred.Included || s.Cred.Source != "file" {
		t.Errorf("cred status = %+v", s.Cred)
	}
}

func TestWriteTarRoundtrip(t *testing.T) {
	e := testenv.New(t)
	buildClaudeProfile(e, "work")
	home := e.P.ProfileHome(tool.Claude, "work")
	if err := os.Symlink("CLAUDE.md", filepath.Join(home, "RULES.md")); err != nil {
		t.Fatal(err)
	}

	s, err := snapshot.Build(e.P, keychain.Null{}, tool.Claude, "work", false,
		snapshot.Options{WithCreds: true})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.WriteTar(&buf); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	got := map[string]*tar.Header{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		h := *hdr
		got[hdr.Name] = &h
		io.Copy(io.Discard, tr)
	}
	if h := got[".credentials.json"]; h == nil || h.Mode&0o777 != 0o600 {
		t.Errorf(".credentials.json header = %+v, want mode 600", h)
	}
	if h := got["RULES.md"]; h == nil || h.Typeflag != tar.TypeSymlink || h.Linkname != "CLAUDE.md" {
		t.Errorf("symlink not preserved: %+v", h)
	}
	if _, has := got["settings.json"]; !has {
		t.Error("settings.json missing from tar")
	}
}

func TestWriteDir(t *testing.T) {
	e := testenv.New(t)
	buildClaudeProfile(e, "work")
	s, err := snapshot.Build(e.P, keychain.Null{}, tool.Claude, "work", false,
		snapshot.Options{WithCreds: true})
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(e.Root, "out")
	if err := s.WriteDir(dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dst, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials perm = %o, want 600", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dst, "skills", "crashlog", "SKILL.md")); err != nil {
		t.Error("nested skill file missing")
	}
}
