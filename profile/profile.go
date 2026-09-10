// Package profile holds the declared profile, the resolved profile a boot
// directory is rendered from, and the extends cascade between them.
//
// A profile is a markdown file's YAML frontmatter plus an opaque rendering
// manifest, converted to JSON as the catalog is read. Cairn interprets only the
// manifest keys it renders and carries the rest untouched, so a key this
// package has never heard of survives the cascade and reaches whatever does
// know it.
package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/hollis-labs/agentkit/agentcontext"
	"github.com/hollis-labs/agentkit/agentlaunch"
)

// Provider names the CLI coding agent harness a profile's boot directory is
// rendered for. It is the profiles table's provider column.
type Provider string

const (
	// ProviderClaude is the Claude Code CLI.
	ProviderClaude Provider = "claude"

	// ProviderCodex is the Codex CLI.
	ProviderCodex Provider = "codex"

	// ProviderOpenCode is the opencode CLI.
	ProviderOpenCode Provider = "opencode"
)

// ErrSettingsProvider reports a spec.settings that is not one document per
// provider: a member naming no harness Cairn knows, or a value that is not an
// object at all.
//
// It is the diagnostic the authoring format change owes an operator. Before
// this key was keyed by provider it held one document, and a profile still
// carrying that shape is not wrong about anything except where it is written —
// so the refusal names the member it found and the shape to move it to, rather
// than reporting a schema violation and leaving the reader to find the memo.
var ErrSettingsProvider = errors.New("settings document is not keyed by provider")

// Providers returns every harness Cairn has a name for, in the order the
// constants above declare them.
//
// It is the one list, and [Provider.Valid] reads it rather than restating it
// as a switch. Naming a provider is now something an operator does — spec.
// settings is keyed by these names, and `--provider` selects one — so a
// diagnostic that has to say what may be written there reads the same list the
// check reads. The caller receives a fresh slice it may modify.
//
// Knowing a name is not the same as having a layout for it. Two of these three
// render nothing today: [github.com/chrispian/cairn/bootdir.LayoutFor] refuses
// them by name, which is a separate answer to a separate question and is
// deliberately not folded in here. A profile may declare settings for a
// provider Cairn cannot yet materialize into, and that document is carried
// rather than refused — it is what makes one profile serve every target.
func Providers() []Provider {
	return []Provider{ProviderClaude, ProviderCodex, ProviderOpenCode}
}

// Valid reports whether p names a harness Cairn knows. The empty provider is
// not valid: a profile that declares no harness is a configuration error
// rather than a default.
func (p Provider) Valid() bool { return slices.Contains(Providers(), p) }

// String returns p as its stored string.
func (p Provider) String() string { return string(p) }

// Spec is a profile's rendering manifest: opaque JSON held as its top-level
// keys, of which Cairn interprets only the ones it renders.
//
// Holding the values as [json.RawMessage] is what makes an unknown key
// harmless. The cascade looks inside a key only when an explicit table names
// it as a keyed collection; every other key, known or not, is taken whole. A
// key nothing renders is written back out exactly as it arrived — see
// [specMergers] for why that promise rules out inferring a merge from the
// shape of the JSON.
type Spec map[string]json.RawMessage

// Manifest keys Cairn renders. Every other key in a [Spec] is carried and
// ignored — see the package comment.
const (
	// SpecKeySlots holds a list of agentcontext.SlotSpec assembled into
	// boot.md.
	SpecKeySlots = "slots"

	// SpecKeyMCP holds a list of agentlaunch.MCPServerSpec rendered into
	// .mcp.json.
	SpecKeyMCP = "mcp"

	// SpecKeySkills holds the names of the skill directories copied into the
	// boot directory's skills tree.
	SpecKeySkills = "skills"

	// SpecKeySkillsDir holds the directory those skill names are copied from.
	// Cairn ships no skills, so a profile declaring skills has to say where
	// they live. Both skill sets resolve against it.
	SpecKeySkillsDir = "skills_dir"

	// SpecKeyPrompts holds the names of the prompt files rendered into the
	// boot directory's commands namespace, one slash-invokable command each.
	//
	// It is [SpecKeySkills] for content. A skill is a directory the harness
	// loads on its own; a prompt is one file a person invokes by name, and the
	// two are declared, cascaded and composed identically because the question
	// — which of the bundle's content does this boot directory carry — is the
	// same one.
	SpecKeyPrompts = "prompts"

	// SpecKeyPromptsDir holds the directory those prompt names are read from.
	// Cairn ships no prompts, so a profile declaring one has to say where it
	// lives — the same rule, for the same reason, as [SpecKeySkillsDir].
	//
	// It is a second key rather than a subdirectory of the skills root. The
	// two are separate collections of separate things, and a profile that
	// keeps its prompts somewhere else should not have to move its skills to
	// say so.
	SpecKeyPromptsDir = "prompts_dir"

	// SpecKeyInstall holds the keys only the installed layer reads. Its
	// "skills" are the skill directories `cairn install` plants into the
	// harness's own skills directory, resolved against [SpecKeySkillsDir] the
	// same way [SpecKeySkills] is.
	//
	// The two skill sets are two questions and not one. [SpecKeySkills] names
	// what a single boot directory carries; this one names what every session
	// on the machine loads. A profile that answered both from one key could
	// not say the first without saying the second.
	//
	// It is a nested object rather than a flat install_skills so that the next
	// install-only key has somewhere to go without another top-level name.
	SpecKeyInstall = "install"

	// SpecKeySettings holds one settings document per provider, keyed by the
	// provider's name — `settings: {claude: {...}, codex: {...}}`. The
	// document chosen for a materialization is written as the cascade composed
	// it and laid out for reading.
	//
	// Keyed by provider because a provider is a materialization target rather
	// than a property of the content. One profile's access, slots, templates
	// and skills serve every harness; only this key is written in a harness's
	// own vocabulary, so only this key is asked which harness it is for. See
	// [Spec.Settings], which selects one and refuses a document that names no
	// provider.
	SpecKeySettings = "settings"

	// SpecKeyAccess holds what a materialized instance is granted beyond the
	// directory it works in. Its "directories" are manifest paths, expanded
	// like every other one.
	//
	// It is neutral, and that is the point of the key existing at all: it
	// names directories rather than one harness's permission field, and the
	// renderer for each provider maps it onto whatever that harness reads.
	// The alternative was for a profile to write the harness's own key into
	// spec.settings, which is a declaration that cannot be carried to a
	// second provider and cannot be composed with the scope.
	//
	// The scope is granted without being declared — an instance works there —
	// and these add to it. It is a nested object for the reason
	// [SpecKeyInstall] is: the next question about what an instance may reach
	// has somewhere to go without another top-level name.
	SpecKeyAccess = "access"

	// SpecKeyFiles maps boot-directory-relative paths to their contents.
	SpecKeyFiles = "files"

	// SpecKeyTemplates maps boot-directory-relative paths to the template
	// text rendered into them. A template's value takes the same two shapes a
	// files value does — a literal, or a source resolved at materialization.
	SpecKeyTemplates = "templates"

	// SpecKeyTrees maps boot-directory-relative paths to the source directory
	// copied there whole.
	SpecKeyTrees = "trees"

	// SpecKeySubagents holds the ids of the profiles rendered as subagent
	// definitions in the boot directory. It is declared by the profile being
	// booted, and names other profiles.
	SpecKeySubagents = "subagents"

	// SpecKeySubagent holds one profile's own declaration of the definition it
	// is rendered as when another profile names it under [SpecKeySubagents].
	// It is an opaque map, carried into the definition's frontmatter the way
	// [SpecKeySettings] is carried into the settings document.
	SpecKeySubagent = "subagent"
)

// Profile is one profile as its file declares it: the frontmatter's fields,
// with the manifest converted into a [Spec] and nothing else interpreted.
//
// It is the input to the cascade, not the thing a boot directory is rendered
// from — see [Resolved].
type Profile struct {
	// ID is the profile's identity, and the name of the file it was read
	// from.
	ID string

	// Extends is the id of the profile this one inherits from, or empty at the
	// root of a chain.
	Extends string

	// Abstract marks a profile that exists to be extended rather than booted.
	// It does not cascade: a concrete profile extending an abstract one is
	// still concrete.
	Abstract bool

	// Name is the profile's display name.
	Name string

	// Description is a one-line summary.
	Description string

	// Provider is the harness the boot directory is rendered for.
	Provider Provider

	// Model is the model identifier handed to that harness.
	Model string

	// Body is the profile's prose. It is the one field the cascade
	// concatenates rather than overwrites.
	Body string

	// Spec is the rendering manifest.
	Spec Spec
}

// Resolved is a profile after the extends cascade: the fields a renderer
// reads, with every ancestor already folded in. Rendering never cascades
// again.
type Resolved struct {
	// ID is the profile the cascade was resolved for — the leaf of the chain.
	ID string

	// Chain is every profile id the cascade folded, in fold order: ID's own
	// chain ancestor-first, then whatever each composed part adds to it — see
	// [ResolveComposition]. No id appears twice, there as here: a part that
	// shares an ancestor with ID contributes only the profiles below it.
	// It is provenance: nothing renders from it.
	Chain []string

	// Abstract is ID's own leaf's flag, carried so a caller can refuse to boot
	// one. It is not inherited, and a composed part never contributes it.
	Abstract bool

	// AlreadyFolded lists the composed parts that contributed nothing, in the
	// order they were given: every profile in the part's own chain had been
	// folded before it was reached, so naming it changed nothing. It is nil
	// for a plain resolution, which composes no parts at all.
	//
	// It is reported rather than refused. Naming a part the resolution already
	// covers is a legitimate thing to do — it makes a composition explicit
	// about what it rests on — so this is a fact for the caller to pass on,
	// not a mistake to reject. See [ResolveComposition].
	AlreadyFolded []string

	// Name, Description, Provider and Model are the closest declared value of
	// each field in the chain.
	Name        string
	Description string
	Provider    Provider
	Model       string

	// Bodies is every declared body in the chain, in fold order and skipping
	// the profiles that declared none — see [Body].
	//
	// It is a list and not a concatenation, and that is the correction of a
	// defect rather than a refinement. The field used to be one string joined
	// ancestor-first "because the persona is additive", and NOTHING RENDERED
	// IT: a profile's prose was read, folded, and dropped. `cairn show` said
	// `cairn boot` rendered it, which was not true of either layer.
	//
	// A list is what the render engine needs, because composing bodies is not
	// concatenating them. A body is a template — see
	// [github.com/chrispian/cairn/template] — so the chain's sections fold
	// closest-wins and one document supplies the shape. Handing that engine a
	// string with the chain already flattened into it would make both
	// impossible.
	Bodies []Body

	// Spec is the composed manifest: a keyed collection merged member by
	// member across the chain, and every other key the value from the closest
	// profile that declares it. See [Resolve].
	Spec Spec
}

// Body is one profile's prose, with the profile it came from.
//
// The id travels with the text because everything the render engine says about
// a body has to name one: which document supplied the shape, which document's
// section won, and which document's prose did not render at all. A list of
// anonymous strings can answer none of those.
type Body struct {
	// ID is the profile the prose was declared in.
	ID string

	// Text is the prose, trimmed of the surrounding blank lines that are the
	// file's formatting rather than the author's content.
	Text string
}

// Slots returns the slot specifications under [SpecKeySlots]. A manifest
// declaring no slots returns nil and no error.
func (s Spec) Slots() ([]agentcontext.SlotSpec, error) {
	var out []agentcontext.SlotSpec
	if err := s.decode(SpecKeySlots, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MCP returns the MCP server definitions under [SpecKeyMCP]. A manifest
// declaring none returns nil and no error.
func (s Spec) MCP() ([]agentlaunch.MCPServerSpec, error) {
	var out []agentlaunch.MCPServerSpec
	if err := s.decode(SpecKeyMCP, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Skills returns the skill names under [SpecKeySkills]. A manifest declaring
// none returns nil and no error.
func (s Spec) Skills() ([]string, error) {
	var out []string
	if err := s.decode(SpecKeySkills, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Prompts returns the prompt names under [SpecKeyPrompts]. A manifest
// declaring none returns nil and no error.
func (s Spec) Prompts() ([]string, error) {
	var out []string
	if err := s.decode(SpecKeyPrompts, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// InstallSkills returns the skill names under [SpecKeyInstall]'s "skills". A
// manifest declaring no install key, and one declaring it without skills,
// both return nil and no error — the key is not obliged to exist, and one
// that does is not obliged to name skills.
//
// It is deliberately not [Spec.Skills]. What every session on the machine
// loads and what one boot directory carries are separate declarations, so a
// profile can hold the installed set without every profile extending it
// planting the same set beside its own boot file.
func (s Spec) InstallSkills() ([]string, error) {
	var installed struct {
		Skills []string `json:"skills"`
	}
	if err := s.decode(SpecKeyInstall, &installed); err != nil {
		return nil, err
	}
	return installed.Skills, nil
}

// AccessDirectories returns the paths under [SpecKeyAccess]'s "directories",
// spelled as the manifest wrote them. A manifest declaring no access key, and
// one declaring it without directories, both return nil and no error — the key
// is not obliged to exist, and one that does is not obliged to name a
// directory.
//
// Expansion is the caller's. A stored profile has no home directory and no
// environment, and [ExpandPath] needs both; the renderer that grants these has
// them on its instance, and it is also the one that knows what the paths are
// for. Returning them raw is what lets a diagnostic quote what the operator
// wrote rather than what an unset variable made of it.
func (s Spec) AccessDirectories() ([]string, error) {
	var access struct {
		Directories []string `json:"directories"`
	}
	if err := s.decode(SpecKeyAccess, &access); err != nil {
		return nil, err
	}
	return access.Directories, nil
}

// SkillsDir returns the directory under [SpecKeySkillsDir] that skill names
// are resolved against. A manifest declaring none returns the empty string and
// no error; whether that is a problem depends on whether any skill was
// declared, which is the skills renderer's question.
func (s Spec) SkillsDir() (string, error) {
	var out string
	if err := s.decode(SpecKeySkillsDir, &out); err != nil {
		return "", err
	}
	return out, nil
}

// PromptsDir returns the directory under [SpecKeyPromptsDir] that prompt names
// are resolved against. A manifest declaring none returns the empty string and
// no error; whether that is a problem depends on whether any prompt was
// declared, which is the prompts renderer's question.
func (s Spec) PromptsDir() (string, error) {
	var out string
	if err := s.decode(SpecKeyPromptsDir, &out); err != nil {
		return "", err
	}
	return out, nil
}

// Subagents returns the profile ids under [SpecKeySubagents]. A manifest
// declaring none returns nil and no error.
func (s Spec) Subagents() ([]string, error) {
	var out []string
	if err := s.decode(SpecKeySubagents, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Subagent returns this profile's own subagent declaration under
// [SpecKeySubagent] exactly as it was stored, and whether the key was declared
// at all. The bytes are not read here: what they hold is the renderer's
// question, and whether they are wanted at all is the question of whether some
// other profile named this one.
//
// A key set to JSON null reads as undeclared, matching [Spec.Settings] and the
// cascade, where null is how a profile clears a key an ancestor declared.
func (s Spec) Subagent() (json.RawMessage, bool) {
	raw, ok := s[SpecKeySubagent]
	if !ok || isUndeclared(raw) {
		return nil, false
	}
	return raw, true
}

// Settings returns the settings document [SpecKeySettings] holds for one
// provider, as the cascade left it, and whether that provider has one at all.
//
// spec.settings is a document per provider — `settings: {claude: {...},
// codex: {...}}` — and this is where one of them is chosen. A provider is a
// materialization target rather than a property of the content, so one profile
// serves every target and the choice belongs to whoever is materializing it,
// not to whoever wrote the profile. The caller passes the target it is
// rendering for: [github.com/chrispian/cairn/bootdir.RenderSettings] passes
// its layout's provider, which is what `--provider` selects.
//
// Nothing here reformats the bytes. The selected document is the sub-value's
// own bytes, lifted out by [json.RawMessage] and never re-encoded, so a
// document exactly one profile in the chain declared is what the operator
// wrote — whitespace, key order and number spelling included — and one
// composed from two profiles is those documents merged at every depth and
// nothing more. Selecting narrows what is returned and re-spells no part of
// it.
//
// A provider the key does not mention, or mentions as JSON null, has no
// document: nil and false, exactly as an undeclared key. Null clears here for
// the reason it clears everywhere else in the cascade, and it is how a profile
// says a target it inherits settings for gets none.
//
// # The old flat form is refused, by name
//
// A spec.settings whose members are not providers reports
// [ErrSettingsProvider]. That is a refusal rather than a fallback, and the
// fallback is what makes it worth one: a flat document silently accepted would
// select nothing for every target, so a profile carrying the operator's
// permission mode would render a boot directory with no permission mode in it
// and say nothing. Cairn would have read a document, understood it, and
// dropped it. The diagnostic names the member it found and the shape to move
// it to, walking the members in sorted order so that a document with more than
// one of them names the same member every time.
//
// A spec.settings that is not an object at all is refused the same way: there
// is nothing to select out of it. What sits under a provider is not read —
// that document belongs to the harness, and a bare list or a bare string there
// is written out as faithfully as an object is.
func (s Spec) Settings(p Provider) (json.RawMessage, bool, error) {
	raw, ok := s[SpecKeySettings]
	if !ok || isUndeclared(raw) {
		return nil, false, nil
	}
	var byProvider map[string]json.RawMessage
	if err := json.Unmarshal(raw, &byProvider); err != nil {
		return nil, false, fmt.Errorf(
			"%w: spec.%s is not an object, so there is no provider's document to select out of it — write it as %s",
			ErrSettingsProvider, SpecKeySettings, settingsShapeHint)
	}
	for _, name := range slices.Sorted(maps.Keys(byProvider)) {
		if Provider(name).Valid() {
			continue
		}
		return nil, false, fmt.Errorf(
			"%w: spec.%s declares %q, which names no harness — a settings document is declared under the "+
				"provider it is for, as %s, and one profile serves every target that way. Cairn knows %s",
			ErrSettingsProvider, SpecKeySettings, name, settingsShapeHint, ProviderList())
	}
	doc, declared := byProvider[p.String()]
	if !declared || isUndeclared(doc) {
		return nil, false, nil
	}
	return doc, true, nil
}

// settingsShapeHint is the shape a diagnostic tells an operator to write, in
// the YAML they authored the profile in rather than in the JSON cairn carries
// it as.
const settingsShapeHint = "`settings: {claude: {...}}`"

// ProviderList names every provider Cairn knows, quoted and comma-separated,
// for the diagnostics that have to offer them.
//
// It is exported because cmd/cairn refuses a `--provider` naming no harness and
// owes the same sentence. Two formatters would be two sentences the moment a
// fourth harness arrived, and an operator reading "cairn knows claude, codex"
// from one command and three names from another would be right to distrust
// both. It reads [Providers], so a name added there reaches every message that
// offers one.
func ProviderList() string {
	names := make([]string, 0, len(Providers()))
	for _, p := range Providers() {
		names = append(names, strconv.Quote(p.String()))
	}
	return strings.Join(names, ", ")
}

// Files returns the path-to-content map under [SpecKeyFiles]. A manifest
// declaring none returns nil and no error.
func (s Spec) Files() (map[string]FileEntry, error) {
	return s.entries(SpecKeyFiles)
}

// entries decodes a manifest key holding a path-to-[FileEntry] map. Two keys
// carry that shape — see [Spec.Files] and [Spec.Templates] — and decoding them
// through one function is what keeps the two legal value forms the same in
// both.
func (s Spec) entries(key string) (map[string]FileEntry, error) {
	var raw map[string]json.RawMessage
	if err := s.decode(key, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]FileEntry, len(raw))
	for rel, v := range raw {
		entry, err := parseFileEntry(v)
		if err != nil {
			return nil, fmt.Errorf("spec key %q: %q: %w", key, rel, err)
		}
		out[rel] = entry
	}
	return out, nil
}

// Templates returns the path-to-template map under [SpecKeyTemplates]. A
// manifest declaring none returns nil and no error.
//
// A value takes the same two shapes a files value does, and for the same
// reason: a template is text, and text a profile already knows is a literal
// while text that lives on disk is a source. What separates the two keys is
// what happens to the text afterwards — a template's markers are substituted
// and a file's bytes are not.
func (s Spec) Templates() (map[string]FileEntry, error) {
	return s.entries(SpecKeyTemplates)
}

// Trees returns the destination-to-source map under [SpecKeyTrees]. A manifest
// declaring none returns nil and no error.
//
// Each value names a directory copied whole to the destination. It is not a
// slot source: a static_dir source concatenates the files it finds into one
// string, which is right for a slot and destroys a directory.
func (s Spec) Trees() (map[string]string, error) {
	var out map[string]string
	if err := s.decode(SpecKeyTrees, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// FileEntry is one value of the files manifest key. A value is either a
// literal string, planted verbatim, or a slot source resolved at
// materialization the same way a slot is.
//
// Both forms exist because both are needed. A literal covers content the
// profile already knows. A source covers content that is only true at boot —
// the case the portfolio's other planters all have: torque plants
// tasks/<id>/task.md, task.json and a per-task process.md rendered from live
// task state, and static path-to-content cannot express that.
//
// Exactly one field is set.
type FileEntry struct {
	// Literal is the content, when the manifest gave a string.
	Literal string

	// Source resolves to the content, when the manifest gave an object. Nil
	// means the entry is a literal.
	Source *agentcontext.SlotSource
}

// IsSource reports whether the entry must be resolved before it can be
// planted.
func (e FileEntry) IsSource() bool { return e.Source != nil }

// parseFileEntry reads one files value in either form. A JSON string is a
// literal; a JSON object is a slot source. Anything else is refused by name,
// because the two legal shapes are easy to describe and a silent coercion here
// would plant the wrong bytes at a path the profile promised.
func parseFileEntry(raw json.RawMessage) (FileEntry, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return FileEntry{}, errors.New("the value is empty")
	}
	switch trimmed[0] {
	case '"':
		var lit string
		if err := json.Unmarshal(trimmed, &lit); err != nil {
			return FileEntry{}, err
		}
		return FileEntry{Literal: lit}, nil
	case '{':
		var src agentcontext.SlotSource
		if err := json.Unmarshal(trimmed, &src); err != nil {
			return FileEntry{}, err
		}
		return FileEntry{Source: &src}, nil
	default:
		return FileEntry{}, fmt.Errorf(
			"the value is neither a string nor a source object — a files entry is either the content itself or %s",
			`{"kind": "...", ...}`)
	}
}

// Expander returns the value of an environment variable, or the empty string
// for one that is not set.
//
// It is a parameter rather than a direct read so that nothing below the
// composition root consults the process environment on its own. A renderer
// reads nothing outside the instance it was handed — the same rule that puts
// the operator's home on the instance rather than in the renderer that expands
// a "~/" — and an environment is the same kind of hidden input.
type Expander func(name string) string

// ExpandEnv returns s with $VAR and ${VAR} replaced by look's answers, and a
// name that is not set replaced by nothing, which is [os.Expand]'s behaviour
// and every shell's.
//
// A nil look expands nothing and returns s unchanged. That is deliberate: a
// caller that was handed no environment leaves the operator's own text in
// place, so a diagnostic quotes what they wrote rather than what an
// unconfigured expansion made of it.
func ExpandEnv(s string, look Expander) string {
	if s == "" || look == nil {
		return s
	}
	return os.Expand(s, look)
}

// ExpandPath returns a manifest path with its variables expanded and a leading
// "~" replaced by home.
//
// The order is variables first. A variable holding a home-relative path — an
// AGENT_HOME of "~/agents" — then has its tilde expanded too, where expanding
// the tilde first would leave it in the middle of the result. Anything else
// beginning with "~", "~user/x" included, is returned untouched and left to the
// caller's absolute-path check, so an unexpanded form fails by naming itself
// rather than by resolving somewhere unexpected.
//
// A path that needs home and has none returns [ErrNoHomeForPath]. Every other
// judgement — whether the result is absolute, whether it exists, whether it is
// the right kind of thing — belongs to the caller, which knows what the path
// was for.
func ExpandPath(raw string, home string, look Expander) (string, error) {
	expanded := ExpandEnv(raw, look)
	if expanded != "~" && !strings.HasPrefix(expanded, "~/") {
		return expanded, nil
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("%w: %q", ErrNoHomeForPath, expanded)
	}
	return filepath.Join(home, strings.TrimPrefix(expanded, "~")), nil
}

// QuotedExpansion names a manifest value for a diagnostic: what the operator
// wrote, and what it expanded to when the two differ.
//
// Naming only the expansion is the failure this exists to prevent. A profile
// writing "$ROOT/docs" with ROOT unset expands to "/docs", which is absolute
// and passes every check but the last, and an error quoting "/docs" sends the
// operator looking for a path they never wrote instead of at the variable they
// did not set.
//
// It is one function so that every diagnostic about an expanded value reads the
// same way, whether the value was a tree's source, a skills directory, or the
// path inside a slot.
func QuotedExpansion(declared, expanded string) string {
	if declared == expanded {
		return fmt.Sprintf("%q", declared)
	}
	return fmt.Sprintf("%q, which expanded to %q", declared, expanded)
}

// ErrNoHomeForPath reports a manifest path written with a leading "~/" where no
// home directory is known.
var ErrNoHomeForPath = errors.New("no home directory to expand a path against")

// isUndeclared reports whether a manifest value carries nothing: absent bytes,
// or the JSON null a profile clears an ancestor's key with.
func isUndeclared(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// decode unmarshals the value at key into dest. A key that is absent, or
// present as JSON null, leaves dest untouched and reports no error — a
// manifest is not obliged to declare anything.
func (s Spec) decode(key string, dest any) error {
	raw, ok := s[key]
	if !ok || isUndeclared(raw) {
		return nil
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("spec key %q: %w", key, err)
	}
	return nil
}
