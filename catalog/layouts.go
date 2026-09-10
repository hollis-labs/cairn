package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TemplatesDir is the bundle subdirectory holding reusable template content.
//
// It is a components catalog rather than a required parallel tree: prose lives
// in the profile that owns it, and moves here when a second profile needs it.
// Extraction is a refactor and not a requirement, which is why nothing in
// cairn reads this directory unless a profile names something in it.
const TemplatesDir = "templates"

// LayoutsDir is the subdirectory of [TemplatesDir] holding layouts — the
// documents a profile's `{{ extends }}` names.
//
// A layout is a document with holes in it and no frontmatter, so it is not a
// profile and does not live with them. It is one directory and one level, for
// the reason [PartsDir] is: a name is a stem, so `layouts/agent.md` is the
// layout `agent` and never `layouts/agent`.
const LayoutsDir = "layouts"

// ErrLayoutName reports a name that cannot be a layout's, because it cannot be
// the base name of a file in the layouts directory.
var ErrLayoutName = errors.New("not a layout name")

// readLayouts reads every layout the bundle holds, keyed by file stem.
//
// A bundle with no templates directory, and one with no layouts directory
// inside it, both hold no layouts and neither is an error. A layout is only
// needed by a profile that names one, and that profile's `{{ extends }}` is
// where the absence is reported — with the name it was looking for, which is
// the diagnostic worth having. Refusing at Open would refuse every bundle
// whose profiles compose no layout at all, which is every bundle that has not
// migrated yet.
func readLayouts(root string) (map[string]string, error) {
	dir := filepath.Join(root, TemplatesDir, LayoutsDir)
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		// Descends into nothing and refuses nothing, exactly as the profiles
		// directory treats a file that is not a profile: a README beside the
		// layouts is not a broken layout.
		if entry.IsDir() || filepath.Ext(entry.Name()) != profileExt {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		text, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		out[strings.TrimSuffix(entry.Name(), profileExt)] = string(text)
	}
	return out, nil
}

// LayoutText returns the unparsed text of the layout stored under name, and
// whether the bundle holds one.
//
// Unparsed on purpose. This package reads files and this one does exactly
// that; what the text means belongs to
// [github.com/chrispian/cairn/template], and a catalog that parsed it would
// be a second place where a template's syntax is known.
func (c *Catalog) LayoutText(name string) (string, bool) {
	text, ok := c.layouts[strings.TrimSpace(name)]
	return text, ok
}

// Layouts returns the name of every layout in the bundle, sorted. It is what
// a diagnostic naming the set reads, and what `cairn list` would enumerate.
func (c *Catalog) Layouts() []string { return append([]string(nil), c.layoutNames...) }

// LayoutPath returns the file a layout of that name is read from, refusing a
// name that cannot be one. It is for a diagnostic that has to say where cairn
// looked.
func (c *Catalog) LayoutPath(name string) (string, error) {
	clean := strings.TrimSpace(name)
	switch {
	case clean == "":
		return "", fmt.Errorf("%w: the name is empty", ErrLayoutName)
	case strings.ContainsRune(clean, '/'), strings.ContainsRune(clean, filepath.Separator):
		return "", fmt.Errorf("%w: %q holds a path separator, and a layout name is a stem",
			ErrLayoutName, name)
	case clean == "." || clean == "..":
		return "", fmt.Errorf("%w: %q", ErrLayoutName, name)
	}
	return filepath.Join(c.root, TemplatesDir, LayoutsDir, clean+profileExt), nil
}
