package bootdir

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/chrispian/cairn/profile"
	"gopkg.in/yaml.v3"
)

// subagentInstance returns an instance whose profile names the given
// definitions, each carrying the declaration written inline.
func subagentInstance(t *testing.T, declarations ...string) *Instance {
	t.Helper()
	ids := make([]string, 0, len(declarations))
	subs := make([]Subagent, 0, len(declarations))
	for i, declaration := range declarations {
		id := []string{"reviewer", "worker", "scribe"}[i]
		ids = append(ids, id)
		subs = append(subs, Subagent{ID: id, Declaration: json.RawMessage(declaration)})
	}
	named, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("encode the named ids: %v", err)
	}
	inst := testInstance(t, profile.Resolved{
		ID:   "engineer",
		Spec: testSpec(t, `{"`+profile.SpecKeySubagents+`": `+string(named)+`}`),
	})
	inst.Subagents = subs
	return inst
}

// frontmatterOf splits a rendered definition into its frontmatter and the
// markdown below it, failing if the file is not fenced the way a harness
// expects.
func frontmatterOf(t *testing.T, content []byte) (map[string]any, string) {
	t.Helper()
	text := string(content)
	if !strings.HasPrefix(text, "---\n") {
		t.Fatalf("the definition does not open with a frontmatter fence:\n%s", text)
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		t.Fatalf("the definition does not close its frontmatter fence:\n%s", text)
	}
	var front map[string]any
	if err := yaml.Unmarshal([]byte(rest[:end+1]), &front); err != nil {
		t.Fatalf("the frontmatter is not YAML: %v\n%s", err, text)
	}
	return front, strings.TrimPrefix(rest[end+len("\n---\n"):], "\n")
}

// TestSubagentsRenderWhatTheNamedProfileDeclared is the shape of the feature:
// one file per named id, at the path the harness reads definitions from, whose
// frontmatter is the named profile's own declaration and whose body is that
// declaration's body key.
func TestSubagentsRenderWhatTheNamedProfileDeclared(t *testing.T) {
	inst := subagentInstance(t, `{
		"description": "Fresh review with no shared context.",
		"tools": ["Read", "Grep", "Glob"],
		"model": "sonnet",
		"body": "You review a diff and report what you found.\n"
	}`)

	files, err := renderSubagents(inst)
	if err != nil {
		t.Fatalf("renderSubagents(): %v", err)
	}
	want := []string{".claude/agents/reviewer.md"}
	if got := filePaths(files); !slices.Equal(got, want) {
		t.Fatalf("rendered %v, want %v", got, want)
	}

	front, body := frontmatterOf(t, files[0].Content)
	if got := front["description"]; got != "Fresh review with no shared context." {
		t.Errorf("description = %#v, want the declared one", got)
	}
	if got := front["tools"]; !slices.Equal(anyStrings(t, got), []string{"Read", "Grep", "Glob"}) {
		t.Errorf("tools = %#v, want the declared list", got)
	}
	if got := front["model"]; got != "sonnet" {
		t.Errorf("model = %#v, want %q", got, "sonnet")
	}
	if body != "You review a diff and report what you found.\n" {
		t.Errorf("the body is %q, want the declared body", body)
	}
	// The body is not frontmatter. It is the one key lifted out, because the
	// harness reads the prompt from below the fence.
	if _, leaked := front[SubagentBodyKey]; leaked {
		t.Errorf("the body was written into the frontmatter as well:\n%s", files[0].Content)
	}
}

// TestSubagentNameIsForcedToTheProfileID covers the one field cairn writes
// rather than transcribes.
//
// The harness resolves a definition by its name field, not by its filename, and
// a definition with no name at all is dropped without a diagnostic — verified
// against Claude Code 2.1.246, whose loader returns null before it logs
// anything when the frontmatter carries no name. Forcing the id is what keeps
// the file a profile named and the name a harness resolves the same string.
func TestSubagentNameIsForcedToTheProfileID(t *testing.T) {
	inst := subagentInstance(t, `{"description": "declares no name at all"}`)

	files, err := renderSubagents(inst)
	if err != nil {
		t.Fatalf("renderSubagents(): %v", err)
	}
	front, _ := frontmatterOf(t, files[0].Content)
	if got := front[SubagentNameKey]; got != "reviewer" {
		t.Errorf("name = %#v, want the profile id", got)
	}
	// First, so that the file reads as one: the name is what everything else
	// in it is about.
	if !strings.HasPrefix(string(files[0].Content), "---\nname: reviewer\n") {
		t.Errorf("the definition does not open with its name:\n%s", files[0].Content)
	}
}

// TestSubagentsRefuseANameThatIsNotTheProfileID states why the forcing is a
// refusal rather than an overwrite. A declaration naming something else is
// something the operator wrote, and rendering the id over it would discard it
// silently; a definition planted under a name nothing named is a file the
// harness loads and no profile can reach.
func TestSubagentsRefuseANameThatIsNotTheProfileID(t *testing.T) {
	inst := subagentInstance(t, `{"name": "code-reviewer", "description": "d"}`)

	_, err := renderSubagents(inst)
	if !errors.Is(err, ErrSubagentDeclaration) {
		t.Fatalf("renderSubagents() = %v, want ErrSubagentDeclaration", err)
	}
	for _, want := range []string{"reviewer", "code-reviewer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
	// The same name written out is not a disagreement, and is not refused.
	same := subagentInstance(t, `{"name": "reviewer", "description": "d"}`)
	files, err := renderSubagents(same)
	if err != nil {
		t.Fatalf("renderSubagents() with a name that agrees: %v", err)
	}
	if got := strings.Count(string(files[0].Content), "name:"); got != 1 {
		t.Errorf("the name is written %d times, want once:\n%s", got, files[0].Content)
	}
}

// TestSubagentsCarryAKeyCairnHasNeverHeardOf is the opaque-map rule. Cairn has
// no tool surface, no permission model and no depth; a declaration key it does
// not know reaches the harness exactly like one it does, because it reads none
// of them.
func TestSubagentsCarryAKeyCairnHasNeverHeardOf(t *testing.T) {
	inst := subagentInstance(t, `{
		"description": "d",
		"permissionMode": "plan",
		"maxTurns": 6,
		"disable-model-invocation": true,
		"mcpServers": [{"name": "notes", "command": "notes-server"}],
		"somethingInventedTomorrow": {"nested": ["a", 1, true, null]}
	}`)

	files, err := renderSubagents(inst)
	if err != nil {
		t.Fatalf("renderSubagents(): %v", err)
	}
	front, _ := frontmatterOf(t, files[0].Content)
	if got := front["permissionMode"]; got != "plan" {
		t.Errorf("permissionMode = %#v, want %q", got, "plan")
	}
	if got := front["maxTurns"]; got != 6 {
		t.Errorf("maxTurns = %#v, want the integer 6", got)
	}
	if got := front["disable-model-invocation"]; got != true {
		t.Errorf("disable-model-invocation = %#v, want the boolean true", got)
	}
	if _, ok := front["mcpServers"].([]any); !ok {
		t.Errorf("mcpServers = %#v, want a list", front["mcpServers"])
	}
	if _, ok := front["somethingInventedTomorrow"].(map[string]any); !ok {
		t.Errorf("somethingInventedTomorrow = %#v, want a mapping", front["somethingInventedTomorrow"])
	}
}

// TestSubagentsPreserveTypesAcrossTheTranscription is the half of "verbatim"
// that a JSON-to-YAML transcription can get wrong. A string that reads as a
// number or a boolean has to arrive as the string it was declared as, and a
// number has to arrive as a number.
func TestSubagentsPreserveTypesAcrossTheTranscription(t *testing.T) {
	inst := subagentInstance(t, `{
		"description": "d",
		"model": "3",
		"flag": "true",
		"count": 6,
		"ratio": 1.5,
		"nothing": null,
		"multiline": "one\ntwo\n",
		"colonised": "a: b",
		"empty": ""
	}`)

	files, err := renderSubagents(inst)
	if err != nil {
		t.Fatalf("renderSubagents(): %v", err)
	}
	front, _ := frontmatterOf(t, files[0].Content)
	for key, want := range map[string]any{
		"model":     "3",
		"flag":      "true",
		"count":     6,
		"ratio":     1.5,
		"nothing":   nil,
		"multiline": "one\ntwo\n",
		"colonised": "a: b",
		"empty":     "",
	} {
		if got := front[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

// TestSubagentsKeepTheDeclaredKeyOrder states that the file is the operator's
// document. Their key order is information they wrote, and sorting it would
// rewrite the file for nothing: the stored bytes are fixed, so walking them is
// already deterministic.
func TestSubagentsKeepTheDeclaredKeyOrder(t *testing.T) {
	inst := subagentInstance(t, `{"zebra": "z", "description": "d", "alpha": "a"}`)

	files, err := renderSubagents(inst)
	if err != nil {
		t.Fatalf("renderSubagents(): %v", err)
	}
	want := []string{"name:", "zebra:", "description:", "alpha:"}
	var got []string
	for _, line := range strings.Split(string(files[0].Content), "\n") {
		if key, _, found := strings.Cut(line, ":"); found && !strings.HasPrefix(line, " ") {
			got = append(got, key+":")
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("the keys are %v, want %v", got, want)
	}
}

// TestSubagentsAreByteStable states the determinism contract for this
// artifact, which is the one whose input is a map: same declaration, same
// bytes, and the ids in the order the manifest named them.
func TestSubagentsAreByteStable(t *testing.T) {
	declaration := `{"description": "d", "tools": ["Read", "Grep"], "nested": {"b": 1, "a": 2}, "body": "prompt\n"}`
	inst := subagentInstance(t, declaration, declaration, declaration)

	first, err := renderSubagents(inst)
	if err != nil {
		t.Fatalf("renderSubagents(): %v", err)
	}
	want := []string{
		".claude/agents/reviewer.md",
		".claude/agents/worker.md",
		".claude/agents/scribe.md",
	}
	if got := filePaths(first); !slices.Equal(got, want) {
		t.Fatalf("rendered %v, want the declared order %v", got, want)
	}
	for i := range 16 {
		again, err := renderSubagents(inst)
		if err != nil {
			t.Fatalf("renderSubagents() on render %d: %v", i, err)
		}
		for j := range again {
			if again[j].Path != first[j].Path || string(again[j].Content) != string(first[j].Content) {
				t.Fatalf("render %d changed %s", i, first[j].Path)
			}
		}
	}
}

// TestSubagentsRenderNoBodyWhenNoneIsDeclared covers the definition that says
// nothing. What a subagent is told is content, and cairn does not require
// content it would have no way to supply.
func TestSubagentsRenderNoBodyWhenNoneIsDeclared(t *testing.T) {
	inst := subagentInstance(t, `{"description": "d"}`)

	files, err := renderSubagents(inst)
	if err != nil {
		t.Fatalf("renderSubagents(): %v", err)
	}
	if got := string(files[0].Content); got != "---\nname: reviewer\ndescription: d\n---\n" {
		t.Errorf("the definition is %q, want frontmatter and nothing else", got)
	}
}

// TestSubagentsAreAbsentWhenNoneAreNamed covers the ordinary profile. It names
// no subagents, so no definitions directory appears at all — an empty one
// would suggest a definition failed to render.
func TestSubagentsAreAbsentWhenNoneAreNamed(t *testing.T) {
	files, err := renderSubagents(testInstance(t, profile.Resolved{ID: "engineer"}))
	if err != nil {
		t.Fatalf("renderSubagents(): %v", err)
	}
	if len(files) != 0 {
		t.Errorf("a profile naming no subagents rendered %v", filePaths(files))
	}
}

// TestSubagentsRefuseADeclarationTheyCannotRender covers every shape that
// cannot become a definition, each refused by name rather than transcribed
// into something nobody wrote.
func TestSubagentsRefuseADeclarationTheyCannotRender(t *testing.T) {
	for name, declaration := range map[string]string{
		"a list":                      `["Read", "Grep"]`,
		"a string":                    `"a reviewer"`,
		"a number":                    `6`,
		"a repeated key":              `{"description": "one", "description": "two"}`,
		"a body that is not a string": `{"description": "d", "body": ["a", "b"]}`,
		"a name that is not a string": `{"name": 6, "description": "d"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderSubagents(subagentInstance(t, declaration))
			if !errors.Is(err, ErrSubagentDeclaration) {
				t.Fatalf("renderSubagents() = %v, want ErrSubagentDeclaration", err)
			}
			if !strings.Contains(err.Error(), "reviewer") {
				t.Errorf("the refusal %q does not name the profile", err)
			}
		})
	}
}

// TestSubagentsRefuseAnIDThatCannotNameAFile covers the ids that would reach
// outside the definitions directory or arrive at the harness as something
// other than what was named.
func TestSubagentsRefuseAnIDThatCannotNameAFile(t *testing.T) {
	for _, id := range []string{"", ".", "..", "team/reviewer", "code reviewer"} {
		inst := testInstance(t, profile.Resolved{ID: "engineer"})
		inst.Subagents = []Subagent{{ID: id, Declaration: json.RawMessage(`{"description": "d"}`)}}

		_, err := renderSubagents(inst)
		if !errors.Is(err, ErrSubagentID) {
			t.Errorf("renderSubagents() with the id %q = %v, want ErrSubagentID", id, err)
		}
	}
}

// TestSubagentsRefuseAnIDNamedTwice covers the manifest that names one profile
// twice. The definitions directory holds one file per id, so the second is the
// first; rendering it once would quietly accept a manifest that says something
// the operator did not mean.
func TestSubagentsRefuseAnIDNamedTwice(t *testing.T) {
	inst := subagentInstance(t, `{"description": "d"}`, `{"description": "d"}`)
	inst.Subagents[1].ID = inst.Subagents[0].ID

	_, err := renderSubagents(inst)
	if !errors.Is(err, ErrSubagentID) {
		t.Fatalf("renderSubagents() = %v, want ErrSubagentID", err)
	}
	if !strings.Contains(err.Error(), "reviewer") {
		t.Errorf("the refusal %q does not name the id", err)
	}
}

// TestSubagentsAreDroppedAndReportedWithNoDefinitionsDirectory is the
// warn-and-drop rule for this artifact: content the profile declared, and
// nowhere the harness reads it from, is dropped and named rather than refused.
//
// Both collections, one decision. Prompts and subagents were the same refusal
// over two keys, so changing one would have moved the wall rather than removed
// it — see [Undeclared].
func TestSubagentsAreDroppedAndReportedWithNoDefinitionsDirectory(t *testing.T) {
	inst := subagentInstance(t, `{"description": "d"}`)
	inst.Layout.SubagentsDir = ""

	files, err := renderSubagents(inst)
	if err != nil {
		t.Fatalf("renderSubagents() = %v, want the definitions dropped and no error", err)
	}
	if len(files) != 0 {
		t.Errorf("renderSubagents() planted %d file(s) with nowhere to put them", len(files))
	}

	report := Undeclared(inst)
	if len(report) != 1 {
		t.Fatalf("Undeclared() = %v, want one line", report)
	}
	if !strings.Contains(report[0], "reviewer") {
		t.Errorf("the report %q does not name what was declared", report[0])
	}
}

// TestSubagentsHaveNoDepth is docs/plan.md's structural depth cap, asserted
// rather than assumed. The renderer walks the definitions it was handed and
// nothing else: a named profile's own spec.subagents is not read here, and
// there is no path by which one could be.
func TestSubagentsHaveNoDepth(t *testing.T) {
	inst := subagentInstance(t, `{
		"description": "d",
		"`+profile.SpecKeySubagents+`": ["deeper"],
		"body": "prompt\n"
	}`)

	files, err := renderSubagents(inst)
	if err != nil {
		t.Fatalf("renderSubagents(): %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("rendered %v, want only the named definition", filePaths(files))
	}
	// It is carried into the frontmatter like any other key cairn does not
	// know — transcribed, never followed.
	if !strings.Contains(string(files[0].Content), "deeper") {
		t.Errorf("the declaration's own key was dropped:\n%s", files[0].Content)
	}
}

// anyStrings returns a decoded YAML list as strings.
func anyStrings(t *testing.T, value any) []string {
	t.Helper()
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("%#v is not a list", value)
	}
	out := make([]string, 0, len(list))
	for _, element := range list {
		text, ok := element.(string)
		if !ok {
			t.Fatalf("%#v is not a string", element)
		}
		out = append(out, text)
	}
	return out
}
