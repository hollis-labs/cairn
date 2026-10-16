package bootdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cairn/profile"
)

// contentKind is one collection of authored content a boot directory carries:
// the manifest keys that declare it and name its directory, the nouns a
// diagnostic calls it by, and the sentinels a refusal wraps.
//
// Skills and prompts are the two, and everything they share is written once
// against this — resolving the source directory, and checking that a declared
// name is one entry directly beneath it. The second of those is a containment
// rule, and a containment rule with two copies is one relaxation away from a
// name that escapes its directory.
//
// What is not shared is what each collection is: a skill is a directory copied
// whole, a prompt is one file substituted. Those live in their own renderers.
type contentKind struct {
	// key is the manifest key holding the declared names, spelled the way a
	// diagnostic names it — "skills", "install.skills", "prompts".
	key string

	// dirKey is the manifest key naming the directory they are read from.
	dirKey string

	// plural is what a collection of them is called, for the sentence saying
	// cairn ships none of its own.
	plural string

	// entry is what one declared name has to name: "skill directory",
	// "prompt file".
	entry string

	// nameErr and sourceErr are the sentinels a bad name and an unusable
	// directory wrap.
	nameErr   error
	sourceErr error
}

// contentSource resolves the directory a content collection is read from, from
// the raw value declared under c.dirKey.
//
// Variables and a leading "~/" are expanded, the same way a tree's source is
// and for the same reason: a content directory is a location on the operator's
// machine, and writing it out in full in every profile is how it goes stale.
// Both the environment and home are the instance's rather than the process's,
// so that a renderer reads nothing outside the instance it was handed.
//
// The result must be absolute: a relative path would resolve against whatever
// directory cairn happened to be invoked from, which is not a property of the
// profile.
func contentSource(declared string, c contentKind, names []string, home string, look profile.Expander) (string, error) {
	raw := strings.TrimSpace(declared)
	if raw == "" {
		return "", fmt.Errorf(
			"%w: spec.%s declares %s, but spec.%s is not set and cairn ships no %s of its own",
			c.sourceErr, c.key, quotedNames(names), c.dirKey, c.plural)
	}
	dir, err := profile.ExpandPath(raw, home, look)
	if err != nil {
		return "", fmt.Errorf("%w: spec.%s: %w", c.sourceErr, c.dirKey, err)
	}
	named := profile.QuotedExpansion(raw, dir)
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: spec.%s is %s, which is not an absolute path",
			c.sourceErr, c.dirKey, named)
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: spec.%s is %s, which does not exist",
			c.sourceErr, c.dirKey, named)
	case err != nil:
		return "", fmt.Errorf("stat the %s directory %s: %w", c.plural, dir, err)
	case !info.IsDir():
		return "", fmt.Errorf("%w: spec.%s is %s, which is not a directory",
			c.sourceErr, c.dirKey, named)
	}
	return dir, nil
}

// checkContentName rejects any name that cannot be one entry directly beneath
// a content source. A name holding a separator would reach outside the source
// on the way in and outside the planted directory on the way out.
func checkContentName(name string, c contentKind) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: spec.%s holds an empty name", c.nameErr, c.key)
	case name == "." || name == "..":
		return fmt.Errorf("%w: spec.%s holds %q", c.nameErr, c.key, name)
	case strings.ContainsRune(name, '/'), strings.ContainsRune(name, filepath.Separator):
		return fmt.Errorf("%w: %q holds a path separator, so it does not name one %s",
			c.nameErr, name, c.entry)
	}
	return nil
}

// quotedNames renders names as a comma-separated list of quoted values, so
// that an error can say what the manifest actually declared.
func quotedNames(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return strings.Join(quoted, ", ")
}
