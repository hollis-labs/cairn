package slots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/hollis-labs/cairn/profile"
	"github.com/hollis-labs/libs/harness/core/agentcontext"
)

// ErrFileSource reports a files entry whose source could not be resolved. It
// names the path, which the library's own error cannot: the resolver is handed
// a slot and knows nothing about the manifest entry the slot was built from.
var ErrFileSource = errors.New("unresolved file source")

// ResolveFiles turns a manifest's files entries into the content each is
// planted with. A literal passes through untouched; a source is resolved
// through the same resolvers a slot uses.
//
// It runs commands and makes requests, so it belongs here and in the
// composition root rather than in a renderer — the same rule [Assemble]
// follows, and for the same reason.
//
// **A source that fails fails the boot**, which is the opposite of a slot. A
// slot that does not resolve leaves a section out of the boot file and the
// agent asks its tools instead; a file that does not resolve leaves a hole at
// a path the profile promised, and nothing downstream can notice it is
// missing. The two are different failures and they get different answers.
//
// A source that resolves to nothing is not a failure and plants an empty file.
// The resolver was reached and answered; that the answer was empty is content,
// and content is a black box here. The slots are therefore declared
// non-required deliberately — the library's Required flag fails the assembly
// on an empty result as well as on a failed one, and conflating those two
// would turn `torque task list` finding no tasks into a boot that will not
// start.
//
// A manifest that declares no files, or only literals, resolves nothing and
// makes no calls.
func ResolveFiles(ctx context.Context, spec profile.Spec, opts Options) (map[string]string, error) {
	return ResolveEntries(ctx, spec, profile.SpecKeyFiles, opts)
}

// ResolveEntries is [ResolveFiles] over any manifest key holding a
// path-to-entry map.
//
// Two keys hold that shape: spec.files, whose values are planted verbatim, and
// spec.templates, whose values have their markers substituted first. What is
// resolved is the same in both — a literal passes through, a source is
// resolved — so resolving them through one function is what keeps a template
// source and a file source from ever behaving differently.
func ResolveEntries(ctx context.Context, spec profile.Spec, key string, opts Options) (map[string]string, error) {
	declared, err := entriesOf(spec, key)
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return nil, nil
	}

	out := make(map[string]string, len(declared))
	var sourced []string
	for rel, entry := range declared {
		if entry.IsSource() {
			sourced = append(sourced, rel)
			continue
		}
		out[rel] = entry.Literal
	}
	if len(sourced) == 0 {
		return out, nil
	}
	// Sorted so that the request is the same twice and an error names the
	// same entry twice; a map has no order and this one reaches a resolver.
	slices.Sort(sourced)

	if err := checkEntryKinds(spec[key], key, declared, sourced); err != nil {
		return nil, err
	}

	provider := opts.Provider
	if provider == nil {
		if provider, err = defaultProvider(); err != nil {
			return nil, err
		}
	}

	// One slot per sourced file, named by its path. The assembled rendering is
	// discarded — what is wanted is each result's own content, at its own
	// path — so the request carries no limits: a budget here would silently
	// truncate a file rather than a section.
	req := agentcontext.ContextRequest{
		Workdir:    opts.Workdir,
		Provenance: opts.Provenance,
		Slots:      make([]agentcontext.SlotSpec, 0, len(sourced)),
	}
	for _, rel := range sourced {
		req.Slots = append(req.Slots, agentcontext.SlotSpec{
			Name:   rel,
			Source: expandSource(*declared[rel].Source, opts.Env),
		})
	}

	result, err := provider.Assemble(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("resolve profile manifest %s: %w", key, err)
	}
	// Every failure is on its own result, because no slot is required. That is
	// what lets the error name the path: the library records the resolver's
	// error against the slot, and the slot's name is where the file goes.
	if result != nil {
		expansions, expErr := Expansions(spec, key, opts.Env)
		if expErr != nil {
			return nil, expErr
		}
		for _, s := range result.Slots {
			if s.Err != nil {
				// The same information loss the slot report fixes, on the key
				// where it fails the boot rather than warning: the resolver was
				// handed the expanded value and can only name that one.
				return nil, fmt.Errorf("%w: spec.%s entry %q: %s%w",
					ErrFileSource, key, s.Name, prefixed(expansions[s.Name]), s.Err)
			}
			out[s.Name] = s.Content
		}
	}
	// Every promised path now has content, or the boot does not happen. A
	// provider that answered for fewer slots than it was asked about would
	// otherwise leave a hole at exactly the kind of path this function exists
	// to keep filled, and nothing downstream would notice.
	for _, rel := range sourced {
		if _, resolved := out[rel]; !resolved {
			return nil, fmt.Errorf("%w: spec.%s entry %q: the provider returned no result for it",
				ErrFileSource, key, rel)
		}
	}
	return out, nil
}

// entriesOf reads the path-to-entry map under key.
func entriesOf(spec profile.Spec, key string) (map[string]profile.FileEntry, error) {
	switch key {
	case profile.SpecKeyFiles:
		return spec.Files()
	case profile.SpecKeyTemplates:
		return spec.Templates()
	default:
		return nil, fmt.Errorf("%w: spec.%s does not hold path-to-entry values", ErrFileSource, key)
	}
}

// prefixed renders a diagnostic note to stand ahead of the resolver's own
// message, and renders nothing at all for an empty one so a source with no
// variable in it reads exactly as it did before.
func prefixed(note string) string {
	if note == "" {
		return ""
	}
	return note + ": "
}

// checkEntryKinds reports an entry whose source kind is missing or is not one
// the library recognizes, naming the path it would have been planted at.
//
// It is the slot check of [checkKinds] asked of the other place a
// [agentcontext.SlotSource] is written, for the same reason: the YAML habit
// that spells the kind key `type:` does not stop at the slots key, and an
// operator who moves a working source into a files entry carries the habit
// with it.
func checkEntryKinds(raw json.RawMessage, key string, declared map[string]profile.FileEntry, sourced []string) error {
	// A manifest that will not re-decode is not this check's problem: it
	// decoded once already to produce declared, and the kind check below still
	// runs without the raw entry objects.
	var entries map[string]json.RawMessage
	_ = json.Unmarshal(raw, &entries)

	for _, rel := range sourced {
		var source map[string]json.RawMessage
		_ = json.Unmarshal(entries[rel], &source)

		named := fmt.Sprintf("spec.%s entry %q", key, rel)
		if err := kindDiagnostic(named, declared[rel].Source.Kind, source); err != nil {
			return err
		}
	}
	return nil
}
