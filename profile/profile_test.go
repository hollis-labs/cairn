package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/harness/core/agentcontext"
)

func TestProviderValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider Provider
		want     bool
	}{
		{ProviderClaude, true},
		{ProviderCodex, true},
		{ProviderOpenCode, true},
		{Provider(""), false},
		{Provider("Claude"), false},
		{Provider("gemini"), false},
	}

	for _, tc := range tests {
		t.Run(tc.provider.String(), func(t *testing.T) {
			t.Parallel()

			if got := tc.provider.Valid(); got != tc.want {
				t.Errorf("Provider(%q).Valid() = %v, want %v", tc.provider, got, tc.want)
			}
		})
	}
}

func TestProviderString(t *testing.T) {
	t.Parallel()

	if got := ProviderClaude.String(); got != "claude" {
		t.Errorf("ProviderClaude.String() = %q, want claude", got)
	}
	if got := Provider("").String(); got != "" {
		t.Errorf("Provider(\"\").String() = %q, want the empty string", got)
	}
}

func TestSpecSlots(t *testing.T) {
	t.Parallel()

	s := spec(t, map[string]string{SpecKeySlots: `[
		{"name":"role","section":"## Role","required":true,
		 "source":{"kind":"inline","inline":{"content":"you are"}}},
		{"name":"notes","source":{"kind":"static_file","static_file":{"path":"notes.md"}}}
	]`})

	slots, err := s.Slots()
	if err != nil {
		t.Fatalf("Slots() = error %v", err)
	}
	if len(slots) != 2 {
		t.Fatalf("Slots() returned %d slots, want 2", len(slots))
	}
	if slots[0].Name != "role" || slots[0].Section != "## Role" || !slots[0].Required {
		t.Errorf("slot 0 = %+v", slots[0])
	}
	if got := string(slots[0].Source.Kind); got != "inline" {
		t.Errorf("slot 0 kind = %q, want inline", got)
	}
	if got := slots[0].Source.Inline.Content; got != "you are" {
		t.Errorf("slot 0 inline content = %q, want %q", got, "you are")
	}
	if slots[1].Name != "notes" {
		t.Errorf("slot 1 name = %q, want notes", slots[1].Name)
	}
}

func TestSpecMCP(t *testing.T) {
	t.Parallel()

	s := spec(t, map[string]string{SpecKeyMCP: `[
		{"name":"fs","command":"mcp-fs","args":["--root","/tmp"],"env":{"TOKEN":"x"}}
	]`})

	servers, err := s.MCP()
	if err != nil {
		t.Fatalf("MCP() = error %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("MCP() returned %d servers, want 1", len(servers))
	}
	got := servers[0]
	if got.Name != "fs" || got.Command != "mcp-fs" {
		t.Errorf("server = %+v", got)
	}
	if !slices.Equal(got.Args, []string{"--root", "/tmp"}) {
		t.Errorf("server args = %v", got.Args)
	}
	if got.Env["TOKEN"] != "x" {
		t.Errorf("server env = %v", got.Env)
	}
}

func TestSpecSkillsAndSkillsDir(t *testing.T) {
	t.Parallel()

	s := spec(t, map[string]string{
		SpecKeySkills:    `["code-review","capture-decision"]`,
		SpecKeySkillsDir: `"/srv/skills"`,
	})

	skills, err := s.Skills()
	if err != nil {
		t.Fatalf("Skills() = error %v", err)
	}
	if !slices.Equal(skills, []string{"code-review", "capture-decision"}) {
		t.Errorf("Skills() = %v", skills)
	}

	dir, err := s.SkillsDir()
	if err != nil {
		t.Fatalf("SkillsDir() = error %v", err)
	}
	if dir != "/srv/skills" {
		t.Errorf("SkillsDir() = %q, want /srv/skills", dir)
	}
}

// TestSpecInstallSkills covers the key the installed layer reads, and the
// three shapes it has to survive: declared, declared without skills, and a
// manifest with no install key at all.
//
// The last two are the same answer for a reason. The key is a namespace for
// install-only declarations, so a profile that declares one of them and not
// skills has declared no installed skills — not an error, and not the boot
// directory's set either.
func TestSpecInstallSkills(t *testing.T) {
	t.Parallel()

	s := spec(t, map[string]string{
		SpecKeySkills:  `["boot-only"]`,
		SpecKeyInstall: `{"skills":["commit","push"]}`,
	})
	installed, err := s.InstallSkills()
	if err != nil {
		t.Fatalf("InstallSkills() = error %v", err)
	}
	if !slices.Equal(installed, []string{"commit", "push"}) {
		t.Errorf("InstallSkills() = %v", installed)
	}
	// The two sets are separate declarations and neither reads the other.
	booted, err := s.Skills()
	if err != nil || !slices.Equal(booted, []string{"boot-only"}) {
		t.Errorf("Skills() = %v, %v; want [boot-only], nil", booted, err)
	}

	for name, manifest := range map[string]Spec{
		"install without skills": spec(t, map[string]string{SpecKeyInstall: `{"other":1}`}),
		"an empty install":       spec(t, map[string]string{SpecKeyInstall: `{}`}),
		"no install key":         spec(t, map[string]string{SpecKeySkills: `["boot-only"]`}),
	} {
		got, err := manifest.InstallSkills()
		if err != nil || got != nil {
			t.Errorf("InstallSkills() with %s = %v, %v; want nil, nil", name, got, err)
		}
	}
}

// TestSpecAccessDirectories covers the access key the settings renderer reads,
// and the same three shapes the install key has to survive: declared, declared
// without directories, and no access key at all.
//
// The paths come back exactly as the manifest wrote them. Expanding here would
// mean a declared profile answering a question it has no home directory and no
// environment to answer, and it would cost the caller the declared form a
// diagnostic quotes.
func TestSpecAccessDirectories(t *testing.T) {
	t.Parallel()

	s := spec(t, map[string]string{
		SpecKeyAccess: `{"directories":["~/dev/nanite","$WORK/docs"]}`,
	})
	dirs, err := s.AccessDirectories()
	if err != nil {
		t.Fatalf("AccessDirectories() = error %v", err)
	}
	if want := []string{"~/dev/nanite", "$WORK/docs"}; !slices.Equal(dirs, want) {
		t.Errorf("AccessDirectories() = %v, want the manifest's own spelling %v", dirs, want)
	}

	for name, manifest := range map[string]Spec{
		"access without directories": spec(t, map[string]string{SpecKeyAccess: `{"other":1}`}),
		"an empty access":            spec(t, map[string]string{SpecKeyAccess: `{}`}),
		"no access key":              spec(t, map[string]string{SpecKeySkills: `["review"]`}),
	} {
		got, err := manifest.AccessDirectories()
		if err != nil || got != nil {
			t.Errorf("AccessDirectories() with %s = %v, %v; want nil, nil", name, got, err)
		}
	}
}

func TestSpecSettingsAreCarriedVerbatim(t *testing.T) {
	t.Parallel()

	// Odd spacing and key order on purpose: the manifest is what the operator
	// wrote, and nothing reformats it on the way through. Selecting a
	// provider's document out of the wrapper must not either — the sub-value
	// is lifted out as its own bytes, so this arrives at the harness's file
	// with "b" still ahead of "a".
	const raw = `{ "b":  1,
  "a": [2,   3] }`

	s := spec(t, map[string]string{SpecKeySettings: `{"claude": ` + raw + `}`})

	got, ok, err := s.Settings(ProviderClaude)
	if err != nil {
		t.Fatalf("Settings(%q): %v", ProviderClaude, err)
	}
	if !ok {
		t.Fatal("Settings() reported the key as undeclared")
	}
	if string(got) != raw {
		t.Errorf("Settings() = %s, want the stored bytes %s", got, raw)
	}
}

// TestSpecSettingsSelectTheTargetsDocument is the whole of what keying by
// provider buys: one profile carries a document for every harness, and a
// materialization gets exactly the one it is being written for.
//
// A provider the key does not mention has none, which is a distinct answer
// from "the key is undeclared" only to whoever wrote the profile — every
// caller treats them the same, because a target with nothing to write writes
// nothing.
func TestSpecSettingsSelectTheTargetsDocument(t *testing.T) {
	t.Parallel()

	s := spec(t, map[string]string{SpecKeySettings: `{
		"claude": {"permissions": {"defaultMode": "auto"}},
		"codex":  {"approval": "never"},
		"opencode": null
	}`})

	for _, tc := range []struct {
		provider Provider
		want     string
		declared bool
	}{
		{provider: ProviderClaude, want: `{"permissions":{"defaultMode":"auto"}}`, declared: true},
		{provider: ProviderCodex, want: `{"approval":"never"}`, declared: true},
		{provider: ProviderOpenCode},
	} {
		t.Run(tc.provider.String(), func(t *testing.T) {
			t.Parallel()

			got, declared, err := s.Settings(tc.provider)
			if err != nil {
				t.Fatalf("Settings(%q): %v", tc.provider, err)
			}
			if declared != tc.declared {
				t.Fatalf("Settings(%q) declared = %v, want %v (%s)", tc.provider, declared, tc.declared, got)
			}
			if compactJSONForTest(t, got) != compactJSONForTest(t, json.RawMessage(tc.want)) {
				t.Errorf("Settings(%q) = %s, want %s", tc.provider, got, tc.want)
			}
		})
	}
}

// compactJSONForTest removes a document's insignificant whitespace so that two
// spellings of one document compare equal. An absent document compares as the
// empty string rather than being reported, since the caller has already
// checked whether there was one.
func compactJSONForTest(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact %s: %v", raw, err)
	}
	return buf.String()
}

// TestSpecSettingsRefuseADocumentThatNamesNoProvider covers the authoring
// format change, from the side an operator meets it: a profile still carrying
// the flat document is told what to do about it.
//
// The refusal is the point. Accepting the flat form would select nothing for
// every target and render a boot directory with none of the settings the
// profile plainly declares — cairn having read the document, understood it,
// and dropped it without a word.
func TestSpecSettingsRefuseADocumentThatNamesNoProvider(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		manifest string
		names    string
	}{
		{
			name:     "the flat document every profile used to declare",
			manifest: `{"permissions": {"defaultMode": "auto"}, "effortLevel": "xhigh"}`,
			names:    `"effortLevel"`,
		},
		{
			name:     "a misspelled provider beside a real one",
			manifest: `{"claude": {"a": 1}, "cluade": {"b": 2}}`,
			names:    `"cluade"`,
		},
		{
			name:     "not an object at all",
			manifest: `["a settings document that is a list"]`,
			names:    "not an object",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := spec(t, map[string]string{SpecKeySettings: tc.manifest})
			got, declared, err := s.Settings(ProviderClaude)
			if !errors.Is(err, ErrSettingsProvider) {
				t.Fatalf("Settings() = %s, %v, %v; want ErrSettingsProvider", got, declared, err)
			}
			if declared || got != nil {
				t.Errorf("Settings() returned %s, %v alongside its error; want nothing", got, declared)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("the diagnostic %q does not name %s", err, tc.names)
			}
			// The shape to move to, in the YAML the profile was authored in.
			if !strings.Contains(err.Error(), "settings: {claude: {...}}") &&
				!strings.Contains(err.Error(), "not an object") {
				t.Errorf("the diagnostic %q does not say what shape to write", err)
			}
		})
	}
}

func TestSpecSettingsUndeclared(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    Spec
	}{
		{name: "a nil spec", s: nil},
		{name: "an empty spec", s: Spec{}},
		{name: "another key only", s: spec(t, map[string]string{SpecKeySkills: `["a"]`})},
		{name: "the key holding no bytes", s: Spec{SpecKeySettings: json.RawMessage("")}},
		{name: "the key cleared with null", s: spec(t, map[string]string{SpecKeySettings: `null`})},
		{
			name: "another provider only",
			s:    spec(t, map[string]string{SpecKeySettings: `{"codex":{"approval":"never"}}`}),
		},
		{
			name: "this provider cleared with null",
			s:    spec(t, map[string]string{SpecKeySettings: `{"claude":null}`}),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok, err := tc.s.Settings(ProviderClaude)
			if err != nil {
				t.Fatalf("Settings(%q): %v", ProviderClaude, err)
			}
			if ok {
				t.Errorf("Settings() reported the key declared, returning %s", got)
			}
			if got != nil {
				t.Errorf("Settings() = %s, want nil", got)
			}
		})
	}
}

// TestSpecFiles covers both forms a files value may take. A string is content
// the profile already knows and is planted verbatim; an object is a slot
// source, resolved at materialization by whatever resolves slots. This
// accessor decodes them and does not resolve anything.
func TestSpecFiles(t *testing.T) {
	t.Parallel()

	s := spec(t, map[string]string{SpecKeyFiles: `{
		"docs/notes.md":     "hello",
		"x.txt":             "",
		"tasks/T-1/task.md": {"kind":"cmd","cmd":{"run":"torque task get T-1 --format md"}}
	}`})

	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() = error %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("Files() returned %d entries, want 3: %+v", len(files), files)
	}

	// An empty literal is a literal. A profile that declares a path with no
	// content means a file with no content, not an entry that went missing.
	for path, want := range map[string]string{"docs/notes.md": "hello", "x.txt": ""} {
		entry := files[path]
		if entry.IsSource() {
			t.Errorf("%s decoded as a source, want a literal", path)
			continue
		}
		if entry.Literal != want {
			t.Errorf("%s holds %q, want %q", path, entry.Literal, want)
		}
	}

	task := files["tasks/T-1/task.md"]
	if !task.IsSource() {
		t.Fatalf("tasks/T-1/task.md decoded as the literal %q, want a source", task.Literal)
	}
	if task.Literal != "" {
		t.Errorf("a source entry also carries the literal %q; exactly one field is set", task.Literal)
	}
	if got := task.Source.Kind; got != agentcontext.SlotSourceKindCmd {
		t.Errorf("the source kind is %q, want %q", got, agentcontext.SlotSourceKindCmd)
	}
	if want := "torque task get T-1 --format md"; task.Source.Cmd.Run != want {
		t.Errorf("the source command is %q, want %q", task.Source.Cmd.Run, want)
	}
}

// TestSpecFilesRefusesAValueThatIsNeitherFormByName covers a files value that
// is a number, a list, a boolean or a null.
//
// Refusing rather than coercing is the point. The two legal shapes are easy to
// describe, and a silent coercion would plant bytes nobody wrote at a path a
// profile promised — which is the one place in the boot directory where cairn
// has committed to a path without knowing what goes in it.
func TestSpecFilesRefusesAValueThatIsNeitherFormByName(t *testing.T) {
	t.Parallel()

	for _, value := range []string{`42`, `["a","b"]`, `true`, `null`} {
		s := spec(t, map[string]string{SpecKeyFiles: `{"notes/wrong.md": ` + value + `}`})

		_, err := s.Files()
		if err == nil {
			t.Fatalf("Files() accepted the value %s", value)
		}
		if !strings.Contains(err.Error(), "notes/wrong.md") {
			t.Errorf("the error for %s does not name the path: %v", value, err)
		}
		if !strings.Contains(err.Error(), SpecKeyFiles) {
			t.Errorf("the error for %s does not name the key: %v", value, err)
		}
	}
}

// TestSpecFilesLeavesAnUnknownKindToWhateverResolvesIt states where the line
// is. A source object whose kind is missing or wrong is still an object, so it
// decodes here; it fails where it is resolved, which is the package that knows
// which kinds are wired.
func TestSpecFilesLeavesAnUnknownKindToWhateverResolvesIt(t *testing.T) {
	t.Parallel()

	s := spec(t, map[string]string{SpecKeyFiles: `{"a.md":{"type":"inline","inline":{"content":"x"}}}`})

	files, err := s.Files()
	if err != nil {
		t.Fatalf("Files() = error %v, want the source carried through", err)
	}
	if entry := files["a.md"]; !entry.IsSource() || entry.Source.Kind != "" {
		t.Errorf("a.md decoded to %+v, want a source with no kind", entry)
	}
}

// TestSpecSubagents covers the parent's half of the feature: a list of the
// profile ids whose definitions the boot directory carries.
func TestSpecSubagents(t *testing.T) {
	t.Parallel()

	s := spec(t, map[string]string{SpecKeySubagents: `["reviewer", "worker"]`})
	got, err := s.Subagents()
	if err != nil {
		t.Fatalf("Subagents() = error %v", err)
	}
	if len(got) != 2 || got[0] != "reviewer" || got[1] != "worker" {
		t.Errorf("Subagents() = %v, want the declared ids in order", got)
	}
}

// TestSpecSubagentIsCarriedVerbatim covers the child's half. The declaration
// is opaque here for the same reason the settings document is: what it holds
// belongs to the harness that reads it, and this package hands over the bytes.
func TestSpecSubagentIsCarriedVerbatim(t *testing.T) {
	t.Parallel()

	const raw = `{"description":"Fresh review.","tools":["Read"],"aKeyNothingKnows":1}`
	got, ok := spec(t, map[string]string{SpecKeySubagent: raw}).Subagent()
	if !ok {
		t.Fatal("Subagent() reported the key as undeclared")
	}
	if string(got) != raw {
		t.Errorf("Subagent() = %s, want the stored bytes %s", got, raw)
	}
}

// TestSpecSubagentUndeclared covers every way the key can be absent, including
// the explicit null a descendant clears an ancestor's declaration with.
func TestSpecSubagentUndeclared(t *testing.T) {
	t.Parallel()

	for name, s := range map[string]Spec{
		"a nil spec":               nil,
		"an empty spec":            {},
		"another key only":         spec(t, map[string]string{SpecKeySkills: `["a"]`}),
		"the key holding no bytes": {SpecKeySubagent: json.RawMessage("")},
		"the key set to null":      spec(t, map[string]string{SpecKeySubagent: `null`}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := s.Subagent()
			if ok || got != nil {
				t.Errorf("Subagent() = %s, %v; want nil, false", got, ok)
			}
		})
	}
}

func TestSpecAccessorsOnAnEmptyManifest(t *testing.T) {
	t.Parallel()

	// A manifest is not obliged to declare anything, and a profile that never
	// had one at all is the same case.
	for _, s := range []Spec{nil, {}, spec(t, map[string]string{
		SpecKeySlots:     `null`,
		SpecKeyMCP:       `null`,
		SpecKeySkills:    `null`,
		SpecKeyInstall:   `null`,
		SpecKeyFiles:     `null`,
		SpecKeySubagents: `null`,
	})} {
		slots, err := s.Slots()
		if err != nil || slots != nil {
			t.Errorf("Slots() = %v, %v; want nil, nil", slots, err)
		}
		servers, err := s.MCP()
		if err != nil || servers != nil {
			t.Errorf("MCP() = %v, %v; want nil, nil", servers, err)
		}
		skills, err := s.Skills()
		if err != nil || skills != nil {
			t.Errorf("Skills() = %v, %v; want nil, nil", skills, err)
		}
		installed, err := s.InstallSkills()
		if err != nil || installed != nil {
			t.Errorf("InstallSkills() = %v, %v; want nil, nil", installed, err)
		}
		granted, err := s.AccessDirectories()
		if err != nil || granted != nil {
			t.Errorf("AccessDirectories() = %v, %v; want nil, nil", granted, err)
		}
		dir, err := s.SkillsDir()
		if err != nil || dir != "" {
			t.Errorf("SkillsDir() = %q, %v; want \"\", nil", dir, err)
		}
		files, err := s.Files()
		if err != nil || files != nil {
			t.Errorf("Files() = %v, %v; want nil, nil", files, err)
		}
		named, err := s.Subagents()
		if err != nil || named != nil {
			t.Errorf("Subagents() = %v, %v; want nil, nil", named, err)
		}
	}
}

func TestSpecMalformedValueNamesItsKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
		call  func(Spec) error
	}{
		{
			name:  "slots is not a list",
			key:   SpecKeySlots,
			value: `{"name":"role"}`,
			call:  func(s Spec) error { _, err := s.Slots(); return err },
		},
		{
			name:  "mcp is not a list",
			key:   SpecKeyMCP,
			value: `{"name":"fs"}`,
			call:  func(s Spec) error { _, err := s.MCP(); return err },
		},
		{
			name:  "skills holds numbers",
			key:   SpecKeySkills,
			value: `[1,2]`,
			call:  func(s Spec) error { _, err := s.Skills(); return err },
		},
		{
			name:  "install is not an object",
			key:   SpecKeyInstall,
			value: `["commit"]`,
			call:  func(s Spec) error { _, err := s.InstallSkills(); return err },
		},
		{
			name:  "access is not an object",
			key:   SpecKeyAccess,
			value: `["~/dev"]`,
			call:  func(s Spec) error { _, err := s.AccessDirectories(); return err },
		},
		{
			name:  "skills_dir is not a string",
			key:   SpecKeySkillsDir,
			value: `["/srv/skills"]`,
			call:  func(s Spec) error { _, err := s.SkillsDir(); return err },
		},
		{
			name:  "files is not a map at all",
			key:   SpecKeyFiles,
			value: `["notes.md"]`,
			call:  func(s Spec) error { _, err := s.Files(); return err },
		},
		{
			name:  "subagents is not a list",
			key:   SpecKeySubagents,
			value: `{"reviewer":true}`,
			call:  func(s Spec) error { _, err := s.Subagents(); return err },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.call(spec(t, map[string]string{tc.key: tc.value}))
			if err == nil {
				t.Fatalf("decoding %s from %s = no error", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error = %q, want it to name the key %q", err, tc.key)
			}
		})
	}
}
