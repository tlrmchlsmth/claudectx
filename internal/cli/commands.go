package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tlrmchlsmth/claudectx/internal/fsx"
	"github.com/tlrmchlsmth/claudectx/internal/linker"
	"github.com/tlrmchlsmth/claudectx/internal/procs"
	"github.com/tlrmchlsmth/claudectx/internal/store"
)

// runningAgents summarizes running agent processes, e.g.
// "9 claude (pids 123, 456, 789, …), 1 codex (pid 42)".
func runningAgents() string {
	ps := procs.FindRunning()
	if len(ps) == 0 {
		return ""
	}
	byName := map[string][]int{}
	for _, p := range ps {
		byName[p.Name] = append(byName[p.Name], p.PID)
	}
	var parts []string
	for _, name := range []string{"claude", "codex"} {
		pids := byName[name]
		if len(pids) == 0 {
			continue
		}
		shown := make([]string, 0, 3)
		for i, pid := range pids {
			if i == 3 {
				shown = append(shown, "…")
				break
			}
			shown = append(shown, fmt.Sprintf("%d", pid))
		}
		label := "pids"
		if len(pids) == 1 {
			label = "pid"
		}
		parts = append(parts, fmt.Sprintf("%d %s (%s %s)", len(pids), name, label, strings.Join(shown, ", ")))
	}
	return strings.Join(parts, ", ")
}

func (a *App) isTTY() bool {
	f, ok := a.Stdout.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (a *App) cmdList(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	names, err := a.S.List()
	if err != nil {
		return err
	}
	if hasFlag(args, "--json") {
		return json.NewEncoder(a.Stdout).Encode(map[string]any{
			"current": st.Current, "previous": st.Previous, "contexts": names,
			"terminal": a.P.TerminalContext,
		})
	}
	for _, n := range names {
		marker, suffix := "  ", ""
		if n == st.Current {
			marker = "* "
		}
		if n == a.P.TerminalContext {
			suffix = "  (this terminal)"
		}
		line := marker + n + suffix
		if n == st.Current && a.isTTY() {
			line = "\x1b[32m" + line + "\x1b[0m"
		}
		fmt.Fprintln(a.Stdout, line)
	}
	if a.P.TerminalContext != "" && a.P.TerminalContext != st.Current {
		fmt.Fprintf(a.Stdout, "\nthis terminal is pinned to %q via CLAUDE_CONFIG_DIR (global is %q); `cx off` to unpin\n",
			a.P.TerminalContext, st.Current)
	}
	return nil
}

func (a *App) cmdCurrent() error {
	// A terminal pinned via `claudectx env` sees its own context, not the
	// global one — that is the whole point of terminal scoping.
	if a.P.TerminalContext != "" {
		fmt.Fprintln(a.Stdout, a.P.TerminalContext)
		return nil
	}
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, st.Current)
	return nil
}

// contextSummary is the data behind `show`.
type contextSummary struct {
	Name             string   `json:"name"`
	Current          bool     `json:"current"`
	ClaudeModel      string   `json:"claude_model,omitempty"`
	ClaudeMode       string   `json:"claude_permission_mode,omitempty"`
	ClaudeSkills     []string `json:"claude_skills"`
	CodexSkills      []string `json:"codex_skills"`
	MCPServers       []string `json:"mcp_servers"`
	ClaudeMD         bool     `json:"claude_md"`
	AgentsMD         bool     `json:"agents_md"`
	ClaudeCredStash  bool     `json:"claude_credentials_stashed"`
	CodexAuthPresent bool     `json:"codex_auth_present"`
}

func (a *App) summarize(name string, current bool) contextSummary {
	s := contextSummary{Name: name, Current: current,
		ClaudeSkills: []string{}, CodexSkills: []string{}, MCPServers: []string{}}

	if data, err := os.ReadFile(filepath.Join(a.P.CtxClaudeDir(name), "settings.json")); err == nil {
		var settings struct {
			Model       string `json:"model"`
			Permissions struct {
				DefaultMode string `json:"defaultMode"`
			} `json:"permissions"`
		}
		if json.Unmarshal(data, &settings) == nil {
			s.ClaudeModel = settings.Model
			s.ClaudeMode = settings.Permissions.DefaultMode
		}
	}
	s.ClaudeSkills = skillNames(filepath.Join(a.P.CtxClaudeDir(name), "skills"))
	s.CodexSkills = skillNames(filepath.Join(a.P.CtxCodexDir(name), "skills"))

	if data, err := os.ReadFile(a.P.CtxClaudeJSON(name)); err == nil {
		var cj struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(data, &cj) == nil {
			for k := range cj.MCPServers {
				s.MCPServers = append(s.MCPServers, k)
			}
		}
	}

	s.ClaudeMD = fileExists(filepath.Join(a.P.CtxClaudeDir(name), "CLAUDE.md"))
	s.AgentsMD = fileExists(filepath.Join(a.P.CtxCodexDir(name), "AGENTS.md"))
	s.ClaudeCredStash = fileExists(a.P.CtxKeychainStash(name))
	s.CodexAuthPresent = fileExists(filepath.Join(a.P.CtxCodexDir(name), "auth.json"))
	return s
}

func skillNames(dir string) []string {
	names := []string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return names
	}
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(dir, e.Name(), "SKILL.md")) {
			names = append(names, e.Name())
		}
	}
	return names
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (a *App) cmdShow(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	jsonOut := hasFlag(args, "--json")
	name := st.Current
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			name = arg
		}
	}
	if !a.S.Exists(name) {
		return fmt.Errorf("no such context %q", name)
	}
	s := a.summarize(name, name == st.Current)
	if jsonOut {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}

	marker := ""
	if s.Current {
		marker = " (current)"
	}
	fmt.Fprintf(a.Stdout, "context: %s%s\n", s.Name, marker)
	fmt.Fprintf(a.Stdout, "  claude:\n")
	fmt.Fprintf(a.Stdout, "    model:       %s\n", orDash(s.ClaudeModel))
	fmt.Fprintf(a.Stdout, "    permissions: %s\n", orDash(s.ClaudeMode))
	fmt.Fprintf(a.Stdout, "    CLAUDE.md:   %v\n", s.ClaudeMD)
	fmt.Fprintf(a.Stdout, "    skills:      %d %s\n", len(s.ClaudeSkills), joinOrEmpty(s.ClaudeSkills))
	fmt.Fprintf(a.Stdout, "    credentials: %s\n", presence(s.ClaudeCredStash, "stashed", "none stashed"))
	fmt.Fprintf(a.Stdout, "  codex:\n")
	fmt.Fprintf(a.Stdout, "    AGENTS.md:   %v\n", s.AgentsMD)
	fmt.Fprintf(a.Stdout, "    skills:      %d %s\n", len(s.CodexSkills), joinOrEmpty(s.CodexSkills))
	fmt.Fprintf(a.Stdout, "    auth.json:   %s\n", presence(s.CodexAuthPresent, "present", "absent"))
	fmt.Fprintf(a.Stdout, "  mcp servers:   %d %s\n", len(s.MCPServers), joinOrEmpty(s.MCPServers))
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func joinOrEmpty(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return "(" + strings.Join(items, ", ") + ")"
}

func presence(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func (a *App) cmdCreate(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	// --from is opt-in cloning; the default is an empty context. Detect
	// whether --from was given at all, since `--from` with no value means
	// "from the current context".
	fromGiven := false
	for _, arg := range args {
		if arg == "--from" || strings.HasPrefix(arg, "--from=") {
			fromGiven = true
		}
	}
	from, args := flagValue(args, "--from")
	empty := hasFlag(args, "--empty")
	var name string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			name = arg
			break
		}
	}
	if name == "" {
		return fmt.Errorf("usage: claudectx create <name> [--from [<ctx>]]")
	}
	if err := store.ValidateName(name); err != nil {
		return err
	}
	if a.S.Exists(name) {
		return fmt.Errorf("context %q already exists", name)
	}
	if empty && fromGiven {
		return fmt.Errorf("--empty and --from are mutually exclusive")
	}

	// Default: an empty context. `--empty` makes that explicit.
	if !fromGiven {
		if err := a.S.ScaffoldContext(name); err != nil {
			return err
		}
		// Seed claude.json with just the onboarding flags so a fresh context
		// doesn't drop the user back into first-run setup.
		seed := seedClaudeJSON(a.P.CtxClaudeJSON(st.Current))
		if err := store.WriteFileAtomic(a.P.CtxClaudeJSON(name), seed, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(a.Stdout, "created empty context %q\n", name)
		return nil
	}

	if from == "" {
		from = st.Current // `--from` with no value clones the current context
	}
	if !a.S.Exists(from) {
		return fmt.Errorf("no such context %q", from)
	}
	// Capture the freshest claude.json for the source before copying.
	if from == st.Current {
		if err := store.CopyFileAtomic(a.P.ClaudeJSON, a.P.CtxClaudeJSON(from), 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// Credentials are never copied between contexts (see isCredentialPath):
	// the clone starts with no logins so each context holds its own key.
	err = fsx.CopyTree(a.P.ContextDir(from), a.P.ContextDir(name), isCredentialPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.P.CtxSecretsDir(name), 0o700); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "created context %q from %q (credentials not copied — log in per context)\n", name, from)
	return nil
}

// isCredentialPath reports whether rel (a path relative to a context dir)
// holds authentication material that must never be cloned by `create --from`.
// Covers all three credential stores:
//   - secrets/                  Claude macOS Keychain stash
//   - claude/.credentials.json  Claude OAuth token on Linux
//   - codex/auth.json[.lock]    Codex ChatGPT login or OPENAI_API_KEY
func isCredentialPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	switch rel {
	case "secrets",
		"claude/.credentials.json",
		"codex/auth.json",
		"codex/auth.json.lock":
		return true
	}
	return strings.HasPrefix(rel, "secrets/")
}

// seedClaudeJSON extracts onboarding-related flags from an existing
// claude.json so new empty contexts skip first-run setup.
func seedClaudeJSON(src string) []byte {
	keep := []string{"hasCompletedOnboarding", "lastOnboardingVersion", "installMethod", "autoUpdates"}
	out := map[string]any{}
	if data, err := os.ReadFile(src); err == nil {
		var full map[string]any
		if json.Unmarshal(data, &full) == nil {
			for _, k := range keep {
				if v, ok := full[k]; ok {
					out[k] = v
				}
			}
		}
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	return append(data, '\n')
}

func (a *App) cmdDelete(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	yes := hasFlag(args, "--yes") || hasFlag(args, "-y")
	var name string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			name = arg
			break
		}
	}
	if name == "" {
		return fmt.Errorf("usage: claudectx delete <name> [--yes]")
	}
	if !a.S.Exists(name) {
		return fmt.Errorf("no such context %q", name)
	}
	if name == st.Current {
		return fmt.Errorf("refusing to delete the active context %q — switch away first", name)
	}
	if !yes && !a.confirm(fmt.Sprintf("delete context %q? (it will be moved to backups, not erased)", name)) {
		return fmt.Errorf("aborted")
	}
	dst, err := a.S.Trash(name)
	if err != nil {
		return err
	}
	if st.Previous == name {
		st.Previous = ""
		if err := a.S.Save(st); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.Stdout, "deleted %q (recoverable at %s)\n", name, dst)
	return nil
}

func (a *App) cmdRename(args []string) error {
	var names []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			names = append(names, arg)
		}
	}
	if len(names) != 2 {
		return fmt.Errorf("usage: claudectx rename <old> <new> [--force]")
	}
	oldName, newName := names[0], names[1]
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	if err := store.ValidateName(newName); err != nil {
		return err
	}
	if !a.S.Exists(oldName) {
		return fmt.Errorf("no such context %q", oldName)
	}
	if a.S.Exists(newName) {
		return fmt.Errorf("context %q already exists", newName)
	}
	force := hasFlag(args, "--force") || hasFlag(args, "-f")
	renamingCurrent := oldName == st.Current
	if renamingCurrent && !force {
		if ps := a.ProcScan(); ps != "" {
			fmt.Fprintf(a.Stderr, "warning: running agents are using the current context: %s\n", ps)
			fmt.Fprintln(a.Stderr, "(the rename is atomic, but open sessions may briefly see a dangling path)")
			if !a.confirm("rename anyway?") {
				return fmt.Errorf("aborted (use --force to skip this check)")
			}
		}
	}
	if err := os.Rename(a.P.ContextDir(oldName), a.P.ContextDir(newName)); err != nil {
		return err
	}
	if renamingCurrent {
		// Live links still point at the old path — repoint before saving state.
		if err := linker.Replace(a.P.ClaudeDir, a.P.CtxClaudeDir(newName)); err != nil {
			return err
		}
		if err := linker.Replace(a.P.CodexDir, a.P.CtxCodexDir(newName)); err != nil {
			return err
		}
		st.Current = newName
	}
	if st.Previous == oldName {
		st.Previous = newName
	}
	if err := a.S.Save(st); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "renamed %q -> %q\n", oldName, newName)
	return nil
}

// captureLiveClaudeJSON refreshes the context's claude.json copy from the
// live file (used before translating the current context).
func (a *App) captureLiveClaudeJSON(name string) error {
	err := store.CopyFileAtomic(a.P.ClaudeJSON, a.P.CtxClaudeJSON(name), 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// pushContextClaudeJSON propagates the context's claude.json back to the
// live file (used after a translation edits the current context's copy).
func (a *App) pushContextClaudeJSON(name string) error {
	err := store.CopyFileAtomic(a.P.CtxClaudeJSON(name), a.P.ClaudeJSON, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
