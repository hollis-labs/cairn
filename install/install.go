// Package install renders the installed layer — the configuration a harness
// reads for every session on the machine, rather than for one boot directory
// — from the same profile a boot directory is rendered from.
//
// It is the same artifacts at different paths. The renderers are
// [github.com/chrispian/cairn/bootdir]'s, run over an [bootdir.Instance] whose
// [bootdir.Layout] names the installed paths, so the two layers cannot drift
// apart the way two copies of a renderer would. What is not shared is what
// only makes sense per session: a boot file assembled from slots, the MCP
// servers a boot directory declares, and the arbitrary paths spec.files
// plants.
//
// # cairn install is human-executed
//
// An agent running under the provider home this package writes rewrites its own
// live configuration mid-session. Nothing in this package is safe to invoke
// against a live home to "check that it works"; [Check] against a fixture root
// is.
package install

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/profile"
)

// StagingPattern is the [os.MkdirTemp] pattern for the directory a render is
// staged in before it is moved into place. It is created inside the install
// root so that every move is a rename within one filesystem.
const StagingPattern = ".cairn-install-*"

// ErrNoRoot reports that the install root is not set.
var ErrNoRoot = errors.New("install root is not set")

// ErrRootNotAbsolute reports an install root that is not an absolute path.
var ErrRootNotAbsolute = errors.New("install root is not an absolute path")

// ErrRootNotFound reports that the install root does not exist. Unlike the
// database, Cairn never creates it: the installed layer goes inside a home
// directory that already exists, and creating one would mean cairn had
// resolved the wrong path.
var ErrRootNotFound = errors.New("install root not found")

// ErrRootNotDirectory reports an install root that exists and is not a
// directory.
var ErrRootNotDirectory = errors.New("install root is not a directory")

// ErrNoProfile reports that a [Layer] carries no resolved profile.
var ErrNoProfile = errors.New("layer has no resolved profile")

// File is one rendered file of the installed layer, with a path relative to
// the install root. It is [bootdir.File] because it is the same artifact.
type File = bootdir.File

// Layer is one render of the installed layer: where it goes, what it is
// rendered from, and the material rendering needs that the profile does not
// carry. Everything that varies is here, so nothing below reads the
// environment.
type Layer struct {
	// Root is the directory the provider directories are written beneath. The
	// zero Root is an error, never a default.
	Root Root

	// Profile is the resolved profile the layer is rendered from.
	//
	// It is normally abstract — the installed layer is usually the root of the
	// cascade — and that is not checked. Refusing an abstract profile here
	// would refuse the profile this package mostly exists to render; `cairn
	// boot` is where a direct boot of one is refused.
	Profile *profile.Resolved

	// Provider is the harness this layer is materialized for. The zero value
	// means the profile's own declaration, which is what `--provider`
	// defaults to.
	//
	// It is a field rather than a read of Profile.Provider because the two
	// answer different questions. The profile says which harness it was
	// written against; this says which one is being written now, and the
	// second is the operator's call — spec.settings carries a document per
	// provider precisely so that one profile can be materialized into more
	// than one. Everything below reads it through [Layer.provider] so there is
	// one answer rather than four lookups that could disagree.
	Provider profile.Provider

	// Home is the operator's home directory, used to expand a manifest path
	// written with a leading "~/". Carried rather than read, for the reason
	// [bootdir.Instance].Home is.
	Home string

	// Templates is the manifest's templates, keyed by destination, with every
	// value already resolved to its text. It arrives resolved for the reason
	// [bootdir.Instance].Templates does: a template may name a source, and
	// resolving one is I/O.
	//
	// Only the destinations the provider's tree lists under `installed` are
	// rendered. The rest are boot-directory artifacts.
	Templates map[string]string

	// Sections is each declared slot's rendered section, keyed by slot name,
	// resolved by the caller from the kinds in [slots.DeterministicKinds] and
	// no others. A template's markers for anything else substitute nothing
	// here — see [layerInstance].
	Sections map[string]string

	// Env answers an environment variable named in a manifest path — the
	// skills directory, and each of spec.access.directories. Carried for the
	// reason [bootdir.Instance].Env is.
	//
	// It is the one input to this layer that the operator's shell decides, and
	// that matters here in a way it does not for a boot directory. [Check]
	// re-renders and diffs against disk, so a manifest path holding a variable
	// makes the comparison depend on the environment the check ran from:
	// installed with it set and checked with it unset reports the file
	// modified with no source change. An access directory is the likeliest
	// place for that, since a grant fails closed and the drift is the only
	// signal. Nothing here prevents it — a renderer is handed a lookup and asks
	// no questions about it — so a profile that wants an installed layer worth
	// checking spells its paths out or uses "~/".
	Env profile.Expander

	// Values are the instance values a template may substitute, keyed by the
	// names in [bootdir.ValueNames]. This layer is not one session, so the ones
	// that describe a session — scope, and the session segment — are empty here
	// and substitute nothing.
	//
	// A key outside that list substitutes nothing either, which matters because
	// this field is public and an external caller fills it directly. Reaching a
	// template is not something a key added here can do: substitution fills a
	// value marker only from the names cairn declares it fills.
	Values map[string]string
}

// provider returns the harness this layer is materialized for: [Layer].
// Provider when it names one, and the profile's own declaration otherwise.
//
// The fallback is the flag's own default written into the type. `--provider`
// selects a target and defaults to what the profile declares, so a Layer built
// without one behaves exactly as it did before a target could be selected —
// and a caller outside cmd/cairn that never heard of the flag keeps rendering
// the harness its profile names.
//
// A nil Profile answers the empty provider, which every caller of this already
// refuses by name: [Render] and [checkLayer] test the profile first, and
// harnessFor reports the empty provider rather than defaulting.
func (l *Layer) provider() profile.Provider {
	if l.Provider != "" {
		return l.Provider
	}
	if l.Profile == nil {
		return ""
	}
	return l.Profile.Provider
}

// Result is what one [Install] wrote.
type Result struct {
	// Root is the install root the files were written beneath.
	Root string

	// Files are the paths written, relative to the root and slash-separated,
	// in render order. It is the manifest [Check] diffs against disk, and the
	// set its sweep treats as cairn's.
	Files []string
}

// Renderer produces one artifact of the installed layer.
//
// It carries the same [bootdir.Renderer] the boot directory runs, plus where
// the artifact lands and, when the artifact is a directory, which of its
// subdirectories cairn fills — see [Renderer.Fills].
type Renderer struct {
	// Artifact names what this renderer produces, for diagnostics and for
	// reading the registration list. It is a label relative to the provider
	// directory, not a path.
	Artifact string

	// Fills names the subdirectories of Artifact this renderer writes whole,
	// read from the profile the layer is rendered from. A nil Fills means
	// Artifact is one file.
	//
	// It says that Artifact is a directory cairn writes into rather than one
	// cairn owns, and it is how [Check] learns which parts of it are cairn's.
	// A named subdirectory is claimed and swept to the bottom; anything else
	// in Artifact is the operator's and is reported as [StatusUnclaimed],
	// which says what was found without failing the check.
	//
	// The names come from the manifest through this registration and never
	// from a render, which is what keeps a leftover findable: a profile that
	// stopped shipping one file of a skill it still declares renders less than
	// it did, and a claim set scoped to the render would stop looking in
	// exactly that case. Per named subdirectory, the old whole-directory
	// property holds unchanged.
	//
	// What is not named is not claimed, and that is the point. ~/.claude/skills
	// holds skills the operator wrote as well as the ones cairn plants, and a
	// rule that claimed the directory whole reported every hand-written one as
	// drift on every run — the same disease [SweepPlan] describes for
	// settings.local.json, one level down.
	Fills func(*profile.Resolved) ([]string, error)

	// Merge, when set, narrows the claim inside one file: it is handed the
	// render and whatever is already at the artifact's path, and returns the
	// bytes cairn will write there.
	//
	// It is the third narrowing on this type and the one for a file whose
	// contents are only partly cairn's. Fills says which parts of a directory
	// artifact are claimed; Normalize says how a claim is compared; this says
	// what a claim covers inside one file. [SweepPlan] needs no new shape for
	// it — settings.json is still one exact [SweepPlan.Claims] path, still
	// rendered on every run, so the sweep half never reaches it.
	//
	// It exists for the same disease Fills was written for, in a file rather
	// than a directory. ~/.claude/settings.json is a document the harness and
	// the operator write too, and a render-and-overwrite install deletes
	// whatever they put there: it demonstrably did, to a live `model` key that
	// cairn deliberately declines to declare. What cairn did not render is not
	// cairn's to remove — see [mergeSettingsDocument] for what that means key
	// by key.
	//
	// A merge changes what both halves of the layer mean, and it has to change
	// both or neither. [Install] writes the merge rather than the render, so
	// the operator's keys survive; [Check] compares the merge against what is
	// on disk rather than the render, so the question it answers becomes
	// "would an install change this file?" — and a key cairn never declared is
	// then neither written nor reported, which is the whole rule.
	//
	// It runs before Normalize. A merge that cannot safely preserve the existing
	// document returns an error so Install can refuse before overwriting it.
	//
	// A renderer without one is written and compared as the render, which is
	// the default for every artifact cairn owns whole.
	Merge func(rendered, existing []byte) ([]byte, error)

	// Claim, when set, decides whether a file artifact is claimed for this
	// resolved profile before it is rendered. It is only for optional files
	// whose renderer legitimately produces nothing when no matching manifest
	// input exists.
	Claim func(*profile.Resolved) (bool, error)

	// Normalize, when set, is applied to the render and to the bytes on disk
	// before a check compares them, so that a difference it forgives is a
	// difference in neither.
	//
	// It exists for one shape of false alarm. A JSON artifact is written by
	// cairn and rewritten by the harness, and the two lay the same document out
	// differently; a byte comparison then reports every run as drift over
	// whitespace, which is the failure mode [SweepPlan] describes for a sweep
	// that claims too much. Normalizing is how a check keeps saying something.
	//
	// What it forgives has to stay narrow, and that is why this is a byte
	// transformation rather than a comparison. [bootdir.IndentJSON] moves
	// whitespace and changes nothing else, so a changed value, a respelled
	// number and a collapsed duplicate all still read as modified. A
	// normalizer that parsed both sides and compared them as values would
	// forgive far more than the operator asked it to, in the one layer that is
	// not disposable.
	//
	// It stays a whitespace transformation now that Merge exists beside it.
	// Which keys a check answers for is Merge's question and is settled before
	// this runs; what this decides is only how the two documents are laid out
	// when they are compared. Folding one into the other would put both
	// answers in a function whose name promises neither.
	//
	// A renderer without one is compared byte for byte, which is the default
	// for every artifact that is prose.
	Normalize func([]byte) []byte

	// Render is the boot-directory renderer this artifact is produced by. The
	// instance it is handed carries a [bootdir.Layout] naming the installed
	// paths, so the same function serves both layers.
	Render func(inst *bootdir.Instance) ([]File, error)

	// Kind is the artifact kind the tree declared — one of bootdir's Kind
	// constants. It is what decides which artifact carries the generated-file
	// marker, so that the decision is made on what an artifact *is* rather
	// than on how its path happens to be spelled.
	Kind string
}

// installRenders maps an installed artifact kind onto the shared
// boot-directory renderer that produces it.
//
// The installed layer is deliberately shorter than a boot directory's, and the
// shortness is in the documents rather than here: a tree lists the artifacts
// this layer holds, and what it does not list is not rendered.
//
// There is no boot file: slots are resolved when an instance is materialized,
// and the installed layer is not. There is no MCP configuration: plan §6 drops
// the audit that used to guard it, and user-level MCP is not a file in this
// directory.
//
// There are no spec.files, no spec.trees, and no subagent definitions, and
// that follows from where the two layers write rather than from anyone's
// taste. A boot directory is created fresh and refuses to plant if it already
// exists, so an arbitrary path→content map can only ever land on empty ground.
// The installed layer writes into a directory that already exists and is full
// of the operator's live state, where the same map lands on whatever is already
// there. It compounds with the sweep: rendering them here would make cairn
// start claiming ownership of arbitrary paths in a home directory for
// orphan-reporting purposes, and having claimed them, report on them.
//
// Templates are rendered, but only the destinations a tree's installed section
// names. A template free to name any path in the operator's home would be the
// same problem in a new key, and it would cost the check its whole point:
// which artifacts cairn claims is settled by the tree, not by the profile being
// checked, which is what lets a check report a file left behind by a profile
// that stopped declaring one. A template declared for any other destination is
// a boot-directory artifact and is not rendered here.
//
// [Renderer.Fills] is not a hole in that. It lets a profile name the
// subdirectories of an artifact a tree already lists — never a new artifact,
// and never a path outside one — and the leftover case still holds inside
// every subdirectory named.
//
// The map is keyed by implementation name, and an artifact that names none
// uses the implementation registered under its kind. That is how one kind can
// have two documents: a settings artifact is JSON here and TOML there, and the
// tree says which, exactly as it does for a boot directory.
var installRenders = map[string]func(inst *bootdir.Instance) ([]File, error){
	bootdir.KindAgents:          bootdir.RenderAgentsTemplate,
	bootdir.KindPointer:         bootdir.RenderPointerTemplate,
	bootdir.KindSettings:        bootdir.RenderSettings,
	bootdir.KindSkills:          bootdir.RenderInstallSkills,
	bootdir.CodexConfigRenderer: bootdir.RenderCodexConfig,
}

// installMerges, installNormalizers, installClaims and installFills are the
// behaviours a tree's installed artifact may name.
//
// The documents name which one applies; the logic stays here. Preserving an
// operator's `model` and `modelSettings` while rewriting the keys cairn owns
// is real logic and not a template, and moving it into a document would only
// mean writing an interpreter for it.
var (
	installMerges = map[string]func(rendered, existing []byte) ([]byte, error){
		"json-settings": mergeSettingsArtifact,
		"toml-document": mergeTOMLDocument,
	}
	installNormalizers = map[string]func([]byte) []byte{
		"json-indent":   bootdir.IndentJSON,
		"toml-document": normalizeTOMLDocument,
	}
	installClaims = map[string]func(*profile.Resolved) (bool, error){
		"codex-config": codexConfigClaimed,
	}
	installFills = map[string]func(*profile.Resolved) ([]string, error){
		"install-skills": installedSkillNames,
	}
)

// ErrInstalledLayout reports a tree whose installed section names an artifact
// kind or a behaviour this package does not have. It is a malformed document
// rather than anything an operator did, and it is an error rather than an
// omission for the reason every other refusal here is: an installed layer
// missing an artifact looks exactly like one that never had it.
var ErrInstalledLayout = errors.New("installed layout names something cairn does not have")

// installRenderers builds one provider's installed renderers, in the order its
// tree lists them, resolving each artifact's kind and behaviours.
func installRenderers(il bootdir.InstalledLayout) ([]Renderer, error) {
	out := make([]Renderer, 0, len(il.Artifacts))
	for _, a := range il.Artifacts {
		impl := a.Render
		if impl == "" {
			impl = a.Kind
		}
		render, ok := installRenders[impl]
		if !ok {
			return nil, fmt.Errorf("%w: the %s tree installs an artifact of kind %q with %q",
				ErrInstalledLayout, il.Layout.Provider, a.Kind, impl)
		}
		r := Renderer{Kind: a.Kind, Artifact: a.Label, Render: render}
		if err := namedBehaviour(a.Merge, installMerges, &r.Merge, "merge", il, a); err != nil {
			return nil, err
		}
		if err := namedBehaviour(a.Normalize, installNormalizers, &r.Normalize, "normalize", il, a); err != nil {
			return nil, err
		}
		if err := namedBehaviour(a.Claim, installClaims, &r.Claim, "claim", il, a); err != nil {
			return nil, err
		}
		if err := namedBehaviour(a.Fills, installFills, &r.Fills, "fills", il, a); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// namedBehaviour looks name up in table and assigns it to dest, leaving dest
// alone when the document named none.
func namedBehaviour[F any](name string, table map[string]F, dest *F, what string,
	il bootdir.InstalledLayout, a bootdir.InstalledArtifact) error {
	if name == "" {
		return nil
	}
	fn, ok := table[name]
	if !ok {
		return fmt.Errorf("%w: the %s tree's %q artifact names the %s behaviour %q",
			ErrInstalledLayout, il.Layout.Provider, a.Label, what, name)
	}
	*dest = fn
	return nil
}

func codexConfigClaimed(resolved *profile.Resolved) (bool, error) {
	if resolved == nil {
		return false, ErrNoProfile
	}
	if _, declared, err := resolved.Spec.Settings(profile.ProviderCodex); declared || err != nil {
		return declared, err
	}
	dirs, err := resolved.Spec.AccessDirectories()
	if err != nil {
		return false, err
	}
	if len(dirs) > 0 {
		return true, nil
	}
	mcp, err := resolved.Spec.MCP()
	if err != nil {
		return false, err
	}
	return len(mcp) > 0, nil
}

// installedSkillNames returns the skill directories the installed layer claims
// inside its skills directory: the ones spec.install.skills names, and no
// others.
//
// It is the skills artifact's [Renderer.Fills], so what cairn claims there is
// what the profile declared rather than what one render happened to write —
// and a directory the profile never named is left alone.
func installedSkillNames(resolved *profile.Resolved) ([]string, error) {
	if resolved == nil {
		return nil, ErrNoProfile
	}
	return resolved.Spec.InstallSkills()
}

// GeneratedMarker returns the one line the installed instruction file opens
// with: the command that wrote it and the profile it came from.
//
// Plan §9 holds cairn's own prose out of rendered agent files until the
// operator has reviewed it, and the test that separates this from what §9
// guards is whether the operator could have written the same sentence in a
// profile body. "Escalate to your reports_to" — yes, trivially, and cairn
// taking that sentence is precisely what §9 is about. "These bytes were
// rendered by cairn install from profile X" — no. A profile body cannot
// truthfully assert its own rendering provenance; the claim would be false the
// moment the same body rendered anywhere else. Only the renderer knows. §9
// prohibits cairn making claims about an agent's conduct or about what content
// means; this is cairn describing its own action, which it is the sole
// authority on.
//
// It states the fact and stops: no advice about what to edit instead, and
// nothing about which session sees it. It is an HTML comment so that an
// operator opening the file in an editor sees it while it stays quiet in an
// agent's context.
//
// The boot directory gets no marker. It is created fresh, disposable, and
// never hand-edited with an expectation of persistence, so the line would be
// noise in every session's context with no reader to serve.
func GeneratedMarker(profileID string) string {
	return "<!-- Generated by `cairn install` from profile " + strconv.Quote(profileID) + ". -->"
}

// DirMode is the mode a directory of the installed layer is created with.
const DirMode fs.FileMode = 0o755
