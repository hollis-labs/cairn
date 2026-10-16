package bootdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hollis-labs/cairn/profile"
)

// ErrTreeSource reports a declared tree whose source directory is unusable:
// the manifest names nothing, the path is not absolute once a leading "~/" is
// expanded, or it is not a directory.
var ErrTreeSource = errors.New("tree source is unusable")

// renderTrees returns every file of every directory the manifest copies whole.
//
// It is the bulk counterpart to a files entry. A single document reaches a boot
// directory through spec.files with a static_file source; a directory of them
// reaches it here, with its shape intact. A static_dir source is deliberately
// not the answer: it concatenates what it finds into one string, which is what
// a slot wants and the opposite of a copy.
//
// Cairn reads none of it. What a tree is for, and whether anything in the boot
// directory looks at it, belongs to whoever declared it.
//
// A profile declaring no trees renders nothing and reports no error. The output
// is deterministic: destinations in sorted order, and the files inside each in
// the order [CopyTree] walks them.
func renderTrees(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	declared, err := inst.Profile.Spec.Trees()
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return nil, nil
	}
	dests := make([]string, 0, len(declared))
	for dest := range declared {
		dests = append(dests, dest)
	}
	slices.Sort(dests)

	var files []File
	for _, dest := range dests {
		if strings.TrimSpace(dest) == "" {
			return nil, fmt.Errorf("%w: spec.%s holds an entry whose destination is empty",
				ErrArtifactPath, profile.SpecKeyTrees)
		}
		source, err := treeSource(dest, declared[dest], inst.Home, inst.Env)
		if err != nil {
			return nil, err
		}
		copied, err := CopyTree(source, dest)
		if err != nil {
			return nil, fmt.Errorf("spec.%s %q: %w", profile.SpecKeyTrees, dest, err)
		}
		files = append(files, copied...)
	}
	return files, nil
}

// treeSource resolves the directory a declared tree is copied from.
//
// A tree source is the manifest key most likely to want a variable: it is
// nothing but "point at a path". It expands the same way a slot's static path
// does, and a leading "~/" the same way again — a source directory is a
// location on the operator's machine, and writing it out in full in every
// profile is how it goes stale.
func treeSource(dest, declared, home string, look profile.Expander) (string, error) {
	raw := strings.TrimSpace(declared)
	if raw == "" {
		return "", fmt.Errorf("%w: spec.%s declares %q with no source directory",
			ErrTreeSource, profile.SpecKeyTrees, dest)
	}
	dir, err := profile.ExpandPath(raw, home, look)
	if err != nil {
		return "", fmt.Errorf("%w: spec.%s copies %q: %w", ErrTreeSource, profile.SpecKeyTrees, dest, err)
	}
	named := profile.QuotedExpansion(raw, dir)
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w: spec.%s copies %q from %s, which is not an absolute path",
			ErrTreeSource, profile.SpecKeyTrees, dest, named)
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: spec.%s copies %q from %s, which does not exist",
			ErrTreeSource, profile.SpecKeyTrees, dest, named)
	case err != nil:
		return "", fmt.Errorf("stat the tree source %s: %w", dir, err)
	case !info.IsDir():
		return "", fmt.Errorf("%w: spec.%s copies %q from %s, which is not a directory",
			ErrTreeSource, profile.SpecKeyTrees, dest, named)
	}
	return dir, nil
}
