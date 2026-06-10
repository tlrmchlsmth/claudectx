package translate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/tlrmchlsmth/claudectx/internal/report"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/tomlx"
)

// claudeMCPServer mirrors one entry of mcpServers in claude.json.
type claudeMCPServer struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// planMCP maps mcpServers (claude.json) <-> [mcp_servers.*] (config.toml).
func planMCP(ctx Context, opts Options) (report.Section, error) {
	if opts.Direction == ClaudeToCodex {
		return mcpClaudeToCodex(ctx, opts)
	}
	return mcpCodexToClaude(ctx, opts)
}

func mcpClaudeToCodex(ctx Context, opts Options) (report.Section, error) {
	sec := report.Section{Name: "mcp"}
	servers, err := readClaudeMCP(ctx.ClaudeJSON)
	if err != nil {
		return sec, err
	}
	if len(servers) == 0 {
		sec.Notes = append(sec.Notes, report.LossNote{
			Severity: report.Info, Artifact: "mcp",
			Message: "no MCP servers in claude.json — nothing to translate",
		})
		return sec, nil
	}

	configPath := filepath.Join(ctx.CodexDir, "config.toml")
	existing := readTOMLTables(configPath)

	names := sortedKeys(servers)
	for _, name := range names {
		s := servers[name]
		artifact := "mcp:" + name

		// Only stdio servers translate mechanically. Codex http support is
		// version-dependent, so emit a paste-ready snippet instead of writing
		// config we can't verify.
		if s.Type == "http" || s.Type == "sse" || (s.Command == "" && s.URL != "") {
			snippet := fmt.Sprintf("[mcp_servers.%s]\nurl = %q", name, s.URL)
			sec.Actions = append(sec.Actions, report.Action{
				Kind: report.Skip, Dst: configPath,
				Description: fmt.Sprintf("MCP server %q (%s)", name, s.Type),
				Notes: []report.LossNote{{
					Severity: report.Lost, Artifact: artifact,
					Message: fmt.Sprintf("%s transport support varies by Codex version — add manually if yours supports it:\n        %s",
						s.Type, strings.ReplaceAll(snippet, "\n", "\n        ")),
				}},
			})
			continue
		}
		if s.Command == "" {
			sec.Actions = append(sec.Actions, report.Action{
				Kind: report.Skip, Dst: configPath,
				Description: fmt.Sprintf("MCP server %q", name),
				Notes: []report.LossNote{{
					Severity: report.Warn, Artifact: artifact,
					Message: "entry has neither command nor url — skipped",
				}},
			})
			continue
		}

		if _, exists := existing["mcp_servers."+name]; exists && !opts.Force {
			sec.Actions = append(sec.Actions, report.Action{
				Kind: report.Skip, Dst: configPath,
				Description: fmt.Sprintf("MCP server %q", name),
				Notes: []report.LossNote{{
					Severity: report.Info, Artifact: artifact,
					Message: "already present in config.toml — use --force to overwrite",
				}},
			})
			continue
		}

		kv := map[string]any{"command": s.Command}
		if len(s.Args) > 0 {
			kv["args"] = s.Args
		}
		if len(s.Env) > 0 {
			kv["env"] = s.Env
		}
		tableName := "mcp_servers." + name
		sec.Actions = append(sec.Actions, report.Action{
			Kind: report.Merge, Dst: configPath,
			Description: fmt.Sprintf("MCP server %q (stdio)", name),
			Apply: func() error {
				block, err := tomlx.EmitTable(tableName, kv)
				if err != nil {
					return err
				}
				doc := ""
				if data, err := os.ReadFile(configPath); err == nil {
					doc = string(data)
				}
				out, err := tomlx.SpliceTable(doc, tableName, block)
				if err != nil {
					return err
				}
				return store.WriteFileAtomic(configPath, []byte(out), 0o644)
			},
		})
	}
	return sec, nil
}

func mcpCodexToClaude(ctx Context, opts Options) (report.Section, error) {
	sec := report.Section{Name: "mcp"}
	configPath := filepath.Join(ctx.CodexDir, "config.toml")

	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		sec.Notes = append(sec.Notes, report.LossNote{
			Severity: report.Info, Artifact: "mcp",
			Message: "no config.toml in codex dir — nothing to translate",
		})
		return sec, nil
	}
	if err != nil {
		return sec, err
	}

	var parsed struct {
		MCPServers map[string]map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(data, &parsed); err != nil {
		return sec, fmt.Errorf("parsing %s: %w", configPath, err)
	}
	if len(parsed.MCPServers) == 0 {
		sec.Notes = append(sec.Notes, report.LossNote{
			Severity: report.Info, Artifact: "mcp",
			Message: "no [mcp_servers.*] tables in config.toml — nothing to translate",
		})
		return sec, nil
	}

	existing, err := readClaudeMCP(ctx.ClaudeJSON)
	if err != nil {
		return sec, err
	}

	// knownKeys are the codex server fields we can express in claude.json.
	knownKeys := map[string]bool{"command": true, "args": true, "env": true, "url": true}
	toAdd := map[string]claudeMCPServer{}

	for _, name := range sortedKeys(parsed.MCPServers) {
		table := parsed.MCPServers[name]
		artifact := "mcp:" + name
		var notes []report.LossNote

		if _, ok := existing[name]; ok && !opts.Force {
			sec.Actions = append(sec.Actions, report.Action{
				Kind: report.Skip, Dst: ctx.ClaudeJSON,
				Description: fmt.Sprintf("MCP server %q", name),
				Notes: []report.LossNote{{
					Severity: report.Info, Artifact: artifact,
					Message: "already present in claude.json — use --force to overwrite",
				}},
			})
			continue
		}

		entry := claudeMCPServer{}
		if cmd, ok := table["command"].(string); ok {
			entry.Command = cmd
		}
		if url, ok := table["url"].(string); ok {
			entry.URL = url
			entry.Type = "http"
		}
		if args, ok := table["args"].([]any); ok {
			for _, a := range args {
				if s, ok := a.(string); ok {
					entry.Args = append(entry.Args, s)
				}
			}
		}
		if env, ok := table["env"].(map[string]any); ok {
			entry.Env = map[string]string{}
			for k, v := range env {
				if s, ok := v.(string); ok {
					entry.Env[k] = s
				}
			}
		}
		for k := range table {
			if !knownKeys[k] {
				notes = append(notes, report.LossNote{
					Severity: report.Info, Artifact: artifact,
					Message: fmt.Sprintf("codex-specific key %q has no claude.json equivalent — dropped", k),
				})
			}
		}
		if entry.Command == "" && entry.URL == "" {
			sec.Actions = append(sec.Actions, report.Action{
				Kind: report.Skip, Dst: ctx.ClaudeJSON,
				Description: fmt.Sprintf("MCP server %q", name),
				Notes: append(notes, report.LossNote{
					Severity: report.Warn, Artifact: artifact,
					Message: "entry has neither command nor url — skipped",
				}),
			})
			continue
		}

		toAdd[name] = entry
		sec.Actions = append(sec.Actions, report.Action{
			Kind: report.Merge, Dst: ctx.ClaudeJSON,
			Description: fmt.Sprintf("MCP server %q", name),
			Notes:       notes,
		})
	}

	if len(toAdd) > 0 {
		// One combined apply on the last merge action keeps the claude.json
		// read-modify-write atomic for the whole batch.
		for i := range sec.Actions {
			if sec.Actions[i].Kind == report.Merge {
				sec.Actions[i].Apply = nil
			}
		}
		path := ctx.ClaudeJSON
		sec.Actions = append(sec.Actions, report.Action{
			Kind: report.Write, Dst: path,
			Description: fmt.Sprintf("merge %d server(s) into mcpServers", len(toAdd)),
			Apply: func() error {
				return mergeClaudeMCP(path, toAdd)
			},
		})
	}
	return sec, nil
}

func readClaudeMCP(path string) (map[string]claudeMCPServer, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var parsed struct {
		MCPServers map[string]claudeMCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return parsed.MCPServers, nil
}

// mergeClaudeMCP rewrites claude.json with the new servers merged in,
// preserving every other top-level key untouched.
func mergeClaudeMCP(path string, add map[string]claudeMCPServer) error {
	full := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &full); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := full["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return err
		}
	}
	for name, entry := range add {
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		servers[name] = raw
	}
	raw, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	full["mcpServers"] = raw
	out, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(path, append(out, '\n'), 0o600)
}

// readTOMLTables returns the set of dotted table names present in a TOML
// file (empty map when the file is missing or unparsable).
func readTOMLTables(path string) map[string]bool {
	tables := map[string]bool{}
	data, err := os.ReadFile(path)
	if err != nil {
		return tables
	}
	var parsed map[string]any
	if toml.Unmarshal(data, &parsed) != nil {
		return tables
	}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		if prefix != "" {
			tables[prefix] = true
		}
		for k, child := range m {
			name := k
			if prefix != "" {
				name = prefix + "." + k
			}
			walk(name, child)
		}
	}
	walk("", parsed)
	return tables
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
