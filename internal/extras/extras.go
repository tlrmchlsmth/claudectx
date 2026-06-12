// Package extras delivers host credentials for tools beyond claude/codex —
// a gh token, a kubeconfig — alongside a profile injection or exec session.
// Each provider offers the credential in env form (session-scoped delivery,
// nothing on the target filesystem) or file form ($HOME-rooted entries), or
// both; the inject/exec commands pick the forms they can honor.
package extras

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tlrmchlsmth/claudectx/internal/snapshot"
)

// Deps are the host lookups providers need; a seam for tests.
type Deps struct {
	// Output runs a host command and returns its stdout (`gh auth token`).
	Output func(name string, args ...string) (string, error)
	// Getenv reads host environment variables (KUBECONFIG).
	Getenv func(string) string
	// UserHome is the host home directory (~/.kube/config fallback).
	UserHome string
}

// Provider supplies one extra credential.
type Provider interface {
	Name() string
	// Env returns variables for session-scoped delivery, nil when the
	// credential only exists in file form.
	Env() (map[string]string, error)
	// Files returns $HOME-rooted entries for filesystem delivery, nil when
	// the credential only exists in env form.
	Files() ([]snapshot.Entry, error)
}

// Known lists the provider names, for usage text and completion.
var Known = []string{"gh", "kube"}

// Parse resolves a comma-separated spec ("gh,kube") to providers, in spec
// order, rejecting unknowns and duplicates.
func Parse(spec string, d Deps) ([]Provider, error) {
	var out []Provider
	seen := map[string]bool{}
	for _, name := range strings.Split(spec, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate extra %q", name)
		}
		seen[name] = true
		switch name {
		case "gh":
			out = append(out, gh{d})
		case "kube":
			out = append(out, kube{d})
		default:
			return nil, fmt.Errorf("unknown extra %q (known: %s)", name, strings.Join(Known, ", "))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--extras needs at least one of: %s", strings.Join(Known, ", "))
	}
	return out, nil
}

// EnvVars flattens the providers' env credentials into deterministic order.
func EnvVars(ps []Provider) ([][2]string, error) {
	var out [][2]string
	for _, p := range ps {
		m, err := p.Env()
		if err != nil {
			return nil, fmt.Errorf("extra %q: %w", p.Name(), err)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, [2]string{k, m[k]})
		}
	}
	return out, nil
}

// FileEntries flattens the providers' file credentials.
func FileEntries(ps []Provider) ([]snapshot.Entry, error) {
	var out []snapshot.Entry
	for _, p := range ps {
		es, err := p.Files()
		if err != nil {
			return nil, fmt.Errorf("extra %q: %w", p.Name(), err)
		}
		out = append(out, es...)
	}
	return out, nil
}

// gh delivers the GitHub CLI token. Env-only: `gh` (and `gh auth setup-git`
// for git-over-https) honors GH_TOKEN/GITHUB_TOKEN directly, so nothing
// token-shaped needs to land on the target filesystem.
type gh struct{ d Deps }

func (gh) Name() string { return "gh" }

func (g gh) Env() (map[string]string, error) {
	out, err := g.d.Output("gh", "auth", "token")
	if err != nil {
		return nil, fmt.Errorf("`gh auth token` failed: %w (is gh installed and logged in?)", err)
	}
	tok := strings.TrimSpace(out)
	if tok == "" {
		return nil, errors.New("`gh auth token` returned nothing — run `gh auth login` first")
	}
	return map[string]string{"GH_TOKEN": tok, "GITHUB_TOKEN": tok}, nil
}

func (gh) Files() ([]snapshot.Entry, error) { return nil, nil }

// kube delivers the host kubeconfig. File-only: a kubeconfig is inherently
// a file (clusters, users, contexts), and kubectl reads ~/.kube/config with
// no env-var token form.
type kube struct{ d Deps }

func (kube) Name() string { return "kube" }

func (kube) Env() (map[string]string, error) { return nil, nil }

func (k kube) Files() ([]snapshot.Entry, error) {
	path := k.d.Getenv("KUBECONFIG")
	if path != "" {
		// KUBECONFIG may be a list; the first entry is the primary config.
		path = strings.Split(path, string(os.PathListSeparator))[0]
	} else {
		path = filepath.Join(k.d.UserHome, ".kube", "config")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no kubeconfig at %s: %w", path, err)
	}
	return []snapshot.Entry{{
		Rel: ".kube/config", Mode: 0o600, Data: data,
		// Backdated like snapshot's synthesized entries so remote tar never
		// sees a future timestamp under host/VM clock skew.
		ModTime: time.Now().Add(-time.Minute),
	}}, nil
}
