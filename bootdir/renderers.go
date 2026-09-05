package bootdir

import (
	"errors"
	"fmt"

	"github.com/chrispian/cairn/profile"
)

// ErrUnsupportedFeature reports a manifest key that has no provider-native
// materialization for the selected harness.
var ErrUnsupportedFeature = errors.New("unsupported provider feature")

// Renderers returns the artifact renderers a boot directory is rendered from,
// in render order.
//
// The order is the order files appear in a rendering, and it is also the order
// a failure is reported in, so the templates come first: a profile with a
// broken marker should fail on the marker, not on a skill.
//
// Each Artifact is the name of one manifest key or one line of the output
// contract, which is what a diagnostic quotes. It is a label and never a path.
// Two of the artifacts take their paths from the provider's BootDirSpec; the
// rest take them from the manifest, and the templates, skills, prompts,
// subagents, trees and files renderers each emit many files.
func Renderers() []Renderer {
	return claudeRenderers()
}

// RenderersFor returns the renderers for one provider's boot directory.
func RenderersFor(p profile.Provider) []Renderer {
	switch p {
	case profile.ProviderCodex:
		return codexRenderers()
	default:
		return claudeRenderers()
	}
}

func claudeRenderers() []Renderer {
	return []Renderer{
		{Artifact: profile.SpecKeyTemplates, Render: renderTemplates},
		{Artifact: ".mcp.json", Render: renderMCP},
		{Artifact: ".claude/settings.json", Render: RenderSettings},
		{Artifact: SkillsDirName, Render: RenderSkills},
		{Artifact: PromptsDirName, Render: renderPrompts},
		{Artifact: SubagentsDirName, Render: renderSubagents},
		{Artifact: profile.SpecKeyTrees, Render: renderTrees},
		{Artifact: profile.SpecKeyFiles, Render: renderFiles},
	}
}

func codexRenderers() []Renderer {
	return []Renderer{
		{Artifact: profile.SpecKeyTemplates, Render: renderTemplates},
		{Artifact: CodexConfigFileName, Render: RenderSettings},
		{Artifact: CodexSkillsDirName, Render: RenderSkills},
		{Artifact: profile.SpecKeyPrompts, Render: renderUnsupportedPrompts},
		{Artifact: profile.SpecKeySubagents, Render: renderUnsupportedSubagents},
		{Artifact: profile.SpecKeyTrees, Render: renderTrees},
		{Artifact: profile.SpecKeyFiles, Render: renderFiles},
	}
}

func renderUnsupportedPrompts(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	declared, err := inst.Profile.Spec.Prompts()
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: %q has no prompt-command directory in the %q layout; spec.%s declares %s",
		ErrUnsupportedFeature, inst.Layout.Provider, inst.Layout.Provider, profile.SpecKeyPrompts, quotedNames(declared))
}

func renderUnsupportedSubagents(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	if len(inst.Subagents) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: %q has no subagent definition directory in the %q layout; spec.%s names %s",
		ErrUnsupportedFeature, inst.Layout.Provider, inst.Layout.Provider, profile.SpecKeySubagents, quotedNames(subagentIDs(inst.Subagents)))
}
