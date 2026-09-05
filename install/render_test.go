package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/profile"
)

// fixtureSpec decodes a manifest written inline in a test into a
// [profile.Spec], so that a test declares the JSON an operator would have
// stored rather than a map of pre-marshalled values.
func fixtureSpec(t *testing.T, manifest string) profile.Spec {
	t.Helper()
	if manifest == "" {
		return nil
	}
	var spec profile.Spec
	if err := json.Unmarshal([]byte(manifest), &spec); err != nil {
		t.Fatalf("decode the manifest %s: %v", manifest, err)
	}
	return spec
}

// fixtureLayer returns a layer rooted at a fresh temporary directory.
//
// Every test in this package roots there and nowhere else. `cairn install`
// writes the configuration the session running these tests is itself reading,
// so a test that defaulted to a real home directory would rewrite it.
func fixtureLayer(t *testing.T, resolved profile.Resolved) *Layer {
	t.Helper()
	root, err := NewRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewRoot on a temporary directory: %v", err)
	}
	return &Layer{
		Root:    root,
		Profile: &resolved,
		// Templates and values reach a layer already resolved, the way the
		// composition root supplies them: a template may name a source, and
		// reading one is I/O a renderer may not do.
		Templates: map[string]string{
			bootdir.AgentsFileName:  "# <!-- cairn:value profile -->\n\n" + resolved.Body + "\n",
			bootdir.PointerFileName: "@" + bootdir.AgentsFileName + "\n",
			"boot.md":               "a boot-directory destination this layer does not render\n",
		},
		Values: map[string]string{"profile": resolved.ID, "provider": resolved.Provider.String()},
	}
}

// fixtureSkill writes one skill directory under root: files maps
// slash-separated paths inside the skill to their contents, and every path
// named in executable is written with its executable bit set.
func fixtureSkill(t *testing.T, root, name string, files map[string]string, executable ...string) {
	t.Helper()
	for rel, content := range files {
		dest := filepath.Join(root, name, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatalf("create the directory for %s: %v", dest, err)
		}
		mode := fs.FileMode(0o644)
		if slices.Contains(executable, rel) {
			mode = 0o755
		}
		if err := os.WriteFile(dest, []byte(content), mode); err != nil {
			t.Fatalf("write %s: %v", dest, err)
		}
		// os.WriteFile's mode is masked by the umask, and the executable bit is
		// the property under test.
		if err := os.Chmod(dest, mode); err != nil {
			t.Fatalf("set the mode on %s: %v", dest, err)
		}
	}
}

// renderedPaths returns the paths of files, in render order.
func renderedPaths(files []File) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

// renderedFile returns the rendered file at path.
func renderedFile(t *testing.T, files []File, path string) File {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no file was rendered at %q; the render holds %v", path, renderedPaths(files))
	return File{}
}

// declaredManifest returns a manifest declaring every key cairn renders
// somewhere, including the three the installed layer deliberately does not:
// slots, mcp, and files.
//
// The skills come from install.skills. spec.skills is the boot directory's
// key, and this layer does not read it — see
// TestRenderPlantsTheInstalledSkillsAndNotTheBootDirectorys.
func declaredManifest(skillsDir string) string {
	return fmt.Sprintf(`{
	  "slots":      [{"name": "memory", "source": {"kind": "inline", "value": "remembered"}}],
	  "mcp":        [{"name": "memory", "command": "memoryd", "args": ["--stdio"]}],
	  "install":    {"skills": ["code-review"]},
	  "skills_dir": %s,
	  "settings":   {"claude": {"model": "opus"}},
	  "files":      {"notes/todo.md": "do the thing"},
	  "trees":      {"docs": "/nonexistent-on-purpose"},
	  "templates":  {"AGENTS.md": "declared, and resolved onto the layer"}
	}`, strconv.Quote(skillsDir))
}

// fullyDeclaredLayer returns a layer whose profile declares every renderable
// manifest key, with one multi-file skill on disk to copy.
func fullyDeclaredLayer(t *testing.T) *Layer {
	t.Helper()
	skillsDir := t.TempDir()
	fixtureSkill(t, skillsDir, "code-review", map[string]string{
		"SKILL.md":            "# Code review\n",
		"references/style.md": "prefer clarity\n",
		"run.sh":              "#!/bin/sh\necho review\n",
	}, "run.sh")
	return fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Name:     "Base",
		Provider: profile.ProviderClaude,
		Body:     "the operator's prose",
		Spec:     fixtureSpec(t, declaredManifest(skillsDir)),
	})
}

// TestRenderFullyDeclaredProfileRendersTheInstalledArtifactsAndNothingElse
// pins the whole output contract of the installed layer, in render order.
//
// The negative half is the point. The profile declares slots, MCP servers and
// spec.files, all of which a boot directory renders. A boot directory is
// created fresh and refuses to plant over one that exists, so an arbitrary
// path-to-content map can only ever land on empty ground; the installed layer
// writes into a directory already full of the operator's live state, where the
// same map lands on whatever is already there.
func TestRenderFullyDeclaredProfileRendersTheInstalledArtifactsAndNothingElse(t *testing.T) {
	files, err := Render(fullyDeclaredLayer(t))
	if err != nil {
		t.Fatalf("Render a fully declared profile: %v", err)
	}
	want := []string{
		".claude/AGENTS.md",
		".claude/CLAUDE.md",
		".claude/settings.json",
		".claude/skills/code-review/SKILL.md",
		".claude/skills/code-review/references/style.md",
		".claude/skills/code-review/run.sh",
	}
	if got := renderedPaths(files); !slices.Equal(got, want) {
		t.Errorf("Render produced\n\t%v\nwant\n\t%v", got, want)
	}
	for _, absent := range []string{"boot.md", ".mcp.json", "notes/todo.md"} {
		for _, f := range files {
			if f.Path == absent || strings.HasSuffix(f.Path, "/"+absent) {
				t.Errorf("Render produced %q; the installed layer renders no %s", f.Path, absent)
			}
		}
	}
}

// TestRenderEveryPathIsInsideTheProviderDirectory states the containment rule
// as its own assertion, so that a renderer emitting a path at the install
// root — beside the operator's shell configuration — fails here and not only
// as a surprising entry in the list above.
func TestRenderEveryPathIsInsideTheProviderDirectory(t *testing.T) {
	files, err := Render(fullyDeclaredLayer(t))
	if err != nil {
		t.Fatalf("Render a fully declared profile: %v", err)
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, ClaudeDirName+"/") {
			t.Errorf("Render produced %q, which is not under %q", f.Path, ClaudeDirName)
		}
	}
}

// TestCheckInsideReportsAPathOutsideTheProviderDirectory exercises the
// containment check directly. Nothing reachable through [Render] can produce
// such a path today, which is exactly why the check has to be tested from
// here: the case it guards is a future change to a shared renderer.
func TestCheckInsideReportsAPathOutsideTheProviderDirectory(t *testing.T) {
	for _, escape := range []string{"AGENTS.md", ".claudex/AGENTS.md", ".claude"} {
		files := []File{{Path: ClaudeDirName + "/AGENTS.md"}, {Path: escape}}
		err := checkInside(files, ClaudeDirName)
		if !errors.Is(err, ErrUnexpectedArtifact) {
			t.Errorf("checkInside with %q = %v, want ErrUnexpectedArtifact", escape, err)
		}
		if err != nil && !strings.Contains(err.Error(), escape) {
			t.Errorf("checkInside with %q reported %v, which does not name the path", escape, err)
		}
	}
}

// TestRenderPointerIsTheIncludeAndNothingElse holds the pointer file to its
// whole contract: one include of the instruction file, byte for byte, with no
// generated-file marker. The marker would be three times the file and would
// sit in the document that points at the one carrying it.
func TestRenderPointerIsTheIncludeAndNothingElse(t *testing.T) {
	files, err := Render(fullyDeclaredLayer(t))
	if err != nil {
		t.Fatalf("Render a fully declared profile: %v", err)
	}
	pointer := renderedFile(t, files, ".claude/CLAUDE.md")
	if want := "@" + bootdir.AgentsFileName + "\n"; string(pointer.Content) != want {
		t.Errorf(".claude/CLAUDE.md rendered %q, want the template's own text %q", pointer.Content, want)
	}
}

// TestRenderInstructionFileOpensWithTheGeneratedMarker checks the marker is
// the first line, that a blank line separates it from the content, and that
// the content the shared renderer produced is still there behind it.
func TestRenderInstructionFileOpensWithTheGeneratedMarker(t *testing.T) {
	files, err := Render(fullyDeclaredLayer(t))
	if err != nil {
		t.Fatalf("Render a fully declared profile: %v", err)
	}
	agents := renderedFile(t, files, ".claude/AGENTS.md")
	wantPrefix := GeneratedMarker("base") + "\n\n"
	if !strings.HasPrefix(string(agents.Content), wantPrefix) {
		t.Fatalf(".claude/AGENTS.md rendered\n%s\nwant it to open with\n%s", agents.Content, wantPrefix)
	}
	if !strings.Contains(string(agents.Content), `"base"`) {
		t.Errorf(".claude/AGENTS.md does not name the profile it was rendered from:\n%s", agents.Content)
	}
	if !strings.Contains(string(agents.Content), "the operator's prose") {
		t.Errorf(".claude/AGENTS.md lost the profile body behind the marker:\n%s", agents.Content)
	}
}

// TestRenderSettingsCarriesNoGeneratedMarker holds the settings document to
// the harness's schema. JSON has no comment syntax, and a top-level key cairn
// invented would be cairn editing a document whose shape belongs to Claude
// Code.
func TestRenderSettingsCarriesNoGeneratedMarker(t *testing.T) {
	files, err := Render(fullyDeclaredLayer(t))
	if err != nil {
		t.Fatalf("Render a fully declared profile: %v", err)
	}
	settings := renderedFile(t, files, ".claude/settings.json")
	if want := "{\n  \"model\": \"opus\"\n}\n"; string(settings.Content) != want {
		t.Errorf(".claude/settings.json rendered %q, want the stored document laid out %q",
			settings.Content, want)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(settings.Content, &decoded); err != nil {
		t.Fatalf(".claude/settings.json is not valid JSON: %v", err)
	}
}

// TestRenderSettingsGrantAccessAndNeverAScope is the layer's half of the
// access decision, and both halves of it matter.
//
// The directories a profile declares are a standing fact — what every session
// of this profile needs — so they belong in the layer every session reads.
// Withholding them here would mean a declared directory reached a disposable
// boot directory and never the file the harness loads for the rest of them.
//
// A scope is the opposite: it is true of one session, and granting it here
// would hand one session's working directory to every session on the machine.
// [Layer] therefore carries no scope to give and [layerInstance] leaves the
// instance's zero, which is asserted directly — a field that quietly acquired
// a value would grant it without anything else changing.
func TestRenderSettingsGrantAccessAndNeverAScope(t *testing.T) {
	// A real directory: a granted path must name one, so a plausible-looking
	// absolute string would assert the refusal rather than the grant.
	shared, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the fixture directory: %v", err)
	}
	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderClaude,
		Spec: fixtureSpec(t, `{"settings": {"claude": {"model": "opus"}},
			"access": {"directories": [`+strconv.Quote(shared)+`]}}`),
	})

	files, err := Render(lay)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	settings := renderedFile(t, files, ".claude/settings.json")
	var document struct {
		Permissions struct {
			AdditionalDirectories []string `json:"additionalDirectories"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(settings.Content, &document); err != nil {
		t.Fatalf(".claude/settings.json does not decode: %v", err)
	}
	if want := []string{shared}; !slices.Equal(document.Permissions.AdditionalDirectories, want) {
		t.Errorf("permissions.additionalDirectories = %v, want %v",
			document.Permissions.AdditionalDirectories, want)
	}
	if inst := layerInstance(lay, ClaudeLayout()); inst.Scope != "" {
		t.Errorf("the installed layer's instance carries the scope %q, and this layer is read by every session",
			inst.Scope)
	}
}

// TestBootDirectoryInstructionFileCarriesNoGeneratedMarker guards the marker
// against leaking into package bootdir.
//
// A boot directory is created fresh, disposable, and never hand-edited with an
// expectation of persistence, so the line would be noise in every session's
// context with no reader to serve. This asserts it from outside, because the
// mechanism that keeps it out is that install applies the marker after
// bootdir's renderer has returned — which stops holding the moment someone
// moves it one package down.
func TestBootDirectoryInstructionFileCarriesNoGeneratedMarker(t *testing.T) {
	layout, err := bootdir.LayoutFor(profile.ProviderClaude)
	if err != nil {
		t.Fatalf("bootdir.LayoutFor(%q): %v", profile.ProviderClaude, err)
	}
	resolved := profile.Resolved{ID: "base", Name: "Base", Provider: profile.ProviderClaude, Body: "prose"}
	files, err := bootdir.Render(&bootdir.Instance{
		Dir:       filepath.Join(t.TempDir(), "boot"),
		Layout:    layout,
		Profile:   &resolved,
		Templates: map[string]string{bootdir.AgentsFileName: "# base\n"},
	})
	if err != nil {
		t.Fatalf("bootdir.Render: %v", err)
	}
	agents := renderedFile(t, files, bootdir.AgentsFileName)
	if bytes.Contains(agents.Content, []byte("Generated by")) {
		t.Errorf("a boot directory's %s carries a generated-file marker:\n%s",
			bootdir.AgentsFileName, agents.Content)
	}
}

// TestRenderAbstractProfile holds plan §7: the installed layer is normally
// rendered from the abstract root of the cascade, so Render must not refuse
// one. `cairn boot` is what refuses a direct boot.
func TestRenderAbstractProfile(t *testing.T) {
	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Name:     "Base",
		Abstract: true,
		Provider: profile.ProviderClaude,
		Body:     "the abstract root",
	})
	files, err := Render(lay)
	if err != nil {
		t.Fatalf("Render an abstract profile: %v", err)
	}
	want := []string{".claude/AGENTS.md", ".claude/CLAUDE.md"}
	if got := renderedPaths(files); !slices.Equal(got, want) {
		t.Fatalf("Render an abstract profile produced %v, want %v", got, want)
	}
}

// TestRenderProfileDeclaringNothing checks that a manifest with no keys at all
// renders the unconditional artifacts and reports no error. A profile is not
// obliged to declare anything.
func TestRenderProfileDeclaringNothing(t *testing.T) {
	lay := fixtureLayer(t, profile.Resolved{ID: "bare", Provider: profile.ProviderClaude})
	files, err := Render(lay)
	if err != nil {
		t.Fatalf("Render a profile declaring nothing: %v", err)
	}
	want := []string{".claude/AGENTS.md", ".claude/CLAUDE.md"}
	if got := renderedPaths(files); !slices.Equal(got, want) {
		t.Fatalf("Render a profile declaring nothing produced %v, want %v", got, want)
	}
}

// TestMarkGeneratedDropsAnInstructionFileWithNoContent checks that an
// instruction file rendering to no bytes stays absent rather than becoming a
// file holding a marker and nothing else. A file claiming cairn rendered
// something, when cairn rendered nothing, is worse than an absent one.
//
// It is asserted against the wrapper rather than through [Render], because no
// profile reachable through Render can produce it: the instruction file lists
// the provider the profile resolved to, and a profile with no provider is
// refused before any renderer runs. That makes this a guard on the wrapper's
// own rule, which is where the rule lives.
func TestMarkGeneratedDropsAnInstructionFileWithNoContent(t *testing.T) {
	empty := func(_ *bootdir.Instance) ([]File, error) {
		return []File{{Path: ".claude/AGENTS.md"}}, nil
	}
	files, err := markGenerated(empty, "base")(nil)
	if err != nil {
		t.Fatalf("markGenerated over an empty render: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("markGenerated produced %v, want no file at all", renderedPaths(files))
	}
}

// TestMarkGeneratedPropagatesTheRendererError checks the wrapper does not
// swallow what the shared renderer reported.
func TestMarkGeneratedPropagatesTheRendererError(t *testing.T) {
	want := errors.New("the renderer failed")
	failing := func(_ *bootdir.Instance) ([]File, error) { return nil, want }
	if _, err := markGenerated(failing, "base")(nil); !errors.Is(err, want) {
		t.Errorf("markGenerated over a failing render = %v, want %v", err, want)
	}
}

// TestMarkGeneratedDoesNotAliasTheRenderedContent checks the marker is
// prepended into a fresh buffer. A renderer returning a slice backed by an
// array it also holds — a skill's file bytes, say — must not have that array
// written through.
func TestMarkGeneratedDoesNotAliasTheRenderedContent(t *testing.T) {
	original := []byte("# Base\n")
	render := func(_ *bootdir.Instance) ([]File, error) {
		return []File{{Path: ".claude/AGENTS.md", Content: original}}, nil
	}
	files, err := markGenerated(render, "base")(nil)
	if err != nil {
		t.Fatalf("markGenerated: %v", err)
	}
	if string(original) != "# Base\n" {
		t.Errorf("markGenerated wrote through the renderer's own bytes: %q", original)
	}
	if !strings.HasSuffix(string(files[0].Content), "# Base\n") {
		t.Errorf("markGenerated lost the rendered content: %q", files[0].Content)
	}
}

// TestRenderMultiFileSkillLandsAsADirectoryTree holds plan §5: a skill is a
// directory, and the executable bit on a script inside it is load-bearing.
func TestRenderMultiFileSkillLandsAsADirectoryTree(t *testing.T) {
	files, err := Render(fullyDeclaredLayer(t))
	if err != nil {
		t.Fatalf("Render a fully declared profile: %v", err)
	}
	entry := renderedFile(t, files, ".claude/skills/code-review/SKILL.md")
	if string(entry.Content) != "# Code review\n" {
		t.Errorf("SKILL.md rendered %q", entry.Content)
	}
	reference := renderedFile(t, files, ".claude/skills/code-review/references/style.md")
	if string(reference.Content) != "prefer clarity\n" {
		t.Errorf("references/style.md rendered %q", reference.Content)
	}
	script := renderedFile(t, files, ".claude/skills/code-review/run.sh")
	if script.Mode != bootdir.ExecFileMode {
		t.Errorf("run.sh rendered with mode %v, want %v", script.Mode, bootdir.ExecFileMode)
	}
}

// TestRenderExpandsTheSkillsDirectoryAgainstTheLayerHome checks that a
// manifest path written with a leading "~/" is expanded against the home the
// layer carries, and not against the process's own environment.
func TestRenderExpandsTheSkillsDirectoryAgainstTheLayerHome(t *testing.T) {
	home := t.TempDir()
	skillsDir := filepath.Join(home, "skills")
	fixtureSkill(t, skillsDir, "capture-decision", map[string]string{"SKILL.md": "# Capture\n"})

	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderClaude,
		Spec:     fixtureSpec(t, `{"install": {"skills": ["capture-decision"]}, "skills_dir": "~/skills"}`),
	})
	lay.Home = home

	files, err := Render(lay)
	if err != nil {
		t.Fatalf("Render with a home-relative skills directory: %v", err)
	}
	skill := renderedFile(t, files, ".claude/skills/capture-decision/SKILL.md")
	if string(skill.Content) != "# Capture\n" {
		t.Errorf("SKILL.md rendered %q", skill.Content)
	}
}

// TestRenderWithoutAProfile checks that a layer carrying no resolved profile
// is reported rather than dereferenced.
func TestRenderWithoutAProfile(t *testing.T) {
	root, err := NewRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewRoot on a temporary directory: %v", err)
	}
	for name, lay := range map[string]*Layer{
		"a nil layer": nil,
		"no profile":  {Root: root},
	} {
		if _, err := Render(lay); !errors.Is(err, ErrNoProfile) {
			t.Errorf("Render with %s = %v, want ErrNoProfile", name, err)
		}
	}
}

// TestRenderWithoutARoot checks that the zero root is an error rather than a
// default. There is no directory cairn installs into unless one was named.
func TestRenderWithoutARoot(t *testing.T) {
	resolved := profile.Resolved{ID: "base", Provider: profile.ProviderClaude}
	if _, err := Render(&Layer{Profile: &resolved}); !errors.Is(err, ErrNoRoot) {
		t.Errorf("Render with the zero root = %v, want ErrNoRoot", err)
	}
}

// TestRenderUnsupportedProvider checks that a harness with no installed layout
// is refused rather than rendered through Claude Code's.
func TestRenderUnsupportedProvider(t *testing.T) {
	for _, p := range []profile.Provider{profile.ProviderOpenCode, ""} {
		lay := fixtureLayer(t, profile.Resolved{ID: "base", Provider: p})
		if _, err := Render(lay); !errors.Is(err, bootdir.ErrUnsupportedProvider) {
			t.Errorf("Render for provider %q = %v, want bootdir.ErrUnsupportedProvider", p, err)
		}
	}
}

func TestRenderCodexInstalledLayerUsesCodexAndAgentsRoots(t *testing.T) {
	skillsDir := t.TempDir()
	fixtureSkill(t, skillsDir, "capture", map[string]string{"SKILL.md": "# Capture\n"})
	shared, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve shared dir: %v", err)
	}
	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderCodex,
		Spec: fixtureSpec(t, `{
			"templates": {"AGENTS.md": "codex base"},
			"settings": {"codex": {"model": "gpt-5"}},
			"access": {"directories": [`+strconv.Quote(shared)+`]},
			"install": {"skills": ["capture"]},
			"skills_dir": `+strconv.Quote(skillsDir)+`
		}`),
	})
	files, err := Render(lay)
	if err != nil {
		t.Fatalf("Render codex layer: %v", err)
	}
	want := []string{
		".codex/AGENTS.md",
		".codex/config.toml",
		".agents/skills/capture/SKILL.md",
	}
	if got := renderedPaths(files); !slices.Equal(got, want) {
		t.Errorf("Render produced\n\t%v\nwant\n\t%v", got, want)
	}
	config := string(renderedFile(t, files, ".codex/config.toml").Content)
	for _, want := range []string{`model = 'gpt-5'`, `[sandbox_workspace_write]`, `writable_roots = ['` + shared + `']`} {
		if !strings.Contains(config, want) {
			t.Errorf("config.toml missing %q:\n%s", want, config)
		}
	}
	for _, f := range files {
		if strings.Contains(f.Path, "auth.json") || strings.Contains(f.Path, ".mcp.json") {
			t.Errorf("Render planted adapter sidecar %q", f.Path)
		}
	}
}

// TestRenderReportsAFailedRenderer checks that a renderer's own error reaches
// the caller naming what it was rendering.
func TestRenderReportsAFailedRenderer(t *testing.T) {
	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderClaude,
		Spec: fixtureSpec(t, fmt.Sprintf(
			`{"install": {"skills": ["code-review"]}, "skills_dir": %s}`,
			strconv.Quote(filepath.Join(t.TempDir(), "absent")))),
	})
	_, err := Render(lay)
	if !errors.Is(err, bootdir.ErrSkillsSource) {
		t.Fatalf("Render with a missing skills directory = %v, want bootdir.ErrSkillsSource", err)
	}
	if !strings.Contains(err.Error(), SkillsDirName) {
		t.Errorf("the error does not name the artifact being rendered: %v", err)
	}
}

// TestPlanterForClaude checks the registration list a check reads to learn
// which parts of the provider directory cairn owns, including which artifact
// is a directory whose subdirectories the profile names — the difference
// between finding an orphan and not.
func TestPlanterForClaude(t *testing.T) {
	renderers, layout, err := PlanterFor(profile.ProviderClaude)
	if err != nil {
		t.Fatalf("PlanterFor(%q): %v", profile.ProviderClaude, err)
	}
	if layout.Provider != profile.ProviderClaude {
		t.Errorf("PlanterFor returned a layout for %q", layout.Provider)
	}
	if layout.MCP.Declared() {
		t.Errorf("the installed layout declares an MCP path: %+v", layout)
	}
	want := []struct {
		artifact string
		fills    bool
	}{
		{bootdir.AgentsFileName, false},
		{bootdir.PointerFileName, false},
		{SettingsFileName, false},
		{SkillsDirName, true},
	}
	if len(renderers) != len(want) {
		t.Fatalf("PlanterFor returned %d renderers, want %d", len(renderers), len(want))
	}
	for i, w := range want {
		fills := renderers[i].Fills != nil
		if renderers[i].Artifact != w.artifact || fills != w.fills {
			t.Errorf("renderer %d is %q (fills named subdirectories: %v), want %q (%v)",
				i, renderers[i].Artifact, fills, w.artifact, w.fills)
		}
		if renderers[i].Render == nil {
			t.Errorf("renderer %q carries no render function", renderers[i].Artifact)
		}
	}
	// The skills renderer's claim is the profile's declaration, read through
	// the registration rather than through a render.
	skills := renderers[len(renderers)-1]
	names, err := skills.Fills(&profile.Resolved{
		Spec: fixtureSpec(t, `{"skills": ["boot-only"], "install": {"skills": ["installed"]}}`),
	})
	if err != nil {
		t.Fatalf("Fills: %v", err)
	}
	if !slices.Equal(names, []string{"installed"}) {
		t.Errorf("Fills = %v, want [installed]: the installed layer claims install.skills", names)
	}
	if _, err := skills.Fills(nil); !errors.Is(err, ErrNoProfile) {
		t.Errorf("Fills(nil) = %v, want ErrNoProfile", err)
	}
}

// TestRenderPlantsTheInstalledSkillsAndNotTheBootDirectorys is the net effect
// of splitting the key: a profile's spec.skills is a boot directory's, and the
// installed layer neither plants it nor resolves it.
//
// The boot-only name is not on disk anywhere. That is the assertion, not an
// oversight: if the installed layer read spec.skills the render would fail
// looking for it, and a render that succeeds proves the key was never read.
func TestRenderPlantsTheInstalledSkillsAndNotTheBootDirectorys(t *testing.T) {
	skillsDir := t.TempDir()
	fixtureSkill(t, skillsDir, "installed", map[string]string{"SKILL.md": "# Installed\n"})

	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderClaude,
		Spec: fixtureSpec(t, fmt.Sprintf(
			`{"skills": ["boot-only"], "install": {"skills": ["installed"]}, "skills_dir": %s}`,
			strconv.Quote(skillsDir))),
	})
	files, err := Render(lay)
	if err != nil {
		t.Fatalf("Render a profile declaring both skill sets: %v", err)
	}
	if skill := renderedFile(t, files, ".claude/skills/installed/SKILL.md"); string(skill.Content) != "# Installed\n" {
		t.Errorf("the installed skill rendered %q", skill.Content)
	}
	for _, f := range files {
		if strings.HasPrefix(f.Path, ".claude/skills/boot-only/") {
			t.Errorf("Render produced %q; spec.skills belongs to a boot directory", f.Path)
		}
	}

	// And the sweep claims the same one name, so a check does not go looking
	// for the boot directory's skill in the installed layer either.
	plan, err := NewSweepPlan(lay)
	if err != nil {
		t.Fatalf("NewSweepPlan: %v", err)
	}
	if !slices.Equal(plan.Trees, []string{".claude/skills/installed"}) {
		t.Errorf("Trees = %v, want [.claude/skills/installed]", plan.Trees)
	}
}

// TestRenderReportsTheKeyItActuallyRead pins the diagnostic. The two skill
// renders share a body, and an error naming "spec.skills" for a set declared
// under install would send the operator to edit a key they never wrote.
func TestRenderReportsTheKeyItActuallyRead(t *testing.T) {
	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderClaude,
		Spec:     fixtureSpec(t, `{"install": {"skills": ["capture-decision"]}}`),
	})
	_, err := Render(lay)
	if !errors.Is(err, bootdir.ErrSkillsSource) {
		t.Fatalf("Render with no skills_dir = %v, want bootdir.ErrSkillsSource", err)
	}
	if !strings.Contains(err.Error(), "spec.install.skills") {
		t.Errorf("the error does not name the key it read: %v", err)
	}
}

// TestPlanterForUnsupportedProvider checks that the caller receives an error
// rather than an empty layout it might render through.
func TestPlanterForUnsupportedProvider(t *testing.T) {
	for _, p := range []profile.Provider{profile.ProviderOpenCode, "", "nonesuch"} {
		if _, _, err := PlanterFor(p); !errors.Is(err, bootdir.ErrUnsupportedProvider) {
			t.Errorf("PlanterFor(%q) = %v, want bootdir.ErrUnsupportedProvider", p, err)
		}
	}
}

func TestPlanterForCodex(t *testing.T) {
	renderers, layout, err := PlanterFor(profile.ProviderCodex)
	if err != nil {
		t.Fatalf("PlanterFor(%q): %v", profile.ProviderCodex, err)
	}
	if layout.Provider != profile.ProviderCodex {
		t.Errorf("PlanterFor returned a layout for %q", layout.Provider)
	}
	want := []struct {
		artifact string
		fills    bool
	}{
		{CodexDirName + "/" + bootdir.AgentsFileName, false},
		{CodexDirName + "/" + bootdir.CodexConfigFileName, false},
		{bootdir.CodexSkillsDirName, true},
	}
	if len(renderers) != len(want) {
		t.Fatalf("PlanterFor returned %d renderers, want %d", len(renderers), len(want))
	}
	for i, w := range want {
		fills := renderers[i].Fills != nil
		if renderers[i].Artifact != w.artifact || fills != w.fills {
			t.Errorf("renderer %d is %q (fills named subdirectories: %v), want %q (%v)",
				i, renderers[i].Artifact, fills, w.artifact, w.fills)
		}
	}
}

// TestPlanterForReturnsAFreshSlice checks the documented promise that a caller
// may modify what it receives.
func TestPlanterForReturnsAFreshSlice(t *testing.T) {
	first, _, err := PlanterFor(profile.ProviderClaude)
	if err != nil {
		t.Fatalf("PlanterFor(%q): %v", profile.ProviderClaude, err)
	}
	first[0].Artifact = "mutated"
	second, _, err := PlanterFor(profile.ProviderClaude)
	if err != nil {
		t.Fatalf("PlanterFor(%q): %v", profile.ProviderClaude, err)
	}
	if second[0].Artifact != bootdir.AgentsFileName {
		t.Errorf("PlanterFor returned a shared slice: the second call sees %q", second[0].Artifact)
	}
}
