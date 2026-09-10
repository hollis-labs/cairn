package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/profile"
)

// TestMergeSettingsDocument is the ownership rule stated case by case: a key
// the render declares carries the render's value, and a key it does not
// carries the bytes that were found.
func TestMergeSettingsDocument(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		rendered string
		existing string
		want     string
	}{{
		name:     "a key cairn never declared is kept",
		rendered: `{"model":"opus"}`,
		existing: `{"model":"haiku","tui":"fullscreen"}`,
		want:     "{\n  \"model\": \"opus\",\n  \"tui\": \"fullscreen\"\n}\n",
	}, {
		name:     "a key cairn declares is overwritten",
		rendered: `{"model":"opus"}`,
		existing: `{"model":"haiku"}`,
		want:     "{\n  \"model\": \"opus\"\n}\n",
	}, {
		name:     "a key cairn declares and the file lacks is added",
		rendered: `{"model":"opus"}`,
		existing: `{"tui":"fullscreen"}`,
		want:     "{\n  \"tui\": \"fullscreen\",\n  \"model\": \"opus\"\n}\n",
	}, {
		// The permissions case, which is why the rule goes down rather than
		// stopping at the top level: cairn declares defaultMode and the
		// operator's own rules sit beside it.
		name:     "a key cairn never declared one level down is kept",
		rendered: `{"permissions":{"defaultMode":"auto"}}`,
		existing: `{"permissions":{"allow":["Bash(ls:*)"],"defaultMode":"plan"}}`,
		want:     "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(ls:*)\"\n    ],\n    \"defaultMode\": \"auto\"\n  }\n}\n",
	}, {
		// An array is a value, not a collection to compose. Cairn's hooks
		// replace the operator's rather than appending to them.
		name:     "an array at a declared key is replaced whole",
		rendered: `{"hooks":["a"]}`,
		existing: `{"hooks":["b","c"]}`,
		want:     "{\n  \"hooks\": [\n    \"a\"\n  ]\n}\n",
	}, {
		name:     "an object replacing a scalar at a declared key is the render's",
		rendered: `{"permissions":{"defaultMode":"auto"}}`,
		existing: `{"permissions":"auto"}`,
		want:     "{\n  \"permissions\": {\n    \"defaultMode\": \"auto\"\n  }\n}\n",
	}, {
		name:     "nothing on disk is the render",
		rendered: "{\n  \"model\": \"opus\"\n}\n",
		existing: "",
		want:     "{\n  \"model\": \"opus\"\n}\n",
	}, {
		name:     "a file that is not JSON is the render",
		rendered: "{\n  \"model\": \"opus\"\n}\n",
		existing: "not a settings document\n",
		want:     "{\n  \"model\": \"opus\"\n}\n",
	}, {
		name:     "a document that is not an object is the render",
		rendered: "{\n  \"model\": \"opus\"\n}\n",
		existing: `["model"]`,
		want:     "{\n  \"model\": \"opus\"\n}\n",
	}, {
		// Collapsing it silently would edit a document cairn cannot read, in
		// the file that carries the permission mode.
		name:     "a document declaring one key twice is the render",
		rendered: "{\n  \"model\": \"opus\"\n}\n",
		existing: `{"tui":"fullscreen","tui":"compact"}`,
		want:     "{\n  \"model\": \"opus\"\n}\n",
	}, {
		// The undeclared key is what makes this case say anything. With only
		// the declared key at the declared value, refusing and merging produce
		// the same bytes, and the test cannot tell a refusal from a merge that
		// silently dropped the trailing garbage.
		name:     "trailing content after the object is the render",
		rendered: "{\n  \"model\": \"opus\"\n}\n",
		existing: `{"tui":"fullscreen"} and then some`,
		want:     "{\n  \"model\": \"opus\"\n}\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(mergeSettingsDocument([]byte(tc.rendered), []byte(tc.existing)))
			if got != tc.want {
				t.Errorf("merge\n\trendered %s\n\texisting %s\ngave\n\t%q\nwant\n\t%q",
					tc.rendered, tc.existing, got, tc.want)
			}
		})
	}
}

// TestMergeKeepsTheDocumentItWasGiven is the property the check rests on: a
// file that already holds the render merges to itself, so nothing is reported
// and an install is a no-op.
//
// The layouts differ on purpose. The harness relays the document out its own
// way, and the merge has to survive that as [bootdir.IndentJSON] does.
func TestMergeKeepsTheDocumentItWasGiven(t *testing.T) {
	t.Parallel()
	rendered := bootdir.IndentJSON([]byte(`{"model":"opus","permissions":{"defaultMode":"auto"}}`))
	for _, existing := range []string{
		string(rendered),
		`{"model":"opus","permissions":{"defaultMode":"auto"}}`,
		"{\n\t\"model\": \"opus\",\n\t\"permissions\": {\"defaultMode\": \"auto\"}\n}",
	} {
		got := mergeSettingsDocument(rendered, []byte(existing))
		if want := string(bootdir.IndentJSON([]byte(existing))); string(got) != want {
			t.Errorf("merging the render into %q gave\n\t%q\nwant\n\t%q", existing, got, want)
		}
	}
}

// TestMergeIsIdempotent holds the shape that keeps a check quiet across runs:
// installing twice writes the same bytes the second time, so the operator's
// own keys do not migrate around the file one install at a time.
func TestMergeIsIdempotent(t *testing.T) {
	t.Parallel()
	rendered := bootdir.IndentJSON([]byte(`{"model":"opus","env":{"A":"1"}}`))
	once := mergeSettingsDocument(rendered, []byte(`{"tui":"fullscreen","model":"haiku","env":{"B":"2"}}`))
	twice := mergeSettingsDocument(rendered, once)
	if string(once) != string(twice) {
		t.Errorf("merging twice moved the document:\n\tonce  %q\n\ttwice %q", once, twice)
	}
	for _, want := range []string{`"tui": "fullscreen"`, `"model": "opus"`, `"B": "2"`, `"A": "1"`} {
		if !strings.Contains(string(once), want) {
			t.Errorf("the merged document\n\t%s\nis missing %s", once, want)
		}
	}
}

// TestMergeDoesNotRespellWhatItCopies is
// TestInstallRoundTripsHTMLSpecialCharactersInSettings for the half of the
// document cairn did not render.
//
// The merge writes raw bytes from both sides and re-encodes neither, so a key
// or a value holding "<", ">" or "&" comes out as it went in. Anything here
// that reached for [encoding/json.Marshal] would escape them.
func TestMergeDoesNotRespellWhatItCopies(t *testing.T) {
	t.Parallel()
	got := string(mergeSettingsDocument(
		[]byte(`{"model":"opus"}`),
		[]byte(`{"a < b":"c && d > e"}`),
	))
	for _, want := range []string{`"a < b"`, `"c && d > e"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the merged document\n\t%s\nlost %s", got, want)
		}
	}
	for _, escaped := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(got, escaped) {
			t.Errorf("the merged document holds %s: the merge re-encoded what it was handed:\n\t%s",
				escaped, got)
		}
	}
}

// settingsLayer returns a layer whose profile declares document as its
// settings, rooted at a fresh temporary directory.
func settingsLayer(t *testing.T, document string) *Layer {
	t.Helper()
	return fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Name:     "Base",
		Provider: profile.ProviderClaude,
		Bodies:   []profile.Body{{ID: "base", Text: "Read the profile."}},
		Spec:     fixtureSpec(t, `{"settings": {"claude": `+document+`}}`),
	})
}

// plantSettings writes document at the layer's settings path, which is the
// state an install finds when the harness or the operator got there first.
func plantSettings(t *testing.T, lay *Layer, document string) {
	t.Helper()
	dest, err := lay.Root.Path(".claude/" + "settings.json")
	if err != nil {
		t.Fatalf("resolve the settings path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), DirMode); err != nil {
		t.Fatalf("create the provider directory: %v", err)
	}
	if err := os.WriteFile(dest, []byte(document), 0o644); err != nil {
		t.Fatalf("plant %s: %v", dest, err)
	}
}

// TestInstallKeepsASettingsKeyCairnNeverDeclared is the finding this whole
// mechanism was written for, in the form it actually happened.
//
// agent-setup's profiles/base.md declines to declare `model` on purpose — a
// default there goes stale the week a new one ships, and a boot directory
// passed through --settings would promote it over whatever the session chose.
// Cairn reasoned its way to not owning the key, and then a render-and-overwrite
// install deleted the operator's value for it. Nothing renders it, so nothing
// would ever put it back.
func TestInstallKeepsASettingsKeyCairnNeverDeclared(t *testing.T) {
	lay := settingsLayer(t, `{"permissions": {"defaultMode": "auto"}}`)
	plantSettings(t, lay, "{\n  \"model\": \"opusplan\",\n  \"permissions\": {\n    \"defaultMode\": \"auto\"\n  }\n}\n")

	if _, err := Install(lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := string(installed(t, lay.Root, ".claude/"+"settings.json"))
	if !strings.Contains(got, `"model": "opusplan"`) {
		t.Fatalf("the install deleted a key cairn never declared:\n%s", got)
	}
}

// TestInstallOverwritesASettingsKeyCairnDeclares is the other half. Keeping
// what cairn does not own is only worth anything if cairn still owns what it
// declares — including one level down, where the operator's own permission
// rules sit beside the mode cairn sets.
func TestInstallOverwritesASettingsKeyCairnDeclares(t *testing.T) {
	lay := settingsLayer(t, `{"permissions": {"defaultMode": "auto"}}`)
	plantSettings(t, lay, `{"permissions":{"defaultMode":"bypassPermissions","allow":["Bash(ls:*)"]},"model":"opusplan"}`)

	if _, err := Install(lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := string(installed(t, lay.Root, ".claude/"+"settings.json"))
	if strings.Contains(got, "bypassPermissions") {
		t.Errorf("the install left a declared key the operator had changed:\n%s", got)
	}
	if !strings.Contains(got, `"defaultMode": "auto"`) {
		t.Errorf("the install did not write the declared value:\n%s", got)
	}
	for _, kept := range []string{`"model": "opusplan"`, `"Bash(ls:*)"`} {
		if !strings.Contains(got, kept) {
			t.Errorf("the install dropped %s, which cairn never declared:\n%s", kept, got)
		}
	}
}

// TestInstallOnACleanRootIsTheRender pins the boundary this change does not
// cross. A root with no settings document — a new machine, and every boot
// directory — gets the render and nothing else, which is why the goldens do
// not move.
func TestInstallOnACleanRootIsTheRender(t *testing.T) {
	lay := settingsLayer(t, `{"permissions": {"defaultMode": "auto"}}`)
	rendered, err := Render(lay)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, err := Install(lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := installed(t, lay.Root, ".claude/"+"settings.json")
	want := renderedFile(t, rendered, ".claude/"+"settings.json").Content
	if string(got) != string(want) {
		t.Errorf("the installed settings hold\n\t%q\nwant the render\n\t%q", got, want)
	}
}

// TestInstallTwiceWritesTheSameSettings is idempotence where an operator sees
// it: the second install of an unchanged profile leaves the file alone, so
// their own keys do not drift around it one run at a time.
func TestInstallTwiceWritesTheSameSettings(t *testing.T) {
	lay := settingsLayer(t, `{"permissions": {"defaultMode": "auto"}}`)
	plantSettings(t, lay, `{"model":"opusplan","permissions":{"defaultMode":"plan"}}`)

	if _, err := Install(lay); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	once := string(installed(t, lay.Root, ".claude/"+"settings.json"))
	if _, err := Install(lay); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if twice := string(installed(t, lay.Root, ".claude/"+"settings.json")); twice != once {
		t.Errorf("the second install rewrote the document:\n\tonce  %q\n\ttwice %q", once, twice)
	}
}
