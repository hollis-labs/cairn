package bootdir

import (
	"fmt"
	"slices"
	"strings"

	"github.com/hollis-labs/cairn/profile"
)

// renderFiles returns the arbitrary files a profile's manifest declares, each
// planted at its own path with its own content and nothing added to either.
//
// This is the manifest's escape hatch: content that has to reach a boot
// directory and is not one of the artifacts cairn knows by name rides here,
// and cairn neither reads it nor asks what it is for.
//
// The content arrives on the instance already resolved — see [Instance].Files.
// A manifest entry may name a slot source rather than a literal, and resolving
// one runs commands and makes requests, which a renderer may not do.
//
// The paths are emitted in sorted order, because a map has none and a
// rendering has to be the same twice. Whether a path can name something inside
// the boot directory is [Render]'s question — it asks it of every renderer, so
// asking it again here would be a second opinion that could disagree.
func renderFiles(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	declared := inst.Files
	if len(declared) == 0 {
		return nil, nil
	}
	rels := make([]string, 0, len(declared))
	for rel := range declared {
		rels = append(rels, rel)
	}
	slices.Sort(rels)

	files := make([]File, 0, len(rels))
	for _, rel := range rels {
		// An empty path is the one case [Render] cannot report usefully: its
		// error quotes the path, and there is nothing there to quote.
		if strings.TrimSpace(rel) == "" {
			return nil, fmt.Errorf("%w: spec.%s holds an entry whose path is empty",
				ErrArtifactPath, profile.SpecKeyFiles)
		}
		files = append(files, File{Path: rel, Content: []byte(declared[rel])})
	}
	return files, nil
}
