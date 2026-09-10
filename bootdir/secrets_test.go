package bootdir_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/profile"
)

// TestATemplateCannotReachAManifestValue is a regression guard on the one place
// a substituted value would do real damage.
//
// A template exists to be read into a model's context, and the boot directory
// holding it is handed to a harness. spec.mcp[].env is where an MCP server's
// API keys live, and spec.settings holds whatever the operator put there. A
// marker vocabulary wide enough to reach a manifest key — anything shaped like
// "substitute spec.mcp" — would let a template pull those into the one artifact
// whose whole purpose is to be read aloud.
//
// The rule is therefore structural rather than a promise about secrets, which
// nobody can enforce: there are exactly two marker verbs, one substitutes a
// slot the operator declared a source for, and the other substitutes a name
// from [bootdir.ValueNames], which holds no manifest key. A marker naming one
// has nothing to read.
//
// The test hands Values a map that does carry the manifest keys, holding the
// secret, because that is the only version of this test worth running. Handing
// over a well-formed six-key map and finding no secret in the output would
// assert nothing about cairn — the secret was never in the input. Rendering it
// this way asserts what actually protects the file: substitution reads a value
// marker from the names cairn declares it fills, not from the map it was given.
// The composition root narrows that map too, and is tested where it lives.
func TestATemplateCannotReachAManifestValue(t *testing.T) {
	const secret = "sk-live-do-not-render-me"

	// A template asking for every value cairn will substitute, plus a slot.
	var markers strings.Builder
	for _, name := range bootdir.ValueNames() {
		fmt.Fprintf(&markers, "%s: <!-- cairn:value %s -->\n", name, name)
	}
	markers.WriteString("<!-- cairn:slot note -->\n")

	inst := instance(t, &profile.Resolved{
		ID:       "engineer",
		Name:     "Engineer",
		Provider: profile.ProviderClaude,
		Model:    "opus",
		Bodies:   []profile.Body{{ID: "engineer", Text: "engineer persona"}},
		Spec: profile.Spec{
			"mcp": json.RawMessage(`[{"name":"vanta","command":"vanta-mcp",` +
				`"env":{"VANTA_TOKEN":"` + secret + `"},"args":["--token","` + secret + `"]}]`),
			"settings":  json.RawMessage(`{"claude":{"env":{"ANTHROPIC_API_KEY":"` + secret + `"}}}`),
			"templates": json.RawMessage(`{"AGENTS.md": "declared, and resolved onto the instance"}`),
		},
	})
	// Every value cairn fills, plus the manifest keys a template would name to
	// reach the secret, holding it.
	markers.WriteString("mcp: <!-- cairn:value mcp -->\n")
	markers.WriteString("settings: <!-- cairn:value settings -->\n")

	inst.Templates = map[string]string{"AGENTS.md": markers.String()}
	inst.Sections = map[string]string{"note": "## Note\n\nnothing secret"}
	inst.Values = map[string]string{
		"binding": "eng", "profile": "engineer", "provider": "claude",
		"model": "opus", "scope": "/repo", "session": "s1",
		"mcp": secret, "settings": secret,
	}

	files, err := bootdir.Render(inst)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	var sawTemplate, sawMCP bool
	for _, f := range files {
		switch f.Path {
		case "AGENTS.md":
			sawTemplate = true
			if strings.Contains(string(f.Content), secret) {
				t.Errorf("%s carries a manifest value:\n%s", f.Path, f.Content)
			}
			if strings.Contains(string(f.Content), "vanta") {
				t.Errorf("%s names a declared MCP server:\n%s", f.Path, f.Content)
			}
		case inst.Layout.MCP.RelPath:
			sawMCP = true
			if !strings.Contains(string(f.Content), secret) {
				t.Errorf("%s did not carry the declared server env, so this test proves nothing:\n%s",
					f.Path, f.Content)
			}
		}
	}
	if !sawTemplate {
		t.Fatal("no template was rendered, so the assertion above never ran")
	}
	if !sawMCP {
		t.Fatal("no MCP config was rendered, so the value under test never reached the boot directory")
	}
}

// TestAMarkerNamingAManifestKeyReachesNothing is the other half: a template
// that tries to reach a manifest key gets nothing whatever the caller's map
// holds, and does not silently render the marker's own text into an agent's
// context either.
//
// A value naming a manifest key was refused until [bootdir.ValueNames] stopped
// gating the parser. It renders the empty string now — the same nothing the
// refusal produced, minus the failed boot — and the operator is told, so a
// template reaching for spec.mcp is not a silent no-op.
//
// The map below carries the secret under exactly the keys the markers name, so
// this asserts the property the refusal used to give: it holds for every map,
// not only for maps that were narrowed before they arrived. Asserting it
// against a map that never held the secret would be a statement about Go's map
// lookup rather than about cairn.
//
// Still refused are the markers with no meaning at all: a verb cairn has not
// got, and a body that is not a verb and one name. Leaving either in place
// would plant the marker's own text where an agent reads it.
func TestAMarkerNamingAManifestKeyReachesNothing(t *testing.T) {
	const secret = "sk-live-do-not-render-me"
	hostile := map[string]string{"mcp": secret, "settings": secret, "profile": "engineer"}

	t.Run("a manifest key renders nothing and is reported", func(t *testing.T) {
		for _, name := range []string{"mcp", "settings"} {
			marker := fmt.Sprintf("<!-- cairn:value %s -->", name)
			got, err := bootdir.Substitute("keys: "+marker+"\n", nil, hostile)
			if err != nil {
				t.Fatalf("Substitute(%q): %v", marker, err)
			}
			if got != "keys: \n" {
				t.Errorf("Substitute(%q) = %q, want the marker to have reached nothing", marker, got)
			}
			unfilled, err := bootdir.Unfilled(marker, nil)
			if err != nil {
				t.Fatalf("Unfilled(%q): %v", marker, err)
			}
			if len(unfilled) != 1 || unfilled[0].Name != name {
				t.Errorf("Unfilled(%q) = %v, want the operator told about it", marker, unfilled)
			}
		}
	})
	t.Run("a name cairn does fill still renders", func(t *testing.T) {
		// Out of the same hostile map, so the assertions above are not passing
		// because substitution stopped working.
		got, err := bootdir.Substitute("<!-- cairn:value profile -->", nil, hostile)
		if err != nil {
			t.Fatalf("Substitute(): %v", err)
		}
		if got != "engineer" {
			t.Errorf("Substitute() = %q, want the value cairn fills", got)
		}
	})
	t.Run("a marker with no meaning is refused", func(t *testing.T) {
		for _, marker := range []string{
			"<!-- cairn:spec mcp -->",
			"<!-- cairn:value -->",
		} {
			if _, err := bootdir.Substitute(marker, nil, hostile); err == nil {
				t.Errorf("Substitute(%q) was accepted, want a refusal", marker)
			}
		}
	})
}

// instance builds a claude instance around a resolved profile.
func instance(t *testing.T, resolved *profile.Resolved) *bootdir.Instance {
	t.Helper()
	layout, err := bootdir.LayoutFor(profile.ProviderClaude)
	if err != nil {
		t.Fatalf("claude layout: %v", err)
	}
	return &bootdir.Instance{Dir: t.TempDir(), Layout: layout, Profile: resolved}
}
