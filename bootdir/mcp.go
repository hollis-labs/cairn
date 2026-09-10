package bootdir

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chrispian/cairn/profile"
)

// ErrMCPServer reports a server in a profile's manifest that cannot be written
// into the MCP configuration: it declares no name, or it declares one another
// entry already claimed. Both are refusals to lose a server silently — the
// configuration is a map keyed by name, so an unnamed or repeated entry would
// vanish into the entry beside it.
var ErrMCPServer = errors.New("invalid mcp server")

// mcpConfig is the MCP configuration document: one object keyed by server
// name, under the single key the harness reads.
type mcpConfig struct {
	Servers map[string]mcpServerConfig `json:"mcpServers"`
}

// mcpServerConfig is one server's entry. The fields are the ones a profile
// declares; an empty list or environment is omitted rather than written as an
// empty collection, so a server that takes no arguments reads as one.
type mcpServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// renderMCP returns the MCP configuration built from the profile's manifest.
//
// A profile declaring no servers renders no file rather than an empty
// document: an empty document is a claim that the harness serves nothing,
// which is not the same as a profile that said nothing about MCP.
//
// The output is deterministic. Both maps in the document are marshalled by
// encoding/json, which writes map keys in sorted order, so two renders of the
// same manifest are byte-identical whatever order the servers were declared
// in.
func renderMCP(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	declared, err := inst.Profile.Spec.MCP()
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return nil, nil
	}
	if !inst.Layout.MCP.Declared() {
		return nil, fmt.Errorf(
			"%w: spec.%s declares %d servers, and the %s layout declares no path for an MCP configuration",
			ErrProviderLayout, profile.SpecKeyMCP, len(declared), inst.Layout.Provider)
	}

	servers := make(map[string]mcpServerConfig, len(declared))
	for i, server := range declared {
		name := strings.TrimSpace(server.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: the server at index %d of spec.%s has no name",
				ErrMCPServer, i, profile.SpecKeyMCP)
		}
		if _, taken := servers[name]; taken {
			return nil, fmt.Errorf(
				"%w: the server at index %d of spec.%s is named %q, which an earlier entry already claimed",
				ErrMCPServer, i, profile.SpecKeyMCP, name)
		}
		servers[name] = mcpServerConfig{
			Command: server.Command,
			Args:    server.Args,
			Env:     server.Env,
		}
	}

	content, err := json.MarshalIndent(mcpConfig{Servers: servers}, "", JSONIndent)
	if err != nil {
		return nil, fmt.Errorf("encode the MCP configuration: %w", err)
	}
	return []File{{
		Path:    inst.Layout.MCP.RelPath,
		Content: append(content, '\n'),
		Mode:    inst.Layout.MCP.Mode,
	}}, nil
}
