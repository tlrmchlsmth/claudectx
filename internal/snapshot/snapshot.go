// Package snapshot turns a profile into a self-contained, Linux-ready
// config-dir image: the profile home minus host-private noise, with
// credentials translated into the file form the tool reads on Linux
// (and, by default, stripped of long-lived refresh tokens). It is the
// engine behind `claudectx inject`; see docs/design/inject.md.
package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"archive/tar"

	"github.com/tlrmchlsmth/claudectx/internal/keychain"
	"github.com/tlrmchlsmth/claudectx/internal/paths"
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

type Options struct {
	// WithCreds includes the profile's credential, translated to the
	// on-disk form (.credentials.json / auth.json).
	WithCreds bool
	// WithRefreshToken keeps long-lived refresh tokens in the claude
	// credential. Default is access-token-only: a stolen copy expires on
	// its own and the refresh token never leaves the host.
	WithRefreshToken bool
}

// CredStatus reports what Build did about credentials, for user-facing
// messaging. It never carries secret material.
type CredStatus struct {
	Included bool
	// Source is "keychain", "stash", or "file".
	Source string
	// RefreshStripped is true when a refresh token was present and removed.
	RefreshStripped bool
	// ExpiresAt is the claude access token's expiry, when known.
	ExpiresAt time.Time
}

// Entry is one file of the snapshot. Rel is slash-separated, relative to
// the config-dir root.
type Entry struct {
	Rel      string
	Mode     fs.FileMode
	Data     []byte
	Linkname string // symlink target; when set, Data is ignored
	// ModTime is the source file's mtime (synthesized entries get a
	// slightly-past timestamp so remote tar never sees the future under
	// host/VM clock skew).
	ModTime time.Time
}

type Snapshot struct {
	Tool    tool.Tool
	Entries []Entry
	// Skipped lists the top-level names excluded as host-private noise,
	// in walk order, deduplicated.
	Skipped []string
	Cred    CredStatus
}

// Host-private state that is meaningless or sensitive off-host. Keys are
// top-level names inside the profile home.
var claudeExcludes = map[string]bool{
	"projects": true, "todos": true, "shell-snapshots": true,
	"statsig": true, "cache": true, "logs": true, "file-history": true,
	"session-env": true, "local": true, "ide": true, "downloads": true,
	"history.jsonl": true,
}

var codexExcludes = map[string]bool{
	"sessions": true, "archived_sessions": true, "log": true,
	"history.jsonl": true, "cache": true, "tmp": true,
}

// Special-cased paths the walk must not copy verbatim but that aren't
// "skipped" from the user's point of view: credentials are re-added by the
// credential policy, .claude.json is rebuilt slimmed (see claudeJSONEntry).
var claudeSpecial = map[string]bool{".credentials.json": true, ".claude.json": true}
var codexSpecial = map[string]bool{"auth.json": true, "auth.json.lock": true}

// Build assembles the snapshot for one profile. current marks the tool's
// active profile, whose freshest claude.json (and, on macOS, credential)
// live outside the profile dir.
func Build(p paths.Paths, kc keychain.Backend, t tool.Tool, name string, current bool, o Options) (*Snapshot, error) {
	home := p.ProfileHome(t, name)
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("no such %s profile %q", t, name)
	}
	excludes, special := claudeExcludes, claudeSpecial
	if t == tool.Codex {
		excludes, special = codexExcludes, codexSpecial
	}

	s := &Snapshot{Tool: t}
	skipped := map[string]bool{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if special[rel] {
			return nil
		}
		if top := strings.Split(rel, "/")[0]; excludes[top] {
			if !skipped[top] {
				skipped[top] = true
				s.Skipped = append(s.Skipped, top)
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			s.Entries = append(s.Entries, Entry{Rel: rel, Mode: 0o777, Linkname: target, ModTime: synthTime()})
		case d.IsDir():
			return nil // tar -x creates parent dirs; no dir entries needed
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			s.Entries = append(s.Entries, Entry{Rel: rel, Mode: info.Mode().Perm(), Data: data, ModTime: info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(s.Skipped)

	if t == tool.Claude {
		if e, ok := claudeJSONEntry(p, name, current); ok {
			s.Entries = append(s.Entries, e)
		}
	}
	if o.WithCreds {
		if err := s.addCredential(p, kc, name, current, o); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// claudeJSONEntry builds the snapshot's .claude.json. For the current
// profile the live ~/.claude.json is freshest (the profile copy is only
// captured on switch-away). The per-host "projects" key — transcript
// pointers and per-project state for paths that don't exist in the
// container — is stripped; everything else (mcpServers, onboarding flags)
// survives.
func claudeJSONEntry(p paths.Paths, name string, current bool) (Entry, bool) {
	src := p.ProfileClaudeJSON(name)
	if current {
		if _, err := os.Stat(p.ClaudeJSON); err == nil {
			src = p.ClaudeJSON
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return Entry{}, false
	}
	var m map[string]any
	if json.Unmarshal(data, &m) == nil {
		delete(m, "projects")
		if slim, err := json.MarshalIndent(m, "", "  "); err == nil {
			data = append(slim, '\n')
		}
	}
	return Entry{Rel: ".claude.json", Mode: 0o600, Data: data, ModTime: synthTime()}, true
}

func (s *Snapshot) addCredential(p paths.Paths, kc keychain.Backend, name string, current bool, o Options) error {
	if s.Tool == tool.Codex {
		data, err := os.ReadFile(filepath.Join(p.ProfileHome(tool.Codex, name), "auth.json"))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		s.Entries = append(s.Entries, Entry{Rel: "auth.json", Mode: 0o600, Data: data, ModTime: synthTime()})
		s.Cred = CredStatus{Included: true, Source: "file"}
		return nil
	}

	raw, source, err := claudeCredential(p, kc, name, current)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	data, status := stripRefresh(raw, o.WithRefreshToken)
	status.Included = true
	status.Source = source
	s.Cred = status
	s.Entries = append(s.Entries, Entry{Rel: ".credentials.json", Mode: 0o600, Data: data, ModTime: synthTime()})
	return nil
}

// synthTime stamps entries built in-memory. Backdated a minute so a
// container whose clock trails the host never sees a future timestamp
// (remote tar warns loudly about those).
func synthTime() time.Time { return time.Now().Add(-time.Minute) }

// claudeCredential locates the profile's credential payload. The current
// profile's token lives in the live Keychain (the stash only exists for
// inactive profiles); Linux profiles carry .credentials.json in-dir.
func claudeCredential(p paths.Paths, kc keychain.Backend, name string, current bool) ([]byte, string, error) {
	if current && p.KeychainEnabled {
		c, err := kc.Read()
		if err == nil {
			return []byte(c.Password), "keychain", nil
		}
		if !errors.Is(err, keychain.ErrNotFound) {
			return nil, "", err
		}
	}
	if data, err := os.ReadFile(p.KeychainStash(name)); err == nil {
		var c keychain.Credential
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, "", fmt.Errorf("corrupt keychain stash for profile %q: %w", name, err)
		}
		return []byte(c.Password), "stash", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	if data, err := os.ReadFile(filepath.Join(p.ProfileHome(tool.Claude, name), ".credentials.json")); err == nil {
		return data, "file", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	return nil, "", nil
}

// stripRefresh removes long-lived refresh tokens from a claude credential
// payload — both the claudeAiOauth login and any mcpOAuth entries — unless
// keep is set, and extracts the access token expiry for reporting. A
// payload that isn't the expected JSON shape passes through verbatim.
func stripRefresh(raw []byte, keep bool) ([]byte, CredStatus) {
	var st CredStatus
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw, st
	}
	if oauth, ok := m["claudeAiOauth"].(map[string]any); ok {
		if exp, ok := oauth["expiresAt"].(float64); ok {
			st.ExpiresAt = time.UnixMilli(int64(exp))
		}
		if _, has := oauth["refreshToken"]; has && !keep {
			delete(oauth, "refreshToken")
			st.RefreshStripped = true
		}
	}
	if mcp, ok := m["mcpOAuth"].(map[string]any); ok && !keep {
		for _, v := range mcp {
			if server, ok := v.(map[string]any); ok {
				if _, has := server["refreshToken"]; has {
					delete(server, "refreshToken")
					st.RefreshStripped = true
				}
			}
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return raw, st
	}
	return append(data, '\n'), st
}

// TotalBytes is the snapshot's payload size, for reporting.
func (s *Snapshot) TotalBytes() int64 {
	var n int64
	for _, e := range s.Entries {
		n += int64(len(e.Data))
	}
	return n
}

// WriteTar streams the snapshot as a tar archive. File entries carry their
// permission bits (credentials are 0600); parent directories are implied —
// tar -x creates them.
func (s *Snapshot) WriteTar(w io.Writer) error {
	tw := tar.NewWriter(w)
	for _, e := range s.Entries {
		hdr := &tar.Header{
			Name:    e.Rel,
			Mode:    int64(e.Mode.Perm()),
			ModTime: e.ModTime,
		}
		if e.Linkname != "" {
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.Linkname
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.Data))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if e.Linkname == "" {
			if _, err := tw.Write(e.Data); err != nil {
				return err
			}
		}
	}
	return tw.Close()
}

// WriteDir materializes the snapshot into a local directory (the dir:
// target).
func (s *Snapshot) WriteDir(dst string) error {
	for _, e := range s.Entries {
		target := filepath.Join(dst, filepath.FromSlash(e.Rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if e.Linkname != "" {
			os.Remove(target)
			if err := os.Symlink(e.Linkname, target); err != nil {
				return err
			}
			continue
		}
		if err := os.WriteFile(target, e.Data, e.Mode.Perm()); err != nil {
			return err
		}
		// WriteFile perms are masked by umask and ignored for existing
		// files; credentials must end up exactly 0600.
		if err := os.Chmod(target, e.Mode.Perm()); err != nil {
			return err
		}
	}
	return nil
}
