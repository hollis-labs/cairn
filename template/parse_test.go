package template_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/chrispian/cairn/template"
)

func TestParse_Tags(t *testing.T) {
	doc, err := template.Parse("engineer", strings.Join([]string{
		"{{ extends agent }}",
		"",
		"{{ section charter }}",
		"# Engineer",
		"{{ end }}",
		"",
		"{{ section context }}",
		"{{ cmd: git status --short --branch }}",
		"{{ end }}",
	}, "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Extends != "agent" {
		t.Errorf("Extends = %q, want %q", doc.Extends, "agent")
	}

	var sections []string
	for _, n := range doc.Nodes {
		if n.Kind == template.NodeSection {
			sections = append(sections, n.Name)
		}
	}
	if got, want := strings.Join(sections, ","), "charter,context"; got != want {
		t.Errorf("sections = %q, want %q", got, want)
	}

	srcs := template.Sources(doc)
	if len(srcs) != 1 {
		t.Fatalf("Sources = %d, want 1", len(srcs))
	}
	if srcs[0].Kind != "cmd" {
		t.Errorf("Kind = %q, want %q", srcs[0].Kind, "cmd")
	}
	if want := "git status --short --branch"; srcs[0].Arg != want {
		t.Errorf("Arg = %q, want %q", srcs[0].Arg, want)
	}
	if srcs[0].Document != "engineer" {
		t.Errorf("Document = %q, want %q", srcs[0].Document, "engineer")
	}
}

// A source's kind is carried and never checked, which is what keeps a
// predefined vocabulary out of the engine.
func TestParse_SourceKindIsNotAVocabulary(t *testing.T) {
	doc, err := template.Parse("p", "{{ tesseract: recall cairn }}\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	srcs := template.Sources(doc)
	if len(srcs) != 1 || srcs[0].Kind != "tesseract" {
		t.Fatalf("Sources = %+v, want one source of kind tesseract", srcs)
	}
}

func TestParse_Errors(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"unclosed tag", "{{ yield charter\nmore text\n", "never closed"},
		{"unknown verb", "{{ include charter }}\n", `declares the verb "include"`},
		{"empty tag", "{{ }}\n", "is empty"},
		{"verb with no name", "{{ yield }}\n", "a verb and one name"},
		{"bad name", "{{ yield charter! }}\n", "is not a name"},
		{"unclosed section", "{{ section charter }}\nprose\n", "never closed"},
		{"stray end", "prose\n{{ end }}\n", "closes nothing"},
		{"nested section", "{{ section a }}{{ section b }}{{ end }}{{ end }}\n", "does not nest"},
		{"two extends", "{{ extends a }}\n{{ extends b }}\n", "composes into one layout"},
		{"parent takes no name", "{{ parent charter }}\n", "takes no name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := template.Parse("p", tc.text)
			if err == nil {
				t.Fatalf("Parse(%q) = nil error, want one", tc.text)
			}
			if !errors.Is(err, template.ErrSyntax) {
				t.Errorf("error does not wrap ErrSyntax: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// An unclosed tag is reported at the line it was written on, and does not
// swallow a close three lines down.
func TestParse_UnclosedTagStopsAtTheLine(t *testing.T) {
	_, err := template.Parse("p", "one\ntwo {{ yield a\nthree\nfour }}\n")
	if err == nil {
		t.Fatal("Parse = nil error, want one")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error = %v, want it to name line 2", err)
	}
}

func TestParse_NeedsAName(t *testing.T) {
	if _, err := template.Parse("  ", "text\n"); err == nil {
		t.Fatal("Parse with no document name = nil error, want one")
	}
}
