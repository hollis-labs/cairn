// Package catalog is Cairn's profile bundle: a directory of files, read into
// memory whole at the start of a command.
//
// The catalog is the store. A profile is a markdown file with YAML
// frontmatter, a binding is a small YAML file, and git is the review surface —
// so there is nothing to seed, nothing to migrate, and no second copy of the
// operator's profiles to keep in step with the first.
//
// Reading is almost all this package does. It creates no directory and no
// file, and it does not treat an absent bundle as a starting state to be
// conjured: a read that finds nothing says what it was looking for and where,
// which is the one thing a command pointed at the wrong bundle needs to hear.
//
// The exception is [MarshalBinding], which renders a binding to bytes and
// hands them back. It is here because the parser is here and a format with two
// owners drifts; it is not a write, because deciding that a file may be
// created is the composition root's call and `--save-as` is where that is
// made.
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

// BindingsDir is the bundle subdirectory holding one YAML file per binding.
const BindingsDir = "bindings"

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

// ErrBindingNotFound reports that no binding file exists for a name.
var ErrBindingNotFound = errors.New("binding not found")

// ErrBindingName reports a name that cannot be a binding's, because it cannot
// be the base name of a file in the bindings directory.
var ErrBindingName = errors.New("not a binding name")

// ErrNoHome reports that the bundle path fell back to the home directory and
// no home directory is known.
var ErrNoHome = errors.New("home directory unknown")

// Binding is one file of the bindings directory: a saved composition. A base
// profile, the parts merged onto it, the skills and prompts the boot directory
// carries, and the scope that boot works in.
//
// Sprawl lands here rather than in profiles, which is the whole reason a
// binding says several things and not one. A composition worth reusing is a
// few lines of YAML; a profile is a document. The fields below are the list,
// and this sentence deliberately does not count them: it used to say four, and
// went on saying four after prompts made it five.
//
// Name is the exception among them, and the exception is about where the value
// comes from rather than about how many there are: it is the file's own name,
// not something the file declares.
type Binding struct {
	// Name is the binding's identity — what `cairn boot` is given. It is the
	// file's base name, so a binding cannot disagree with what it is called.
	Name string

	// ProfileID is the profile this binding boots.
	ProfileID string

	// Parts are the profiles merged after that profile's extends chain
	// resolves, closest-wins and in this order — exactly what --with names,
	// and held as the operator wrote them. Empty for a binding that composes
	// nothing.
	//
	// A part is held as written and not as it expands, because what a binding
	// records has to stay true when the bundle moves. `$CAIRN_PROFILE_ROOT/x.md`
	// saved as an absolute path would be a binding that worked on one machine.
	Parts []string

	// Skills are added to the ones the resolved profile carries, by id, the
	// way --skill adds them. Empty for a binding that adds none.
	//
	// Optional, and not the only way to say it: a stable role whose skills
	// never change declares them in its profile, or in a part this binding
	// names. The field exists so that a --skill passed at boot survives
	// --save-as, because a flag vanishing from the thing that claims to save
	// what you just did is the surprising outcome.
	Skills []string

	// Prompts are added the same way, by id, as --prompt adds them. Empty for
	// a binding that adds none.
	//
	// It is a separate field from Skills for the reason the manifest keys are
	// separate: a boot directory carries a set of skills the harness loads and
	// a set of prompts a person invokes, and a binding that could only say one
	// of them would be a saved composition that does not save the composition.
	Prompts []string

	// Scope is where that boot works: a directory path, as the file wrote it.
	// Empty means no declared scope.
	//
	// It is a path and only a path. A bundle-wide registry of short names for
	// directories used to stand in front of this field, and retiring it is
	// what makes the value here readable on its own — a binding says where it
	// works, and nothing else in the bundle can change the answer.
	Scope string
}

// Catalog is one bundle, read.
//
// Everything is read at [Open] and nothing is read after it. A command resolves
// a chain, a subagent's profile and a binding's scope from the same snapshot,
// so a file edited mid-command cannot make one lookup disagree with the next.
type Catalog struct {
	root string

	profiles map[string]profile.Profile
	bindings map[string]Binding

	// layouts is the unparsed text of every layout, keyed by file stem. It is
	// read here because the catalog is the store and a layout is one more file
	// in the bundle; it is unparsed because what the text means is the render
	// engine's — see [Catalog.LayoutText].
	layouts map[string]string

	// The listing orders, sorted at Open. They are held rather than recomputed
	// so that [Catalog.Profiles] and [Catalog.Bindings] are reads and not
	// sorts.
	profileIDs   []string
	bindingNames []string
	layoutNames  []string
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
// An absent root, an absent profiles directory, an unparseable profile and a
// binding naming a profile that is not there are all refusals, and each names
// the file it was reading. Failing at Open rather than at the first lookup is
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
	if c.bindings, err = readBindings(dir, c.profiles); err != nil {
		return nil, err
	}
	if c.layouts, err = readLayouts(dir); err != nil {
		return nil, err
	}

	c.profileIDs = sortedKeys(c.profiles)
	c.bindingNames = sortedKeys(c.bindings)
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

// Binding returns the binding stored under name, or an error wrapping
// [ErrBindingNotFound] when no such file exists.
func (c *Catalog) Binding(name string) (*Binding, error) {
	b, ok := c.bindings[strings.TrimSpace(name)]
	if !ok {
		return nil, fmt.Errorf("load binding %q from %s: %w", name, filepath.Join(c.root, BindingsDir), ErrBindingNotFound)
	}
	return &b, nil
}

// Bindings returns every binding in the bundle, ordered by name.
func (c *Catalog) Bindings() []Binding {
	out := make([]Binding, 0, len(c.bindingNames))
	for _, name := range c.bindingNames {
		out = append(out, c.bindings[name])
	}
	return out
}

// There is no Scope lookup here, and there is no ResolvedScope beside
// [Catalog.Bindings], because there is nothing left for either to resolve.
// [Binding.Scope] is the directory, trimmed as it was read, and a caller that
// wants where a binding's boot works reads that field.
//
// It was not always so: a bundle-wide registry mapped short names to
// directories, so the scope in a binding file could be a name this catalog had
// to look up, and the listing needed a method to ask. The registry retired —
// `--scope nanite` bought over `--scope ~/dev/hollis-labs/apps/nanite` what a
// shell alias buys, at the price of a second name for every directory — and a
// method called ResolvedScope that returned its argument's field unchanged
// would be a resolution step that no longer resolves anything, which is a
// worse thing to leave standing than a deletion.
