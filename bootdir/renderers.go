package bootdir

import (
	"fmt"
	"slices"

	"github.com/chrispian/cairn/profile"
)

// bootRenderers is every renderer a boot tree can name, keyed by the manifest
// key it renders.
//
// It is a registry rather than a list because which renderers run, and in what
// order, is the tree's to say — see [Layout.Renders]. What stays here is the
// rendering itself, which is the same function for every harness: a skill is
// copied the same way into whichever directory the tree names.
var bootRenderers = map[string]func(*Instance) ([]File, error){
	profile.SpecKeyTemplates: renderTemplates,
	profile.SpecKeyMCP:       renderMCP,
	profile.SpecKeySettings:  RenderSettings,
	profile.SpecKeySkills:    RenderSkills,
	profile.SpecKeyPrompts:   renderPrompts,
	profile.SpecKeySubagents: renderSubagents,
	profile.SpecKeyTrees:     renderTrees,
	profile.SpecKeyFiles:     renderFiles,

	// A second implementation of one key. Which of the two runs is the tree's
	// to say — see [Layout.RenderImpls] — and not a provider test taken here.
	CodexConfigRenderer: RenderCodexConfig,
}

// CodexConfigRenderer is the name a layout document gives the settings
// renderer that produces Codex's config.toml.
const CodexConfigRenderer = "codex-config"

// Renderers returns the artifact renderers l's boot directory is rendered
// from, in the order its document names them.
//
// The order is the order files appear in a rendering, and it is also the order
// a failure is reported in, so a tree names the templates first: a profile
// with a broken marker should fail on the marker, not on a skill.
//
// Each Artifact is the name of the manifest key its renderer reads, which is
// what a diagnostic quotes. It is a label and never a path — the templates,
// skills, prompts, subagents, trees and files renderers each emit many files,
// at paths the tree and the manifest decide between them.
//
// A tree naming a renderer cairn does not have reports [ErrProviderLayout],
// which is the malformed-document case rather than anything an operator did.
func Renderers(l Layout) ([]Renderer, error) {
	out := make([]Renderer, 0, len(l.Renders))
	for _, key := range l.Renders {
		impl := key
		if named := l.RenderImpls[key]; named != "" {
			impl = named
		}
		render, ok := bootRenderers[impl]
		if !ok {
			return nil, fmt.Errorf("%w: the %s layout renders %q with %q, which cairn has no renderer for",
				ErrProviderLayout, l.Provider, key, impl)
		}
		out = append(out, Renderer{Artifact: key, Render: render})
	}
	return out, nil
}

// RendererKeys returns every manifest key a layout document may name under
// `renders`, sorted. It exists so that a document's own tests, and a
// diagnostic, can name the set rather than repeat it.
func RendererKeys() []string {
	out := make([]string, 0, len(bootRenderers))
	for key := range bootRenderers {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
