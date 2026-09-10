package bootdir

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chrispian/cairn/profile"
)

// writePrompt writes one prompt file under root.
func writePrompt(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create %s: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(root, name+PromptFileExt), []byte(content), 0o644); err != nil {
		t.Fatalf("write the prompt %q: %v", name, err)
	}
}

// promptsInstance returns an instance declaring names, read from source.
func promptsInstance(t *testing.T, source string, names ...string) *Instance {
	t.Helper()
	manifest := map[string]any{profile.SpecKeyPrompts: names}
	if source != "" {
		manifest[profile.SpecKeyPromptsDir] = source
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode the prompts manifest: %v", err)
	}
	return testInstance(t, profile.Resolved{ID: "engineer", Spec: testSpec(t, string(encoded))})
}

// The two substituted inputs of the test below, and the marker text that
// stands for each. They are spelled once so that the assertion and the fixture
// cannot drift, and they are deliberately strings that appear nowhere else in
// this package: a test whose expected output could be produced by copying the
// input through is a test that cannot fail for the reason it exists.
const (
	promptSlotMarker  = "<!-- cairn:slot repository -->"
	promptValueMarker = "<!-- cairn:value scope -->"
	promptSection     = "## Repository\nonly-the-slot-supplies-this"
	promptScope       = "/only/the/instance/supplies/this"
)

// TestPromptsArePlantedAsNamespacedCommandsAndSubstituted is the whole feature
// in one assertion: a destination and a substitution.
//
// It goes through [Render] rather than [RenderPrompts] so that the
// registration is under test too — a renderer that works and is not registered
// plants nothing, and every other assertion here would still pass.
//
// The destination half is the reason the boot/ namespace exists: a file at
// .claude/commands/boot/handoff.md is invoked `/boot:handoff`, and the same
// file one directory up is a different command. So the path is asserted
// exactly, and the rendering is checked for the flattened path as well —
// planting to .claude/commands/handoff.md would satisfy "a command was
// planted" and answer "Unknown command" to the thing the operator types.
//
// The substitution half is asserted against text that exists only on the
// instance. The section and the scope below appear nowhere in the prompt file,
// so a render that copied the file through, or that planted the source path
// instead of the bytes, produces output missing both — and the raw marker is
// checked for by name, because a marker cairn failed to act on is text an
// agent would read as an instruction.
func TestPromptsArePlantedAsNamespacedCommandsAndSubstituted(t *testing.T) {
	source := t.TempDir()
	writePrompt(t, source, "handoff",
		"Write the handoff.\n\n"+promptSlotMarker+"\n\nScope: "+promptValueMarker+"\n")
	writePrompt(t, source, "reset-scope", "Re-read the scope.\n")

	inst := promptsInstance(t, source, "handoff", "reset-scope")
	inst.Sections = map[string]string{"repository": promptSection}
	inst.Values = map[string]string{"scope": promptScope}

	files, err := Render(inst)
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}

	want := []string{
		".claude/commands/boot/handoff.md",
		".claude/commands/boot/reset-scope.md",
	}
	if got := filePaths(files); !slices.Equal(got, want) {
		t.Fatalf("rendered %v, want %v", got, want)
	}
	// The flattened path, named rather than inferred from the list above: it
	// is the one wrong destination that looks right.
	for _, f := range files {
		if f.Path == ".claude/commands/handoff.md" {
			t.Error("the prompt was planted outside the boot/ namespace, where /boot:handoff cannot reach it")
		}
	}

	got := string(fileByPath(t, files, ".claude/commands/boot/handoff.md").Content)
	wantText := "Write the handoff.\n\n" + promptSection + "\n\nScope: " + promptScope + "\n"
	if got != wantText {
		t.Errorf("the planted prompt holds:\n%s\nwant:\n%s", got, wantText)
	}
	for _, marker := range []string{promptSlotMarker, promptValueMarker} {
		if strings.Contains(got, marker) {
			t.Errorf("the planted prompt still holds %s, so an agent reads the marker as prose", marker)
		}
	}
}

// TestPromptSourcesAreTheTextTheRenderSubstitutes pins the one property that
// makes exporting the read worth anything: the report and the render see the
// same bytes at the same destination.
//
// `cairn boot` reports the markers in a prompt that stood for nothing, and it
// can only ask that of the source text. If [PromptSources] ever resolved the
// prompts directory, or decided which file a name is, separately from the
// render, the report would describe a file that was never planted — which is a
// worse version of the silence the report was added to end. So the two are
// asserted against each other rather than against a literal: the source's Path
// is the rendered file's path, and the source's Text substituted by hand is
// the rendered file's content.
//
// The unsubstituted half is asserted too. A Text that had already been through
// [Substitute] would satisfy the path claim and carry no markers left to
// report, which is the failure that would look most like working.
func TestPromptSourcesAreTheTextTheRenderSubstitutes(t *testing.T) {
	source := t.TempDir()
	text := "Write the handoff.\n\n" + promptSlotMarker + "\n\nScope: " + promptValueMarker + "\n"
	writePrompt(t, source, "handoff", text)

	inst := promptsInstance(t, source, "handoff")
	inst.Sections = map[string]string{"repository": promptSection}
	inst.Values = map[string]string{"scope": promptScope}

	sources, err := PromptSources(inst)
	if err != nil {
		t.Fatalf("PromptSources(): %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("PromptSources() returned %d sources, want the one declared prompt", len(sources))
	}
	got := sources[0]
	if got.Name != "handoff" {
		t.Errorf("the source is named %q, want the name spec.prompts declared", got.Name)
	}
	if want := filepath.Join(source, "handoff"+PromptFileExt); got.From != want {
		t.Errorf("the source was read from %q, want %q", got.From, want)
	}
	if got.Text != text {
		t.Errorf("the source text is\n%q\nwant the file's own bytes\n%q", got.Text, text)
	}
	for _, marker := range []string{promptSlotMarker, promptValueMarker} {
		if !strings.Contains(got.Text, marker) {
			t.Errorf("the source text has lost %s, so nothing can report it as unfilled", marker)
		}
	}

	files, err := renderPrompts(inst)
	if err != nil {
		t.Fatalf("renderPrompts(): %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("renderPrompts() returned %d files, want one", len(files))
	}
	if got.Path != files[0].Path {
		t.Errorf("the source plants at %q and the render plants at %q; a report reading the first "+
			"would name a file the second never wrote", got.Path, files[0].Path)
	}
	rendered, err := Substitute(got.Text, inst.Sections, inst.Values)
	if err != nil {
		t.Fatalf("Substitute(the source text): %v", err)
	}
	if rendered != string(files[0].Content) {
		t.Errorf("the source text substitutes to\n%q\nand the render wrote\n%q",
			rendered, files[0].Content)
	}
}

// TestPromptSubstitutionComesFromTheInstance is the control for the test
// above. The same fixture with nothing on the instance has to lose both
// substituted strings, and keep the prose around them.
//
// Without it that test passes for a render that planted the file's own bytes
// and never substituted anything — as long as the fixture happened to spell
// its markers the way the assertion did. This is what makes the section and
// the scope inputs whose absence is visible in the output.
func TestPromptSubstitutionComesFromTheInstance(t *testing.T) {
	source := t.TempDir()
	writePrompt(t, source, "handoff",
		"Write the handoff.\n\n"+promptSlotMarker+"\n\nScope: "+promptValueMarker+"\n")

	inst := promptsInstance(t, source, "handoff")
	files, err := renderPrompts(inst)
	if err != nil {
		t.Fatalf("renderPrompts(): %v", err)
	}
	got := string(fileByPath(t, files, ".claude/commands/boot/handoff.md").Content)
	for _, absent := range []string{promptSection, promptScope, promptSlotMarker, promptValueMarker} {
		if strings.Contains(got, absent) {
			t.Errorf("with nothing on the instance the prompt holds %q:\n%s", absent, got)
		}
	}
	// A slot that stood for nothing takes its own line with it, and leaves the
	// operator's blank lines either side of it alone; a value sharing a line
	// leaves the line and its prose. Both are Substitute's rules, and a prompt
	// gets them because it is a template and not a second renderer.
	want := "Write the handoff.\n\n\nScope: \n"
	if got != want {
		t.Errorf("the planted prompt holds %q, want %q", got, want)
	}
}

// TestPromptsDeclaringNoneRenderNothing is the rule every content renderer
// follows: cairn ships no prompts, and a profile that declares none gets none.
func TestPromptsDeclaringNoneRenderNothing(t *testing.T) {
	for _, manifest := range []string{"", "{}", `{"prompts":[]}`, `{"prompts":null}`} {
		inst := testInstance(t, profile.Resolved{ID: "base", Spec: testSpec(t, manifest)})
		files, err := renderPrompts(inst)
		if err != nil {
			t.Errorf("renderPrompts() with manifest %q: %v", manifest, err)
		}
		if len(files) != 0 {
			t.Errorf("manifest %q rendered %v, want nothing", manifest, filePaths(files))
		}
	}
}

// TestPromptsSourceIsRefused covers every way spec.prompts_dir can fail to
// name a directory prompts can be read from.
func TestPromptsSourceIsRefused(t *testing.T) {
	existing := t.TempDir()
	notADir := filepath.Join(existing, "file")
	if err := os.WriteFile(notADir, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", notADir, err)
	}

	for _, tc := range []struct {
		name   string
		source string
	}{
		{"undeclared", ""},
		{"relative", "prompts"},
		{"absent", filepath.Join(existing, "nowhere")},
		{"not a directory", notADir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := promptsInstance(t, tc.source, "handoff")
			if _, err := renderPrompts(inst); !errors.Is(err, ErrPromptsSource) {
				t.Errorf("renderPrompts() returned error %v, want ErrPromptsSource", err)
			}
		})
	}
}

// TestPromptsSourceExpands pins that spec.prompts_dir takes the same "~/" and
// $VAR every other manifest value naming somewhere to read from takes, and
// takes them from the instance rather than from the process.
func TestPromptsSourceExpands(t *testing.T) {
	home := t.TempDir()
	writePrompt(t, filepath.Join(home, "prompts"), "handoff", "the handoff\n")

	for _, source := range []string{"~/prompts", "$BUNDLE/prompts"} {
		inst := promptsInstance(t, source, "handoff")
		inst.Home = home
		inst.Env = func(name string) string {
			if name == "BUNDLE" {
				return home
			}
			return ""
		}
		files, err := renderPrompts(inst)
		if err != nil {
			t.Fatalf("renderPrompts() with prompts_dir %q: %v", source, err)
		}
		if got := string(fileByPath(t, files, ".claude/commands/boot/handoff.md").Content); got != "the handoff\n" {
			t.Errorf("prompts_dir %q planted %q", source, got)
		}
	}
}

// TestPromptNameIsRefused covers the names that cannot name one file directly
// beneath the source. The first four are containment — a separator reaches
// outside the source going in and outside the commands directory coming out —
// and the last two are the mistakes an operator makes with a flat directory of
// files.
func TestPromptNameIsRefused(t *testing.T) {
	source := t.TempDir()
	writePrompt(t, source, "handoff", "the handoff\n")

	for _, tc := range []struct {
		name  string
		names []string
	}{
		{"empty", []string{""}},
		{"dot", []string{"."}},
		{"dot dot", []string{".."}},
		{"a separator", []string{"shared/handoff"}},
		{"an escape", []string{"../handoff"}},
		{"the extension written out", []string{"handoff.md"}},
		{"the extension in another case", []string{"handoff.MD"}},
		{"declared twice", []string{"handoff", "handoff"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := promptsInstance(t, source, tc.names...)
			if _, err := renderPrompts(inst); !errors.Is(err, ErrPromptName) {
				t.Errorf("renderPrompts() for %v returned error %v, want ErrPromptName", tc.names, err)
			}
		})
	}
}

// TestPromptNotFoundStopsTheRender states why a missing prompt is a refusal
// and not an omission: the command it would have planted is one the operator
// declared, and a boot directory without it answers "Unknown command" with
// nothing anywhere saying why.
func TestPromptNotFoundStopsTheRender(t *testing.T) {
	source := t.TempDir()
	writePrompt(t, source, "handoff", "the handoff\n")

	inst := promptsInstance(t, source, "handoff", "absent")
	_, err := renderPrompts(inst)
	if !errors.Is(err, ErrPromptNotFound) {
		t.Fatalf("renderPrompts() returned error %v, want ErrPromptNotFound", err)
	}
	if want := filepath.Join(source, "absent.md"); !strings.Contains(err.Error(), want) {
		t.Errorf("the error %q does not name %s, which is the file the operator would create", err, want)
	}
}

// TestPromptContentIsRefused covers a declared prompt that plants nothing an
// operator could invoke: a directory where a file was named, an empty file,
// one whose every marker stood for nothing, and a symlink to a file that is
// not there.
//
// The dangling link is the one that would otherwise be reported wrongly rather
// than not at all. It is absent as far as os.Stat is concerned, so the
// not-found diagnostic would say "nothing at <path>" — and send an operator to
// create a file with a broken link already sitting in its place.
//
// The last is the one worth having. It is the case a template renders no file
// for, deliberately, and a prompt cannot: a template names its own
// destination, while a prompt is named in a list and typed by that name, so
// planting nothing turns a declaration into "Unknown command" at the moment
// somebody uses it.
func TestPromptContentIsRefused(t *testing.T) {
	source := t.TempDir()
	writePrompt(t, source, "empty", "")
	writePrompt(t, source, "whitespace", "\n  \n")
	writePrompt(t, source, "hollow", promptSlotMarker+"\n")
	if err := os.MkdirAll(filepath.Join(source, "directory.md"), 0o755); err != nil {
		t.Fatalf("create the fixture directory: %v", err)
	}

	if err := os.Symlink(filepath.Join(source, "gone.md"), filepath.Join(source, "dangling.md")); err != nil {
		t.Fatalf("create the dangling symlink: %v", err)
	}

	for _, name := range []string{"empty", "whitespace", "hollow", "directory", "dangling"} {
		t.Run(name, func(t *testing.T) {
			inst := promptsInstance(t, source, name)
			if _, err := renderPrompts(inst); !errors.Is(err, ErrPromptContent) {
				t.Errorf("renderPrompts() for %q returned error %v, want ErrPromptContent", name, err)
			}
		})
	}

	// The emptied case names what emptied it. A prompt is the one place a slot
	// failure reaches the exit status, and "this rendered nothing" without the
	// slot's name leaves the operator to guess which of a profile's slots it
	// was — while the failure itself is already on stderr under that name.
	t.Run("the emptied case names the marker", func(t *testing.T) {
		_, err := renderPrompts(promptsInstance(t, source, "hollow"))
		if err == nil {
			t.Fatal("a prompt that substituted away to nothing was planted")
		}
		if !strings.Contains(err.Error(), `slot "repository"`) {
			t.Errorf("the error %q does not name the marker that stood for nothing", err)
		}
		// And the empty file says the other thing, so the two are told apart.
		_, err = renderPrompts(promptsInstance(t, source, "empty"))
		if err == nil || !strings.Contains(err.Error(), "holds nothing") {
			t.Errorf("an empty prompt reported %v, want it to say the file holds nothing", err)
		}
	})
}

// TestPromptMarkerIsRefused pins that a prompt is held to the marker syntax
// every template is. A malformed marker inside cairn's own namespace is a
// mistake in the document, and leaving it in place would plant its text into a
// file an agent reads.
func TestPromptMarkerIsRefused(t *testing.T) {
	source := t.TempDir()
	writePrompt(t, source, "handoff", "<!-- cairn:conditional scope -->\n")

	inst := promptsInstance(t, source, "handoff")
	_, err := renderPrompts(inst)
	if !errors.Is(err, ErrMarker) {
		t.Errorf("renderPrompts() returned error %v, want ErrMarker", err)
	}
	if err != nil && !strings.Contains(err.Error(), "handoff") {
		t.Errorf("the error %q does not name the prompt it came from", err)
	}
}

// TestPromptsAreOrderedDeterministically states the property a rendering has
// to hold whatever the filesystem hands back.
func TestPromptsAreOrderedDeterministically(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"zulu", "alpha", "mike"} {
		writePrompt(t, source, name, name+"\n")
	}
	inst := promptsInstance(t, source, "zulu", "alpha", "mike")
	want := []string{
		".claude/commands/boot/zulu.md",
		".claude/commands/boot/alpha.md",
		".claude/commands/boot/mike.md",
	}
	for i := range 8 {
		files, err := renderPrompts(inst)
		if err != nil {
			t.Fatalf("renderPrompts() on pass %d: %v", i, err)
		}
		if got := filePaths(files); !slices.Equal(got, want) {
			t.Fatalf("pass %d rendered %v, want %v", i, got, want)
		}
	}
}

// TestPromptsAreDroppedAndReportedWhenTheLayoutHasNowhereForThem covers the
// layout that declares no prompts directory. The prompts are dropped, the boot
// carries on, and [Undeclared] names what was lost.
//
// It replaces a test that pinned [ErrProviderLayout] here. The refusal's own
// argument was that writing nowhere looks exactly like declaring nothing —
// which is true, and is about silence rather than about refusing, so the
// report satisfies it. See [Undeclared] for the rest of the reasoning,
// including why a tree naming a renderer cairn does not have still fails.
func TestPromptsAreDroppedAndReportedWhenTheLayoutHasNowhereForThem(t *testing.T) {
	source := t.TempDir()
	writePrompt(t, source, "handoff", "the handoff\n")

	inst := promptsInstance(t, source, "handoff")
	inst.Layout.PromptsDir = ""

	files, err := renderPrompts(inst)
	if err != nil {
		t.Fatalf("renderPrompts() = %v, want the prompts dropped and no error", err)
	}
	if len(files) != 0 {
		t.Errorf("renderPrompts() planted %d file(s) with nowhere to put them", len(files))
	}

	// The drop is not silent, and the report reads off the same directory the
	// renderer read, so the two cannot disagree.
	report := Undeclared(inst)
	if len(report) != 1 {
		t.Fatalf("Undeclared() = %v, want one line", report)
	}
	for _, want := range []string{profile.SpecKeyPrompts, "handoff", string(inst.Layout.Provider)} {
		if !strings.Contains(report[0], want) {
			t.Errorf("the report %q does not name %q", report[0], want)
		}
	}
}
