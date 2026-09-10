package profile

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrCycle reports that following extends arrived back at a profile the walk
// had already visited. The wrapping error names the ids in the order they were
// walked, ending with the one that closed the loop.
var ErrCycle = errors.New("extends cycle")

// ErrNilLoader reports that [Resolve] was given no [Loader] to read profiles
// through. It is a programming error rather than a configuration one.
var ErrNilLoader = errors.New("nil loader")

// ErrNilProfile reports that a [Loader] returned neither a profile nor an
// error. It is a violation of the [Loader] contract, caught here so it surfaces
// as a named error instead of a nil dereference further down.
var ErrNilProfile = errors.New("loader returned no profile and no error")

// Loader is what [Resolve] reads profiles through: one declared profile by id,
// with no cascade applied. The catalog implements it.
//
// A Loader that has no profile for an id returns an error — the catalog's
// ErrProfileNotFound — rather than a nil profile. [Resolve] propagates that
// error, so what "not found" means stays the Loader's to define.
type Loader interface {
	Profile(ctx context.Context, id string) (*Profile, error)
}

// Resolve walks id's extends chain and folds it into one [Resolved].
//
// The walk goes leaf to root and the fold goes root to leaf, so every field is
// closest-wins: the value from the nearest profile in the chain that declares
// it, where declaring means non-empty for a string and present for a top-level
// key of [Spec].
//
// A [Spec] key composes by one rule: keyed collections merge by key, and
// everything else replaces. A descendant's member of a keyed collection —
// a slot by its name, a template by its destination, a skill by its id —
// replaces the ancestor's member at that key and leaves the rest standing. A
// key that is not a keyed collection is taken whole and unread, which includes
// every key this package has never heard of: what is keyed, and by what, is an
// explicit table and nothing else earns a merge. See [specMergers].
//
// Restating an ancestor's member is how a profile keeps it, and a member set
// to JSON null removes it where the collection is a map. A key set to JSON
// null clears the whole collection. An empty list or object adds nothing and
// clears nothing.
//
// A key exactly one profile in the chain declares is carried byte for byte:
// two declared values are what a merge needs, so one never reaches a merger.
//
// [Profile.Body] is carried rather than composed: [Resolved.Bodies] is every
// declared body in fold order, and what composing them means belongs to the
// engine that renders them — see [github.com/chrispian/cairn/template]. It
// used to be concatenated here, ancestor-first, and the concatenation was
// never rendered by anything.
//
// [Profile.Abstract] does not cascade. [Resolved.Abstract] is the leaf's own
// flag, carried rather than acted on: Resolve never refuses to resolve an
// abstract profile, because `cairn install` legitimately resolves one and only
// a direct boot has reason to object.
func Resolve(ctx context.Context, l Loader, id string) (*Resolved, error) {
	return ResolveComposition(ctx, l, id, nil)
}

// ResolveComposition resolves id and then each part in order, folding the whole
// sequence into one [Resolved].
//
// A composition needs no second mechanism, and this is why: an extends chain is
// a sequence of profiles folded closest-wins, an explicit list of parts is a
// sequence of profiles folded closest-wins, and one fold does both. Each part's
// own extends chain is walked before it merges, so the sequence folded is id's
// chain, then the first part's, then the second's, and a part is closer than
// everything ahead of it.
//
// A part is an ordinary profile and is loaded through the same [Loader]. That
// is what lets `cairn boot x --with ./part.md` work without the loader below
// knowing what a path is: the caller keys the file under an id of its own
// choosing, and the part's own extends resolves through the catalog like any
// other.
//
// # A part contributes what it adds, not what was already settled
//
// A profile the fold has already reached is skipped when a later chain names it
// again.
//
// This is [walk]'s own rule at the outer level, and nothing more. A walk
// already refuses to visit a profile twice within one chain; the fold is a
// sequence of chains, and the same guard belongs across the sequence. Read it
// that way rather than as a composition rule of its own: the defect it closes
// was not a missing rule, it was cairn failing to apply its own rule one level
// out. What both spellings keep is that no profile is ever folded after one of
// its own descendants.
//
// So this is not an optimization, and removing it is not a simplification.
//
// What it prevents is a composition reverting the target's own overrides.
// `cairn boot engineer --with <part>`, where the part extends the same abstract
// base engineer does and says nothing about the key in question, folds base a
// second time — and that second copy lands in front of engineer. So base's
// value wins over the leaf that overrode it, and the value that wins is the
// ANCESTOR'S, contributed from inside the part's chain by a profile the part
// never mentions and the operator never named.
//
// Every key is exposed, not only the scalar fields, and the concrete harm is
// what got this caught. spec.templates is an objectByKey, so against cairn's
// own examples/bundle the composed spec.templates for AGENTS.md flipped from
// engineer.md to base.md: `cairn boot engineer --with <any part extending
// base>` wrote the boot directory's AGENTS.md from BASE's template under
// engineer's heading, telling the agent it is something other than what it is.
// A flag that was supposed to add one thing silently replaced the instructions.
// spec.files, spec.trees, spec.slots, spec.mcp and spec.settings are the same
// shape. And a key only that ancestor declares would reach a merger it should
// never have reached, coming back re-encoded where a single declarer is
// promised byte for byte — see [Resolve].
//
// A chain is linear and ancestor-first, which is what makes the skip safe to
// state so broadly: an id appearing in two chains is followed, in both, only by
// its own descendants. So a second occurrence is always an ancestor arriving
// behind a descendant that already had its say, and it is always already folded
// at its correct position, with its contribution in the accumulation. Nothing
// is lost by skipping it, and everything after it in the later chain still
// folds, in order, and still beats what is ahead of it.
//
// The skip is cumulative — against the target's chain and against every earlier
// part, not against the target's alone. Two parts sharing an ancestor with each
// other but not with the target is the same inversion one step over: the shared
// ancestor would land between them and revert whatever the first part
// overrode. Restricting the skip to the target's chain would fix the case that
// gets reported and leave its twin standing.
//
// Ordinary extends resolution is untouched by all of it. A single chain cannot
// repeat an id — [walk] refuses that as a cycle — so with no parts there is
// never anything to skip.
//
// A part every profile of whose chain was already folded contributed nothing,
// and is named in [Resolved.AlreadyFolded] so the caller can say so. Skipping
// silently is the one thing this must not do: the operator typed a flag and it
// changed nothing, which is the shape cairn reports everywhere else — a
// declared slot that filled nothing, a value marker it cannot fill.
//
// # Abstract
//
// [Resolved.Abstract] is id's own leaf and never a part's. Abstract marks a
// profile that exists to be extended rather than booted, which is exactly what
// a part is; letting one decide whether the composition may boot would refuse
// the case this exists for.
func ResolveComposition(ctx context.Context, l Loader, id string, parts []string) (*Resolved, error) {
	if l == nil {
		return nil, fmt.Errorf("resolving profile %q: %w", id, ErrNilLoader)
	}

	chain, ids, err := walk(ctx, l, id)
	if err != nil {
		return nil, err
	}
	abstract := chain[len(chain)-1].Abstract

	var alreadyFolded []string
	folded := make(map[string]bool, len(ids))
	for _, walked := range ids {
		folded[walked] = true
	}
	for _, part := range parts {
		// Each part gets its own walk, and so its own cycle guard: two parts
		// naming one profile is a composition an operator meant, where one
		// chain arriving back at a profile it already walked is not.
		partChain, partIDs, err := walk(ctx, l, part)
		if err != nil {
			return nil, fmt.Errorf("composing profile %q with %q: %w", id, part, err)
		}
		contributed := false
		for i, partID := range partIDs {
			if folded[partID] {
				continue
			}
			folded[partID] = true
			chain = append(chain, partChain[i])
			ids = append(ids, partID)
			contributed = true
		}
		// Recorded rather than inferred by the caller. Whether a part added
		// anything is known exactly here and nowhere else: a caller could
		// compare chains and guess, and would have to reproduce this loop to
		// do it.
		if !contributed {
			alreadyFolded = append(alreadyFolded, part)
		}
	}

	out := &Resolved{
		ID:            id,
		Chain:         ids,
		Abstract:      abstract,
		AlreadyFolded: alreadyFolded,
		Spec:          Spec{},
	}

	for _, p := range chain {
		if p.Name != "" {
			out.Name = p.Name
		}
		if p.Description != "" {
			out.Description = p.Description
		}
		if p.Provider != "" {
			out.Provider = p.Provider
		}
		if p.Model != "" {
			out.Model = p.Model
		}
		if body := strings.TrimSpace(p.Body); body != "" {
			out.Bodies = append(out.Bodies, Body{ID: p.ID, Text: body})
		}
		for key, raw := range p.Spec {
			prev, declared := out.Spec[key]
			if !declared {
				// The first profile to declare a key hands over its bytes
				// unread. Nothing is composed until something else declares
				// the same key.
				out.Spec[key] = raw
				continue
			}
			merged, err := mergeSpecKey(key, prev, raw)
			if err != nil {
				return nil, fmt.Errorf("resolving profile %q: profile %q: %w", id, p.ID, err)
			}
			out.Spec[key] = merged
		}
	}
	return out, nil
}

// walk follows extends from id to the root of its chain and returns both the
// profiles and their ids, ancestor-first. A chain is linear — extends names one
// profile — so revisiting an id is always a cycle.
func walk(ctx context.Context, l Loader, id string) ([]*Profile, []string, error) {
	var (
		chain []*Profile
		ids   []string
		seen  = make(map[string]bool)
		// from is the profile whose extends named the id being loaded, empty
		// for the leaf, so a missing ancestor says who referenced it.
		from string
	)

	for cur := id; ; {
		if seen[cur] {
			return nil, nil, fmt.Errorf("resolving profile %q: %w: %s",
				id, ErrCycle, strings.Join(append(slices.Clone(ids), cur), " -> "))
		}
		seen[cur] = true
		ids = append(ids, cur)

		p, err := l.Profile(ctx, cur)
		if err != nil {
			return nil, nil, loadErr(cur, from, err)
		}
		if p == nil {
			return nil, nil, loadErr(cur, from, ErrNilProfile)
		}

		chain = append(chain, p)
		if p.Extends == "" {
			break
		}
		from, cur = cur, p.Extends
	}

	slices.Reverse(chain)
	slices.Reverse(ids)

	return chain, ids, nil
}

// loadErr wraps a failure to load id, naming the profile whose extends
// referenced it so a broken chain says where the dangling reference lives.
// An empty from means id was the profile asked for.
func loadErr(id, from string, err error) error {
	if from == "" {
		return fmt.Errorf("loading profile %q: %w", id, err)
	}
	return fmt.Errorf("loading profile %q, extended by %q: %w", id, from, err)
}
