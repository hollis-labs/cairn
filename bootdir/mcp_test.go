package bootdir

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/profile"
)

// mcpManifest declares several servers, out of alphabetical order and with
// several environments, so that a rendering has more than one map to order.
const mcpManifest = `{"mcp": [
  {"name": "zulu",    "command": "zulu-server", "args": ["--port", "7"], "env": {"ZULU_TOKEN": "z", "ALPHA": "a"}},
  {"name": "alpha",   "command": "alpha-server", "env": {"B": "2", "A": "1", "C": "3"}},
  {"name": "mike",    "command": "mike-server", "args": ["--verbose"]},
  {"name": "bravo",   "command": "bravo-server"}
]}`

// TestMCPIsByteStableAcrossRenders is the map-ordering guard. Both the server
// catalog and each server's environment are Go maps, whose iteration order
// varies between two runs of the same program; encoding/json writes map keys
// in sorted order, and this asserts that rather than assuming it.
func TestMCPIsByteStableAcrossRenders(t *testing.T) {
	inst := testInstance(t, profile.Resolved{ID: "reviewer", Spec: testSpec(t, mcpManifest)})

	first, err := renderMCP(inst)
	if err != nil {
		t.Fatalf("renderMCP(): %v", err)
	}
	for i := range 32 {
		again, err := renderMCP(inst)
		if err != nil {
			t.Fatalf("renderMCP() on render %d: %v", i, err)
		}
		if string(again[0].Content) != string(first[0].Content) {
			t.Fatalf("render %d differs from the first:\n%s\nwant\n%s",
				i, again[0].Content, first[0].Content)
		}
	}

	want := `{
  "mcpServers": {
    "alpha": {
      "command": "alpha-server",
      "env": {
        "A": "1",
        "B": "2",
        "C": "3"
      }
    },
    "bravo": {
      "command": "bravo-server"
    },
    "mike": {
      "command": "mike-server",
      "args": [
        "--verbose"
      ]
    },
    "zulu": {
      "command": "zulu-server",
      "args": [
        "--port",
        "7"
      ],
      "env": {
        "ALPHA": "a",
        "ZULU_TOKEN": "z"
      }
    }
  }
}
`
	if got := string(first[0].Content); got != want {
		t.Errorf(".mcp.json is\n%s\nwant\n%s", got, want)
	}
}

// TestMCPRendersTheHarnessShape checks the document a harness actually reads:
// one object under mcpServers, keyed by name, with an argument list and an
// environment only where the profile declared one.
func TestMCPRendersTheHarnessShape(t *testing.T) {
	inst := testInstance(t, profile.Resolved{ID: "reviewer", Spec: testSpec(t, mcpManifest)})

	files, err := renderMCP(inst)
	if err != nil {
		t.Fatalf("renderMCP(): %v", err)
	}
	if want := inst.Layout.MCP.RelPath; files[0].Path != want {
		t.Errorf("rendered at %q, want %q", files[0].Path, want)
	}

	var document struct {
		Servers map[string]map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(files[0].Content, &document); err != nil {
		t.Fatalf("decode the rendered .mcp.json: %v", err)
	}
	if len(document.Servers) != 4 {
		t.Fatalf("the document declares %d servers, want 4", len(document.Servers))
	}
	for _, absent := range []string{"args", "env"} {
		if _, present := document.Servers["bravo"][absent]; present {
			t.Errorf("bravo declares no %s, but the rendering holds one", absent)
		}
	}
	if _, present := document.Servers["mike"]["env"]; present {
		t.Error("mike declares no env, but the rendering holds one")
	}
}

// TestMCPIsAbsentWhenNoServerIsDeclared keeps cairn from writing an empty
// manifest, which would assert that the harness serves nothing rather than
// that the profile said nothing about MCP.
func TestMCPIsAbsentWhenNoServerIsDeclared(t *testing.T) {
	for _, manifest := range []string{"", `{}`, `{"mcp": []}`, `{"mcp": null}`} {
		inst := testInstance(t, profile.Resolved{ID: "quiet", Spec: testSpec(t, manifest)})

		files, err := renderMCP(inst)
		if err != nil {
			t.Fatalf("renderMCP() with manifest %q: %v", manifest, err)
		}
		if len(files) != 0 {
			t.Errorf("renderMCP() with manifest %q wrote %v, want no file", manifest, filePaths(files))
		}
	}
}

// TestMCPRefusesAServerItWouldLose covers the two entries that cannot become a
// key of the document. Both would otherwise disappear into the entry beside
// them, leaving a boot directory that serves less than the profile declared.
func TestMCPRefusesAServerItWouldLose(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		names    []string
	}{
		{
			name:     "an unnamed server names its index",
			manifest: `{"mcp": [{"name": "alpha"}, {"command": "nameless"}]}`,
			names:    []string{"index 1"},
		},
		{
			name:     "a repeated name names both the name and the index",
			manifest: `{"mcp": [{"name": "alpha"}, {"name": "bravo"}, {"name": "alpha"}]}`,
			names:    []string{"index 2", `"alpha"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := testInstance(t, profile.Resolved{ID: "reviewer", Spec: testSpec(t, tt.manifest)})

			_, err := renderMCP(inst)
			if !errors.Is(err, ErrMCPServer) {
				t.Fatalf("renderMCP() error = %v, want ErrMCPServer", err)
			}
			for _, want := range tt.names {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error %q does not name %s", err, want)
				}
			}
		})
	}
}

// TestMCPCarriesAMalformedManifestOut leaves the operator's own JSON to the
// package that decodes it, rather than restating the diagnosis here.
func TestMCPCarriesAMalformedManifestOut(t *testing.T) {
	inst := testInstance(t, profile.Resolved{ID: "reviewer", Spec: testSpec(t, `{"mcp": {"not": "a list"}}`)})

	if _, err := renderMCP(inst); err == nil {
		t.Fatal("renderMCP() with a malformed mcp key returned no error")
	}
}
