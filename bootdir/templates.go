package bootdir

import (
	"fmt"
	"slices"
	"strings"

	"github.com/chrispian/cairn/profile"
)

// renderTemplates returns every file the manifest's templates produce, each at
// the path the manifest declared for it.
//
// This is the whole of the boot directory's prose. No file here is named by
// cairn: there is no instruction file it insists on, no section it composes,
// and no order it imposes. A profile that declares no template renders no
// prose at all, which is the same rule that has always governed skills and MCP
// servers — cairn ships no content, and a document it wrote for an operator
// who did not ask for one is content.
//
// The template text arrives on the instance already resolved — see
// [Instance].Templates — because a template may be a source rather than a
// literal, and resolving one runs commands and makes requests.
//
// The paths are emitted in sorted order, because a map has none and a
// rendering has to be the same twice.
func renderTemplates(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	if len(inst.Templates) == 0 {
		return nil, nil
	}
	rels := make([]string, 0, len(inst.Templates))
	for rel := range inst.Templates {
		rels = append(rels, rel)
	}
	slices.Sort(rels)

	files := make([]File, 0, len(rels))
	for _, rel := range rels {
		if rel == PointerFileName && !inst.Layout.Pointer.Declared() {
			continue
		}
		// An empty path is the one case [Render] cannot report usefully: its
		// error quotes the path, and there is nothing there to quote.
		if strings.TrimSpace(rel) == "" {
			return nil, fmt.Errorf("%w: spec.%s holds an entry whose path is empty",
				ErrArtifactPath, profile.SpecKeyTemplates)
		}
		file, err := templateFile(inst, rel, Artifact{RelPath: rel})
		if err != nil {
			return nil, err
		}
		files = append(files, file...)
	}
	return files, nil
}

// templateFile substitutes the template declared at dest and returns it as one
// file written at artifact's path, or no file when the manifest declares no
// template for that destination.
//
// It is the one place substitution happens. The installed layer renders two
// destinations at paths of its own and the boot directory renders every
// destination where it was declared, and both arrive here, so the two layers
// cannot differ in what a marker means.
func templateFile(inst *Instance, dest string, artifact Artifact) ([]File, error) {
	text, declared := inst.Templates[dest]
	if !declared {
		return nil, nil
	}
	if !artifact.Declared() {
		return nil, fmt.Errorf(
			"%w: spec.%s declares %q, but this layout declares no path for it",
			ErrProviderLayout, profile.SpecKeyTemplates, dest)
	}
	rendered, err := Substitute(text, inst.Sections, inst.Values)
	if err != nil {
		return nil, fmt.Errorf("spec.%s %q: %w", profile.SpecKeyTemplates, dest, err)
	}
	// A template that substitutes away to nothing renders no file. An empty
	// document is a claim that a profile said something and meant nothing by
	// it, where an absent one is the profile not having declared it.
	if strings.TrimSpace(rendered) == "" {
		return nil, nil
	}
	if !strings.HasSuffix(rendered, "\n") {
		rendered += "\n"
	}
	return []File{{
		Path:    artifact.RelPath,
		Content: []byte(rendered),
		Mode:    artifact.Mode,
	}}, nil
}

// RenderAgentsTemplate returns the instruction file the installed layer
// renders: the manifest's template for [AgentsFileName], written at the path
// this layout reads it from.
//
// It exists because the installed layer claims a fixed set of paths and the
// boot directory does not. `install --check` derives what it may report on
// from the renderer registration rather than from a render, which is what lets
// it find a file left behind by a profile that stopped declaring one; a
// template free to name any destination in the operator's home would make that
// set depend on the profile being checked, in exactly the case the check
// exists for. The boot directory has no such constraint — it is created fresh
// and refuses to plant over anything.
func RenderAgentsTemplate(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	return templateFile(inst, AgentsFileName, inst.Layout.Agents)
}

// RenderPointerTemplate returns the harness's own instruction file for the
// installed layer, from the manifest's template for [PointerFileName].
//
// The pointer is a template like everything else. It used to be one line cairn
// wrote — an include of [AgentsFileName] — which was safe only while that file
// was always rendered. Under templates it need not be, and a hardcoded include
// of a file that is not there resolves to nothing with no diagnostic at all:
// the harness reads the pointer, finds no such import, and carries on. A
// pointer a profile declares is a pointer a profile can keep true.
func RenderPointerTemplate(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	return templateFile(inst, PointerFileName, inst.Layout.Pointer)
}
