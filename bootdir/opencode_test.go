package bootdir

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chrispian/cairn/profile"
)

// An OpenCode boot drops spec.mcp and spec.settings rather than rendering
// them, and the report names the tree, each key, what it declared and where
// the rendering arrives, so a profile author sees exactly what did not land.
func TestOpenCodeReportsTheKeysItDoesNotRender(t *testing.T) {
	layout, err := LayoutFor(profile.ProviderOpenCode)
	if err != nil {
		t.Fatalf("LayoutFor(%q): %v", profile.ProviderOpenCode, err)
	}
	inst := testInstance(t, profile.Resolved{Spec: testSpec(t, `{
		"templates": {"AGENTS.md": "# agent\n", "CLAUDE.md": "@AGENTS.md\n"},
		"mcp": [{"name": "tools", "command": "tools-mcp"}, {"name": "search", "command": "search-mcp"}],
		"settings": {"opencode": {"model": "anthropic/claude-sonnet-4-6"}, "claude": {"env": {}}}
	}`)})
	inst.Layout = layout

	files, err := Render(inst)
	if err != nil {
		t.Fatalf("Render opencode = %v, want mcp and settings dropped", err)
	}
	for _, f := range files {
		switch f.Path {
		case "CLAUDE.md", "opencode.json", ".mcp.json":
			t.Errorf("the OpenCode render planted %q", f.Path)
		}
	}

	report := Undeclared(inst)
	if len(report) != 2 {
		t.Fatalf("Undeclared() = %q, want one line each for mcp and settings", report)
	}
	for i, want := range [][]string{
		{"opencode layout", "spec.mcp", `"search"`, `"tools"`, "CW-20260930-0136"},
		{"opencode layout", "spec.settings", `"opencode"`, "CW-20260930-0136"},
	} {
		for _, w := range want {
			if !strings.Contains(report[i], w) {
				t.Errorf("line %d %q does not carry %s", i, report[i], w)
			}
		}
	}
}

// A profile declaring neither key, or settings only for another harness,
// hears nothing about them.
func TestOpenCodeIsQuietAboutKeysTheProfileDidNotDeclare(t *testing.T) {
	layout, err := LayoutFor(profile.ProviderOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	inst := testInstance(t, profile.Resolved{Spec: profile.Spec{
		profile.SpecKeySettings: json.RawMessage(`{"claude":{"env":{}}}`),
	}})
	inst.Layout = layout
	if report := Undeclared(inst); len(report) != 0 {
		t.Errorf("Undeclared() = %q, want nothing", report)
	}
}
