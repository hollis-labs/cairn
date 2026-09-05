package bootdir

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/chrispian/cairn/profile"
	"github.com/hollis-labs/agentkit/agentlaunch"
)

// ErrNoProfile reports that an [Instance] carries no resolved profile. Every
// artifact is derived from one, so there is nothing to render without it.
var ErrNoProfile = errors.New("instance has no resolved profile")

// ErrArtifactPath reports that a [Renderer] produced a [File] whose path
// cannot name something inside the boot directory: it is empty, absolute, not
// slash-separated, escapes upward, or targets a reserved name.
var ErrArtifactPath = errors.New("invalid artifact path")

// ErrDuplicatePath reports that two rendered files claim the same path inside
// the boot directory. Which one won would depend on renderer order, so neither
// does.
var ErrDuplicatePath = errors.New("duplicate artifact path")

// File is one rendered boot-directory artifact, held in memory. Rendering
// produces every file before anything is written, so a render that fails
// writes nothing at all.
type File struct {
	// Path is the file's location relative to the boot directory root, always
	// slash-separated regardless of platform — ".claude/settings.json", never
	// an absolute path and never one that escapes upward.
	Path string

	// Content is the file's exact bytes.
	Content []byte

	// Mode is the permission mode to write with. Zero means [DefaultFileMode].
	Mode fs.FileMode
}

// mode returns the permission mode f is written with, substituting
// [DefaultFileMode] for the zero value.
func (f File) mode() fs.FileMode {
	if f.Mode == 0 {
		return DefaultFileMode
	}
	return f.Mode
}

// Instance is one materialized boot directory: the resolved profile, where it
// goes, and everything that varies between two materializations. It is the
// only input a [Renderer] receives, so nothing further down reads the
// environment.
type Instance struct {
	// Dir is the absolute path of the boot directory to write.
	Dir string

	// Layout is where this instance's harness reads each artifact from.
	Layout Layout

	// Profile is the fully resolved profile. Rendering never cascades again.
	Profile *profile.Resolved

	// Home is the operator's home directory, used to expand a manifest path
	// written with a leading "~/". It is carried on the instance rather than
	// read by the renderer that needs it, so that the rule below — a renderer
	// consults nothing outside the instance — holds without an exception.
	// Empty means no home is known, and a manifest path that needs one then
	// fails by saying so.
	Home string

	// Env answers an environment variable named in a manifest path — a tree's
	// source, or the skills directory. It is carried for the same reason Home
	// is: a renderer reads nothing outside the instance it was handed, and the
	// process environment is exactly that. Nil expands nothing, so a caller
	// that supplies none leaves the operator's own text in the diagnostic.
	Env profile.Expander

	// Scope is the directory the materialized instance works in, or empty for
	// no declared scope. It is a rendered field, not a validated one — the
	// containment check that guards the write lives in package scope.
	Scope string

	// Templates is the manifest's templates, keyed by boot-directory-relative
	// destination, with every value already resolved to its text. It arrives
	// resolved because a template may name a source rather than a literal, and
	// resolving one runs commands and makes requests. Nil renders no prose at
	// all.
	Templates map[string]string

	// Sections is each declared slot's rendered section, keyed by slot name: a
	// heading and its content together, or the empty string for a slot that
	// failed to resolve or resolved to nothing. A marker standing for an empty
	// section leaves nothing behind, which is how a template's own headings
	// avoid outliving the content under them.
	Sections map[string]string

	// Values are the instance values a template may substitute, keyed by the
	// names in [ValueNames]. A name in that list and absent here substitutes
	// nothing, which is what an undeclared scope looks like.
	//
	// A key outside that list is not a way to reach a template. Substitution
	// fills a value marker only from the names [ValueNames] declares, so
	// putting spec.mcp under the key "mcp" here renders nothing rather than
	// rendering the servers' env into the file an agent reads. The key set is
	// a boundary, not a convention — see [Substitute].
	Values map[string]string

	// Files is the manifest's arbitrary files, keyed by boot-directory-relative
	// path, with every value already resolved. It arrives resolved for the
	// same reason Boot does: a files entry may name a slot source, and
	// resolving one runs commands and makes requests. Nil renders no extra
	// files.
	Files map[string]string

	// Subagents are the definitions this instance plants, in the order the
	// manifest named them, each carrying the named profile's own declaration.
	// They arrive resolved for the same reason Boot and Files do: naming a
	// subagent means reading another profile out of the catalog and walking
	// its extends chain, and a renderer reads nothing it was not handed. Empty
	// renders no definitions.
	Subagents []Subagent
}

// Renderer produces one boot-directory artifact. It is a value rather than an
// interface so that registering an artifact is one entry in [Renderers] plus a
// render function in its own file.
//
// A renderer receives only an [Instance]. It must not consult a clock, a
// random source, or the process environment: the same instance has to render
// byte-identical files every time. It may read the content the instance names
// — that is how a skill's bytes reach a [File] — but only from a path the
// instance carries, never one it resolves itself.
type Renderer struct {
	// Artifact names what this renderer produces, for diagnostics and for
	// reading the registration list. It is a label, not a path: the skills
	// renderer emits many files.
	Artifact string

	// Render returns the files this renderer contributes. Returning no files
	// is legal and means the profile declared nothing for this artifact.
	Render func(inst *Instance) ([]File, error)
}

// Render returns every file of inst's boot directory, with each path validated
// and checked for collisions. It writes nothing — it only reads the content
// inst names — so a caller can diff a rendering against disk without touching
// the boot directory.
//
// Errors wrap [ErrNoProfile], [ErrArtifactPath] or [ErrDuplicatePath], or come
// from a renderer unchanged.
func Render(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	return RenderWith(RenderersFor(inst.Layout.Provider), inst)
}

// RenderWith is [Render] over a caller-supplied renderer list.
//
// It exists for the installed layer, which is the same artifacts written to
// different paths and without the ones that only make sense per session — a
// boot file assembled from slots, and MCP servers a boot directory declares.
// Rendering it through this rather than through a second copy of every
// renderer is what keeps the two layers from drifting apart: they are the same
// functions over an [Instance] whose [Layout] names different paths.
func RenderWith(renderers []Renderer, inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	seen := make(map[string]struct{})
	var out []File
	for _, r := range renderers {
		files, err := r.Render(inst)
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", r.Artifact, err)
		}
		for _, f := range files {
			clean, err := CleanArtifactPath(f.Path)
			if err != nil {
				return nil, fmt.Errorf("render %s: %w", r.Artifact, err)
			}
			if _, dup := seen[clean]; dup {
				return nil, fmt.Errorf("render %s: %w: %q", r.Artifact, ErrDuplicatePath, clean)
			}
			seen[clean] = struct{}{}
			f.Path = clean
			out = append(out, f)
		}
	}
	return out, nil
}

// CleanArtifactPath validates a rendered file's path and returns it cleaned.
//
// Artifact paths are slash-separated by contract, so a backslash is rejected
// rather than silently accepted as a filename character on Unix and a
// separator on Windows. The cleaned path is then put to
// [agentlaunch.ValidateBootDirRelPath], which is the same question asked by
// the library that plants boot directories for the rest of the portfolio: two
// independent opinions on whether a path stays inside the directory, and a
// disagreement fails the render.
func CleanArtifactPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("%w: the path is empty", ErrArtifactPath)
	}
	if strings.ContainsRune(raw, '\\') {
		return "", fmt.Errorf("%w: %q is not slash-separated", ErrArtifactPath, raw)
	}
	if path.IsAbs(raw) {
		return "", fmt.Errorf("%w: %q is absolute", ErrArtifactPath, raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q does not name a file inside the boot dir", ErrArtifactPath, raw)
	}
	if err := agentlaunch.ValidateBootDirRelPath(clean); err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrArtifactPath, raw, err)
	}
	return clean, nil
}
