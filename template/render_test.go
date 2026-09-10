package template_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/chrispian/cairn/template"
)

// layouts is a [template.LayoutLoader] over a map of unparsed layout text.
func layouts(t *testing.T, m map[string]string) template.LayoutLoader {
	t.Helper()
	return template.LayoutFunc(func(name string) (*template.Document, error) {
		text, ok := m[name]
		if !ok {
			return nil, template.ErrLayout
		}
		return template.Parse(name, text)
	})
}

// parse is Parse with the error fataled, for the many tests that are not about
// parsing.
func parse(t *testing.T, name, text string) *template.Document {
	t.Helper()
	doc, err := template.Parse(name, text)
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return doc
}

// The shape from the decision: a layout yields, a profile sections, and the
// rendered document is the layout filled in the layout's own order.
func TestRender_ProfileFillsALayout(t *testing.T) {
	lay := layouts(t, map[string]string{
		"agent": "{{ yield charter }}\n\n{{ yield lens }}\n\n{{ yield context }}\n",
	})
	doc := parse(t, "engineer", strings.Join([]string{
		"{{ extends agent }}",
		"",
		"{{ section context }}",
		"## Repository",
		"{{ cmd: git status }}",
		"{{ end }}",
		"",
		"{{ section charter }}",
		"# Engineer",
		"You implement one task end to end.",
		"{{ end }}",
	}, "\n"))

	got, err := template.Render([]*template.Document{doc}, template.Options{
		Layouts: lay,
		Sources: map[string]string{"engineer#0": "## main\n M render.go"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := strings.Join([]string{
		"# Engineer",
		"You implement one task end to end.",
		"",
		"## Repository",
		"## main",
		" M render.go",
		"",
	}, "\n")
	if got.Text != want {
		t.Errorf("Render:\n%q\nwant:\n%q", got.Text, want)
	}
	// The layout's order wins, not the profile's declaration order, and the
	// unyielded lens takes its blank line with it.
	if strings.Index(got.Text, "# Engineer") > strings.Index(got.Text, "## Repository") {
		t.Error("sections rendered in the profile's order, not the layout's")
	}
}

// Empty collapses: a source producing nothing takes its tag and the blank line
// around it, and the surviving text is not reflowed.
func TestRender_EmptyCollapses(t *testing.T) {
	cases := []struct {
		name, text, want string
		values           map[string]string
	}{
		{
			name: "a tag alone on a line takes the line and the gap",
			text: "before\n\n{{ value scope }}\n\nafter\n",
			want: "before\n\nafter\n",
		},
		{
			name: "a tag sharing a line keeps the line and loses its whitespace",
			text: "- scope: {{ value scope }}\n",
			want: "- scope:\n",
		},
		{
			name:   "a filled tag renders in place",
			text:   "- scope: {{ value scope }}\n",
			want:   "- scope: ~/dev\n",
			values: map[string]string{"scope": "~/dev"},
		},
		{
			name: "consecutive empty tags collapse to nothing",
			text: "before\n\n{{ value a }}\n\n{{ value b }}\n\n{{ value c }}\n\nafter\n",
			want: "before\n\nafter\n",
		},
		{
			name: "a document that collapses entirely renders nothing",
			text: "{{ value a }}\n\n{{ value b }}\n",
			want: "",
		},
		{
			name: "blank space no removal touched is left as authored",
			text: "one\n\n\n\ntwo\n",
			want: "one\n\n\n\ntwo\n",
		},
		{
			name: "a fenced block's own blank lines survive a removal elsewhere",
			text: "```\na\n\n\nb\n```\n\n{{ value gone }}\n\nafter\n",
			want: "```\na\n\n\nb\n```\n\nafter\n",
		},
		{
			name: "a leading empty tag leaves no leading blank line",
			text: "{{ value gone }}\n\nfirst\n",
			want: "first\n",
		},
		{
			name: "a trailing empty tag leaves no trailing blank line",
			text: "last\n\n{{ value gone }}\n",
			want: "last\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := tc.values
			if values == nil {
				// Declared and empty, so nothing is reported as unknown: an
				// empty value is a fact about the instance.
				values = map[string]string{"scope": "", "a": "", "b": "", "c": "", "gone": ""}
			}
			got, err := template.Render([]*template.Document{parse(t, "p", tc.text)},
				template.Options{Values: values})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if got.Text != tc.want {
				t.Errorf("Render(%q) =\n%q\nwant:\n%q", tc.text, got.Text, tc.want)
			}
		})
	}
}

// A markdown hard break is two trailing spaces, and a line no tag vanished
// from keeps them.
func TestRender_HardBreakSurvives(t *testing.T) {
	got, err := template.Render(
		[]*template.Document{parse(t, "p", "one  \ntwo\n")}, template.Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got.Text != "one  \ntwo\n" {
		t.Errorf("Render = %q, want the two trailing spaces kept", got.Text)
	}
}

// Sections merge closest-wins, which is the cascade's only rule.
func TestRender_SectionsReplaceClosestWins(t *testing.T) {
	lay := layouts(t, map[string]string{"agent": "{{ yield charter }}\n"})
	base := parse(t, "base", "{{ extends agent }}\n{{ section charter }}base charter\n{{ end }}\n")
	leaf := parse(t, "engineer", "{{ extends agent }}\n{{ section charter }}engineer charter\n{{ end }}\n")

	got, err := template.Render([]*template.Document{base, leaf}, template.Options{Layouts: lay})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got.Text != "engineer charter\n" {
		t.Errorf("Render = %q, want the leaf's section alone", got.Text)
	}
}

// {{ parent }} is how a section adds to an ancestor's instead of replacing it.
func TestRender_ParentAdds(t *testing.T) {
	lay := layouts(t, map[string]string{"agent": "{{ yield charter }}\n"})
	base := parse(t, "base", "{{ extends agent }}\n{{ section charter }}base charter\n{{ end }}\n")
	leaf := parse(t, "engineer",
		"{{ extends agent }}\n{{ section charter }}{{ parent }}\n\nand also the engineer's\n{{ end }}\n")

	got, err := template.Render([]*template.Document{base, leaf}, template.Options{Layouts: lay})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "base charter\n\nand also the engineer's\n"
	if got.Text != want {
		t.Errorf("Render = %q, want %q", got.Text, want)
	}
}

// A {{ parent }} with no ancestor content collapses like any other empty tag.
func TestRender_ParentWithNoAncestorCollapses(t *testing.T) {
	lay := layouts(t, map[string]string{"agent": "{{ yield charter }}\n"})
	leaf := parse(t, "engineer",
		"{{ extends agent }}\n{{ section charter }}{{ parent }}\n\nonly the engineer's\n{{ end }}\n")

	got, err := template.Render([]*template.Document{leaf}, template.Options{Layouts: lay})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got.Text != "only the engineer's\n" {
		t.Errorf("Render = %q, want no leading blank line", got.Text)
	}
}

// A layout may extend a layout: the outermost is the frame and the inner one
// carries defaults.
func TestRender_LayoutChain(t *testing.T) {
	lay := layouts(t, map[string]string{
		"frame": "# {{ yield title }}\n\n{{ yield charter }}\n\n---\n{{ yield footer }}\n",
		"agent": "{{ extends frame }}\n{{ section footer }}rendered by cairn\n{{ end }}\n",
	})
	doc := parse(t, "engineer", strings.Join([]string{
		"{{ extends agent }}",
		"{{ section title }}Engineer{{ end }}",
		"{{ section charter }}You implement one task.{{ end }}",
	}, "\n"))

	got, err := template.Render([]*template.Document{doc}, template.Options{Layouts: lay})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "# Engineer\n\nYou implement one task.\n\n---\nrendered by cairn\n"
	if got.Text != want {
		t.Errorf("Render =\n%q\nwant:\n%q", got.Text, want)
	}
}

// A profile overrides a layout's default section.
func TestRender_ProfileBeatsLayoutDefault(t *testing.T) {
	lay := layouts(t, map[string]string{
		"agent": "{{ section charter }}the default charter\n{{ end }}\n",
	})
	doc := parse(t, "engineer",
		"{{ extends agent }}\n{{ section charter }}the engineer's charter\n{{ end }}\n")

	got, err := template.Render([]*template.Document{doc}, template.Options{Layouts: lay})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got.Text != "the engineer's charter\n" {
		t.Errorf("Render = %q, want the profile's section", got.Text)
	}
}

// A document that extends nothing is its own frame, so the simplest possible
// profile — prose and no tags — renders exactly its prose.
func TestRender_NoLayoutRendersTheBody(t *testing.T) {
	doc := parse(t, "p", "Write user-facing documentation only.\n")
	got, err := template.Render([]*template.Document{doc}, template.Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got.Text != "Write user-facing documentation only.\n" {
		t.Errorf("Render = %q, want the body verbatim", got.Text)
	}
}

// The rendering document is the closest that declares extends, so a composed
// part with prose of its own does not take over the leaf's shape.
func TestRender_ExtendsDecidesTheFrame(t *testing.T) {
	lay := layouts(t, map[string]string{"agent": "{{ yield charter }}\n{{ yield direction }}\n"})
	leaf := parse(t, "engineer",
		"{{ extends agent }}\n{{ section charter }}# Engineer{{ end }}\n")
	part := parse(t, "docs-only", "{{ section direction }}Documentation only.{{ end }}\n")

	got, err := template.Render([]*template.Document{leaf, part}, template.Options{Layouts: lay})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "# Engineer\nDocumentation only.\n"
	if got.Text != want {
		t.Errorf("Render = %q, want %q", got.Text, want)
	}
}

// Prose that does not render is reported rather than passed over.
func TestRender_ReportsDroppedProse(t *testing.T) {
	lay := layouts(t, map[string]string{"agent": "{{ yield charter }}\n"})
	leaf := parse(t, "engineer",
		"{{ extends agent }}\n{{ section charter }}# Engineer{{ end }}\n")
	part := parse(t, "docs-only", "Documentation only, and in no section.\n")

	var lines []string
	_, err := template.Render([]*template.Document{leaf, part}, template.Options{
		Layouts: lay,
		Report:  func(s string) { lines = append(lines, s) },
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !containsSubstring(lines, "docs-only: prose outside a section is not rendered") {
		t.Errorf("reported %q, want the dropped prose named", lines)
	}
}

// A section nobody yields is content an author wrote and no document carries.
func TestRender_ReportsUnusedSection(t *testing.T) {
	lay := layouts(t, map[string]string{"agent": "{{ yield charter }}\n"})
	doc := parse(t, "engineer", strings.Join([]string{
		"{{ extends agent }}",
		"{{ section charter }}# Engineer{{ end }}",
		"{{ section lens }}You are skeptical.{{ end }}",
	}, "\n"))

	var lines []string
	got, err := template.Render([]*template.Document{doc}, template.Options{
		Layouts: lay,
		Report:  func(s string) { lines = append(lines, s) },
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Join(got.Unused, ",") != "lens" {
		t.Errorf("Unused = %v, want [lens]", got.Unused)
	}
	if !containsSubstring(lines, `yields it nowhere`) {
		t.Errorf("reported %q, want the unyielded section named", lines)
	}
}

// A value the instance does not carry renders nothing and is reported; one it
// carries as the empty string is silent.
func TestRender_ValueReporting(t *testing.T) {
	doc := parse(t, "p", "- a: {{ value known }}\n- b: {{ value typo }}\n")
	var lines []string
	got, err := template.Render([]*template.Document{doc}, template.Options{
		Values: map[string]string{"known": ""},
		Report: func(s string) { lines = append(lines, s) },
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got.Text != "- a:\n- b:\n" {
		t.Errorf("Render = %q", got.Text)
	}
	// One line and not two: the unknown name is reported and the declared
	// empty one is silent, because an empty value is a fact about the instance
	// rather than something that went wrong.
	if len(lines) != 1 {
		t.Fatalf("reported %d lines, want 1: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "typo") {
		t.Errorf("report = %q, want it to name the unknown value", lines[0])
	}
}

// A source that produced no text is reported: the section is missing from the
// file and nothing in the file says so.
func TestRender_ReportsEmptySource(t *testing.T) {
	doc := parse(t, "p", "{{ cmd: git status }}\n")
	var lines []string
	got, err := template.Render([]*template.Document{doc}, template.Options{
		Report: func(s string) { lines = append(lines, s) },
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got.Text != "" {
		t.Errorf("Render = %q, want nothing", got.Text)
	}
	if !containsSubstring(lines, "produced no text") {
		t.Errorf("reported %q, want the empty source named", lines)
	}
}

func TestRender_LayoutErrors(t *testing.T) {
	t.Run("missing layout", func(t *testing.T) {
		doc := parse(t, "p", "{{ extends nope }}\n")
		_, err := template.Render([]*template.Document{doc},
			template.Options{Layouts: layouts(t, nil)})
		if !errors.Is(err, template.ErrLayout) {
			t.Errorf("err = %v, want ErrLayout", err)
		}
	})
	t.Run("cycle", func(t *testing.T) {
		lay := layouts(t, map[string]string{
			"a": "{{ extends b }}\n",
			"b": "{{ extends a }}\n",
		})
		doc := parse(t, "p", "{{ extends a }}\n")
		_, err := template.Render([]*template.Document{doc}, template.Options{Layouts: lay})
		if !errors.Is(err, template.ErrLayoutCycle) {
			t.Errorf("err = %v, want ErrLayoutCycle", err)
		}
	})
	t.Run("nil loader with an extends", func(t *testing.T) {
		doc := parse(t, "p", "{{ extends a }}\n")
		if _, err := template.Render([]*template.Document{doc}, template.Options{}); err == nil {
			t.Error("Render with a nil loader = nil error, want one")
		}
	})
}

// An empty chain, and a chain of bodiless profiles, render nothing rather than
// failing: a profile with no body is an ordinary member of a cascade.
func TestRender_EmptyChain(t *testing.T) {
	for _, docs := range [][]*template.Document{nil, {nil}, {parse(t, "p", "")}} {
		got, err := template.Render(docs, template.Options{})
		if err != nil {
			t.Fatalf("Render(%v): %v", docs, err)
		}
		if got.Text != "" {
			t.Errorf("Render = %q, want nothing", got.Text)
		}
	}
}

// {{ parent }} outside a section is refused rather than rendered as nothing.
func TestRender_ParentOutsideASection(t *testing.T) {
	doc := parse(t, "p", "{{ parent }}\n")
	_, err := template.Render([]*template.Document{doc}, template.Options{})
	if !errors.Is(err, template.ErrSyntax) {
		t.Errorf("err = %v, want ErrSyntax", err)
	}
}

// Sources are collected across a whole chain, each with its own id, so two
// documents writing the same command are hydrated separately.
func TestSources_AcrossAChain(t *testing.T) {
	a := parse(t, "a", "{{ cmd: git status }}\n{{ cmd: git log }}\n")
	b := parse(t, "b", "{{ section s }}{{ cmd: git status }}{{ end }}\n")
	srcs := template.Sources(a, b)
	if len(srcs) != 3 {
		t.Fatalf("Sources = %d, want 3", len(srcs))
	}
	ids := map[string]bool{}
	for _, s := range srcs {
		if ids[s.ID] {
			t.Errorf("duplicate source id %q", s.ID)
		}
		ids[s.ID] = true
	}
	if srcs[2].Document != "b" {
		t.Errorf("third source came from %q, want b", srcs[2].Document)
	}
}

func containsSubstring(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

// A document holding nothing but section declarations is a fragment: it
// declares content and no shape, so it never becomes the frame even when it is
// the closest document in the chain.
//
// This is the case a composed part is. Without the rule, `--with docs-only`
// would hand the shape of the whole document to the part, because closest-wins
// makes a part closer than the profile being booted.
func TestRender_AFragmentIsNeverTheFrame(t *testing.T) {
	lay := layouts(t, map[string]string{"agent": "{{ yield charter }}\n{{ yield direction }}\n"})
	leaf := parse(t, "engineer", strings.Join([]string{
		"{{ extends agent }}",
		"{{ section charter }}# Engineer{{ end }}",
	}, "\n"))
	// No {{ extends }}, and nothing outside its one section.
	part := parse(t, "docs-only", "{{ section direction }}Documentation only.{{ end }}\n")

	got, err := template.Render([]*template.Document{leaf, part}, template.Options{Layouts: lay})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if want := "# Engineer\nDocumentation only.\n"; got.Text != want {
		t.Errorf("Render = %q, want %q", got.Text, want)
	}
}

// A chain of nothing but fragments has content and no shape, so it renders
// nothing — and says which sections nobody could carry.
func TestRender_AllFragmentsRenderNothingAndSaySo(t *testing.T) {
	a := parse(t, "docs-only", "{{ section direction }}Documentation only.{{ end }}\n")
	b := parse(t, "quiet", "{{ section tone }}Be brief.{{ end }}\n")

	var lines []string
	got, err := template.Render([]*template.Document{a, b}, template.Options{
		Report: func(s string) { lines = append(lines, s) },
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got.Text != "" {
		t.Errorf("Render = %q, want nothing", got.Text)
	}
	if strings.Join(got.Unused, ",") != "direction,tone" {
		t.Errorf("Unused = %v, want [direction tone]", got.Unused)
	}
	if len(lines) != 2 {
		t.Errorf("reported %q, want a line per unyielded section", lines)
	}
}

// A layout whose entire content is one tag has shape. It reads like a
// degenerate case and is the common one: a layout is mostly yields.
func TestRender_AOneTagDocumentHasShape(t *testing.T) {
	for _, text := range []string{"{{ value profile }}\n", "{{ cmd: git status }}\n"} {
		got, err := template.Render([]*template.Document{parse(t, "p", text)}, template.Options{
			Values:  map[string]string{"profile": "engineer"},
			Sources: map[string]string{"p#0": "## main"},
		})
		if err != nil {
			t.Fatalf("Render(%q): %v", text, err)
		}
		if got.Text == "" {
			t.Errorf("Render(%q) = nothing, want the tag's content", text)
		}
	}
}
