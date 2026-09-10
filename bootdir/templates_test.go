package bootdir

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/chrispian/cairn/profile"
)

// templateInstance returns an instance carrying resolved template text.
func templateInstance(t *testing.T, templates map[string]string) *Instance {
	t.Helper()
	inst := testInstance(t, profile.Resolved{ID: "engineer"})
	inst.Templates = templates
	return inst
}

// TestTemplatesRenderWhereTheProfileSaid is the shape of the whole model. Cairn
// names no file: the destinations came out of the manifest, and a profile that
// wants three documents in three places gets them.
func TestTemplatesRenderWhereTheProfileSaid(t *testing.T) {
	inst := templateInstance(t, map[string]string{
		"AGENTS.md":               "the instruction file\n",
		"docs/how-we-work.md":     "somewhere else entirely\n",
		"deeply/nested/thing.txt": "not even markdown\n",
	})

	files, err := renderTemplates(inst)
	if err != nil {
		t.Fatalf("renderTemplates(): %v", err)
	}
	want := []string{"AGENTS.md", "deeply/nested/thing.txt", "docs/how-we-work.md"}
	if got := filePaths(files); !slices.Equal(got, want) {
		t.Fatalf("rendered %v, want %v in sorted order", got, want)
	}
	for _, f := range files {
		if got, want := string(f.Content), inst.Templates[f.Path]; got != want {
			t.Errorf("%s holds %q, want %q", f.Path, got, want)
		}
	}
}

// TestATemplateIsTheOnlyWayToGetProse is the reversal this model makes, stated
// as a test because it overturns a documented rule.
//
// The instruction file used to be rendered always, empty if a profile declared
// nothing, on the reasoning that a boot directory missing one looks like a
// render that stopped halfway. Under templates there is no file cairn insists
// on: a profile that declares no template gets no prose, because a document
// cairn wrote for an operator who did not ask for one is content, and cairn
// ships no content.
func TestATemplateIsTheOnlyWayToGetProse(t *testing.T) {
	files, err := Render(testInstance(t, profile.Resolved{ID: "engineer"}))
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}
	if len(files) != 0 {
		t.Errorf("a profile declaring nothing rendered %v", filePaths(files))
	}
}

// TestATemplateThatSubstitutesAwayRendersNoFile covers the document whose every
// marker filled nothing. An empty file is a profile saying something and
// meaning nothing by it; an absent one is the profile not having said it.
//
// [Substitute] takes each vanished marker's own line with it, which leaves the
// author's blank lines around them rather than nothing at all. This trim is
// what turns that residue into no file, so it stays even though the rendering
// it sees is shorter than it used to be.
func TestATemplateThatSubstitutesAwayRendersNoFile(t *testing.T) {
	inst := templateInstance(t, map[string]string{"AGENTS.md": "\n<!-- cairn:slot memory -->\n\n"})

	files, err := renderTemplates(inst)
	if err != nil {
		t.Fatalf("renderTemplates(): %v", err)
	}
	if len(files) != 0 {
		t.Errorf("a template that filled nothing rendered %v", filePaths(files))
	}
}

// TestTemplatesEndInOneNewline covers the one edit substitution makes to a
// document. A text file ends in a newline, and whether the operator's last
// marker filled one is not something the file's final byte should depend on.
func TestTemplatesEndInOneNewline(t *testing.T) {
	inst := templateInstance(t, map[string]string{"AGENTS.md": "no trailing newline"})

	files, err := renderTemplates(inst)
	if err != nil {
		t.Fatalf("renderTemplates(): %v", err)
	}
	if got := string(files[0].Content); got != "no trailing newline\n" {
		t.Errorf("rendered %q, want one trailing newline", got)
	}
}

// TestTemplatesAreByteStable states the determinism contract for the artifact
// whose input is a map: same instance, same bytes, same order.
func TestTemplatesAreByteStable(t *testing.T) {
	inst := templateInstance(t, map[string]string{
		"a.md": "<!-- cairn:value profile --> and <!-- cairn:slot one -->\n",
		"b.md": "<!-- cairn:slot two -->\n",
		"c.md": "plain\n",
	})
	inst.Sections = map[string]string{"one": "## One\n\nfirst", "two": "## Two\n\nsecond"}
	inst.Values = map[string]string{"profile": "engineer"}

	first, err := renderTemplates(inst)
	if err != nil {
		t.Fatalf("renderTemplates(): %v", err)
	}
	for i := range 16 {
		again, err := renderTemplates(inst)
		if err != nil {
			t.Fatalf("renderTemplates() on render %d: %v", i, err)
		}
		for j := range again {
			if again[j].Path != first[j].Path || string(again[j].Content) != string(first[j].Content) {
				t.Fatalf("render %d changed %s", i, first[j].Path)
			}
		}
	}
}

// TestTemplatesRefuseAMarkerTheyCannotActOn names the template a bad marker was
// written in. The substitution error knows the marker and not the file, and a
// profile with a dozen templates needs to be told which one to open.
func TestTemplatesRefuseAMarkerTheyCannotActOn(t *testing.T) {
	inst := templateInstance(t, map[string]string{"docs/guide.md": "<!-- cairn:section repo -->"})

	_, err := renderTemplates(inst)
	if !errors.Is(err, ErrMarker) {
		t.Fatalf("renderTemplates() = %v, want ErrMarker", err)
	}
	if !strings.Contains(err.Error(), "docs/guide.md") {
		t.Errorf("the refusal %q does not name the template", err)
	}
}

// TestTemplatesArriveResolvedRatherThanFromTheManifest states where the text
// comes from, which is the half of this renderer that is easy to get wrong. A
// template's value may be a slot source rather than a literal, and resolving
// one runs commands and makes requests — which a renderer may not do.
func TestTemplatesArriveResolvedRatherThanFromTheManifest(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID: "engineer",
		Spec: testSpec(t, `{"templates": {"AGENTS.md":
			{"kind":"static_file","static_file":{"path":"/never/read/by/a/renderer.md"}}}}`),
	})
	inst.Templates = map[string]string{"AGENTS.md": "the resolved text\n"}

	files, err := renderTemplates(inst)
	if err != nil {
		t.Fatalf("renderTemplates(): %v", err)
	}
	if got := string(fileByPath(t, files, "AGENTS.md").Content); got != "the resolved text\n" {
		t.Errorf("AGENTS.md holds %q, want the resolved text", got)
	}
}

// TestTheInstalledLayerRendersTwoDestinations covers the one place a template's
// destination is not where it lands.
//
// `install --check` derives what it may report on from the renderer
// registration rather than from a render, which is what lets it find a file
// left by a profile that stopped declaring one. A template free to name any
// path in the operator's home would make that set depend on the profile being
// checked — in exactly the case the check exists for. So the installed layer
// renders two destinations at paths of its own and ignores the rest.
func TestTheInstalledLayerRendersTwoDestinations(t *testing.T) {
	inst := templateInstance(t, map[string]string{
		"AGENTS.md":  "the instruction file\n",
		"CLAUDE.md":  "@" + "AGENTS.md" + "\n",
		"boot.md":    "a boot-directory destination\n",
		"notes/x.md": "another one\n",
	})
	// The destination each artifact renders from is the tree's to name, so a
	// layout carries it beside the path it lands at. Cairn does not know that
	// an instruction file is called AGENTS.md; this fixture is standing in for
	// the document that says so.
	inst.Layout = Layout{
		Provider: profile.ProviderClaude,
		Agents:   Artifact{Dest: "AGENTS.md", RelPath: ".claude/AGENTS.md"},
		Pointer:  Artifact{Dest: "CLAUDE.md", RelPath: ".claude/CLAUDE.md"},
	}

	agents, err := RenderAgentsTemplate(inst)
	if err != nil {
		t.Fatalf("RenderAgentsTemplate(): %v", err)
	}
	if got := filePaths(agents); !slices.Equal(got, []string{".claude/AGENTS.md"}) {
		t.Errorf("the instruction file rendered at %v", got)
	}
	pointer, err := RenderPointerTemplate(inst)
	if err != nil {
		t.Fatalf("RenderPointerTemplate(): %v", err)
	}
	if got := filePaths(pointer); !slices.Equal(got, []string{".claude/CLAUDE.md"}) {
		t.Errorf("the pointer rendered at %v", got)
	}
}

// TestTheInstalledLayerRefusesADestinationItHasNoPathFor covers a layout that
// names a destination and no path to put it at. Reporting it is the rule for
// every other artifact — content declared with nowhere to put it is a render
// that would look complete and not be.
//
// A tree that lists no agents artifact at all is a different case and not a
// fault: nothing registers the renderer, so nothing is rendered, the same way
// the Codex tree carries no pointer. What a document cannot do is declare the
// artifact and leave it without a path, because parsing refuses that outright.
func TestTheInstalledLayerRefusesADestinationItHasNoPathFor(t *testing.T) {
	inst := templateInstance(t, map[string]string{"AGENTS.md": "declared\n"})
	inst.Layout = Layout{Provider: profile.ProviderClaude, Agents: Artifact{Dest: "AGENTS.md"}}

	if _, err := RenderAgentsTemplate(inst); !errors.Is(err, ErrProviderLayout) {
		t.Fatalf("RenderAgentsTemplate() = %v, want ErrProviderLayout", err)
	}
}
