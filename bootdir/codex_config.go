package bootdir

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/chrispian/cairn/profile"
	goprovider "github.com/hollis-labs/go-providers/provider"
	"github.com/pelletier/go-toml/v2"
)

// ErrConfigConflict reports a Codex config key that both spec.settings.codex
// and Cairn's provider mapping try to write.
var ErrConfigConflict = errors.New("codex config key collision")

func renderCodexConfig(inst *Instance) ([]File, error) {
	stored, declared, err := inst.Profile.Spec.Settings(inst.Layout.Provider)
	if err != nil {
		return nil, err
	}
	declaredDoc, err := codexSettingsObject(stored, declared)
	if err != nil {
		return nil, err
	}
	generated, err := codexGeneratedConfig(inst)
	if err != nil {
		return nil, err
	}
	if !declared && len(generated) == 0 {
		return nil, nil
	}
	if !inst.Layout.Settings.Declared() {
		return nil, fmt.Errorf(
			"%w: there is a Codex config document to write — spec.%s, spec.%s, spec.%s, or the directories cairn grants — and this layout declares no path for one",
			ErrProviderLayout, profile.SpecKeySettings, profile.SpecKeyAccess, profile.SpecKeyMCP)
	}
	merged, err := mergeCodexConfigDocs(declaredDoc, generated)
	if err != nil {
		return nil, err
	}
	content, err := toml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("encode Codex config.toml: %w", err)
	}
	return []File{{
		Path:    inst.Layout.Settings.RelPath,
		Content: content,
		Mode:    inst.Layout.Settings.Mode,
	}}, nil
}

func codexSettingsObject(raw json.RawMessage, declared bool) (map[string]any, error) {
	if !declared {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf(
			"%w: spec.%s.%s is not an object, so it cannot be rendered as config.toml",
			profile.ErrSettingsProvider, profile.SpecKeySettings, profile.ProviderCodex)
	}
	if out == nil {
		return nil, nil
	}
	return out, nil
}

func codexGeneratedConfig(inst *Instance) (map[string]any, error) {
	dirs, err := grantedDirectories(inst)
	if err != nil {
		return nil, err
	}
	servers, err := codexMCPServers(inst)
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 && len(servers) == 0 {
		return nil, nil
	}

	text, err := (&goprovider.CodexAdapter{
		WritableRoots: dirs,
	}).ConfigDocument(goprovider.PlantContext{MCPServers: servers})
	if err != nil {
		return nil, err
	}
	var generated map[string]any
	if err := toml.Unmarshal([]byte(text), &generated); err != nil {
		return nil, fmt.Errorf("decode generated Codex config.toml: %w", err)
	}
	// The adapter emits headless policy defaults with every config document.
	// Those are provider defaults, not a Cairn-owned install claim. Profiles
	// can declare them explicitly in spec.settings.codex when they want them.
	delete(generated, "approval_policy")
	delete(generated, "sandbox_mode")
	return generated, nil
}

func codexMCPServers(inst *Instance) ([]goprovider.MCPServerSpec, error) {
	declared, err := inst.Profile.Spec.MCP()
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return nil, nil
	}
	out := make([]goprovider.MCPServerSpec, 0, len(declared))
	for i, server := range declared {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: the server at index %d of spec.%s has no name",
				ErrMCPServer, i, profile.SpecKeyMCP)
		}
		keys := mapsKeys(server.Env)
		slices.Sort(keys)
		env := make([]string, 0, len(server.Env))
		for _, key := range keys {
			env = append(env, key+"="+server.Env[key])
		}
		out = append(out, goprovider.MCPServerSpec{
			Name:    name,
			Command: server.Command,
			Args:    server.Args,
			Env:     env,
		})
	}
	return out, nil
}

func mapsKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func mergeCodexConfigDocs(declared, generated map[string]any) (map[string]any, error) {
	out := cloneConfigMap(declared)
	for key, value := range generated {
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("%w: spec.%s.%s declares %q, which Cairn derives for Codex from spec.%s or spec.%s",
				ErrConfigConflict, profile.SpecKeySettings, profile.ProviderCodex, key, profile.SpecKeyAccess, profile.SpecKeyMCP)
		}
		out[key] = value
	}
	return out, nil
}

func cloneConfigMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return make(map[string]any)
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
