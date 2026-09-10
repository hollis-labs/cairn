// Package catalog is Cairn's profile bundle: a directory of files, read into
// memory whole at the start of a command.
//
// The catalog is the store. A profile is a markdown file with YAML
// frontmatter, a layout is a markdown file, and git is the review surface —
// so there is nothing to seed, nothing to migrate, and no second copy of the
// operator's profiles to keep in step with the first.
//
// Reading is all this package does. It creates no directory and no file, and
// it does not treat an absent bundle as a starting state to be conjured: a
// read that finds nothing says what it was looking for and where, which is
// the one thing a command pointed at the wrong bundle needs to hear.
//
// It used to have one exception: a marshaller that rendered a saved
// composition to bytes for `--save-as` to write. Bindings retired —
// launch-time assembly belongs to the launcher, and cairn holds no launch
// state — so this package reads, and the sentence above needs no exception.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chrispian/cairn/profile"
)

// DirName is the base name of the configuration directory a bundle defaults
// to. It names the contents rather than a consumer of them: several tools may
// read the same profiles, so the directory is "agents" rather than "cairn".
const DirName = "agents"

// ProfilesDir is the bundle subdirectory holding one markdown file per
// profile.
const ProfilesDir = "profiles"

// PartsDir is the one subdirectory of [ProfilesDir] that is also read, so that
// a bundle with many small reusable profiles can put them somewhere without
// them becoming a second kind of thing.
//
// It is an organizational convention and nothing else. A file under it is an
// ordinary profile: same parser, same closed frontmatter, same merge rules,
// and the same global id namespace as the files beside it — profiles/parts/
// docs-only.md is the profile `docs-only`, listed, shown, booted, extended and
// composed by that bare id. It is never `parts/docs-only`, because an id holds
// no separator; see parseProfile, where that rule is enforced and where the
// reason it must hold is written down.
//
// One directory and one level, deliberately. Reading every subdirectory would
// make the layout of the bundle part of its meaning — a file moved into a
// folder for tidiness would join the catalog, and a folder of notes beside the
// profiles would be read as profiles. The cost of naming exactly one directory
// is that the others stay ignored **silently**: profiles/roles/x.md is not a
// broken profile and is not reported as one, exactly as a README beside the
// profiles is not. That was already true of every subdirectory and is only
// worth writing down now that one of them has stopped being true.
const PartsDir = "parts"

// ErrBundleNotFound reports that the bundle directory is absent, or is not a
// directory.
var ErrBundleNotFound = errors.New("profile bundle not found")

// ErrNoProfilesDir reports a bundle with no profiles directory in it. A
// directory holding no profiles is not an empty catalog, it is a sign cairn
// was pointed somewhere that is not a bundle.
var ErrNoProfilesDir = errors.New("profile bundle holds no profiles directory")

// ErrProfileNotFound reports that no profile file exists for an id.
var ErrProfileNotFound = errors.New("profile not found")

// ErrDuplicateProfileID reports two files claiming one profile id.
//
// It became reachable when [PartsDir] did. While the catalog was one flat
// directory the id namespace could not collide: an id must equal its file's
// stem, and one directory holds one file by that name — so the filesystem
// enforced uniqueness and nothing here had to. Two directories can each hold
// docs-only.md, and then the bundle answers to one id with two documents.
//
// It is refused rather than resolved by precedence. A rule like "the root
// wins" would be silent shadowing: the losing file stays on disk, is edited,
// is committed, and never takes effect, and the operator's evidence that
// something is wrong is that their change did nothing. Both paths are named
// because the fix is to rename one and the reader has to know which two.
var ErrDuplicateProfileID = errors.New("two profiles claim one id")

// ErrNoHome reports that the bundle path fell back to the home directory and
// no home directory is known.
var ErrNoHome = errors.New("home directory unknown")

// Catalog is one bundle, read.
//
// Everything is read at [Open] and nothing is read after it. A command resolves
// a chain, a subagent's profile and a layout from the same snapshot,
// so a file edited mid-command cannot make one lookup disagree with the next.
type Catalog struct {
	root string

	profiles map[string]profile.Profile

	// layouts is the unparsed text of every layout, keyed by file stem. It is
	// read here because the catalog is the store and a layout is one more file
	// in the bundle; it is unparsed because what the text means is the render
	// engine's — see [Catalog.LayoutText].
	layouts map[string]string

	// The listing orders, sorted at Open. They are held rather than recomputed
	// so that [Catalog.Profiles] and [Catalog.Layouts] are reads and not
	// sorts.
	profileIDs  []string
	layoutNames []string
}

// DefaultRoot returns the bundle directory: envRoot when it is set,
// $XDG_CONFIG_HOME/agents when that is set, and $HOME/.config/agents
// otherwise. It reports [ErrNoHome] only when it actually needs a home.
//
// Every input is passed rather than read, so nothing here consults the process
// environment on its own. The name of the variable envRoot came from stays at
// the composition root, which is the only place that knows cairn has flags.
func DefaultRoot(envRoot, xdgConfigHome, home string) (string, error) {
	if p := strings.TrimSpace(envRoot); p != "" {
		return p, nil
	}
	if x := strings.TrimSpace(xdgConfigHome); x != "" {
		return filepath.Join(x, DirName), nil
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("%w: pass --profile to say where the profile bundle is", ErrNoHome)
	}
	return filepath.Join(home, ".config", DirName), nil
}

// Open reads the bundle rooted at root.
//
// An absent root, an absent profiles directory and an unparseable profile are
// all refusals, and each names the file it was reading. Failing at Open rather than at the first lookup is
// what makes the diagnostic useful: the operator hears that the bundle is
// wrong, instead of hearing that the profile they asked for is missing from a
// bundle that was never read.
func Open(root string) (*Catalog, error) {
	dir, err := checkDir(root)
	if err != nil {
		return nil, err
	}
	c := &Catalog{root: dir}

	if c.profiles, err = readProfiles(dir); err != nil {
		return nil, err
	}
	if c.layouts, err = readLayouts(dir); err != nil {
		return nil, err
	}

	c.profileIDs = sortedKeys(c.profiles)
	c.layoutNames = sortedKeys(c.layouts)
	return c, nil
}

// Root returns the directory this catalog was read from, absolute if the
// caller's path was.
func (c *Catalog) Root() string { return c.root }

// Profile returns the profile stored under id, or an error wrapping
// [ErrProfileNotFound] when no such file exists. It implements
// [profile.Loader].
//
// The context is the interface's and is not consulted: the bundle was read
// before this was called, so there is no work here to cancel.
func (c *Catalog) Profile(_ context.Context, id string) (*profile.Profile, error) {
	p, ok := c.profiles[strings.TrimSpace(id)]
	if !ok {
		return nil, fmt.Errorf("load profile %q from %s: %w", id, filepath.Join(c.root, ProfilesDir), ErrProfileNotFound)
	}
	return &p, nil
}

// Profiles returns every profile in the bundle, ordered by id.
func (c *Catalog) Profiles() []profile.Profile {
	out := make([]profile.Profile, 0, len(c.profileIDs))
	for _, id := range c.profileIDs {
		out = append(out, c.profiles[id])
	}
	return out
}

// There is no binding lookup here and no scope lookup either, because there is
// nothing left for either to resolve.
//
// Bindings retired — see the package comment. Before them a bundle-wide
// registry mapped short names to directories, so the scope in a binding file
// could be a name this catalog had to look up, and the listing needed a method
// to ask. The registry went first and the bindings after it; a method that
// returned its argument's field unchanged would be a resolution step that no
// longer resolves anything, which is a worse thing to leave standing than a
// deletion.
