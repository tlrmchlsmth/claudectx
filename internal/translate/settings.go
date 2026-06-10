package translate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/tlrmchlsmth/claudectx/internal/report"
	"github.com/tlrmchlsmth/claudectx/internal/store"
	"github.com/tlrmchlsmth/claudectx/internal/tomlx"
)

// modeMap translates Claude's permissions.defaultMode into Codex's
// approval_policy + sandbox_mode pair.
var modeMap = map[string]struct{ approval, sandbox string }{
	"bypassPermissions": {"never", "danger-full-access"},
	"acceptEdits":       {"on-failure", "workspace-write"},
	"default":           {"on-request", "workspace-write"},
	"plan":              {"on-request", "workspace-write"},
}

// approvalMap is the reverse direction.
var approvalMap = map[string]string{
	"never":      "bypassPermissions",
	"on-failure": "acceptEdits",
	"on-request": "default",
	"untrusted":  "default",
}

type claudeSettings struct {
	Model       string `json:"model"`
	Permissions struct {
		DefaultMode string   `json:"defaultMode"`
		Allow       []string `json:"allow"`
		Deny        []string `json:"deny"`
	} `json:"permissions"`
}

func planSettings(ctx Context, opts Options) (report.Section, error) {
	if opts.Direction == ClaudeToCodex {
		return settingsClaudeToCodex(ctx)
	}
	return settingsCodexToClaude(ctx)
}

func settingsClaudeToCodex(ctx Context) (report.Section, error) {
	sec := report.Section{Name: "settings"}
	settingsPath := filepath.Join(ctx.ClaudeDir, "settings.json")
	configPath := filepath.Join(ctx.CodexDir, "config.toml")

	data, err := os.ReadFile(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		sec.Notes = append(sec.Notes, report.LossNote{
			Severity: report.Info, Artifact: "settings",
			Message: "no settings.json in claude dir — nothing to translate",
		})
		return sec, nil
	}
	if err != nil {
		return sec, err
	}
	var s claudeSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return sec, fmt.Errorf("parsing %s: %w", settingsPath, err)
	}

	var notes []report.LossNote
	mode := s.Permissions.DefaultMode
	if mode == "" {
		mode = "default"
	}
	mapped, ok := modeMap[mode]
	if !ok {
		notes = append(notes, report.LossNote{
			Severity: report.Warn, Artifact: "settings:permissions.defaultMode",
			Message: fmt.Sprintf("unknown mode %q — falling back to on-request/workspace-write", mode),
		})
		mapped = modeMap["default"]
	}

	if n := len(s.Permissions.Allow); n > 0 {
		notes = append(notes, report.LossNote{
			Severity: report.Lost, Artifact: "settings:permissions.allow",
			Message: fmt.Sprintf("%d allow rule(s) have no Codex equivalent (Codex has only approval_policy/sandbox_mode)", n),
		})
	}
	if n := len(s.Permissions.Deny); n > 0 {
		notes = append(notes, report.LossNote{
			Severity: report.Lost, Artifact: "settings:permissions.deny",
			Message: fmt.Sprintf("%d deny rule(s) have no Codex equivalent", n),
		})
	}
	if s.Model != "" {
		notes = append(notes, report.LossNote{
			Severity: report.Lost, Artifact: "settings:model",
			Message: fmt.Sprintf("no cross-vendor mapping for model %q — set Codex `model` manually", s.Model),
		})
	}

	approval, sandbox := mapped.approval, mapped.sandbox
	sec.Actions = append(sec.Actions, report.Action{
		Kind: report.Merge, Dst: configPath,
		Description: fmt.Sprintf("defaultMode %q -> approval_policy=%q, sandbox_mode=%q", mode, approval, sandbox),
		Notes:       notes,
		Apply: func() error {
			doc := ""
			if data, err := os.ReadFile(configPath); err == nil {
				doc = string(data)
			}
			out, err := tomlx.SetTopLevel(doc, "approval_policy", approval)
			if err != nil {
				return err
			}
			out, err = tomlx.SetTopLevel(out, "sandbox_mode", sandbox)
			if err != nil {
				return err
			}
			return store.WriteFileAtomic(configPath, []byte(out), 0o644)
		},
	})
	return sec, nil
}

func settingsCodexToClaude(ctx Context) (report.Section, error) {
	sec := report.Section{Name: "settings"}
	configPath := filepath.Join(ctx.CodexDir, "config.toml")
	settingsPath := filepath.Join(ctx.ClaudeDir, "settings.json")

	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		sec.Notes = append(sec.Notes, report.LossNote{
			Severity: report.Info, Artifact: "settings",
			Message: "no config.toml in codex dir — nothing to translate",
		})
		return sec, nil
	}
	if err != nil {
		return sec, err
	}
	var parsed struct {
		ApprovalPolicy string `toml:"approval_policy"`
		SandboxMode    string `toml:"sandbox_mode"`
		Model          string `toml:"model"`
	}
	if err := toml.Unmarshal(data, &parsed); err != nil {
		return sec, fmt.Errorf("parsing %s: %w", configPath, err)
	}

	var notes []report.LossNote
	if parsed.ApprovalPolicy == "" {
		sec.Notes = append(sec.Notes, report.LossNote{
			Severity: report.Info, Artifact: "settings",
			Message: "config.toml sets no approval_policy — nothing to translate",
		})
		return sec, nil
	}
	mode, ok := approvalMap[parsed.ApprovalPolicy]
	if !ok {
		notes = append(notes, report.LossNote{
			Severity: report.Warn, Artifact: "settings:approval_policy",
			Message: fmt.Sprintf("unknown approval_policy %q — falling back to \"default\"", parsed.ApprovalPolicy),
		})
		mode = "default"
	}
	if parsed.SandboxMode != "" {
		notes = append(notes, report.LossNote{
			Severity: report.Info, Artifact: "settings:sandbox_mode",
			Message: fmt.Sprintf("sandbox_mode %q has no direct Claude equivalent (folded into defaultMode)", parsed.SandboxMode),
		})
	}
	if parsed.Model != "" {
		notes = append(notes, report.LossNote{
			Severity: report.Lost, Artifact: "settings:model",
			Message: fmt.Sprintf("no cross-vendor mapping for model %q — set Claude `model` manually", parsed.Model),
		})
	}

	finalMode := mode
	sec.Actions = append(sec.Actions, report.Action{
		Kind: report.Merge, Dst: settingsPath,
		Description: fmt.Sprintf("approval_policy %q -> defaultMode %q", parsed.ApprovalPolicy, finalMode),
		Notes:       notes,
		Apply: func() error {
			return mergeClaudeSettingsMode(settingsPath, finalMode)
		},
	})
	return sec, nil
}

// mergeClaudeSettingsMode sets permissions.defaultMode in settings.json,
// preserving all other keys.
func mergeClaudeSettingsMode(path, mode string) error {
	full := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &full); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	perms := map[string]json.RawMessage{}
	if raw, ok := full["permissions"]; ok {
		if err := json.Unmarshal(raw, &perms); err != nil {
			return err
		}
	}
	modeRaw, _ := json.Marshal(mode)
	perms["defaultMode"] = modeRaw
	permsRaw, err := json.Marshal(perms)
	if err != nil {
		return err
	}
	full["permissions"] = permsRaw
	out, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(path, append(out, '\n'), 0o644)
}
