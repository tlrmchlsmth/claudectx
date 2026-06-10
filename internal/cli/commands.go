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
	"github.com/tlrmchlsmth/claudectx/internal/tool"
)

// runningAgents summarizes running agent processes for one tool ("" = all),
// e.g. "9 claude (pids 123, 456, 789, …)".
func runningAgents(t tool.Tool) string {
	ps := procs.FindRunning()
	if len(ps) == 0 {
		return ""
	}
	byName := map[string][]int{}
	for _, p := range ps {
		if t != "" && p.Name != string(t) {
			continue
		}
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

// cmdStatus is the bare `claudectx` view: both axes at a glance.
func (a *App) cmdStatus() error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	for _, t := range tool.All {
		axis := st.Axis(t)
		pin := a.P.TerminalPin(t)
		line := fmt.Sprintf("%-7s %s", string(t)+":", axis.Current)
		if pin != "" && pin != axis.Current {
			line += fmt.Sprintf("  [this terminal: %s]", pin)
		}
		fmt.Fprintln(a.Stdout, line)
	}
	if a.P.LegacyTerminalPin {
		fmt.Fprintln(a.Stdout, "\nwarning: this terminal's pin points into the old layout — re-run `eval \"$(claudectx env ...)\"` or `cx off`")
	}
	return nil
}

// listTool prints one axis's profiles, current marked.
func (a *App) listTool(t tool.Tool) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	axis := st.Axis(t)
	names, err := a.S.List(t)
	if err != nil {
		return err
	}
	pin := a.P.TerminalPin(t)
	for _, n := range names {
		marker, suffix := "  ", ""
		if n == axis.Current {
			marker = "* "
		}
		if n == pin {
			suffix = "  (this terminal)"
		}
		line := marker + n + suffix
		if n == axis.Current && a.isTTY() {
			line = "\x1b[32m" + line + "\x1b[0m"
		}
		fmt.Fprintln(a.Stdout, line)
	}
	return nil
}

type axisJSON struct {
	Current  string   `json:"current"`
	Previous string   `json:"previous,omitempty"`
	Profiles []string `json:"profiles"`
	Terminal string   `json:"terminal,omitempty"`
}

func (a *App) cmdList(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	if hasFlag(args, "--json") {
		out := map[string]axisJSON{}
		for _, t := range tool.All {
			names, err := a.S.List(t)
			if err != nil {
				return err
			}
			if names == nil {
				names = []string{}
			}
			axis := st.Axis(t)
			out[string(t)] = axisJSON{
				Current: axis.Current, Previous: axis.Previous,
				Profiles: names, Terminal: a.P.TerminalPin(t),
			}
		}
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, t := range tool.All {
		fmt.Fprintf(a.Stdout, "%s:\n", t)
		if err := a.listToolIndented(t, st); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) listToolIndented(t tool.Tool, st *store.State) error {
	axis := st.Axis(t)
	names, err := a.S.List(t)
	if err != nil {
		return err
	}
	pin := a.P.TerminalPin(t)
	for _, n := range names {
		marker, suffix := "  ", ""
		if n == axis.Current {
			marker = "* "
		}
		if n == pin {
			suffix = "  (this terminal)"
		}
		fmt.Fprintln(a.Stdout, "  "+marker+n+suffix)
	}
	return nil
}

func (a *App) cmdCurrent(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	// A specific tool: print the bare name (scriptable). A terminal pinned
	// via `claudectx env` sees its own profile — that's the point of pins.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		t, err := tool.Parse(args[0])
		if err != nil {
			return err
		}
		if pin := a.P.TerminalPin(t); pin != "" {
			fmt.Fprintln(a.Stdout, pin)
			return nil
		}
		fmt.Fprintln(a.Stdout, st.Axis(t).Current)
		return nil
	}
	for _, t := range tool.All {
		cur := st.Axis(t).Current
		if pin := a.P.TerminalPin(t); pin != "" {
			cur = pin + "  (this terminal)"
		}
		fmt.Fprintf(a.Stdout, "%-7s %s\n", string(t)+":", cur)
	}
	return nil
}

// claudeSummary / codexSummary are the data behind `show`.
type claudeSummary struct {
	Name       string   `json:"name"`
	Current    bool     `json:"current"`
	Model      string   `json:"model,omitempty"`
	Mode       string   `json:"permission_mode,omitempty"`
	Skills     []string `json:"skills"`
	MCPServers []string `json:"mcp_servers"`
	ClaudeMD   bool     `json:"claude_md"`
	CredStash  bool     `json:"credentials_stashed"`
}

type codexSummary struct {
	Name        string   `json:"name"`
	Current     bool     `json:"current"`
	Model       string   `json:"model,omitempty"`
	Skills      []string `json:"skills"`
	MCPServers  []string `json:"mcp_servers"`
	AgentsMD    bool     `json:"agents_md"`
	AuthPresent bool     `json:"auth_present"`
}

func (a *App) summarizeClaude(name string, current bool) claudeSummary {
	s := claudeSummary{Name: name, Current: current, Skills: []string{}, MCPServers: []string{}}
	home := a.P.ProfileHome(tool.Claude, name)

	if data, err := os.ReadFile(filepath.Join(home, "settings.json")); err == nil {
		var settings struct {
			Model       string `json:"model"`
			Permissions struct {
				DefaultMode string `json:"defaultMode"`
			} `json:"permissions"`
		}
		if json.Unmarshal(data, &settings) == nil {
			s.Model = settings.Model
			s.Mode = settings.Permissions.DefaultMode
		}
	}
	s.Skills = skillNames(filepath.Join(home, "skills"))
	if data, err := os.ReadFile(a.P.ProfileClaudeJSON(name)); err == nil {
		var cj struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(data, &cj) == nil {
			for k := range cj.MCPServers {
				s.MCPServers = append(s.MCPServers, k)
			}
		}
	}
	s.ClaudeMD = fileExists(filepath.Join(home, "CLAUDE.md"))
	s.CredStash = fileExists(a.P.KeychainStash(name))
	return s
}

func (a *App) summarizeCodex(name string, current bool) codexSummary {
	s := codexSummary{Name: name, Current: current, Skills: []string{}, MCPServers: []string{}}
	home := a.P.ProfileHome(tool.Codex, name)
	s.Skills = skillNames(filepath.Join(home, "skills"))
	s.AgentsMD = fileExists(filepath.Join(home, "AGENTS.md"))
	s.AuthPresent = fileExists(filepath.Join(home, "auth.json"))
	if data, err := os.ReadFile(filepath.Join(home, "config.toml")); err == nil {
		// cheap line scan; full TOML parsing is the translators' job
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "model ") || strings.HasPrefix(trimmed, "model=") {
				if _, v, ok := strings.Cut(trimmed, "="); ok {
					s.Model = strings.Trim(strings.TrimSpace(v), `"`)
				}
			}
			if strings.HasPrefix(trimmed, "[mcp_servers.") {
				name := strings.TrimSuffix(strings.TrimPrefix(trimmed, "[mcp_servers."), "]")
				s.MCPServers = append(s.MCPServers, name)
			}
		}
	}
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
	var positional []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
		}
	}
	if len(positional) == 0 {
		return fmt.Errorf("usage: claudectx show <claude|codex> [name] [--json]")
	}
	t, err := tool.Parse(positional[0])
	if err != nil {
		return err
	}
	name := st.Axis(t).Current
	if len(positional) > 1 {
		name = positional[1]
	}
	if !a.S.Exists(t, name) {
		return fmt.Errorf("no such %s profile %q", t, name)
	}
	current := name == st.Axis(t).Current

	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	marker := ""
	if current {
		marker = " (current)"
	}

	if t == tool.Claude {
		s := a.summarizeClaude(name, current)
		if jsonOut {
			return enc.Encode(s)
		}
		fmt.Fprintf(a.Stdout, "claude profile: %s%s\n", s.Name, marker)
		fmt.Fprintf(a.Stdout, "  model:       %s\n", orDash(s.Model))
		fmt.Fprintf(a.Stdout, "  permissions: %s\n", orDash(s.Mode))
		fmt.Fprintf(a.Stdout, "  CLAUDE.md:   %v\n", s.ClaudeMD)
		fmt.Fprintf(a.Stdout, "  skills:      %d %s\n", len(s.Skills), joinOrEmpty(s.Skills))
		fmt.Fprintf(a.Stdout, "  mcp servers: %d %s\n", len(s.MCPServers), joinOrEmpty(s.MCPServers))
		fmt.Fprintf(a.Stdout, "  credentials: %s\n", presence(s.CredStash, "stashed", "none stashed"))
		return nil
	}
	s := a.summarizeCodex(name, current)
	if jsonOut {
		return enc.Encode(s)
	}
	fmt.Fprintf(a.Stdout, "codex profile: %s%s\n", s.Name, marker)
	fmt.Fprintf(a.Stdout, "  model:       %s\n", orDash(s.Model))
	fmt.Fprintf(a.Stdout, "  AGENTS.md:   %v\n", s.AgentsMD)
	fmt.Fprintf(a.Stdout, "  skills:      %d %s\n", len(s.Skills), joinOrEmpty(s.Skills))
	fmt.Fprintf(a.Stdout, "  mcp servers: %d %s\n", len(s.MCPServers), joinOrEmpty(s.MCPServers))
	fmt.Fprintf(a.Stdout, "  auth.json:   %s\n", presence(s.AuthPresent, "present", "absent"))
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
	// --from is opt-in cloning; the default is an empty profile. Detect
	// whether --from was given at all, since `--from` with no value means
	// "from the current profile".
	fromGiven := false
	for _, arg := range args {
		if arg == "--from" || strings.HasPrefix(arg, "--from=") {
			fromGiven = true
		}
	}
	from, args := flagValue(args, "--from")
	empty := hasFlag(args, "--empty")
	var positional []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
		}
	}
	if len(positional) < 2 {
		return fmt.Errorf("usage: claudectx create <claude|codex> <name> [--from [<profile>]]")
	}
	t, err := tool.Parse(positional[0])
	if err != nil {
		return err
	}
	name := positional[1]
	if err := store.ValidateName(name); err != nil {
		return err
	}
	if a.S.Exists(t, name) {
		return fmt.Errorf("%s profile %q already exists", t, name)
	}
	if empty && fromGiven {
		return fmt.Errorf("--empty and --from are mutually exclusive")
	}

	// Default: an empty profile. `--empty` makes that explicit.
	if !fromGiven {
		if err := a.S.ScaffoldProfile(t, name); err != nil {
			return err
		}
		if t == tool.Claude {
			// Seed claude.json with just the onboarding flags so a fresh
			// profile doesn't drop the user back into first-run setup.
			seed := seedClaudeJSON(a.P.ProfileClaudeJSON(st.Claude.Current))
			if err := store.WriteFileAtomic(a.P.ProfileClaudeJSON(name), seed, 0o600); err != nil {
				return err
			}
		}
		fmt.Fprintf(a.Stdout, "created empty %s profile %q\n", t, name)
		return nil
	}

	if from == "" {
		from = st.Axis(t).Current // `--from` with no value clones the current profile
	}
	if !a.S.Exists(t, from) {
		return fmt.Errorf("no such %s profile %q", t, from)
	}
	// Capture the freshest claude.json for the source before copying.
	if t == tool.Claude && from == st.Claude.Current {
		if err := store.CopyFileAtomic(a.P.ClaudeJSON, a.P.ProfileClaudeJSON(from), 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	// Credentials are never copied between profiles (see isCredentialPath):
	// the clone starts with no logins so each profile holds its own key.
	err = fsx.CopyTree(a.P.ProfileDir(t, from), a.P.ProfileDir(t, name), isCredentialPath(t))
	if err != nil {
		return err
	}
	if t == tool.Claude {
		if err := os.MkdirAll(a.P.ProfileSecretsDir(name), 0o700); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.Stdout, "created %s profile %q from %q (credentials not copied — log in per profile)\n", t, name, from)
	return nil
}

// isCredentialPath returns the per-tool exclusion rule for `create --from`:
// paths (relative to a profile dir) holding authentication material that
// must never be cloned.
//   - claude: secrets/ (Keychain stash), home/.credentials.json (Linux OAuth)
//   - codex:  home/auth.json[.lock] (ChatGPT login or OPENAI_API_KEY)
func isCredentialPath(t tool.Tool) func(rel string) bool {
	return func(rel string) bool {
		rel = filepath.ToSlash(rel)
		if t == tool.Claude {
			return rel == "secrets" || strings.HasPrefix(rel, "secrets/") ||
				rel == "home/.credentials.json"
		}
		return rel == "home/auth.json" || rel == "home/auth.json.lock"
	}
}

// seedClaudeJSON extracts onboarding-related flags from an existing
// claude.json so new empty profiles skip first-run setup.
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
	var positional []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
		}
	}
	if len(positional) != 2 {
		return fmt.Errorf("usage: claudectx delete <claude|codex> <name> [--yes]")
	}
	t, err := tool.Parse(positional[0])
	if err != nil {
		return err
	}
	name := positional[1]
	if !a.S.Exists(t, name) {
		return fmt.Errorf("no such %s profile %q", t, name)
	}
	axis := st.Axis(t)
	if name == axis.Current {
		return fmt.Errorf("refusing to delete the active %s profile %q — switch away first", t, name)
	}
	if !yes && !a.confirm(fmt.Sprintf("delete %s profile %q? (it will be moved to backups, not erased)", t, name)) {
		return fmt.Errorf("aborted")
	}
	dst, err := a.S.Trash(t, name)
	if err != nil {
		return err
	}
	if axis.Previous == name {
		axis.Previous = ""
		if err := a.S.Save(st); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.Stdout, "deleted %s profile %q (recoverable at %s)\n", t, name, dst)
	return nil
}

func (a *App) cmdRename(args []string) error {
	st, err := a.S.Load()
	if err != nil {
		return err
	}
	force := hasFlag(args, "--force") || hasFlag(args, "-f")
	var positional []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
		}
	}
	if len(positional) != 3 {
		return fmt.Errorf("usage: claudectx rename <claude|codex> <old> <new> [--force]")
	}
	t, err := tool.Parse(positional[0])
	if err != nil {
		return err
	}
	oldName, newName := positional[1], positional[2]
	if err := store.ValidateName(newName); err != nil {
		return err
	}
	if !a.S.Exists(t, oldName) {
		return fmt.Errorf("no such %s profile %q", t, oldName)
	}
	if a.S.Exists(t, newName) {
		return fmt.Errorf("%s profile %q already exists", t, newName)
	}
	axis := st.Axis(t)
	renamingCurrent := oldName == axis.Current
	if renamingCurrent && !force {
		if ps := a.ProcScan(t); ps != "" {
			fmt.Fprintf(a.Stderr, "warning: running agents are using the current %s profile: %s\n", t, ps)
			fmt.Fprintln(a.Stderr, "(the rename is atomic, but open sessions may briefly see a dangling path)")
			if !a.confirm("rename anyway?") {
				return fmt.Errorf("aborted (use --force to skip this check)")
			}
		}
	}
	if err := os.Rename(a.P.ProfileDir(t, oldName), a.P.ProfileDir(t, newName)); err != nil {
		return err
	}
	if renamingCurrent {
		// The live link still points at the old path — repoint before saving.
		if err := linker.Replace(a.P.LiveDir(t), a.P.ProfileHome(t, newName)); err != nil {
			return err
		}
		axis.Current = newName
	}
	if axis.Previous == oldName {
		axis.Previous = newName
	}
	if err := a.S.Save(st); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "renamed %s profile %q -> %q\n", t, oldName, newName)
	return nil
}
