package install

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/chrispian/cairn/profile"
)

// SweepPlan is exactly what cairn claims in the install root, and therefore
// exactly what a check will report on as a finding.
//
// It is derived from the provider's renderer registration and the profile's
// declarations rather than from one render's output — see [Renderer.Fills] —
// which is what lets a check look into a directory the current profile renders
// nothing into. A sweep scoped to what was rendered would stop looking in
// precisely the case where something was left behind.
//
// # Cairn does not own the provider directory
//
// The plan is three lists rather than one because ~/.claude is a live
// harness's home: session state, credentials, caches, one directory per
// project. Cairn writes three files into it and fills the skill directories
// its profile names, and claims nothing else.
//
// A sweep that read the provider directory one level deep and called every
// unrendered file an orphan would report settings.local.json and
// .credentials.json on every run of every real installation, so `--check`
// would exit non-zero forever and stop meaning anything. That is the same
// disease as a lint gate configured not to fail.
//
// [SweepPlan.Shared] is that same rule one level down. ~/.claude/skills is
// shared with the operator, whose hand-written skills sit beside the ones
// cairn plants, so cairn claims the directories it was told to plant and
// reports the rest without failing on them.
//
// It is exported so that a caller can print how far a check reached. A check
// that reports nothing says nothing unless its scope can be read.
type SweepPlan struct {
	// Claims are the exact file paths this layer's renderers can produce,
	// relative to the install root and slash-separated. A claim the render
	// did not produce, sitting on disk anyway, is a [StatusOrphan] — that is
	// how a settings.json left behind by a profile that stopped declaring one
	// gets found. A path that is not a claim is not cairn's and is not
	// reported, whatever is in it.
	//
	// A claim may be only partly cairn's, and that needs no shape here. An
	// artifact declaring a [Renderer.Merge] is still one exact path, and a
	// render that produces it is classified by the manifest half — which is
	// where how much of it is cairn's gets decided. A render that does not
	// produce it is the orphan above, unchanged: a profile that declares no
	// settings renders no settings document, and the whole file is then the
	// leftover. Neither case wants a fourth list. Do not add one.
	Claims []string

	// Trees are the directories cairn fills whole: for each renderer that
	// declares [Renderer.Fills], one path per subdirectory the profile named.
	// Each is walked to the bottom, and every file in one that the render did
	// not produce is a [StatusOrphan]. Here the whole-directory rule is right:
	// cairn writes every file of a skill it was told to plant, so anything
	// else inside that skill's directory is a leftover.
	Trees []string

	// Shared are the artifacts those trees sit in — directories cairn writes
	// into and does not own. Each is read one level deep, and what is in one
	// and is not a tree is reported as [StatusUnclaimed]: named, so a check
	// says what it saw, and not a finding, because cairn did not put it there
	// and will not touch it.
	//
	// A skill directory the profile stopped declaring lands here rather than
	// in Trees, so it is still named — the lost case is reported, without the
	// false alarm on every skill the operator wrote by hand.
	Shared []string
}

// NewSweepPlan returns the [SweepPlan] for lay: the provider directory its
// profile is installed into, the file artifacts that provider's renderers
// register, and for each directory artifact the subdirectories lay's profile
// declares.
//
// The registration list is the source, not the render. A profile that declares
// a skill whose source lost a file renders less than it did, and a plan
// derived from the render would stop looking inside that skill in exactly the
// case where something was left behind.
//
// A subdirectory name that cannot name one directory beneath its artifact is
// not claimed — see [subdirOf]. The render refuses the same names outright, so
// a check never reaches this; it matters only for a plan built on its own.
//
// Errors wrap [ErrNoProfile],
// [github.com/chrispian/cairn/bootdir.ErrUnsupportedProvider] for a provider
// the installed layer has no harness for, or come from reading the manifest
// key a [Renderer.Fills] is declared over.
func NewSweepPlan(lay *Layer) (SweepPlan, error) {
	if lay == nil || lay.Profile == nil {
		return SweepPlan{}, ErrNoProfile
	}
	h, err := harnessFor(lay.provider())
	if err != nil {
		return SweepPlan{}, err
	}
	var plan SweepPlan
	for _, r := range h.renderers {
		if r.Artifact == "" {
			continue
		}
		// Renderer.Artifact is a label relative to the provider directory,
		// which is why the two arrive together from one lookup: a directory
		// and an artifact list answered by different switch statements could
		// disagree, and the sweep would claim the wrong paths.
		p := path.Join(h.dir, r.Artifact)
		if r.Fills == nil {
			if r.Claim != nil {
				claimed, err := r.Claim(lay.Profile)
				if err != nil {
					return SweepPlan{}, fmt.Errorf("plan the %s artifact: %w", r.Artifact, err)
				}
				if !claimed {
					continue
				}
			}
			plan.Claims = append(plan.Claims, p)
			continue
		}
		names, err := r.Fills(lay.Profile)
		if err != nil {
			return SweepPlan{}, fmt.Errorf("plan the %s artifact: %w", r.Artifact, err)
		}
		plan.Shared = append(plan.Shared, p)
		for _, name := range names {
			if sub, ok := subdirOf(p, name); ok {
				plan.Trees = append(plan.Trees, sub)
			}
		}
	}
	slices.Sort(plan.Claims)
	plan.Claims = slices.Compact(plan.Claims)
	slices.Sort(plan.Trees)
	plan.Trees = slices.Compact(plan.Trees)
	slices.Sort(plan.Shared)
	plan.Shared = slices.Compact(plan.Shared)
	return plan, nil
}

// subdirOf returns the path of the subdirectory of dir named name, and whether
// name can name one at all.
//
// An empty name, "." or "..", and a name holding a separator are refused
// rather than joined. [path.Join] would normalize each of them into a path
// that is not one directory beneath dir — ".." reaches the provider directory
// itself — and a plan that claimed one would sweep somewhere the profile never
// asked for.
func subdirOf(dir, name string) (string, bool) {
	name = strings.TrimSpace(name)
	switch {
	case name == "", name == ".", name == "..":
		return "", false
	case strings.ContainsAny(name, `/\`):
		return "", false
	}
	return path.Join(dir, name), true
}

// Check renders lay and compares it against what is on disk beneath lay's
// root, returning what it found.
//
// It reports and never repairs. It does not create a missing file, does not
// rewrite a modified one, and does not delete an orphan — not on a flag, not
// on a follow-up call. Anything that writes belongs in [Install], where the
// operator asked for a write. Nothing here should acquire one later.
//
// Two halves, and the second is the one a check built from a manifest leaves
// out:
//
//  1. Every file the render produces is looked up on disk: identical, absent,
//     different, something that is not a file, or unreadable.
//  2. Every directory in the [SweepPlan] is swept — the claimed ones for files
//     the render did not produce, the shared ones for what cairn never claimed
//     at all.
//
// The first half can only confirm what it already knew to look for. The second
// is why the plan is derived from the renderer registration.
//
// The returned error is for a check that could not run: an unusable root, a
// filesystem that cannot report symbolic links, a failed render. What the
// check found is never an error — it comes back in the [Report], and
// [Report.ExitCode] is what turns it into a process exit.
func Check(lay *Layer) (*Report, error) {
	if lay == nil || lay.Profile == nil {
		return nil, ErrNoProfile
	}
	fsys, err := lay.Root.FS()
	if err != nil {
		return nil, err
	}
	report, err := CheckFS(fsys, lay)
	if err != nil {
		return nil, err
	}
	// CheckFS is handed a filesystem and no location, so it cannot name the
	// root it read. Here the root is known.
	report.Root = lay.Root.Dir()
	return report, nil
}

// CheckFS is [Check] against a supplied filesystem, so a test does not need a
// real directory and a caller can check a root it is not standing in.
//
// The parameter type is the mechanism rather than a convenience.
// [fs.ReadLinkFS] carries Open, ReadLink and Lstat and no method that writes,
// so "--check repairs nothing" is a property of what this function was handed
// rather than a claim about its body. A function given a directory path can
// always call os.WriteFile on it; one given a filesystem view cannot.
//
// Lstat is also why a plain [fs.FS] will not do. A Stat that follows links
// reports a symbolic link as whatever it points at, so a link standing in for
// a rendered file could compare equal to the render — and a link is exactly
// what an operator reaches for when they want an installed file to be editable
// in place.
//
// It leaves [Report.Root] empty: the filesystem it reads names no location,
// and lay's root may not be the root it was handed. [Check] fills it.
func CheckFS(fsys fs.ReadLinkFS, lay *Layer) (*Report, error) {
	if fsys == nil {
		return nil, errors.New("install: check was given no filesystem to read")
	}
	if lay == nil || lay.Profile == nil {
		return nil, ErrNoProfile
	}
	rendered, err := Render(lay)
	if err != nil {
		return nil, err
	}
	plan, err := NewSweepPlan(lay)
	if err != nil {
		return nil, err
	}
	entries := checkManifest(fsys, rendered, comparisons(lay.provider()))
	entries = append(entries, sweep(fsys, plan, manifestPaths(rendered))...)
	return &Report{Entries: entries}, nil
}

// manifestPaths returns the set of paths the render produced, so the sweep can
// skip what the first half has already classified against the bytes.
func manifestPaths(rendered []File) map[string]struct{} {
	paths := make(map[string]struct{}, len(rendered))
	for _, f := range rendered {
		paths[f.Path] = struct{}{}
	}
	return paths
}

// comparison is how one artifact is held against what is on disk: which of
// the bytes there are cairn's, and how the two are laid out before they are
// compared. Both are narrowings of the claim and both are declared on the
// [Renderer], so they travel together.
//
// The zero comparison is the default and the common one: the whole file is
// cairn's, and the bytes are compared as they are.
type comparison struct {
	// merge is the artifact's [Renderer.Merge]: the bytes an install would
	// write, given the render and what is already there.
	merge func(rendered, existing []byte) []byte

	// normalize is the artifact's [Renderer.Normalize].
	normalize func([]byte) []byte
}

// comparisons returns the [comparison] of every artifact that narrows one,
// keyed by the path it lands at, so a check can find the one for a rendered
// file.
//
// It reads the registration list rather than the render, for the reason
// [NewSweepPlan] does: which artifacts cairn claims — how much of one, and how
// it is compared — is settled where they are registered, not by the profile
// being checked.
//
// A provider with no harness returns nothing rather than an error. The render
// that reaches a comparison has already gone through [Render], which refuses
// that provider outright, so there is nothing here to report a second opinion
// on.
func comparisons(p profile.Provider) map[string]comparison {
	h, err := harnessFor(p)
	if err != nil {
		return nil
	}
	out := make(map[string]comparison)
	for _, r := range h.renderers {
		if r.Artifact == "" || (r.Merge == nil && r.Normalize == nil) {
			continue
		}
		out[path.Join(h.dir, r.Artifact)] = comparison{merge: r.Merge, normalize: r.Normalize}
	}
	return out
}

// checkManifest is the first half: every rendered file, looked up on disk, in
// render order.
func checkManifest(fsys fs.ReadLinkFS, rendered []File, how map[string]comparison) []Entry {
	entries := make([]Entry, 0, len(rendered))
	for _, f := range rendered {
		entries = append(entries, checkRendered(fsys, f, how[f.Path]))
	}
	return entries
}

// checkRendered classifies one rendered file against what is at its path.
//
// Only the content is compared, and by default that means the bytes. A mode is
// not compared: the mode a file lands with depends on the umask of whoever ran
// the install, so reporting one would report the operator's shell rather than
// the layer's content.
//
// What the file is held against is what an install would write there, which
// for an artifact cairn owns whole is the render and nothing else. An artifact
// registered with a [Renderer.Merge] is held against the merge instead, so a
// key cairn never declared is not a finding — it is not cairn's, an install
// would not touch it, and reporting it would be the check crying wolf over
// somebody else's file. That is the same rule [Renderer.Fills] applies to a
// directory, in a file.
//
// An artifact registered with a [Renderer.Normalize] has it applied to both
// sides after that, so the comparison forgives exactly what that function
// moves and nothing else. The detail still counts the bytes actually on disk,
// because that is the number the operator sees in a directory listing.
//
// A path that cannot be read is a finding rather than a failure of the check.
// One unreadable file in a skills tree should not hide what the rest of the
// layer looks like.
func checkRendered(fsys fs.ReadLinkFS, f File, how comparison) Entry {
	entry := Entry{Path: f.Path}
	info, err := fs.Lstat(fsys, f.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		entry.Status = StatusMissing
		return entry
	case err != nil:
		entry.Status = StatusUnreadable
		entry.Detail = err.Error()
		return entry
	}
	if !info.Mode().IsRegular() {
		entry.Status = StatusNotAFile
		entry.Detail = describeKind(fsys, f.Path, info.Mode())
		return entry
	}
	content, err := fs.ReadFile(fsys, f.Path)
	if err != nil {
		entry.Status = StatusUnreadable
		entry.Detail = err.Error()
		return entry
	}
	write := f.Content
	if how.merge != nil {
		write = how.merge(f.Content, content)
	}
	found, want := content, write
	if how.normalize != nil {
		found, want = how.normalize(found), how.normalize(want)
	}
	if !bytes.Equal(found, want) {
		entry.Status = StatusModified
		entry.Detail = fmt.Sprintf("the bytes on disk are not %s: %d on disk, %d %s",
			describeWrite(how), len(content), len(write), describeWritten(how))
		return entry
	}
	entry.Status = StatusMatch
	return entry
}

// describeWrite and describeWritten name what a modified file was held
// against, so the detail says which comparison it failed. An artifact cairn
// owns whole is held against the render; one it claims part of is held against
// the merge, and calling that "the render" would name a document that is not
// what an install would write.
func describeWrite(how comparison) string {
	if how.merge != nil {
		return "what an install would write"
	}
	return "the render's"
}

func describeWritten(how comparison) string {
	if how.merge != nil {
		return "to write"
	}
	return "rendered"
}

// sweep is the second half: every file inside a directory of the plan that the
// render did not produce, and what is beside those directories.
//
// The order is the reading order of the plan — the exact claims, then the
// trees they sit above, then what shares a directory with those trees. Each
// path is classified once, and a tree is classified as a tree rather than as
// one of its parent's entries.
//
// A read that fails is reported as a [StatusUnreadable] entry for the
// directory and the sweep carries on, for the reason [checkRendered] does not
// abort either.
func sweep(fsys fs.ReadLinkFS, plan SweepPlan, rendered map[string]struct{}) []Entry {
	seen := make(map[string]struct{})
	var entries []Entry
	for _, claim := range plan.Claims {
		if entry, found := sweepClaim(fsys, claim, rendered); found {
			seen[claim] = struct{}{}
			entries = append(entries, entry)
		}
	}
	claimed := make(map[string]struct{}, len(plan.Trees))
	for _, tree := range plan.Trees {
		claimed[tree] = struct{}{}
		entries = append(entries, sweepTree(fsys, tree, rendered, seen)...)
	}
	for _, dir := range plan.Shared {
		entries = append(entries, sweepShared(fsys, dir, claimed, rendered, seen)...)
	}
	return entries
}

// sweepTree reports what is inside one directory cairn fills whole.
//
// The root is inspected before it is read, for the reason [sweepDir] tests a
// link before descending: cairn renders bytes and has no way to emit a link,
// so a symbolic link where cairn fills a directory is not cairn's tree. It is
// reported and not followed, whatever is on the far end.
//
// A tree that is absent is not a finding. Nothing has been installed there,
// and the manifest half already reports every file that should have been.
func sweepTree(fsys fs.ReadLinkFS, dir string, rendered map[string]struct{}, seen map[string]struct{}) []Entry {
	info, err := fs.Lstat(fsys, dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return []Entry{{Path: dir, Status: StatusUnreadable, Detail: err.Error()}}
	case info.Mode()&fs.ModeSymlink != 0:
		if _, already := seen[dir]; already {
			return nil
		}
		seen[dir] = struct{}{}
		return []Entry{{Path: dir, Status: StatusOrphan, Detail: describeLink(fsys, dir)}}
	}
	return sweepDir(fsys, dir, rendered, seen)
}

// sweepShared reports what is directly inside a directory cairn writes into
// and does not own: every entry that is not one of the trees the profile
// named.
//
// These are the operator's. A skill they wrote by hand sits in the same
// directory as the ones cairn plants, and the rule that made it drift is the
// one this replaces — so it is [StatusUnclaimed]: named, because a check
// should say what it found in a directory it shares, and not a finding,
// because cairn neither wrote it nor will touch it.
//
// It reads one level and descends into nothing. What is inside an unclaimed
// directory is no more cairn's than the directory is, and walking it would
// turn one line of report into however many files the operator keeps there.
func sweepShared(fsys fs.ReadLinkFS, dir string, claimed, rendered, seen map[string]struct{}) []Entry {
	listing, err := fs.ReadDir(fsys, dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return []Entry{{Path: dir, Status: StatusUnreadable, Detail: err.Error()}}
	}

	var entries []Entry
	for _, item := range listing {
		p := path.Join(dir, item.Name())
		if _, isTree := claimed[p]; isTree {
			// A tree of its own, already swept as one.
			continue
		}
		if _, produced := rendered[p]; produced {
			// The manifest half classified this one against the bytes.
			continue
		}
		if _, already := seen[p]; already {
			continue
		}
		seen[p] = struct{}{}

		entry := Entry{Path: p, Status: StatusUnclaimed}
		switch {
		case item.Type()&fs.ModeSymlink != 0:
			entry.Detail = describeLink(fsys, p)
		case !item.IsDir():
			entry.Detail = "not a directory"
		}
		entries = append(entries, entry)
	}
	return entries
}

// sweepClaim reports the one path cairn claims and this render did not
// produce, when something is there anyway.
//
// A claim the render does produce was already classified against its bytes by
// the manifest half, and a claim with nothing at it is simply a file this
// profile does not ask for.
func sweepClaim(fsys fs.ReadLinkFS, claim string, rendered map[string]struct{}) (Entry, bool) {
	if _, produced := rendered[claim]; produced {
		return Entry{}, false
	}
	info, err := fs.Lstat(fsys, claim)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Entry{}, false
	case err != nil:
		return Entry{Path: claim, Status: StatusUnreadable, Detail: err.Error()}, true
	}
	entry := Entry{Path: claim, Status: StatusOrphan}
	if info.Mode()&fs.ModeSymlink != 0 {
		entry.Detail = describeLink(fsys, claim)
	} else if info.IsDir() {
		entry.Detail = "a directory"
	}
	return entry, true
}

// sweepDir reports every file inside dir that the render did not produce,
// descending to the bottom. It is only ever called on a directory cairn fills
// whole — see [sweepTree] for the entry point that checks that.
//
// A directory that is absent is not a finding: nothing has been installed
// there, and the first half already reports every file that should have been.
//
// The symbolic-link test comes before the directory test and stays there. A
// link to a directory is not a directory to descend into — cairn renders bytes
// and has no way to emit a link, so whatever is on the far end is not cairn's
// tree, and following it is how a sweep ends up walking someone's whole source
// layer.
func sweepDir(fsys fs.ReadLinkFS, dir string, rendered map[string]struct{}, seen map[string]struct{}) []Entry {
	listing, err := fs.ReadDir(fsys, dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return []Entry{{Path: dir, Status: StatusUnreadable, Detail: err.Error()}}
	}

	var entries []Entry
	for _, item := range listing {
		p := path.Join(dir, item.Name())
		if _, produced := rendered[p]; produced {
			// The first half classified this one against the bytes.
			continue
		}
		if _, already := seen[p]; already {
			continue
		}
		seen[p] = struct{}{}

		if item.Type()&fs.ModeSymlink != 0 {
			entries = append(entries, Entry{
				Path:   p,
				Status: StatusOrphan,
				Detail: describeLink(fsys, p),
			})
			continue
		}
		if item.IsDir() {
			entries = append(entries, sweepDir(fsys, p, rendered, seen)...)
			continue
		}
		entries = append(entries, Entry{Path: p, Status: StatusOrphan})
	}
	return entries
}

// describeKind names what is at a path that is not a regular file, so that the
// entry says what an operator has to look at rather than only that the path is
// occupied.
func describeKind(fsys fs.ReadLinkFS, p string, mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return describeLink(fsys, p)
	case mode.IsDir():
		return "a directory"
	case mode&fs.ModeNamedPipe != 0:
		return "a named pipe"
	case mode&fs.ModeSocket != 0:
		return "a socket"
	case mode&fs.ModeDevice != 0:
		return "a device"
	default:
		return "not a regular file: mode " + mode.String()
	}
}

// describeLink names a symbolic link's target as it is written in the link,
// unresolved. The operator has to see the string that is actually in the link
// to know what it points at.
func describeLink(fsys fs.ReadLinkFS, p string) string {
	target, err := fs.ReadLink(fsys, p)
	if err != nil {
		return fmt.Sprintf("a symbolic link whose target could not be read: %v", err)
	}
	return "a symbolic link to " + target
}
