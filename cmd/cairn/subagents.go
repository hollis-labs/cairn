package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hollis-labs/cairn/bootdir"
	"github.com/hollis-labs/cairn/profile"
)

// resolveSubagents turns the ids a profile names under spec.subagents into the
// declarations their definitions are rendered from.
//
// It resolves here rather than in a renderer for the reason slots and files
// do: it reads another profile out of the catalog and walks an extends chain,
// and a renderer consults nothing beyond the instance it is handed. What
// crosses over is one opaque JSON value per id — the named profile's own
// spec.subagent — so nothing else about a named profile reaches the boot
// directory of the profile that named it.
//
// A named profile is resolved through the same cascade a booted one is, so a
// subagent declaration may be inherited or restated like any other manifest
// key.
//
// Three refusals, each naming the id and the profile that named it:
//
//   - An id with no profile. It comes back from the cascade, which says which
//     profile referenced it.
//   - An abstract profile, matching `cairn boot`. An abstract profile exists
//     to be extended; a definition is something the harness runs, and running
//     a template runs a profile its author marked unrunnable.
//   - A profile declaring no spec.subagent. The definition would be a file
//     with a name and nothing else — a path the booting profile promised, with
//     nothing declared to put at it.
//
// Depth is one by construction. This walks the ids the booting profile named
// and stops; a named profile's own spec.subagents is never read, and a
// subagent gets no boot directory to hold definitions in.
func resolveSubagents(ctx context.Context, loader profile.Loader, parent *profile.Resolved) ([]bootdir.Subagent, error) {
	declared, err := parent.Spec.Subagents()
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", parent.ID, err)
	}
	if len(declared) == 0 {
		return nil, nil
	}

	out := make([]bootdir.Subagent, 0, len(declared))
	for _, raw := range declared {
		id := strings.TrimSpace(raw)
		named := fmt.Sprintf("profile %q names subagent %q", parent.ID, id)

		child, err := profile.Resolve(ctx, loader, id)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", named, err)
		}
		if child.Abstract {
			return nil, fmt.Errorf("%s, which is abstract: it exists to be extended, not run", named)
		}
		declaration, ok := child.Spec.Subagent()
		if !ok {
			return nil, fmt.Errorf("%s, which declares no spec.%s: a definition is rendered from that key",
				named, profile.SpecKeySubagent)
		}
		out = append(out, bootdir.Subagent{ID: id, Declaration: declaration})
	}
	return out, nil
}
