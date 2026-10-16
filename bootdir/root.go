package bootdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrForeignRepository reports a boot root that resolves inside a git working
// tree which is not the one the scope belongs to.
//
// A boot directory is the agent's working directory. Every git command the
// session runs therefore resolves against whatever working tree contains the
// boot root, while the slots in that same boot.md resolve against the scope —
// so an agent booted from a foreign checkout is handed two accounts of which
// repository it is in, and its shell answers with the wrong one. Nothing about
// that is loud: `git add -A && git commit` succeeds, against a tree the agent
// never touched.
var ErrForeignRepository = errors.New("boot root is inside a repository that is not the scope's")

// CheckRoot reports [ErrForeignRepository] when root resolves inside a git
// working tree other than the one scopeDir belongs to. A root inside the
// scope's own working tree passes, and so does a root inside no working tree
// at all.
//
// This refuses rather than warns, for three reasons. Cairn already refuses the
// neighbouring hazard — a boot directory at or inside the scope, see
// [github.com/hollis-labs/cairn/scope.CheckBootDir] — so refusal is the
// established answer for "cairn is about to write somewhere that will confuse
// the session it is writing for". The failure this catches is silent and
// plausible by construction, and a warning printed by a boot that then exits 0
// and prints a path is a line an operator scrolls past on the way to the path.
// And the boot root is a startup-time property: an operator sets it once, in a
// launcher or an environment file, and fixes it once. A per-boot surprise
// would be a different argument.
//
// The probe is one [os.Lstat] per ancestor looking for a ".git" entry, on two
// paths — roughly a dozen syscalls for a boot that already reads a catalog,
// runs whatever `cmd` slots the profile declares, and makes whatever requests
// its `http_*` slots declare. The cost is not a reason to weaken the answer.
//
// Detection is structural and nothing here shells out. `git rev-parse
// --show-toplevel` is the obvious instrument and the worse one: it needs git
// installed, it needs a subprocess, and it reads a set of environment
// variables — GIT_DIR, GIT_CEILING_DIRECTORIES — that would make the answer
// depend on the caller's environment, which is precisely what this package
// does not do. Walking up for the marker depends on neither.
//
// Both paths are passed rather than read. Nothing in this package consults the
// process environment; see [DefaultRoot], which takes [EnvBootRoot]'s value as
// an argument for the same reason.
//
// The empty scope passes. It declares no target, so there is no repository to
// contradict and no scope the message could name — the same answer
// [github.com/hollis-labs/cairn/scope.Contains] gives an empty ancestor. That
// leaves a boot with no declared scope unguarded, which is a real gap and a
// deliberate one: this check is defined as "a repository other than the
// declared scope", and with nothing declared it has no question to ask.
func CheckRoot(root, scopeDir string) error {
	// An empty root is not this check's to refuse — Location.Dir refuses it,
	// and names it better than a repository check could.
	if strings.TrimSpace(root) == "" || strings.TrimSpace(scopeDir) == "" {
		return nil
	}

	boot, err := findWorkTree(root)
	if err != nil {
		return err
	}
	if boot.info == nil {
		return nil
	}

	target, err := findWorkTree(scopeDir)
	if err != nil {
		return err
	}
	// [os.SameFile] rather than a string comparison, for the reason package
	// scope gives at length: two spellings of one directory are routine on a
	// case-folding or form-folding volume, and resolving symlinks normalizes
	// neither. Device and inode settle every spelling axis at once.
	if target.info != nil && os.SameFile(boot.info, target.info) {
		return nil
	}

	// Built as it will read. A scope that is itself a working tree root would
	// otherwise be printed twice in one clause, which looks like a bug in the
	// message rather than a fact about the tree.
	var expected string
	switch {
	case target.info == nil:
		expected = fmt.Sprintf("the scope %s is inside no repository", scopeDir)
	case target.root == target.start:
		expected = fmt.Sprintf("the scope %s is its own repository", scopeDir)
	default:
		expected = fmt.Sprintf("the scope %s is inside %s", scopeDir, target.root)
	}
	// root is named as it was configured and the repository as it resolves,
	// which on a path reached through a symlink prints two spellings of one
	// tree. That is deliberate: the first is the string an operator has to
	// change, the second is the tree git will actually find, and a message
	// that showed only one of them would be missing whichever half the reader
	// needed.
	return fmt.Errorf("%w: %s is inside %s, and %s — a boot directory is the agent's working directory, so every git command the session runs would target %s rather than the scope; plant boot directories outside every checkout",
		ErrForeignRepository, root, boot.root, expected, boot.root)
}

// workTree is what one ancestry walk found.
type workTree struct {
	// root is the working tree's root directory: the nearest ancestor holding
	// a ".git" entry. Empty when the walk reached the volume root without
	// finding one.
	root string

	// info identifies root for [os.SameFile], and is nil exactly when root is
	// empty. It is the field that answers "is this the same working tree",
	// because root is a string and a string is not a path identity.
	info os.FileInfo

	// start is where the walk began: the deepest existing ancestor of the path
	// asked about, with its symlinks resolved. Compared against root only to
	// phrase the refusal, and safe to compare as a string because both sides
	// came out of this same walk.
	start string
}

// findWorkTree walks up from dir looking for the nearest ancestor that holds a
// ".git" entry, which is the root of the git working tree dir lies in.
//
// The entry is found with [os.Lstat] and its kind is never examined, because
// ".git" is a directory in an ordinary clone and a *file* in a linked worktree
// or a submodule, holding a "gitdir:" line pointing elsewhere. The pointer is
// not followed and does not need to be: the question is which working tree a
// shell started here would be in, and that is answered by the directory
// holding the marker, whichever form the marker takes. Two worktrees of one
// repository are two working trees on two branches with two dirty states, and
// telling them apart is the whole point.
//
// The nearest such ancestor wins, not the outermost. That matches git, which
// stops at the first marker it finds walking up — a shell inside a submodule
// is in the submodule, not in its superproject.
func findWorkTree(dir string) (workTree, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return workTree{}, fmt.Errorf("absolute path of %q: %w", dir, err)
	}
	start, err := deepestExisting(abs)
	if err != nil {
		return workTree{}, err
	}
	for d := start; ; {
		marker := filepath.Join(d, ".git")
		_, err := os.Lstat(marker)
		switch {
		case err == nil:
			info, statErr := os.Stat(d)
			if statErr != nil {
				return workTree{}, fmt.Errorf("stat %q: %w", d, statErr)
			}
			return workTree{root: d, info: info, start: start}, nil
		case !errors.Is(err, fs.ErrNotExist):
			// A directory that cannot be read fails the check closed. An
			// unreadable ancestor is a question that could not be answered,
			// and answering "no repository here" to it would be wrong in the
			// permissive direction — which for a guard is the wrong direction
			// to be wrong in.
			return workTree{}, fmt.Errorf("look for a repository at %q: %w", marker, err)
		}
		parent := filepath.Dir(d)
		// filepath.Dir is its own fixpoint at the volume root, so this detects
		// the end of the walk. It is not a path-identity test — both sides are
		// the same string produced by the same call.
		if parent == d {
			return workTree{start: start}, nil
		}
		d = parent
	}
}

// deepestExisting returns the deepest ancestor of abs that exists, with its
// symlinks resolved.
//
// Resolved because git walks the physical ancestry: a shell's getcwd reports
// the path with symlinks already gone, so "/tmp/x" and "/private/tmp/x" — the
// same directory on macOS — have different ancestries and only one of them is
// the one git will search. A check that walked the lexical path would look in
// a place no git command ever looks.
//
// Deepest *existing* because the boot root routinely does not exist yet: cairn
// creates it. Nothing below the deepest existing ancestor can hold a ".git"
// entry, since nothing below it holds anything, so beginning the search there
// loses no answer.
func deepestExisting(abs string) (string, error) {
	for d := abs; ; {
		resolved, err := filepath.EvalSymlinks(d)
		switch {
		case err == nil:
			return resolved, nil
		case !errors.Is(err, fs.ErrNotExist):
			// ENOTDIR among others: an ancestor that is a regular file is a
			// path no boot directory can be planted under, and reporting it
			// here is better than discovering it at mkdir.
			return "", fmt.Errorf("resolve %q: %w", d, err)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("resolve %q: no ancestor of it exists", abs)
		}
		d = parent
	}
}
