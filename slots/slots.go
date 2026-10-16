// Package slots resolves a profile manifest's slot declarations into the
// content a boot directory's boot.md is written from.
//
// The resolving is not Cairn's. agentkit's agentcontext owns the slot kinds,
// the resolvers, the per-slot timeouts and headers, the byte and token
// budgets, the per-slot provenance, and the determinism contract — including
// the rule that a non-required slot whose resolver fails records the failure
// on its result instead of blocking the assembly. What lives here is the
// wiring between a [profile.Spec] and that library, and deliberately nothing
// more: three portfolio libraries each grew their own context pipeline before
// agentcontext was extracted to end exactly that.
package slots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/hollis-labs/cairn/profile"
	"github.com/hollis-labs/libs/harness/core/agentcontext"
	"github.com/hollis-labs/libs/harness/core/agentcontext/resolvers"
)

// ErrSlotKind reports a source whose declared kind is missing or is not one
// the library recognizes — a slot's, or a files entry's, since both carry the
// same [agentcontext.SlotSource]. It exists so the failure names the source
// and the manifest key, which the library's own sentinel cannot: it is reached
// from a request that no longer knows how the manifest was spelled.
var ErrSlotKind = errors.New("invalid slot kind")

// Options carries what varies between two materializations.
type Options struct {
	// Workdir is what workdir-relative slot paths resolve against — the
	// instance's scope directory, or empty for none.
	Workdir string

	// Limits caps the assembled output. Zero means unlimited.
	Limits agentcontext.Limits

	// Provenance is the request-level attribution threaded onto the result.
	Provenance agentcontext.ProvenanceInput

	// Provider overrides the assembled ContextProvider. Nil means the default
	// wiring: agentcontext.NewProvider(resolvers.Default(), DropUnresolved{}).
	Provider agentcontext.ContextProvider

	// Deterministic restricts resolution to the kinds in [DeterministicKinds],
	// leaving every other declared slot unresolved and therefore absent from
	// the result. It is what the installed layer sets; see that constant for
	// the rule and the reason.
	Deterministic bool

	// Env answers an environment variable named in a source's path or URL. Nil
	// expands nothing, so a caller that supplies none leaves the operator's own
	// text in place and a diagnostic quotes what they wrote. See
	// [expandSources] for which fields are expanded and why it is only those.
	Env Expander
}

// Assemble resolves a profile manifest's slots into the boot file's content.
//
// A manifest that declares no slots — the key absent, null, or an empty list —
// returns a nil result and a nil error. That is not a failure: it means there
// is no boot file to write, and the caller renders none.
//
// Otherwise the declared slots are handed to the provider in declared order,
// with $VAR and ${VAR} expanded in the two fields that name somewhere to read
// from — see [expandSources]. Nothing else here filters, re-orders, validates
// or normalises them; [agentcontext.ContextRequest.Validate] does
// that inside Assemble, and doing it twice would only let the two answers
// drift. Errors are wrapped to name the manifest they came from and stay
// [errors.Is]-comparable against the library's sentinels.
func Assemble(ctx context.Context, spec profile.Spec, opts Options) (*agentcontext.ContextResult, error) {
	declared, err := spec.Slots()
	if err != nil {
		return nil, wrap(err)
	}
	if len(declared) == 0 {
		return nil, nil
	}
	if err := checkKinds(spec[profile.SpecKeySlots], declared); err != nil {
		return nil, wrap(err)
	}
	if opts.Deterministic {
		declared = keepDeterministic(declared)
	}
	if len(declared) == 0 {
		return nil, nil
	}

	provider := opts.Provider
	if provider == nil {
		if provider, err = defaultProvider(); err != nil {
			return nil, wrap(err)
		}
	}

	result, err := provider.Assemble(ctx, agentcontext.ContextRequest{
		Slots:      expandSources(declared, opts.Env),
		Limits:     opts.Limits,
		Workdir:    opts.Workdir,
		Provenance: opts.Provenance,
	})
	if err != nil {
		return nil, wrap(err)
	}
	return result, nil
}

// DeterministicKinds are the source kinds the installed layer resolves: the
// ones whose answer changes only when the operator changes something.
//
// The line is drawn by what `install --check` needs, not by what is safe to
// run. A check re-renders and diffs the result against disk, so a source whose
// value can differ between two renders of one profile reports drift on every
// run — which is a gate configured not to gate, the disease plan §5 names for
// the orphan sweep. A cmd slot reading `git status` is exactly that.
//
// A static file can change too, and reporting that as drift is the point: the
// operator changed the source and has not installed it yet. It is the same
// answer `--check` already gives for a skill whose file was edited.
//
// The other half of the reason is that a check is documented to write nothing
// and to go nowhere near a live configuration. Running a profile's commands
// and making its requests on every `--check` is a larger promise than that
// command makes.
func DeterministicKinds() []agentcontext.SlotSourceKind {
	return []agentcontext.SlotSourceKind{
		agentcontext.SlotSourceKindStaticFile,
		agentcontext.SlotSourceKindStaticDir,
		agentcontext.SlotSourceKindInline,
		agentcontext.SlotSourceKindRoleSummary,
	}
}

// keepDeterministic returns the slots a deterministic assembly resolves.
func keepDeterministic(declared []agentcontext.SlotSpec) []agentcontext.SlotSpec {
	allowed := DeterministicKinds()
	kept := make([]agentcontext.SlotSpec, 0, len(declared))
	for _, slot := range declared {
		if slices.Contains(allowed, slot.Source.Kind) {
			kept = append(kept, slot)
		}
	}
	return kept
}

// Nondeterministic returns the slots a deterministic assembly leaves alone,
// each named with the kind that excluded it, sorted.
//
// It is a question rather than an error returned beside a usable result. The
// render is complete and correct; the slots left out are a fact the operator
// has to be told, and the caller doing the telling is the one that knows which
// layer it is rendering.
func Nondeterministic(spec profile.Spec) ([]string, error) {
	declared, err := spec.Slots()
	if err != nil {
		return nil, wrap(err)
	}
	allowed := DeterministicKinds()
	var out []string
	for _, slot := range declared {
		if !slices.Contains(allowed, slot.Source.Kind) {
			out = append(out, fmt.Sprintf("%q (%s)", slot.Name, slot.Source.Kind))
		}
	}
	slices.Sort(out)
	return out, nil
}

// checkKinds reports a slot whose kind the library will refuse, naming the
// slot and the key rather than leaving the caller with the library's bare
// "unknown slot kind".
//
// The mistake it exists for is specific and near-certain to be made.
// [agentcontext.SlotSource]'s kind field is tagged `json:"kind"` and
// `yaml:"type"`. Every boot profile in the portfolio is YAML and writes
// `type:`; a cairn manifest is JSON and must write "kind". Copying a slot out
// of a working YAML profile therefore produces a slot with no kind at all, and
// the library — which never sees the manifest — can only say that the kind is
// unknown.
func checkKinds(raw json.RawMessage, declared []agentcontext.SlotSpec) error {
	var sources []struct {
		Source map[string]json.RawMessage `json:"source"`
	}
	// A manifest that will not re-decode is not this check's problem: it
	// decoded once already to produce declared, and the kind check below still
	// runs without the raw source objects.
	_ = json.Unmarshal(raw, &sources)

	for i, slot := range declared {
		named := fmt.Sprintf("slot %d", i)
		if slot.Name != "" {
			named = fmt.Sprintf("slot %q", slot.Name)
		}
		var source map[string]json.RawMessage
		if i < len(sources) {
			source = sources[i].Source
		}
		if err := kindDiagnostic(named, slot.Source.Kind, source); err != nil {
			return err
		}
	}
	return nil
}

// defaultProvider builds the provider [Assemble] uses when [Options] names
// none: the seven app-neutral resolvers, and [DropUnresolved] over the
// library's own renderer so that a slot which produced nothing leaves no
// heading behind.
//
// resolvers.WithSkillIndex is deliberately not wired. Per the MVP plan the
// eighth kind gets added when a profile needs one, and none does yet; until
// then a manifest declaring a skill_index slot fails with
// [agentcontext.ErrUnknownSlotKind], which is the honest answer. This is a
// decision, not an oversight.
func defaultProvider() (agentcontext.ContextProvider, error) {
	return agentcontext.NewProvider(resolvers.Default(), DropUnresolved{})
}

// wrap names the manifest an error came out of without hiding it: the
// library's sentinels stay reachable through errors.Is.
func wrap(err error) error {
	return fmt.Errorf("assemble profile manifest slots: %w", err)
}
