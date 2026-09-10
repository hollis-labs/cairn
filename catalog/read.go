package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/chrispian/cairn/profile"
)

// profileExt is the extension a profile file is written with. Anything else in
// the profiles directory is ignored rather than refused — a README beside the
// profiles is not a broken profile.
const profileExt = ".md"

// checkDir reports whether path names an existing directory, and returns it.
//
// It is the first thing [Open] does, and the reason it is separate from the
// reads below is the diagnostic. A bundle that is not there makes every read
// under it fail, and without this the operator gets the first of those — "no
// profiles directory", naming a path inside a directory that does not exist —
// instead of being told that the bundle they named is not a bundle.
func checkDir(path string) (string, error) {
	dir := strings.TrimSpace(path)
	if dir == "" {
		return "", fmt.Errorf("%w: no directory was named", ErrBundleNotFound)
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: %s", ErrBundleNotFound, dir)
	case err != nil:
		return "", fmt.Errorf("read the profile bundle %s: %w", dir, err)
	case !info.IsDir():
		return "", fmt.Errorf("%w: %s is not a directory", ErrBundleNotFound, dir)
	}
	return dir, nil
}

// readProfiles reads every profile file the bundle holds: the profiles
// directory, and the immediate contents of [PartsDir] inside it.
//
// The two locations fill ONE map, and that is the whole of the feature. A part
// is not a kind, so there is no second map, no second parser and no second
// namespace for a lookup to consult — everything downstream of here sees the
// flat id map it has always seen, and neither the resolver, the composer, the
// listing nor `--save-as` learns that a subdirectory exists.
//
// Order is root first and parts second, which decides nothing. It is not a
// precedence: a duplicate id is refused rather than resolved, so no traversal
// order can pick a winner and reordering these two calls would change no
// outcome but the order of the two paths in the diagnostic.
func readProfiles(root string) (map[string]profile.Profile, error) {
	dir := filepath.Join(root, ProfilesDir)
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: %s", ErrNoProfilesDir, dir)
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	out := make(map[string]profile.Profile, len(entries))
	// The file each id was read from, kept only so that a duplicate can name
	// both documents. It is discarded with this function: an id maps to one
	// profile or the bundle did not open, so nothing downstream has a question
	// this could answer.
	at := make(map[string]string, len(entries))
	if err := readProfileDir(dir, entries, out, at); err != nil {
		return nil, err
	}

	// parts is read through the entry the root listing already returned, not
	// by joining the name and reading it. The difference is symlinks: os.ReadDir
	// reports the directory entry's own type, so a `parts` that is a symlink to
	// a directory has IsDir false here and is passed over, where reading the
	// joined path would have followed it. A bundle is a directory of files that
	// git reviews, and a link out of the tree is content nobody reviewing the
	// bundle can see.
	//
	// Passed over silently, as every other subdirectory is. See [PartsDir].
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() != PartsDir {
			continue
		}
		sub := filepath.Join(dir, PartsDir)
		subEntries, err := os.ReadDir(sub)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", sub, err)
		}
		if err := readProfileDir(sub, subEntries, out, at); err != nil {
			return nil, err
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("%w: neither %s nor %s holds a %s file",
			ErrNoProfilesDir, dir, filepath.Join(dir, PartsDir), profileExt)
	}
	return out, nil
}

// readProfileDir reads the profile files of one directory into out, recording
// where each came from in at.
//
// It descends into nothing. The root listing's own subdirectories are skipped
// here and [readProfiles] reaches the one it reads deliberately, so this is
// the same loop over both locations and neither of them can grow a third by
// accident.
func readProfileDir(dir string, entries []os.DirEntry, out map[string]profile.Profile, at map[string]string) error {
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != profileExt {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		text, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		p, err := parseProfile(string(text), entry.Name())
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if first, ok := at[p.ID]; ok {
			return fmt.Errorf("%w: %q is declared by %s and by %s — rename one, "+
				"since a profile is named by its id wherever the file sits",
				ErrDuplicateProfileID, p.ID, first, path)
		}
		at[p.ID] = path
		out[p.ID] = p
	}
	return nil
}

// ReadProfile reads one profile out of a file named directly, instead of out
// of a bundle's profiles directory.
//
// It is how a composition loads a part by path. The part is an ordinary
// profile in the ordinary format — the same frontmatter keys, the same closed
// set, the same manifest conversion, the same diagnostics. What it is not is a
// catalog entry: the file has no bundle to be listed in and no id the bundle
// knows, so the caller decides what to call it, and its extends still resolves
// against the catalog.
//
// The file is NOT held to being named after the id it declares, and that is
// the difference between this and [parseProfile].
//
// The reason is narrow and it is the whole of it: [parseProfile]'s rule is
// about the catalog's map, which is keyed by id while the listing walks file
// names, so a bundled file disagreeing with itself resolves under one spelling
// and lists under the other. That reason does not transfer, because there is no
// map here keyed by anything a profile declares — a part is keyed by the path
// it was read from, and its id is overwritten with that path by the caller. The
// check would be made and its result discarded.
//
// What the rule cost is likewise narrow, and worth being accurate about: a
// generated part that declares no id already worked, taking the file's name.
// It bit only a generator that writes an id AND picks one the tempfile it
// landed in is not named after — which is the natural thing to write, and a
// requirement to name the file after the id would be the friction the path form
// exists to remove, wearing a different hat.
//
// Any extension is read, not only [profileExt]. The bundle's directory listing
// skips a file that is not a profile because it cannot tell one from a README;
// a path names exactly one file, and refusing to read the file the operator
// pointed at because of how it is spelled would be a rule with nothing behind
// it.
func ReadProfile(path string) (*profile.Profile, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the profile %s: %w", path, err)
	}
	base := filepath.Base(path)
	p, err := parseFile(string(text), strings.TrimSuffix(base, filepath.Ext(base)))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &p, nil
}

// sortedKeys returns a map's keys in order, for the listings that are read
// rather than searched.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
