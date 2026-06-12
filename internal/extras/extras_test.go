package extras

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func deps(t *testing.T) Deps {
	return Deps{
		Output: func(name string, args ...string) (string, error) {
			t.Fatalf("unexpected Output(%s %v)", name, args)
			return "", nil
		},
		Getenv:   func(string) string { return "" },
		UserHome: t.TempDir(),
	}
}

func TestParse(t *testing.T) {
	d := deps(t)
	ps, err := Parse("gh,kube", d)
	if err != nil || len(ps) != 2 || ps[0].Name() != "gh" || ps[1].Name() != "kube" {
		t.Fatalf("Parse = %v, %v", ps, err)
	}
	if _, err := Parse("gh,gh", d); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate should fail: %v", err)
	}
	if _, err := Parse("aws", d); err == nil || !strings.Contains(err.Error(), "unknown extra") {
		t.Errorf("unknown should fail: %v", err)
	}
	if _, err := Parse(" , ", d); err == nil {
		t.Error("empty spec should fail")
	}
}

func TestGhEnv(t *testing.T) {
	d := deps(t)
	d.Output = func(name string, args ...string) (string, error) {
		if name != "gh" || strings.Join(args, " ") != "auth token" {
			t.Fatalf("Output(%s %v)", name, args)
		}
		return "ghp_abc\n", nil
	}
	ps, _ := Parse("gh", d)
	env, err := EnvVars(ps)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"GITHUB_TOKEN", "ghp_abc"}, {"GH_TOKEN", "ghp_abc"}}
	if len(env) != 2 {
		t.Fatalf("env = %v", env)
	}
	for _, w := range want {
		found := false
		for _, kv := range env {
			if kv == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %v in %v", w, env)
		}
	}
	if files, err := FileEntries(ps); err != nil || len(files) != 0 {
		t.Errorf("gh should be env-only: %v, %v", files, err)
	}
}

func TestGhEnvErrors(t *testing.T) {
	d := deps(t)
	d.Output = func(string, ...string) (string, error) { return "", errors.New("boom") }
	ps, _ := Parse("gh", d)
	if _, err := EnvVars(ps); err == nil || !strings.Contains(err.Error(), "logged in") {
		t.Errorf("err = %v", err)
	}
	d.Output = func(string, ...string) (string, error) { return "\n", nil }
	ps, _ = Parse("gh", d)
	if _, err := EnvVars(ps); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("err = %v", err)
	}
}

func TestKubeFiles(t *testing.T) {
	d := deps(t)
	// Fallback location: ~/.kube/config under UserHome.
	cfg := filepath.Join(d.UserHome, ".kube", "config")
	os.MkdirAll(filepath.Dir(cfg), 0o755)
	os.WriteFile(cfg, []byte("clusters: []\n"), 0o600)

	ps, _ := Parse("kube", d)
	files, err := FileEntries(ps)
	if err != nil || len(files) != 1 {
		t.Fatalf("files = %v, %v", files, err)
	}
	e := files[0]
	if e.Rel != ".kube/config" || e.Mode != 0o600 || string(e.Data) != "clusters: []\n" {
		t.Errorf("entry = %+v", e)
	}
	if env, err := EnvVars(ps); err != nil || len(env) != 0 {
		t.Errorf("kube should be file-only: %v, %v", env, err)
	}
}

func TestKubeKUBECONFIGListTakesFirst(t *testing.T) {
	d := deps(t)
	first := filepath.Join(t.TempDir(), "a")
	os.WriteFile(first, []byte("first"), 0o600)
	d.Getenv = func(k string) string {
		if k == "KUBECONFIG" {
			return first + string(os.PathListSeparator) + "/nope/b"
		}
		return ""
	}
	ps, _ := Parse("kube", d)
	files, err := FileEntries(ps)
	if err != nil || len(files) != 1 || string(files[0].Data) != "first" {
		t.Fatalf("files = %v, %v", files, err)
	}
}

func TestKubeMissingConfigFails(t *testing.T) {
	ps, _ := Parse("kube", deps(t))
	if _, err := FileEntries(ps); err == nil || !strings.Contains(err.Error(), "no kubeconfig") {
		t.Errorf("err = %v", err)
	}
}
