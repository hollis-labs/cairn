package main

import (
	"bytes"
	"encoding/json"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/profile"
)

// showJSONFlagUsage describes --json on `cairn show`. It says "instead of"
// rather than "as well as", because the two never both appear on stdout: the
// document is the whole output of this command, in one form or the other.
const showJSONFlagUsage = "print one JSON object describing what the target resolves to, " +
	"instead of the document laid out for reading"

// showReport is what `cairn show --json` prints: everything the prose document
// says, in the shape a program reads, so that nothing has to parse the prose
// to find it out.
//
// It exists for the reason [bootReport] does, one command over. `cairn boot`
// used to be read by a launcher scraping the rendered AGENTS.md with sed, and
// --json was added there so a launcher could stop. `cairn show` is the other
// half of that surface — it is the "what will this resolve to" preview, and
// the mitigation docs/plan.md §3 names for a manifest key that can no longer
// be read off one profile's row — and a consumer sourcing a list from it was
// reading a human-facing document laid out with json.Indent, aligned in
// columns, and carrying a paragraph of explanation in the middle. That is the
// same defect in the same repository, and fixing it in one command while
// leaving it in the other would be an odd place to stop.
//
// # The contract
//
// It is [bootReport]'s, deliberately and in every clause, because a consumer
// reading both documents in one session should not have to learn two sets of
// rules:
//
//   - snake_case keys.
//   - Every key emitted on every call. The key set is the contract; a key that
//     came and went would make a consumer handle two shapes for one meaning.
//   - A value cairn does not have is null, never "" and never []. An empty
//     string is the shape most likely to be interpolated straight into a
//     document or an argv, where null forces the consumer to decide.
//   - No version field, and the rule that replaces it is the same one: **new
//     keys are free; renaming or removing one is breaking and must update
//     every consumer in the same change.**
//
// Where a key means the same thing in both documents it is spelled the same
// way and carries the same value. Scope is the one that matters, absolute and
// symlink-resolved in both, null in both when there is none: a launcher that
// showed a scope and then booted into a different one would be worse than one
// that never showed it. ProfileRoot is the second, and it is the same rule
// with a second reader — both commands report the bundle off the catalog they
// opened, so neither spelling can be tidied on its own without the two
// documents starting to disagree about one directory.
//
// # Why Spec nests where bootReport is flat
//
// [bootReport] is flat because every value in it is a scalar or a list of
// them, so there is nothing for nesting to group. The reason was never "flat
// is the house style"; it was that a consumer reads one key at a time out of a
// shell. This document's payload is a map from manifest key to an arbitrary
// JSON value, which has nowhere to go but under a key of its own, and
// `jq -r '.spec.settings.value'` is the same one-key read.
//
// The pairing inside it is structural on purpose. The value and the names
// beside it are one fact — that is the entire reason this command exists, and
// the prose says it by printing them on adjacent lines — so they are one
// object here rather than two parallel maps a consumer could iterate out of
// step. Two maps would also be two key sets, and nothing would stop them
// disagreeing.
//
// # Provenance is per key, and the shape must not suggest otherwise
//
// [specEntry.Contributors] answers "which contributors declared this key",
// never "which of them supplied the member in front of you". The distinction
// is the one runShow's [declaringProfiles] records, and it is a limit of the
// cascade rather than of its caller: the second answer cannot be assembled
// without a second copy of profile.specMergers.
//
// A JSON shape implying otherwise would be worse than the prose, because a
// consumer would build a UI on it and the UI would be confidently wrong. So
// Contributors sits beside Value as a sibling of the whole value, never inside
// it and never keyed by anything within it. If per-member provenance is ever
// wanted it is a change to the cascade and a separate task.
type showReport struct {
	// Profile is the profile the cascade was resolved for — the leaf of the
	// chain, and the id the target resolved to. Nothing here is named after
	// anything, which is why there is one id and not two: the target IS the
	// profile, where it used to be a binding name that resolved to one.
	Profile string `json:"profile"`

	// Chain is every profile id the cascade folded, ancestor-first, with
	// whatever each composed part added standing after it. It is the fold
	// order, which is what a reader checks a composition for.
	//
	// It is never null and never empty: a resolution that produced no chain
	// produced no document. A part named by --with appears here and so names
	// itself; --skill, --prompt and --set do not, which is why
	// [specEntry.Contributors] exists.
	Chain []string `json:"chain"`

	// Name and Description are the closest declared value of each in the
	// chain, or null when no profile in it declares one.
	Name        *string `json:"name"`
	Description *string `json:"description"`

	// Provider is the harness a boot of this target would materialize into:
	// what --provider named, or what the chain declares, or null when neither
	// says anything.
	//
	// It is the target and not the declaration, for the reason [showReport].
	// Scope is what a boot would work in: this command is the preview of a
	// boot, and a preview that reported the profile's own answer to a question
	// the operator had just answered with a flag would be answering a
	// different question than the one they asked.
	//
	// Nullable where [bootReport.Provider] is a plain string, and the two are
	// consistent rather than in conflict: a boot refuses a target that is not
	// one cairn knows a layout for, so by the time a boot is described there
	// is always one. This command shows what is there, including the abstract
	// root, and profile.Provider.Valid documents the empty provider as a
	// configuration error rather than a default — so it is a thing a reader
	// can be shown and must be able to tell from "claude".
	Provider *string `json:"provider"`

	// Model is the closest declared model in the chain, or null.
	Model *string `json:"model"`

	// Abstract is the leaf's own flag: true when `cairn boot` would refuse
	// this target. It is not inherited and a composed part never contributes
	// it, so it describes the profile named and nothing above it.
	Abstract bool `json:"abstract"`

	// Scope is the directory a boot of this target would work in, absolute and
	// symlink-resolved, or null when no --scope was given or the one that was
	// given did not resolve.
	//
	// The last of those three is why the stderr line survives --json. A scope
	// that did not resolve is reported there and null here, exactly as the
	// prose document leaves the field empty and says why on stderr; a consumer
	// reading only stdout sees "no scope", which is what a boot would grant.
	Scope *string `json:"scope"`

	// ProfileRoot is the bundle the profile was read out of, which is also
	// what $CAIRN_PROFILE_ROOT expands to in every manifest value that names
	// somewhere to read from. It is never null: a resolution that found no
	// bundle produced no document.
	//
	// It is [bootReport.ProfileRoot], and the two are one key rather than two
	// that happen to agree — same spelling, same value, read off the catalog
	// each command opened. What that key is for is argued there, where a
	// launcher acts on it.
	ProfileRoot string `json:"profile_root"`

	// Spec is the composed manifest, one entry per key, each carrying the
	// merged value and the contributors that declared it.
	//
	// Keys are whatever the profiles declared, including the ones cairn
	// renders nothing from — an unknown key is carried through the cascade and
	// is shown here like any other, because a document that silently dropped
	// what it did not recognize would be the worst possible answer to "what
	// does this resolve to". Go's encoder sorts a map's keys, so the order
	// here is the prose document's sorted order and is stable across calls.
	//
	// An empty manifest is `{}` and not null, which is not an exception to the
	// null rule but the rule applied. Null is for a value cairn does not have,
	// and there is no such state for a manifest: a profile declaring nothing
	// has an empty one. The ambiguity [bootReport.ProjectDirArg] spells null to
	// avoid — "these zero things" versus "no such thing" — does not arise,
	// because "no manifest at all" is not something a resolved profile can be.
	Spec map[string]specEntry `json:"spec"`
}

// specEntry is one manifest key: what it merged to, and who declared it.
type specEntry struct {
	// Value is the merged value, carried through as the cascade holds it.
	//
	// It survives this document the way it survives the prose one. Encoding a
	// json.RawMessage re-indents its tokens and changes nothing else, and this
	// document is written with HTML escaping off, so key order, string
	// spelling and number spelling all arrive as the operator wrote them. That
	// is load-bearing rather than tidy: spec.settings reaches the harness's
	// settings document with those bytes, and an operator diffing the two is
	// diffing content.
	//
	// A literal JSON null here is a declaration and not an absence. It is how
	// a profile clears an ancestor's key — see profile.mergeAt, which carries
	// a clearing null forward as the folded value — so `"settings": {"value":
	// null, ...}` means this profile deliberately has no settings, and is a
	// different fact from `settings` being absent from [showReport.Spec],
	// which means nothing in the chain ever mentioned it. A consumer that
	// treats the two as one will render an inherited settings document for a
	// profile that went to the trouble of saying it has none.
	Value json.RawMessage `json:"value"`

	// Contributors names who declared this key: the profiles in the chain that
	// declare it, ancestor-first, followed by any flag that contributed to it.
	//
	// Not every member is a profile id, and the list is one list for the
	// reason the prose prints one column. A key can be declared by a profile
	// and then added to by something that is not one: "--skill", "--prompt"
	// and "--set" appear as they are spelled. A consumer must not assume a
	// member resolves as a profile — what these are is what a reader would
	// have to change to change the value, which is the question the column
	// answers.
	//
	// The count is the fact worth reading. One name and the value is that
	// profile's own declaration, converted from the YAML it was authored in
	// and never re-serialized. Anything else is a composition: the members are
	// what those contributors declared, and the order among them is the
	// cascade's rather than anything to read meaning into.
	//
	// Null, never [], if cairn cannot name one. Every key in a resolved
	// manifest reaches it from something, so this is unreachable rather than a
	// state; null is still what it must be, because [] would claim cairn
	// looked and found there were none. The value is shown either way — the
	// prose document prints such a key with an empty column rather than
	// refusing, and a --json that refused a document its prose form prints
	// would make the two disagree about whether a profile is showable.
	Contributors []string `json:"contributors"`
}

// showDocument renders what `cairn show --json` prints, laid out at
// [bootdir.JSONIndent] with HTML escaping off — the way every other JSON
// document cairn writes is rendered, and for the same reason [bootDocument]
// has escaping off. "&", "<" and ">" are ordinary characters in a path and in
// the prose an operator writes into a slot, this document quotes both, and an
// operator reads it at a terminal long before any program parses it.
//
// It is one object and nothing else, so `cairn show x --json | jq` is one
// value. [json.Encoder] supplies the trailing newline that makes it a line.
//
// An error means a manifest value is not JSON, which is unreachable through
// either route into a Spec: the catalog builds every value from the YAML the
// operator wrote rather than accepting JSON text, and a merge composes valid
// JSON out of valid JSON. It is returned rather than worked around because
// the alternatives are both worse than saying so — the prose form can fall
// back to printing the bytes it could not lay out, and this one cannot emit
// something that is not JSON into a document a program will parse.
func showJSONDocument(resolved *profile.Resolved, provider profile.Provider, scopeDir, profileRoot string,
	declared map[string][]string) (string, error) {

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", bootdir.JSONIndent)
	if err := enc.Encode(newShowReport(resolved, provider, scopeDir, profileRoot, declared)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// newShowReport describes one resolution.
//
// declared is the same map the prose rendering is handed — the chain re-read
// key by key, with the flags' contributions already appended — so the two
// forms of this command cannot disagree about who declared what.
func newShowReport(resolved *profile.Resolved, provider profile.Provider, scopeDir, profileRoot string,
	declared map[string][]string) showReport {

	spec := make(map[string]specEntry, len(resolved.Spec))
	for key, value := range resolved.Spec {
		spec[key] = specEntry{Value: value, Contributors: nonEmpty(declared[key])}
	}
	return showReport{
		Profile:     resolved.ID,
		Chain:       resolved.Chain,
		Name:        nullable(resolved.Name),
		Description: nullable(resolved.Description),
		Provider:    nullable(provider.String()),
		Model:       nullable(resolved.Model),
		Abstract:    resolved.Abstract,
		Scope:       nullable(scopeDir),
		ProfileRoot: profileRoot,
		Spec:        spec,
	}
}

// nonEmpty returns s, or nil when it holds nothing — the "absent is null,
// never []" rule enforced for a list, as [nullable] enforces it for a string.
func nonEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
