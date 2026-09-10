package catalog

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

// layoutBundle writes a bundle holding one profile and the named layouts.
func layoutBundle(t *testing.T, layouts map[string]string) string {
	t.Helper()
	root := t.TempDir()
	writeProfileFile(t, root, "p", "---\nid: p\n---\n")
	for name, text := range layouts {
		writeFile(t, filepath.Join(root, TemplatesDir, LayoutsDir, name), text)
	}
	return root
}

func TestOpenReadsLayouts(t *testing.T) {
	root := layoutBundle(t, map[string]string{
		"agent.md": "{{ yield charter }}\n",
		"frame.md": "{{ yield body }}\n",
		// Not a layout and not a broken one: the profiles directory treats a
		// README beside the profiles the same way.
		"README":    "notes about the layouts\n",
		"notes.txt": "also not a layout\n",
	})

	c, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if want := []string{"agent", "frame"}; !slices.Equal(c.Layouts(), want) {
		t.Errorf("Layouts() = %v, want %v", c.Layouts(), want)
	}
	text, ok := c.LayoutText("agent")
	if !ok || text != "{{ yield charter }}\n" {
		t.Errorf("LayoutText(agent) = %q, %v", text, ok)
	}
	// Unparsed. What the text means belongs to package template, and a
	// catalog that parsed it would be a second place the syntax is known.
	if _, ok := c.LayoutText("nope"); ok {
		t.Error("LayoutText for a name the bundle does not hold reported one")
	}
}

// A bundle with no templates directory, and one with no layouts directory
// inside it, both hold no layouts and neither is an error: a layout is only
// needed by a profile that names one, and that profile's {{ extends }} is
// where the absence is reported.
func TestOpenWithNoLayoutsIsNotAnError(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "p", "---\nid: p\n---\n")

	c, err := Open(root)
	if err != nil {
		t.Fatalf("Open a bundle with no layouts: %v", err)
	}
	if len(c.Layouts()) != 0 {
		t.Errorf("Layouts() = %v, want none", c.Layouts())
	}
}

func TestLayoutPath(t *testing.T) {
	root := layoutBundle(t, map[string]string{"agent.md": "x\n"})
	c, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	got, err := c.LayoutPath("agent")
	if err != nil {
		t.Fatalf("LayoutPath: %v", err)
	}
	if want := filepath.Join(root, TemplatesDir, LayoutsDir, "agent.md"); got != want {
		t.Errorf("LayoutPath = %q, want %q", got, want)
	}

	// A name is a stem, so a separator in one is refused rather than joined:
	// a layout named "../../etc/passwd" would otherwise be a path.
	for _, bad := range []string{"", "  ", "sub/agent", ".", ".."} {
		if _, err := c.LayoutPath(bad); !errors.Is(err, ErrLayoutName) {
			t.Errorf("LayoutPath(%q) = %v, want ErrLayoutName", bad, err)
		}
	}
}
