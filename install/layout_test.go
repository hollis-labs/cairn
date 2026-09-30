package install

import (
	"errors"
	"strings"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/profile"
)

// TestInstalledRenderersComeFromTheTree covers the registration list now that
// it is read rather than written: each artifact the tree lists gets the shared
// renderer for its kind, in the tree's order, carrying the behaviours the tree
// named.
//
// The order is not incidental. It is the order the written files are reported
// in, which is what an operator reads after an install.
func TestInstalledRenderersComeFromTheTree(t *testing.T) {
	for _, tc := range []struct {
		provider profile.Provider
		labels   []string
		merges   []string
		fills    []string
	}{
		{
			provider: profile.ProviderClaude,
			labels:   []string{"AGENTS.md", "CLAUDE.md", "settings.json", "skills"},
			merges:   []string{"settings.json"},
			fills:    []string{"skills"},
		},
		{
			provider: profile.ProviderCodex,
			labels:   []string{".codex/AGENTS.md", ".codex/config.toml", ".agents/skills"},
			merges:   []string{".codex/config.toml"},
			fills:    []string{".agents/skills"},
		},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			renderers, _, err := PlanterFor(tc.provider)
			if err != nil {
				t.Fatalf("PlanterFor(%q): %v", tc.provider, err)
			}
			var labels, merges, fills []string
			for _, r := range renderers {
				if r.Render == nil {
					t.Errorf("the %q artifact has no renderer", r.Artifact)
				}
				labels = append(labels, r.Artifact)
				if r.Merge != nil {
					merges = append(merges, r.Artifact)
				}
				if r.Fills != nil {
					fills = append(fills, r.Artifact)
				}
			}
			if strings.Join(labels, ",") != strings.Join(tc.labels, ",") {
				t.Errorf("artifacts = %v, want %v", labels, tc.labels)
			}
			if strings.Join(merges, ",") != strings.Join(tc.merges, ",") {
				t.Errorf("merged artifacts = %v, want %v", merges, tc.merges)
			}
			if strings.Join(fills, ",") != strings.Join(tc.fills, ",") {
				t.Errorf("filled artifacts = %v, want %v", fills, tc.fills)
			}
		})
	}
}

// TestTheInstructionFileIsFoundByKind covers what decides which artifact opens
// with the generated-file marker. It is the artifact's kind, so a tree that
// renamed its instruction file keeps the marker and a later markdown artifact
// does not acquire one by ending in ".md".
func TestTheInstructionFileIsFoundByKind(t *testing.T) {
	for _, p := range bootdir.LayoutProviders() {
		renderers, _, err := PlanterFor(p)
		if errors.Is(err, bootdir.ErrUnsupportedProvider) {
			continue // a boot-only tree, such as OpenCode's, installs nothing
		}
		if err != nil {
			t.Fatalf("PlanterFor(%q): %v", p, err)
		}
		var agents int
		for _, r := range renderers {
			if r.Kind == bootdir.KindAgents {
				agents++
			}
		}
		if agents != 1 {
			t.Errorf("the %s tree installs %d artifacts of kind %q, want exactly one",
				p, agents, bootdir.KindAgents)
		}
	}
}

// TestATreeNamingSomethingInstallDoesNotHaveIsRefused covers a document that
// asks for an artifact kind or a behaviour this package has no implementation
// for. It is a malformed tree rather than anything an operator did, and it is
// refused rather than dropped: an installed layer missing an artifact looks
// exactly like one that never had it.
func TestATreeNamingSomethingInstallDoesNotHaveIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		artifact bootdir.InstalledArtifact
		want     string
	}{
		{
			name:     "an unknown kind",
			artifact: bootdir.InstalledArtifact{Kind: "hooks", Label: "hooks.json"},
			want:     "kind \"hooks\"",
		},
		{
			name:     "an unknown merge",
			artifact: bootdir.InstalledArtifact{Kind: bootdir.KindSettings, Label: "settings.json", Merge: "yaml-document"},
			want:     "merge behaviour \"yaml-document\"",
		},
		{
			name:     "an unknown normalizer",
			artifact: bootdir.InstalledArtifact{Kind: bootdir.KindSettings, Label: "settings.json", Normalize: "prettify"},
			want:     "normalize behaviour \"prettify\"",
		},
		{
			name:     "an unknown claim",
			artifact: bootdir.InstalledArtifact{Kind: bootdir.KindSettings, Label: "settings.json", Claim: "always"},
			want:     "claim behaviour \"always\"",
		},
		{
			name:     "an unknown fills",
			artifact: bootdir.InstalledArtifact{Kind: bootdir.KindSkills, Label: "skills", Fills: "every-skill"},
			want:     "fills behaviour \"every-skill\"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			il := bootdir.InstalledLayout{
				Dir:       ".claude",
				Artifacts: []bootdir.InstalledArtifact{tc.artifact},
				Layout:    bootdir.Layout{Provider: profile.ProviderClaude},
			}
			_, err := installRenderers(il)
			if !errors.Is(err, ErrInstalledLayout) {
				t.Fatalf("installRenderers() = %v, want ErrInstalledLayout", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error %q does not say %q", err, tc.want)
			}
		})
	}
}
