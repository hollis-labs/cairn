package bootdir

import (
	"fmt"
	"strings"

	"github.com/chrispian/cairn/profile"
)

// Undeclared names each collection of authored content a profile declared and
// this tree has nowhere to plant, one line per collection, sorted.
//
// It is the question behind a warn-and-drop, and it is one function so that
// what the renderers drop and what the operator is told cannot disagree. Both
// read [Layout.contentDir]; a tree that gained a directory would stop
// reporting and start planting in the same edit.
//
// # Why this is a report and not a refusal
//
// It used to be a refusal, and the argument for that was written into
// codex.yaml: a boot directory silently missing the prompts a profile asked
// for looks exactly like one whose profile never asked. That argument is
// sound, and it is about SILENCE — which is the thing this report removes. The
// refusal was the strong form of a claim whose weak form was enough.
//
// Cairn already warn-and-drops the more severe case. A declared slot that
// fails renders nothing, prints a line, and the boot exits 0; a missing
// section is context an agent silently reasons without, where a missing prompt
// is a command a person types and is told does not exist. Refusing the milder
// case while dropping the sharper one was an inversion.
//
// What settled it is who could clear the refusal. The only way to satisfy it
// was a per-provider `prompts: null` in the catalog — provider knowledge, in
// the component that owns none of it. agent-setup removing the part that
// carried those nulls left every Codex boot of every profile refusing, because
// one inherited `prompts:` in base reached all fifteen. A rule that can only
// be cleared by breaking the boundary it belongs to is mis-sited, not merely
// expensive.
//
// This is not a general licence to downgrade refusals. A tree naming a
// renderer cairn does not have still fails the render: no catalog edit can
// clear it, nothing an operator did caused it, and the risk there is a
// malformed document rather than a quiet gap. See Tesseract
// `refusals_belong_where_they_can_be_cleared`, which is a lens for thinking
// with rather than a test to run.
func Undeclared(inst *Instance) []string {
	if inst == nil || inst.Profile == nil {
		return nil
	}
	var out []string
	for _, c := range undeclaredContent(inst) {
		out = append(out, fmt.Sprintf(
			"the %s layout has nowhere to plant spec.%s, which declares %s, so this boot directory carries none",
			inst.Layout.Provider, c.key, quotedNames(c.names)))
	}
	return out
}

// dropped is one collection a tree cannot plant: the manifest key that
// declared it, and the names it declared.
type dropped struct {
	key   string
	names []string
}

// undeclaredContent returns the collections this tree has no directory for,
// in manifest-key order so that two runs report the same way.
//
// The pair of them is the whole set, and it is a list rather than a table
// because each side is read differently: prompts are names on the manifest,
// and subagents arrive on the instance already resolved out of the catalog.
func undeclaredContent(inst *Instance) []dropped {
	var out []dropped

	// Errors are swallowed deliberately, and only here. A manifest that will
	// not decode is the renderer's to refuse — it does, with a diagnostic
	// naming the key — and a report that failed first would replace that
	// message with one about reporting.
	if declared, err := inst.Profile.Spec.Prompts(); err == nil && len(declared) > 0 {
		if strings.TrimSpace(inst.Layout.PromptsDir) == "" {
			out = append(out, dropped{key: profile.SpecKeyPrompts, names: declared})
		}
	}
	if len(inst.Subagents) > 0 && strings.TrimSpace(inst.Layout.SubagentsDir) == "" {
		out = append(out, dropped{key: profile.SpecKeySubagents, names: subagentIDs(inst.Subagents)})
	}
	return out
}
