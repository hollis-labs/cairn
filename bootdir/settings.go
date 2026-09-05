package bootdir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/chrispian/cairn/profile"
	goprovider "github.com/hollis-labs/go-providers/provider"
)

// accessDirectoriesKey names spec.access.directories in a diagnostic. It is
// composed from the manifest key rather than spelled out, the way
// [installSkillsKey] is, so a rename of the key reaches the message.
const accessDirectoriesKey = profile.SpecKeyAccess + ".directories"

// ErrAccessDirectory reports a declared access directory Cairn will not grant:
// one that is not an absolute path after expansion, or that does not name an
// existing directory.
//
// Both are refusals rather than corrections, and the absolute one is the
// load-bearing half. A relative path is resolved by whoever reads it, and the
// harness reads this file from inside the boot directory — so ".." granted
// verbatim is the boot root, which holds every other session's boot directory
// for that profile, their prompts and their settings documents. A grant that
// widens by accident is the one outcome this key must never produce, and every
// other manifest path in this package already refuses a relative one by name:
// see [ErrSkillsSource] and [ErrTreeSource].
var ErrAccessDirectory = errors.New("access directory cannot be granted")

// ErrGrantConflict reports that the composed settings document already
// declares a key the access grant is written into.
//
// Cairn owns that key now: spec.access.directories is where directories are
// declared, and it is the only spelling that composes with the scope, unions
// across a chain, and is put to the checks [ErrAccessDirectory] names. A
// document declaring the same key by hand is a second source for one value,
// which is the ambiguity the neutral declaration exists to remove.
//
// It refuses rather than merging the two, and rather than letting the closer
// value win. Writing over the operator's list would silently remove access
// they had before spec.access existed — declaring the harness's key by hand
// was the only way to grant a directory until it did — and unioning would let
// a grant reach the file through a path that gets none of those checks. The
// refusal names the key it found and the key to move it to, which is the same
// argument [RenderSettings] already makes one level up for a settings document
// that is not an object: say so, rather than dropping the grant or replacing
// what the operator wrote.
var ErrGrantConflict = errors.New("the settings document declares a key cairn grants")

// RenderSettings returns the harness settings document, laid out for reading.
//
// Two things compose it. Most of it is the operator's own, verbatim after the
// cascade rather than verbatim as stored, because the settings key is a keyed
// collection: a chain whose profiles each declare part of the document renders
// the composition of them. On top of that Cairn adds the directories the
// instance is granted — the scope it works in, which needs no declaration, and
// whatever spec.access.directories names besides.
//
// # Verbatim after the cascade, and after the target is chosen
//
// spec.settings holds one document per provider, so there is a choice to make
// before there is a document at all, and the promise above is measured from
// after it. The target is inst.Layout.Provider — the layout being materialized
// into, which is what `--provider` selects — and never the profile's own
// declaration, because a provider is a materialization target rather than a
// property of the content. A profile declaring settings for a target this
// instance is not being rendered for contributes none of them, which is the
// point: one profile serves every harness.
//
// Selecting re-spells nothing. [profile.Spec.Settings] lifts the sub-value out
// as its own bytes, so a document exactly one profile declared reaches this
// function exactly as the operator wrote it — which is what keeps the sentence
// below about what costs a document its spelling true of composition alone.
//
// A profile whose spec.settings is not keyed by provider is refused rather
// than read as though it were one target's document — see
// [profile.ErrSettingsProvider], and note that the alternative is silence: a
// flat document would select nothing for every target, and the boot directory
// would come out missing a permission mode the profile plainly declares.
//
// That addition is not Cairn interpreting the operator's values. No rule of
// theirs is read: Cairn models no permission mode, validates no tool name and
// turns no declaration into a policy. What it reads is spec.access, its own
// neutral key, and the paths there are handed to the provider adapter that
// owns the harness's spelling for them — see [accessFragment]. A rule that
// turns out not to enforce is still a fact about the harness rather than a
// defect here.
//
// The two are composed by [profile.MergeSettings], the rule the cascade
// already composes this key by, so a granted directory lands beside the keys
// the operator declared. Beside, and never over: the one place the addition
// could displace something is the key the grant is written into, and a
// document that declares that key by hand is refused rather than overwritten —
// see [ErrGrantConflict], which is the only reason anything here looks inside
// the declared document at all, and looks only where the fragment already
// sits.
//
// That composition is also the one thing that costs a document its exact
// spelling: one Cairn contributes to is decoded and re-encoded at the levels it
// touches, so those keys come back sorted and a duplicate among them collapses.
// A document Cairn contributes nothing to — no scope, no declared directories —
// is never decoded at all and is written exactly as the cascade left it.
//
// The layout is [IndentJSON], which moves whitespace and nothing else — see
// its doc for why that is not the same as handing a hand-spelled document to
// Go's encoder.
//
// A manifest that declares no settings key still renders a file when there is
// a directory to grant. Rendering nothing would mean spec.access.directories
// reaching a boot directory only for profiles that happened to declare an
// unrelated key, which is the silent failure the declaration exists to
// prevent.
func RenderSettings(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	if inst.Layout.Provider == profile.ProviderCodex {
		return renderCodexConfig(inst)
	}
	stored, declared, err := inst.Profile.Spec.Settings(inst.Layout.Provider)
	if err != nil {
		return nil, err
	}
	granted, err := accessFragment(inst)
	if err != nil {
		return nil, err
	}
	if !declared && granted == nil {
		return nil, nil
	}
	if path, conflict := grantConflict(stored, granted); conflict {
		return nil, fmt.Errorf(
			"%w: spec.%s declares %q, which is where cairn writes the directories it grants — declare them under spec.%s, the one spelling that composes with the scope and is checked",
			ErrGrantConflict, profile.SpecKeySettings, path, accessDirectoriesKey)
	}
	document, err := profile.MergeSettings(stored, granted)
	if err != nil {
		return nil, err
	}
	if !inst.Layout.Settings.Declared() {
		return nil, fmt.Errorf(
			"%w: there is a settings document to write — spec.%s, the directories cairn grants, or both — and this layout declares no path for one",
			ErrProviderLayout, profile.SpecKeySettings)
	}
	return []File{{
		Path:    inst.Layout.Settings.RelPath,
		Content: IndentJSON(document),
		Mode:    inst.Layout.Settings.Mode,
	}}, nil
}

// accessFragment returns the settings-document fragment that grants inst its
// directories, or nil when it has none to grant.
//
// The fragment is the provider adapter's own document rather than a shape
// Cairn spells out, and the adapter is constructed carrying the directories
// and nothing else. [goprovider.ClaudeAdapter.SettingsDocument] takes no
// arguments because every input to that document is a field on the adapter, so
// one carrying only AdditionalDirectories returns only the key those map onto
// — Cairn never has to name that key, and the mapping stays in the library
// that owns the harness's schema.
//
// It is also what bounds what the fragment can reach. A document holding one
// key holds no apiKeyHelper and no permissions.defaultMode to write over the
// operator's, and Cairn has no values for either to give the adapter: the
// profile's are in spec.settings, which this does not read, and one of them is
// spelled in a vocabulary the adapter would refuse. What is left is the one key
// the directories do map onto, and a document already declaring that key is
// refused by [RenderSettings] rather than written over. The alternative —
// populate the adapter fully, or read permissions.additionalDirectories back
// out of a full document — puts the mapping here instead, which is the hardcode
// the accessor exists to remove.
//
// This helper is Claude-only. Codex grants directories through config.toml
// under a different key, and [RenderSettings] dispatches to the Codex TOML
// renderer before this function is called.
func accessFragment(inst *Instance) (json.RawMessage, error) {
	dirs, err := grantedDirectories(inst)
	if err != nil || len(dirs) == 0 {
		return nil, err
	}
	switch inst.Layout.Provider {
	case profile.ProviderClaude:
		doc, err := (&goprovider.ClaudeAdapter{AdditionalDirectories: dirs}).SettingsDocument()
		if err != nil {
			return nil, err
		}
		return encodeFragment(doc)
	default:
		return nil, fmt.Errorf("%w: %q, and spec.%s names a directory to grant through it",
			ErrUnsupportedProvider, inst.Layout.Provider, profile.SpecKeyAccess)
	}
}

// grantedDirectories returns every directory inst may reach: the scope first,
// because an instance works there and nothing had to declare it, then each
// path spec.access.directories names.
//
// A declared path is put to the four steps package scope already performs on
// the scope, in that order: expand it, refuse it unless it is absolute, refuse
// it unless it names an existing directory, and resolve its symlinks. The
// scope arrives having had all four — see [github.com/chrispian/cairn/scope.Parse]
// — and it is one of these grants, so canonicalizing the two differently would
// mean the same directory reaching the harness under two spellings depending on
// which of them named it.
//
// Existence is checked for the reason [ErrTreeSource] and [ErrSkillsSource]
// check it, and it has a cost worth stating: a directory the agent is expected
// to create cannot be granted ahead of time. The alternative is worse. The
// diagnostic quoting a declared "$ROOT/docs" that expanded to "/docs" is what
// tells an operator their variable is unset, and without the existence check
// that value is absolute, grantable, and silently wrong — the failure
// [profile.QuotedExpansion] was written for, spelled out in its own doc.
//
// A duplicate is dropped and the first spelling of it stands. Resolution is
// what makes that claim true rather than approximate: two names for one
// directory are one grant, and a repeated entry would read as though Cairn had
// lost track of what it wrote.
//
// The order is the scope and then the manifest's own, which is deterministic
// for one profile and is not a contract: the cascade sorts these by path the
// moment two profiles declare them, so nothing may depend on the sequence.
func grantedDirectories(inst *Instance) ([]string, error) {
	declared, err := inst.Profile.Spec.AccessDirectories()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(declared)+1)
	seen := make(map[string]struct{}, len(declared)+1)
	add := func(dir string) {
		if dir == "" {
			return
		}
		if _, dup := seen[dir]; dup {
			return
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	add(inst.Scope)
	for _, raw := range declared {
		dir, err := grantedDirectory(raw, inst.Home, inst.Env)
		if err != nil {
			return nil, err
		}
		add(dir)
	}
	return out, nil
}

// grantedDirectory resolves one declared access directory, refusing by name
// every form Cairn will not hand to a harness.
//
// It is [treeSource] and the skills directory's resolution over again, on
// purpose: three manifest keys take a path from the operator, and an operator
// who has read one diagnostic should recognise the next. The one step those
// two do not take is resolving symlinks, which this takes because the value
// leaves Cairn — a tree's source is read and discarded, while a granted
// directory is written into a file the harness matches paths against.
func grantedDirectory(raw, home string, look profile.Expander) (string, error) {
	expanded, err := profile.ExpandPath(raw, home, look)
	if err != nil {
		return "", fmt.Errorf("%w: spec.%s declares %q: %w", ErrAccessDirectory, accessDirectoriesKey, raw, err)
	}
	named := profile.QuotedExpansion(raw, expanded)
	if !filepath.IsAbs(expanded) {
		return "", fmt.Errorf("%w: spec.%s declares %s, which is not an absolute path",
			ErrAccessDirectory, accessDirectoriesKey, named)
	}
	info, err := os.Stat(expanded)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: spec.%s declares %s, which does not exist",
			ErrAccessDirectory, accessDirectoriesKey, named)
	case err != nil:
		return "", fmt.Errorf("stat the access directory %s: %w", expanded, err)
	case !info.IsDir():
		return "", fmt.Errorf("%w: spec.%s declares %s, which is not a directory",
			ErrAccessDirectory, accessDirectoriesKey, named)
	}
	resolved, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve the access directory %s: %w", expanded, err)
	}
	return resolved, nil
}

// grantConflict returns the path inside a settings document where fragment
// would write over something composed already declares, and whether there is
// one.
//
// The paths come from the fragment rather than from a constant, which is what
// keeps [accessFragment]'s promise intact: Cairn still never names the key the
// directories map onto, it asks whether the operator's document declares
// anything where the adapter's answer sits. A key the fragment does not claim
// is never looked at, so this reads no rule of the operator's and reports no
// opinion about one.
//
// Two objects at one key are not a conflict — they compose, which is the whole
// point of the deep merge — so the walk goes down. Anything else at a claimed
// key is: an array replaces, a scalar replaces, and a JSON null replaces too,
// so none of them can be left to the merge to decide.
//
// A composed value that is not an object at all reports nothing here. That
// document cannot be added to for a reason of its own, and
// [profile.MergeSettings] is where it is refused — one failure, one place.
//
// The keys are walked in sorted order so that a document colliding more than
// once names the same one every time.
func grantConflict(composed, fragment json.RawMessage) (string, bool) {
	var declared, claimed map[string]json.RawMessage
	if json.Unmarshal(composed, &declared) != nil || json.Unmarshal(fragment, &claimed) != nil {
		return "", false
	}
	for _, key := range slices.Sorted(maps.Keys(claimed)) {
		standing, ok := declared[key]
		if !ok {
			continue
		}
		if isJSONObject(standing) && isJSONObject(claimed[key]) {
			if path, found := grantConflict(standing, claimed[key]); found {
				return key + "." + path, true
			}
			continue
		}
		return key, true
	}
	return "", false
}

// isJSONObject reports whether a value is a JSON object, which is what decides
// whether a conflict walk has another level to go down.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// encodeFragment serializes an adapter's settings document for the merge.
//
// HTML escaping is off for the reason it is off everywhere a manifest value is
// re-encoded: "&", "<" and ">" are legal in a directory name, and a path the
// operator can read is worth more here than one spelled in escapes. The
// trailing newline [json.Encoder] adds is removed, because what this returns
// is a value to compose and not a file to write.
func encodeFragment(doc map[string]any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// IndentJSON returns raw laid out one element per line at [JSONIndent] per
// level, ending in the newline that makes the result a text file.
//
// [json.Indent] is the whole transformation. It moves whitespace between
// tokens and changes nothing else, so key order, string spelling and number
// spelling all survive it — which is what separates laying a document out from
// re-encoding it. Handing a hand-spelled settings document to Go's encoder
// would re-spell its strings and re-order nothing predictably; this does
// neither, and the document that comes out is the one that went in with the
// newlines put back.
//
// A value that is not JSON is returned as it was declared. Every manifest
// value is JSON by construction — the catalog builds it from the YAML the
// operator wrote rather than accepting JSON text — and a merge composes valid
// JSON out of valid JSON, so this is unreachable through either. A renderer
// that dropped an artifact because it could not lay it out prettily would fail
// at exactly the moment the operator most needs to see what is there.
//
// It is exported because a check needs the same transformation: normalizing
// both sides through one function is what keeps a comparison from reporting
// whitespace as drift. See [github.com/chrispian/cairn/install.Renderer].
func IndentJSON(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	var buf bytes.Buffer
	if err := json.Indent(&buf, trimmed, "", JSONIndent); err != nil {
		buf.Reset()
		buf.Write(trimmed)
	}
	return append(buf.Bytes(), '\n')
}
