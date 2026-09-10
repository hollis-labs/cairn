package bootdir

import (
	"errors"
	"fmt"
	"strings"

	"github.com/chrispian/cairn/profile"
)

// ErrInstructionArtifact reports a profile that says what its instruction
// document is twice: a rendered body, and a spec.templates entry for the
// destination the tree's instruction artifact renders from.
//
// It is refused rather than resolved by precedence, for the reason a duplicate
// profile id is. A rule like "the body wins" is silent shadowing: the losing
// document stays in the profile, is edited, is committed, and never takes
// effect, and the operator's evidence that something is wrong is that their
// change did nothing — in the one file that tells an agent what it is.
var ErrInstructionArtifact = errors.New("two documents claim the instruction artifact")

// instructionFile returns the file the tree's instruction artifact renders to
// from the profile's body, and whether the body claimed the artifact at all.
//
// It is one function with two callers on purpose, and both layers go through
// it: a boot directory and the installed layer must not be able to disagree
// about which document an agent reads. That is the same reason [templateFile]
// is the only place a marker is substituted.
//
// # Where it lands
//
// At the path the tree declares for its instruction artifact, in both layers.
// The profile says what the document is and the tree says where a harness
// reads it, which is the render/plant seam exactly: content in the profile,
// placement in the layout.
//
// That is a correction of an inconsistency worth naming, because it is the one
// this rule closes. spec.templates plants a destination AT its destination in
// a boot directory — a profile declaring "docs/x.md" gets a file at
// docs/x.md, and the tree's `path` for its agents artifact is not consulted —
// while the installed layer maps the same destination onto a path of its own.
// So the tree owned placement in one layer and not the other, and the `dest`
// field was a join key rather than a destination. A body has no destination to
// join on: there is one body, the tree names one instruction artifact, and
// there is nothing left for a profile to spell.
//
// A tree that declares no instruction artifact and a profile that has a body
// is [ErrProviderLayout] and not a silent drop. It is the same refusal a
// declared prompt gets from a tree with no prompts directory: a boot directory
// missing the file a profile asked for looks exactly like a boot directory of
// a profile that never asked.
func instructionFile(inst *Instance) ([]File, bool, error) {
	text := strings.TrimSpace(inst.Document)
	if text == "" {
		return nil, false, nil
	}
	artifact := inst.Layout.Agents
	if !artifact.Declared() {
		return nil, false, fmt.Errorf(
			"%w: the profile's body renders a document, and the %s layout declares no instruction artifact for it",
			ErrProviderLayout, inst.Layout.Provider)
	}
	if _, declared := inst.Templates[artifact.Dest]; declared && artifact.Dest != "" {
		return nil, false, fmt.Errorf(
			"%w: the profile's body renders one, and spec.%s declares %q, which the %s layout renders "+
				"as the same artifact — put the document in one of them",
			ErrInstructionArtifact, profile.SpecKeyTemplates, artifact.Dest, inst.Layout.Provider)
	}
	// One trailing newline, for the reason a substituted template gets one: a
	// file an operator reads and diffs ends in a newline.
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []File{{
		Path:    artifact.RelPath,
		Content: []byte(text),
		Mode:    artifact.Mode,
	}}, true, nil
}
