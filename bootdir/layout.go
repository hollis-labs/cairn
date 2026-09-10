// Package bootdir renders a resolved profile into the files of one boot
// directory and writes them there.
//
// Rendering and writing are separate on purpose. [Render] produces every file
// in memory, so a render that fails writes nothing; [Plant] stages the whole
// tree beside the target and moves it into place with one rename, so a boot
// directory is either complete or absent. A half-built boot directory is one
// an agent might boot from.
//
// # The layout is a document, not code
//
// Where each artifact lands is read from a layout document — one per harness,
// under [LayoutDir], embedded in the binary and parsed at startup. No file
// name a harness reads appears in this package's Go: not AGENTS.md, not
// .claude/skills, not config.toml. A second harness is a second tree rather
// than a second code path, and the opinion about what a boot directory should
// contain belongs to whoever authored the tree.
//
// The consequence worth stating is the one cairn wanted: nothing here requires
// a profile to declare an instruction file, because nothing here can recognize
// one. A profile renders what it declares, where the tree says it goes.
package bootdir

import (
	"errors"
	"io/fs"

	"github.com/chrispian/cairn/profile"
	goprovider "github.com/hollis-labs/go-providers/provider"
)

// SkillFileName is the file a skill directory must hold for a harness to load
// the skill at all.
//
// It is not in a layout document because it is not a placement. Both harnesses
// require it, and what it governs is the source directory cairn copies from
// rather than where the copy lands: a directory without it is not a skill, and
// planting one would be planting something no harness will read.
const SkillFileName = "SKILL.md"

// JSONIndent is one level of indentation in a rendered JSON artifact.
//
// Every JSON file cairn writes is laid out rather than compacted, because the
// operator reads these files and diffs them. Two spaces is the harness's own
// convention: Claude Code rewrites the settings document it was given at this
// width, so a rendered document and the one the harness writes back differ by
// their content and never by their shape.
const JSONIndent = "  "

// DefaultFileMode is the mode a rendered file is written with when its [File]
// carries none.
const DefaultFileMode fs.FileMode = 0o644

// DefaultDirMode is the mode the boot directory and every directory inside it
// is created with.
const DefaultDirMode fs.FileMode = 0o755

// ErrUnsupportedProvider reports that cairn holds no layout document for a
// profile's provider, or that the profile declares none at all.
var ErrUnsupportedProvider = errors.New("unsupported provider")

// ErrProviderLayout reports that a layout has nowhere to put something: a
// provider's BootDirSpec no longer declares an artifact the tree takes from
// it, the tree is malformed, or the manifest declares a document this tree
// carries no destination for.
//
// It is an error rather than a fallback in each of those, because the failure
// it prevents is silent and because none of them is an operator's to clear: a
// malformed tree and a spec that moved on are cairn's and the library's, and a
// tree with no instruction artifact renders an agent no instructions at all.
//
// It is NOT what a declared prompt or subagent gets from a tree with no
// directory for one. That was this error's third case, and it retired: the
// content is dropped and named on stderr instead — see [Undeclared] for why,
// and for the one thing that must not happen either way, which is silence.
var ErrProviderLayout = errors.New("this layout has no destination for that")

// Artifact is one file of a boot directory or an installed layer: which
// template destination it renders from, where the harness reads it, and the
// mode it is written with.
type Artifact struct {
	// Dest is the spec.templates destination this artifact renders from, for
	// the artifacts that render from one. Empty for the artifacts cairn builds
	// rather than substitutes — the MCP configuration and the settings
	// document.
	//
	// It is carried rather than assumed because the two layers differ: a boot
	// directory plants a template where the manifest declared it, and the
	// installed layer maps a destination onto a path of its own.
	Dest string

	// RelPath is the path relative to the boot directory root or the install
	// root, slash-separated. Empty means the layout does not carry this
	// artifact.
	RelPath string

	// Mode is the permission mode. Zero means [DefaultFileMode].
	Mode fs.FileMode
}

// Declared reports whether a carries a path at all.
func (a Artifact) Declared() bool { return a.RelPath != "" }

// Layout is where one provider's harness reads each boot-directory artifact
// from. Every field is read from that harness's layout document.
//
// An artifact whose document entry says provider_path is taken from
// go-providers' BootDirSpec, which is the library that owns each harness's
// on-disk convention. Cairn takes the path and the mode from there and
// supplies its own content: the spec's own render functions are never called,
// because some of them have side effects on the operator's real home
// directory.
type Layout struct {
	// Provider is the harness this layout describes.
	Provider profile.Provider

	// Renders names the renderers this tree carries, in render order. Each is
	// a manifest key, and [Renderers] resolves one to the function that
	// renders it.
	//
	// A renderer absent here is one whose content this harness materializes
	// some other way, and that is different from one whose destination is
	// undeclared. Codex has no MCP file because its servers are keys of
	// config.toml, so `mcp` is absent from its list; it has no prompts
	// directory at all, so `prompts` is present and the renderer refuses when
	// a profile declares any.
	Renders []string

	// RenderImpls names which implementation renders a key, for the keys where
	// there is more than one. A key absent here is rendered by the
	// implementation of the same name.
	//
	// It is what keeps a difference in a document's *content* from being a
	// second code path too. A settings document is JSON for one harness and
	// TOML for another, with different keys and a different place for MCP
	// servers — that is not a placement, so a path cannot express it, and a
	// tree names the renderer it wants rather than cairn testing which
	// provider it is.
	//
	// The key stays the label a diagnostic quotes, so "render settings:" reads
	// the same whichever implementation ran.
	RenderImpls map[string]string

	// Agents is where the template destination Agents.Dest names is written.
	Agents Artifact

	// Pointer is the harness's own instruction file, where it reads one
	// separate from Agents. Undeclared when the harness reads Agents directly.
	Pointer Artifact

	// MCP is the MCP server configuration.
	MCP Artifact

	// Settings is the harness settings document, written from the profile's
	// manifest, laid out and not otherwise touched.
	Settings Artifact

	// SkillsDir is the directory declared skills are planted under, one
	// directory per skill.
	SkillsDir string

	// SubagentsDir is the directory subagent definitions are planted under,
	// one file per named profile.
	SubagentsDir string

	// PromptsDir is the directory declared prompts are planted under, one file
	// per prompt.
	PromptsDir string

	// PromptNamespace is the prefix a planted prompt is invoked by:
	// `/<namespace>:<name>`. Empty for a tree that plants no prompts.
	PromptNamespace string

	// DropTemplates are the spec.templates destinations this tree has no
	// reader for. A profile that declares one renders nothing rather than
	// planting a file the harness never opens.
	DropTemplates []string

	// CwdPreference is where the harness expects to be invoked, and
	// ProjectDirArg is its flag pattern for granting access to the scope
	// directory. Cairn does not launch anything, so nothing here reads them;
	// they are carried so that the caller printing a boot directory can also
	// print how to open it.
	CwdPreference goprovider.CwdPreference
	ProjectDirArg string
	EnvAmendments []string

	// HomeResourcePaths are provider-home-relative resources Cairn does not
	// render, but a launcher must deliberately provide when EnvAmendments
	// points the harness at the boot directory as its home.
	HomeResourcePaths []string
}

// Drops reports whether dest is a template destination this tree has no reader
// for.
func (l Layout) Drops(dest string) bool {
	for _, d := range l.DropTemplates {
		if d == dest {
			return true
		}
	}
	return false
}

// InstalledLayout is where one provider's installed layer is written: the
// directory beneath the install root, the artifacts in render order, and the
// [Layout] the shared renderers read their paths from.
//
// It is one value rather than three lookups so that the directory and the
// paths in the layout cannot be answered from different places and disagree.
type InstalledLayout struct {
	// Dir is the provider directory relative to the install root, and the
	// containment boundary a render is checked against. "." is the root
	// itself, which is what a harness reading files from two unrelated
	// directories needs.
	Dir string

	// Artifacts are what this layer holds, in render order.
	Artifacts []InstalledArtifact

	// Layout names each artifact's path beneath the install root.
	Layout Layout
}

// InstalledArtifact is one artifact of an installed layer as its document
// declares it.
//
// Merge, Normalize, Claim and Fills name logic rather than carry it. Preserving
// an operator's own keys while rewriting the ones cairn owns is real logic and
// stays in Go; what belongs in a document is only which of it applies where.
type InstalledArtifact struct {
	// Kind is the artifact kind — the same names a boot tree uses.
	Kind string

	// Label is the artifact's path relative to [InstalledLayout.Dir]. It is
	// what a diagnostic quotes and what the sweep plan joins onto the
	// directory.
	Label string

	// Dest is the spec.templates destination this artifact renders from, empty
	// for the artifacts that render from none.
	Dest string

	// Render names the implementation that produces this artifact, for the
	// kinds where there is more than one. Empty means the implementation
	// registered under Kind.
	Render string

	// Merge, Normalize, Claim and Fills are the names of the behaviours this
	// artifact is rendered and compared through, empty for none.
	Merge     string
	Normalize string
	Claim     string
	Fills     string
}

// LayoutFor returns the [Layout] one provider's boot directory is rendered
// through.
//
// A provider cairn holds no layout document for reports
// [ErrUnsupportedProvider] rather than falling back to a layout that would
// write another harness's files, and so does a profile that declares no
// provider at all.
//
// The refusal names what is implemented, which it did not have to before
// `--provider` existed. A provider used to be something a profile declared, so
// the reader of this diagnostic was looking at the file that said it; now it
// is something an operator can ask for at the terminal, and the answer to
// "codex, then" is worth one clause rather than a lookup. What it must never
// be is a fallback: [profile.Providers] knows three names and cairn holds
// trees for two of them, and a target silently redirected to claude's tree
// would put claude's files at claude's paths for a harness that reads neither.
func LayoutFor(p profile.Provider) (Layout, error) {
	doc, err := layoutFor(p)
	if err != nil {
		return Layout{}, err
	}
	return doc.bootLayout()
}

// InstalledLayoutFor returns the [InstalledLayout] one provider's installed
// layer is rendered through, reporting [ErrUnsupportedProvider] for a harness
// cairn holds no tree for.
func InstalledLayoutFor(p profile.Provider) (InstalledLayout, error) {
	doc, err := layoutFor(p)
	if err != nil {
		return InstalledLayout{}, err
	}
	return doc.installedLayout()
}
