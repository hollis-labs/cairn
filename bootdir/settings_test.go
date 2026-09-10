package bootdir

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/chrispian/cairn/profile"
)

// TestSettingsAreWrittenVerbatim is the assertion behind docs/plan.md §6.
// Cairn models no permission mode, validates no tool name and translates
// nothing: the settings key is the operator's document, and the rendering is
// that document. Every one below is deliberately odd and every one is valid
// JSON, because the shape of a settings document belongs to the harness that
// reads it and to whoever wrote it.
//
// Verbatim is asserted by compacting the render and comparing that to the
// stored bytes compacted — [json.Compact] rather than a second call to the
// [json.Indent] under test, so the two sides are not the same function
// agreeing with itself. What survives that round trip is everything the
// operator could have written: a duplicated key, an unescaped angle bracket, a
// number spelled 0.5e3, a document that is not an object at all.
func TestSettingsAreWrittenVerbatim(t *testing.T) {
	documents := []string{
		`{"permissions":{"allow":["Bash(git status:*)","Read(//tmp/**)"],"deny":[]},"model":"opus"}`,
		"{\n\t\"outer\" : {\n\n\t\t\"inner\":[1,2,3]\n\t},\n  \"spaced\"   :   true\n}",
		`{"unknown_to_every_harness": {"nested": {"deeply": [null, false, 0.5e3]}}}`,
		`{"unicode": "héllo é <b>&amp;</b> — ✓", "emptyObject": {}, "emptyList": []}`,
		`{"duplicate": 1, "duplicate": 2}`,
		`{"permissionMode": "a mode no harness has ever heard of"}`,
		`[]`,
		`"a settings document that is a bare string"`,
		`17`,
	}

	for _, document := range documents {
		// Under the provider key the manifest is written with now. Verbatim is
		// measured from after the selection: what a target's document holds is
		// still the harness's business and not cairn's, so a bare list and a
		// duplicated key are as untouched here as they ever were.
		manifest := `{"settings": {"claude": ` + document + `}}`
		inst := testInstance(t, profile.Resolved{ID: "reviewer", Spec: testSpec(t, manifest)})

		files, err := RenderSettings(inst)
		if err != nil {
			t.Fatalf("RenderSettings() with %s: %v", document, err)
		}
		if len(files) != 1 {
			t.Fatalf("RenderSettings() with %s wrote %v, want one file", document, filePaths(files))
		}
		if want := inst.Layout.Settings.RelPath; files[0].Path != want {
			t.Errorf("rendered at %q, want %q", files[0].Path, want)
		}
		if got, want := compactForTest(t, files[0].Content), compactForTest(t, []byte(document)); got != want {
			t.Errorf("the settings document compacts to\n%s\nwant\n%s", got, want)
		}
		if content := files[0].Content; !bytes.HasSuffix(content, []byte("}\n")) &&
			!bytes.HasSuffix(content, []byte("]\n")) && !bytes.HasSuffix(content, []byte("\n")) {
			t.Errorf("the settings document does not end in a newline: %q", content)
		}
	}
}

// compactForTest removes a document's insignificant whitespace, reporting a
// document that is not JSON rather than hiding it.
func compactForTest(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, bytes.TrimSpace(raw)); err != nil {
		t.Fatalf("compact %q: %v", raw, err)
	}
	return buf.String()
}

// TestSettingsAreLaidOut covers the one edit the renderer makes. The operator
// reads this file and diffs it, and the harness rewrites it at this width, so
// a document arriving compact is written out one key per line.
func TestSettingsAreLaidOut(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID:   "reviewer",
		Spec: profile.Spec{profile.SpecKeySettings: []byte(`{"claude":{"model":"opus","tui":"fullscreen"}}`)},
	})

	files, err := RenderSettings(inst)
	if err != nil {
		t.Fatalf("RenderSettings(): %v", err)
	}
	want := "{\n  \"model\": \"opus\",\n  \"tui\": \"fullscreen\"\n}\n"
	if string(files[0].Content) != want {
		t.Errorf("the settings document is %q, want %q", files[0].Content, want)
	}
}

// TestSettingsKeepASingleTrailingNewline covers a stored document that already
// ends in one: it is not given a second.
func TestSettingsKeepASingleTrailingNewline(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID:   "reviewer",
		Spec: profile.Spec{profile.SpecKeySettings: []byte("{\"claude\": {\"model\": \"opus\"}}\n")},
	})

	files, err := RenderSettings(inst)
	if err != nil {
		t.Fatalf("RenderSettings(): %v", err)
	}
	if want := "{\n  \"model\": \"opus\"\n}\n"; string(files[0].Content) != want {
		t.Errorf("the settings document is %q, want %q", files[0].Content, want)
	}
}

// TestSettingsAreAbsentWhenUndeclared keeps cairn from planting a settings
// file for a profile that asked for none. An empty document is a settings
// document, and a harness reads it as one.
//
// The instance has no scope and declares no directories, which is the other
// half of the condition: a file is written when there is a document or a grant,
// and this is neither.
func TestSettingsAreAbsentWhenUndeclared(t *testing.T) {
	for _, manifest := range []string{"", `{}`, `{"settings": null}`,
		// A key that names other providers and not this one, and a key that
		// clears this one. Both are "nothing to write for this target", which
		// is what makes one profile serve every harness without every harness
		// getting a file.
		`{"settings": {"codex": {"approval": "never"}}}`,
		`{"settings": {"claude": null}}`} {
		inst := testInstance(t, profile.Resolved{ID: "quiet", Spec: testSpec(t, manifest)})

		files, err := RenderSettings(inst)
		if err != nil {
			t.Fatalf("RenderSettings() with manifest %q: %v", manifest, err)
		}
		if len(files) != 0 {
			t.Errorf("RenderSettings() with manifest %q wrote %v, want no file",
				manifest, filePaths(files))
		}
	}
}

// TestSettingsRefuseToBeDropped covers a layout declaring no settings path
// while the manifest declares settings. Rendering nothing would silently
// discard the one artifact the operator wrote out by hand.
func TestSettingsRefuseToBeDropped(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID:   "reviewer",
		Spec: testSpec(t, `{"settings": {"claude": {"model": "opus"}}}`),
	})
	inst.Layout.Settings = Artifact{}

	if _, err := RenderSettings(inst); err == nil {
		t.Fatal("RenderSettings() with no declared settings path returned no error")
	}
}

// TestSettingsGrantTheScope is the machine-checkable half of the grant. An
// instance works in a scope, so the harness is told it may reach it — without
// the operator declaring anything. Whether a session launched with a bare
// `claude` actually honours it is the half no test can answer: it depends on
// the settings tier the file lands in, which is the launcher's, and
// examples/README.md records the question rather than an answer.
//
// It also pins what the grant may not disturb: the operator's own key at the
// top level, and their own key inside the very object the grant lands in.
func TestSettingsGrantTheScope(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID:   "reviewer",
		Spec: testSpec(t, `{"settings": {"claude": {"model": "opus", "permissions": {"defaultMode": "auto"}}}}`),
	})
	inst.Scope = resolved(t, t.TempDir())

	files, err := RenderSettings(inst)
	if err != nil {
		t.Fatalf("RenderSettings(): %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("RenderSettings() wrote %v, want one file", filePaths(files))
	}
	document := decodeSettings(t, files[0].Content)
	if want := []string{inst.Scope}; !slices.Equal(document.Permissions.AdditionalDirectories, want) {
		t.Errorf("permissions.additionalDirectories = %v, want %v",
			document.Permissions.AdditionalDirectories, want)
	}
	// "auto" is not in the adapter's own permission-mode vocabulary. It stands
	// because cairn never hands the operator's mode to the adapter and never
	// reads it: an adapter given only directories has nothing to say about a
	// mode, so there is nothing to overwrite and nothing to validate.
	if document.Permissions.DefaultMode != "auto" {
		t.Errorf("permissions.defaultMode = %q, want the operator's %q",
			document.Permissions.DefaultMode, "auto")
	}
	if document.Model != "opus" {
		t.Errorf("model = %q, want the operator's %q", document.Model, "opus")
	}
	if document.APIKeyHelper != "" {
		t.Errorf("apiKeyHelper = %q, want nothing — cairn has no value for it and must invent none",
			document.APIKeyHelper)
	}
}

// TestSettingsGrantDeclaredDirectories covers spec.access.directories: the
// paths add to the scope rather than replacing it, each is expanded the way
// every other manifest path is, and a directory named twice is granted once.
//
// The directories are real ones. A granted path must name an existing
// directory, so a fixture of plausible-looking absolute strings would assert
// the refusal rather than the grant.
func TestSettingsGrantDeclaredDirectories(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	shared := t.TempDir()
	scope := t.TempDir()
	mkdirAll(t, filepath.Join(home, "dev", "nanite"))
	mkdirAll(t, filepath.Join(work, "docs"))

	inst := testInstance(t, profile.Resolved{
		ID: "reviewer",
		Spec: testSpec(t, `{"settings": {"claude": {"model": "opus"}},
			"access": {"directories": ["~/dev/nanite", "$WORK/docs", `+
			strconv.Quote(shared)+`, `+strconv.Quote(scope)+`]}}`),
	})
	inst.Scope = resolved(t, scope)
	inst.Home = home
	inst.Env = func(name string) string {
		if name == "WORK" {
			return work
		}
		return ""
	}

	files, err := RenderSettings(inst)
	if err != nil {
		t.Fatalf("RenderSettings(): %v", err)
	}
	// The scope leads, the manifest's own order follows, and the scope's
	// second declaration is dropped rather than repeated.
	want := []string{
		resolved(t, scope),
		resolved(t, filepath.Join(home, "dev", "nanite")),
		resolved(t, filepath.Join(work, "docs")),
		resolved(t, shared),
	}
	got := decodeSettings(t, files[0].Content).Permissions.AdditionalDirectories
	if !slices.Equal(got, want) {
		t.Errorf("permissions.additionalDirectories = %v, want %v", got, want)
	}
}

// TestSettingsRefuseADirectoryThatWouldWiden is the security half of the key.
// Every one of these renders a grant if nothing checks it, and the first is
// the one that matters: a relative path is resolved by whoever reads the file,
// the harness reads it from inside the boot directory, and ".." from there is
// the boot root — every other session's boot directory for that profile.
//
// An unset variable appears twice on purpose. "$UNSET" alone expands to
// nothing, which would silently drop the grant; "$UNSET/docs" expands to
// "/docs", which is absolute and would be granted. Neither is caught by the
// same check, and both are caught.
func TestSettingsRefuseADirectoryThatWouldWiden(t *testing.T) {
	for _, declared := range []string{
		"..",
		"relative/dir",
		"./x",
		"~notauser/x",
		"   ",
		"$UNSET",
		"$UNSET/docs",
		"/a/path/that/does/not/exist",
	} {
		inst := testInstance(t, profile.Resolved{
			ID:   "reviewer",
			Spec: testSpec(t, `{"access": {"directories": [`+strconv.Quote(declared)+`]}}`),
		})
		inst.Home = t.TempDir()
		inst.Env = func(string) string { return "" }

		_, err := RenderSettings(inst)
		if !errors.Is(err, ErrAccessDirectory) {
			t.Errorf("RenderSettings() with the directory %q = %v, want %v",
				declared, err, ErrAccessDirectory)
			continue
		}
		if !strings.Contains(err.Error(), declared) {
			t.Errorf("the error for %q is %v, and does not quote what the operator wrote",
				declared, err)
		}
	}
}

// TestSettingsRefuseAFileGrantedAsADirectory covers a declared path that
// exists and is not a directory, which the harness would read as a directory
// that is simply empty.
func TestSettingsRefuseAFileGrantedAsADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(file, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write the fixture file: %v", err)
	}
	inst := testInstance(t, profile.Resolved{
		ID:   "reviewer",
		Spec: testSpec(t, `{"access": {"directories": [`+strconv.Quote(file)+`]}}`),
	})

	if _, err := RenderSettings(inst); !errors.Is(err, ErrAccessDirectory) {
		t.Errorf("RenderSettings() = %v, want %v", err, ErrAccessDirectory)
	}
}

// TestSettingsRefuseAHandDeclaredGrant covers the key cairn writes the grant
// into being declared under spec.settings as well.
//
// Declaring it by hand was the only way to grant a directory before
// spec.access existed, so a profile still doing it must not have its list
// quietly replaced by cairn's — a silent removal of access is the failure the
// neutral declaration exists to prevent. The three shapes are the ways one
// document can occupy the ground the fragment lands on: the same key, a
// non-object where the fragment needs an object, and a null.
func TestSettingsRefuseAHandDeclaredGrant(t *testing.T) {
	for _, document := range []string{
		`{"permissions": {"additionalDirectories": ["/operator/declared"]}}`,
		`{"permissions": "nope"}`,
		`{"permissions": null}`,
	} {
		inst := testInstance(t, profile.Resolved{
			ID:   "reviewer",
			Spec: testSpec(t, `{"settings": {"claude": `+document+`}}`),
		})
		inst.Scope = t.TempDir()

		_, err := RenderSettings(inst)
		if !errors.Is(err, ErrGrantConflict) {
			t.Errorf("RenderSettings() with %s = %v, want %v", document, err, ErrGrantConflict)
			continue
		}
		// The refusal has to be actionable: the key it found, and the key to
		// move it to.
		if !strings.Contains(err.Error(), "permissions") ||
			!strings.Contains(err.Error(), accessDirectoriesKey) {
			t.Errorf("the error for %s is %v, and does not name both the key found and the key to use",
				document, err)
		}
	}
}

// TestSettingsAllowASiblingOfTheGrantedKey is the other side of that refusal.
// A key beside the one cairn writes into is not a conflict — it composes, and
// refusing it would make the grant unusable beside any permissions the
// operator declared.
func TestSettingsAllowASiblingOfTheGrantedKey(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID: "reviewer",
		Spec: testSpec(t, `{"settings": {"claude": {"apiKeyHelper": "/bin/helper",
			"permissions": {"defaultMode": "auto", "allow": ["Bash(git status:*)"]}}}}`),
	})
	// Canonical, the way the composition root supplies it: package scope
	// resolves the scope before it ever reaches an instance.
	inst.Scope = resolved(t, t.TempDir())

	files, err := RenderSettings(inst)
	if err != nil {
		t.Fatalf("RenderSettings(): %v", err)
	}
	document := decodeSettings(t, files[0].Content)
	if document.APIKeyHelper != "/bin/helper" || document.Permissions.DefaultMode != "auto" {
		t.Errorf("the operator's own keys did not survive the grant: %s", files[0].Content)
	}
	if want := []string{inst.Scope}; !slices.Equal(document.Permissions.AdditionalDirectories, want) {
		t.Errorf("permissions.additionalDirectories = %v, want %v",
			document.Permissions.AdditionalDirectories, want)
	}
}

// TestSettingsAreWrittenForAGrantAlone covers a profile that declares
// directories and no settings document. Rendering nothing would make the grant
// conditional on an unrelated key, so an access declaration would reach a boot
// directory for some profiles and silently vanish for others.
func TestSettingsAreWrittenForAGrantAlone(t *testing.T) {
	shared := resolved(t, t.TempDir())
	inst := testInstance(t, profile.Resolved{
		ID:   "quiet",
		Spec: testSpec(t, `{"access": {"directories": [`+strconv.Quote(shared)+`]}}`),
	})

	files, err := RenderSettings(inst)
	if err != nil {
		t.Fatalf("RenderSettings(): %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("RenderSettings() wrote %v, want one file", filePaths(files))
	}
	want := "{\n  \"permissions\": {\n    \"additionalDirectories\": [\n      " +
		strconv.Quote(shared) + "\n    ]\n  }\n}\n"
	if string(files[0].Content) != want {
		t.Errorf("the settings document is %q, want %q", files[0].Content, want)
	}
}

// TestGrantedFragmentCarriesNothingButTheDirectories is the guard behind the
// whole approach. Cairn never names permissions.additionalDirectories: it
// builds an adapter carrying the directories and nothing else, and merges
// whatever document that adapter returns.
//
// That is safe only for as long as an adapter given one input answers with one
// key. If the library ever starts emitting a default mode, or any other key,
// from an adapter cairn never gave a value for, that key would land in the
// operator's settings document over whatever they declared there. This fails
// when it does, here, rather than in a file nobody diffs.
func TestGrantedFragmentCarriesNothingButTheDirectories(t *testing.T) {
	inst := testInstance(t, profile.Resolved{ID: "reviewer"})
	inst.Scope = t.TempDir()

	fragment, err := accessFragment(inst)
	if err != nil {
		t.Fatalf("accessFragment(): %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(fragment, &top); err != nil {
		t.Fatalf("the fragment does not decode: %v", err)
	}
	if len(top) != 1 {
		t.Fatalf("the fragment holds %d keys, want the one the directories map onto: %s", len(top), fragment)
	}
	var permissions map[string]json.RawMessage
	if err := json.Unmarshal(top["permissions"], &permissions); err != nil {
		t.Fatalf("the fragment's one key is not the permissions object: %s", fragment)
	}
	if len(permissions) != 1 {
		t.Errorf("the fragment's permissions object holds %d keys, want only the directories: %s",
			len(permissions), fragment)
	}
}

// TestSettingsRefuseToGrantThroughADocumentThatIsNotOne covers a settings
// document that is valid JSON and is not an object — a list, a string, a
// number. Nothing can be added to one, and the render says so rather than
// dropping the grant or replacing what the operator wrote.
func TestSettingsRefuseToGrantThroughADocumentThatIsNotOne(t *testing.T) {
	for _, document := range []string{`[]`, `"a settings document that is a bare string"`, `17`} {
		inst := testInstance(t, profile.Resolved{
			ID:   "reviewer",
			Spec: testSpec(t, `{"settings": {"claude": `+document+`}}`),
		})
		inst.Scope = t.TempDir()

		if _, err := RenderSettings(inst); !errors.Is(err, profile.ErrSpecMerge) {
			t.Errorf("RenderSettings() with the document %s = %v, want %v",
				document, err, profile.ErrSpecMerge)
		}
	}
}

// TestSettingsReportADirectoryThatCannotBeExpanded covers a declared path
// needing a home there is none of. The render fails naming what the operator
// wrote, because a grant that quietly resolved to something else would be a
// directory handed to an agent by accident.
func TestSettingsReportADirectoryThatCannotBeExpanded(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID:   "reviewer",
		Spec: testSpec(t, `{"access": {"directories": ["~/dev/nanite"]}}`),
	})

	_, err := RenderSettings(inst)
	if !errors.Is(err, profile.ErrNoHomeForPath) {
		t.Fatalf("RenderSettings() = %v, want %v", err, profile.ErrNoHomeForPath)
	}
	if !strings.Contains(err.Error(), "~/dev/nanite") {
		t.Errorf("the error is %v, and does not quote the path the operator wrote", err)
	}
}

// mkdirAll creates a fixture directory a test is about to grant.
func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the fixture directory %s: %v", dir, err)
	}
}

// resolved returns dir as the renderer will spell it: symlinks resolved, the
// way package scope canonicalizes the scope. On macOS a t.TempDir() lives
// under a symlinked /var, so a test comparing raw fixture paths against a
// rendering would fail on the spelling rather than on the behaviour.
func resolved(t *testing.T, dir string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve the fixture directory %s: %v", dir, err)
	}
	return out
}

// settingsDocument is the shape a test reads a rendered document back through:
// the keys the operator declares, and the one cairn contributes.
type settingsDocument struct {
	APIKeyHelper string `json:"apiKeyHelper"`
	Model        string `json:"model"`
	Permissions  struct {
		DefaultMode           string   `json:"defaultMode"`
		AdditionalDirectories []string `json:"additionalDirectories"`
	} `json:"permissions"`
}

// decodeSettings reads a rendered settings document, reporting one that does
// not decode rather than hiding it behind a zero value.
func decodeSettings(t *testing.T, content []byte) settingsDocument {
	t.Helper()
	var document settingsDocument
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("the rendered settings document does not decode: %v\n%s", err, content)
	}
	return document
}

// TestSettingsSelectTheTargetsDocument is what keying by provider buys, at the
// renderer: one profile carries a document for every harness and the boot
// directory gets exactly the one it is being written for.
//
// The target is read off the layout rather than off the profile, which is the
// whole of what makes --provider a selector. A profile's own `provider:` says
// what it was written against; the layout says what is being written now.
func TestSettingsSelectTheTargetsDocument(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID: "reviewer",
		// Deliberately declared for a harness this render is not for, as well
		// as for the one it is. A document reaching the wrong harness's file
		// is the failure this key was re-shaped to make impossible.
		Spec: testSpec(t, `{"settings": {
			"claude": {"model": "opus"},
			"codex":  {"model": "a model claude has never heard of"}
		}}`),
	})

	files, err := RenderSettings(inst)
	if err != nil {
		t.Fatalf("RenderSettings(): %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("RenderSettings() wrote %v, want one file", filePaths(files))
	}
	if want := "{\n  \"model\": \"opus\"\n}\n"; string(files[0].Content) != want {
		t.Errorf("the settings document is %q, want %q", files[0].Content, want)
	}
}

// TestSettingsRefuseADocumentThatNamesNoProvider covers the authoring format
// change at the renderer, which is where an operator meets it.
//
// A flat document is refused rather than written, and rather than silently
// selecting nothing: accepting it would plant a boot directory with none of
// the settings the profile plainly declares, and cairn would have read the
// document, understood it, and dropped it without a word. The instance carries
// a scope, so the render has a grant to write and cannot be excused by having
// nothing to do.
func TestSettingsRefuseADocumentThatNamesNoProvider(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID:   "reviewer",
		Spec: testSpec(t, `{"settings": {"permissions": {"defaultMode": "auto"}}}`),
	})
	inst.Scope = resolved(t, t.TempDir())

	files, err := RenderSettings(inst)
	if !errors.Is(err, profile.ErrSettingsProvider) {
		t.Fatalf("RenderSettings() = %v, %v; want profile.ErrSettingsProvider", filePaths(files), err)
	}
	if !strings.Contains(err.Error(), "permissions") {
		t.Errorf("the error is %v, and does not name the member it found", err)
	}
}

func TestCodexConfigRendersSettingsAccessAndMCP(t *testing.T) {
	shared := resolved(t, t.TempDir())
	inst := testInstance(t, profile.Resolved{
		ID: "reviewer",
		Spec: testSpec(t, `{
			"settings": {"codex": {"model": "gpt-5"}},
			"access": {"directories": [`+strconv.Quote(shared)+`]},
			"mcp": [{"name":"notes","command":"notes-mcp","args":["serve"],"env":{"TOKEN":"synthetic"}}]
		}`),
	})
	layout, err := LayoutFor(profile.ProviderCodex)
	if err != nil {
		t.Fatalf("LayoutFor(%q): %v", profile.ProviderCodex, err)
	}
	inst.Layout = layout

	files, err := RenderCodexConfig(inst)
	if err != nil {
		t.Fatalf("RenderCodexConfig: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("RenderCodexConfig wrote %v, want one file", filePaths(files))
	}
	if files[0].Path != "config.toml" {
		t.Errorf("path = %q, want config.toml", files[0].Path)
	}
	content := string(files[0].Content)
	for _, want := range []string{
		`model = 'gpt-5'`,
		`[sandbox_workspace_write]`,
		`writable_roots = ['` + shared + `']`,
		`[mcp_servers.notes]`,
		`command = 'notes-mcp'`,
		`[mcp_servers.notes.env]`,
		`TOKEN = 'synthetic'`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config.toml missing %q:\n%s", want, content)
		}
	}
}

func TestCodexConfigRefusesGeneratedKeyCollisions(t *testing.T) {
	for _, document := range []string{
		`{"sandbox_workspace_write": {"writable_roots": ["/operator"]}}`,
		`{"mcp_servers": {"notes": {"command": "operator-notes"}}}`,
	} {
		inst := testInstance(t, profile.Resolved{
			ID: "reviewer",
			Spec: testSpec(t, `{
				"settings": {"codex": `+document+`},
				"access": {"directories": [`+strconv.Quote(resolved(t, t.TempDir()))+`]},
				"mcp": [{"name":"notes","command":"notes-mcp"}]
			}`),
		})
		layout, err := LayoutFor(profile.ProviderCodex)
		if err != nil {
			t.Fatalf("LayoutFor(%q): %v", profile.ProviderCodex, err)
		}
		inst.Layout = layout

		_, err = RenderCodexConfig(inst)
		if !errors.Is(err, ErrConfigConflict) {
			t.Errorf("RenderCodexConfig with %s = %v, want ErrConfigConflict", document, err)
		}
	}
}
