package slots_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/profile"
	"github.com/hollis-labs/cairn/slots"
	"github.com/hollis-labs/libs/harness/core/agentcontext"
	"github.com/hollis-labs/libs/harness/core/agentcontext/resolvers"
)

// TestASlotThatProducedNothingRendersNothingAtAll is docs/plan.md §5 asserted
// on the bytes of the whole rendering rather than on the absence of a marker.
//
// Three slots go in: one that resolves empty, one that resolves, one that
// fails. One section comes out. The assertion is deliberately on the entire
// string, because a test that only looked for the absence of the old
// "**Unavailable.**" line would pass on a rendering that still emitted a bare
// "## Memory" heading — and a heading with nothing under it is the shape this
// renderer exists to remove.
func TestASlotThatProducedNothingRendersNothingAtAll(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "never-written.md")
	spec := manifest(t, slotted(`[
	  {"name":"tasks",  "section":"## Tasks",  "source":{"kind":"cmd","cmd":{"run":"true"}}},
	  {"name":"repo",   "section":"## Repo",   "source":{"kind":"inline","inline":{"content":"branch=main"}}},
	  {"name":"memory", "section":"## Memory", "source":{"kind":"static_file","static_file":{"path":"`+missing+`"}}}
	]`))

	res, err := slots.Assemble(t.Context(), spec, slots.Options{})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if want := "## Repo\nbranch=main"; res.Rendered != want {
		t.Errorf("the assembly rendered\n%q\nwant only the slot that resolved\n%q", res.Rendered, want)
	}
	// Named individually so a failure says which of the two cases regressed,
	// and so the old marker cannot creep back in under a new spelling.
	for _, absent := range []string{"## Tasks", "## Memory", "never-written.md", "Unavailable"} {
		if strings.Contains(res.Rendered, absent) {
			t.Errorf("the rendering carries %q:\n%s", absent, res.Rendered)
		}
	}

	// The failure is not swallowed, it is redirected: it stays on the result
	// the caller was handed, and `cairn boot` reports it on stderr. It is the
	// operator's to see, and the operator does not read boot.md.
	var failed []string
	for _, s := range res.Slots {
		if s.Err != nil {
			failed = append(failed, s.Name)
		}
	}
	if !slices.Equal(failed, []string{"memory"}) {
		t.Errorf("the result records %v as failed, want just the memory slot", failed)
	}
}

// TestDropUnresolvedDropsWhatAFailedResolverManaged covers a resolver that
// errored after producing something. An earlier revision kept the partial
// content under a marker that said not to trust it. Without the marker there
// is nothing to say so, and half an answer presented as the whole one is worse
// than no section: the agent has no way to tell it is reading a fragment.
func TestDropUnresolvedDropsWhatAFailedResolverManaged(t *testing.T) {
	t.Parallel()

	rendered, _ := slots.DropUnresolved{}.Render([]agentcontext.SlotResult{
		{Name: "partial", Section: "## Partial", Content: "half of it", Err: errors.New("connection reset")},
		{Name: "whole", Section: "## Whole", Content: "all of it"},
	}, agentcontext.Limits{})

	if want := "## Whole\nall of it"; rendered != want {
		t.Errorf("rendered %q, want %q", rendered, want)
	}
}

// TestDropUnresolvedTreatsWhitespaceAsEmpty covers the resolver that succeeded
// and returned a newline. A section holding one blank line is a heading with
// nothing under it wearing a disguise.
func TestDropUnresolvedTreatsWhitespaceAsEmpty(t *testing.T) {
	t.Parallel()

	for _, content := range []string{"", "\n", "   ", " \n\t\n "} {
		rendered, _ := slots.DropUnresolved{}.Render([]agentcontext.SlotResult{
			{Name: "blank", Section: "## Blank", Content: content},
		}, agentcontext.Limits{})
		if rendered != "" {
			t.Errorf("a slot holding %q rendered %q, want nothing", content, rendered)
		}
	}
}

// TestDropUnresolvedDelegatesTheBudget covers the reason this renderer wraps
// the library's rather than replacing it: ordering, truncation and drop
// accounting stay the library's, so what survives the filter is budgeted like
// any other content instead of escaping a limit the operator set.
func TestDropUnresolvedDelegatesTheBudget(t *testing.T) {
	t.Parallel()

	in := []agentcontext.SlotResult{
		{Name: "first", Section: "## First", Content: strings.Repeat("x", 40)},
		{Name: "second", Section: "## Second", Content: strings.Repeat("y", 40)},
	}
	rendered, applied := slots.DropUnresolved{}.Render(in, agentcontext.Limits{MaxBytes: 30})

	if int64(len(rendered)) > 30 {
		t.Errorf("rendered %d bytes past a 30-byte budget:\n%s", len(rendered), rendered)
	}
	if len(applied.TruncatedSlots) == 0 && len(applied.DroppedSlots) == 0 {
		t.Error("the budget was applied without recording a truncation or a drop")
	}
}

// TestDropUnresolvedIsAPassThroughWhenEverySlotResolved pins that this
// renderer changes nothing it does not have to.
func TestDropUnresolvedIsAPassThroughWhenEverySlotResolved(t *testing.T) {
	t.Parallel()

	in := []agentcontext.SlotResult{
		{Name: "a", Section: "## A", Content: "one"},
		{Name: "b", Section: "## B", Content: "two"},
	}
	want, wantApplied := agentcontext.DefaultRenderer{}.Render(in, agentcontext.Limits{})
	got, gotApplied := slots.DropUnresolved{}.Render(in, agentcontext.Limits{})
	if got != want {
		t.Errorf("rendered %q, want the library's own %q", got, want)
	}
	if !reflect.DeepEqual(gotApplied, wantApplied) {
		t.Errorf("applied %+v, want %+v", gotApplied, wantApplied)
	}
}

// TestDropUnresolvedLeavesTheCallersResultsAlone covers the slice the
// dispatcher hands in, which it goes on to return on ContextResult.Slots.
// Filtering in place there would take the failure away from the caller that
// has to report it.
func TestDropUnresolvedLeavesTheCallersResultsAlone(t *testing.T) {
	t.Parallel()

	unreachable := errors.New("unreachable")
	in := []agentcontext.SlotResult{
		{Name: "memory", Section: "## Memory", Content: "half", Err: unreachable},
		{Name: "repo", Section: "## Repo", Content: "branch=main"},
	}
	slots.DropUnresolved{}.Render(in, agentcontext.Limits{})

	if len(in) != 2 {
		t.Fatalf("the caller's slice is now %d long, want 2", len(in))
	}
	if !errors.Is(in[0].Err, unreachable) || in[0].Content != "half" {
		t.Errorf("the caller's failed slot was rewritten to %+v", in[0])
	}
}

// TestTheKindDiagnosticNamesTheKey covers the one mistake this manifest key
// invites. SlotSource's kind field is json:"kind" and yaml:"type"; every boot
// profile in the portfolio is YAML and says type:, and a cairn manifest is
// JSON. The library never sees the manifest, so it can only say the kind is
// unknown — which points at nothing.
func TestTheKindDiagnosticNamesTheKey(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ manifest, wants string }{
		"a slot copied out of a YAML profile": {
			manifest: `[{"name":"note","source":{"type":"inline","inline":{"content":"x"}}}]`,
			wants:    `"kind"`,
		},
		"a slot with no kind at all": {
			manifest: `[{"name":"note","source":{"inline":{"content":"x"}}}]`,
			wants:    `declares no "kind"`,
		},
		"a kind that is not a kind": {
			manifest: `[{"name":"note","source":{"kind":"sqlite"}}]`,
			wants:    `"sqlite"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := slots.Assemble(t.Context(), manifest(t, slotted(tc.manifest)), slots.Options{})
			if !errors.Is(err, slots.ErrSlotKind) {
				t.Fatalf("assemble = %v, want ErrSlotKind", err)
			}
			if !strings.Contains(err.Error(), `slot "note"`) {
				t.Errorf("the error does not name the slot: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the error does not carry %q: %v", tc.wants, err)
			}
		})
	}
}

// TestTheKindDiagnosticListsWhatIsActuallyWired pins the diagnostic's list of
// kinds against the resolver map rather than against the library's constants.
//
// The library defines a skill_index kind that resolvers.Default does not wire,
// so naming it would send an operator to write a slot that then fails. If the
// library starts shipping a default resolver for a kind, this fails here
// instead of leaving the message quietly stale.
func TestTheKindDiagnosticListsWhatIsActuallyWired(t *testing.T) {
	t.Parallel()

	_, err := slots.Assemble(t.Context(),
		manifest(t, slotted(`[{"name":"note","source":{"kind":"sqlite"}}]`)), slots.Options{})
	if err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	for kind := range resolvers.Default() {
		if !strings.Contains(err.Error(), fmt.Sprintf("%q", string(kind))) {
			t.Errorf("kind %q is wired but the diagnostic does not list it: %v", kind, err)
		}
	}
	if strings.Contains(err.Error(), `"skill_index"`) {
		t.Error("the diagnostic lists skill_index, which resolvers.Default does not wire")
	}
}

// slotted wraps a slots array in the manifest object an operator stores.
func slotted(raw string) string {
	return `{"` + profile.SpecKeySlots + `": ` + raw + `}`
}

// TestSectionsRenderOnePerSlot is what makes a template able to place a slot.
// The assembled rendering is one string in the library's own order; a template
// decides both, so each section is wanted on its own, formatted the way the
// assembled one would have formatted it.
func TestSectionsRenderOnePerSlot(t *testing.T) {
	res := &agentcontext.ContextResult{Slots: []agentcontext.SlotResult{
		{Name: "repo", Section: "## Repository", Content: "on branch main"},
		{Name: "memory", Section: "## Memory", Content: "recalled"},
	}}

	got, err := slots.Sections(res)
	if err != nil {
		t.Fatalf("slots.Sections(): %v", err)
	}
	want := map[string]string{
		"repo":   "## Repository\non branch main",
		"memory": "## Memory\nrecalled",
	}
	for name, section := range want {
		if got[name] != section {
			t.Errorf("slots.Sections()[%q] = %q, want %q", name, got[name], section)
		}
	}
}

// TestSectionsDropWhatProducedNothing is docs/plan.md §5 at the level a
// template sees. A slot that failed and one that resolved empty both come back
// as nothing at all — the heading included, which is what keeps a template from
// holding a heading with nothing under it.
func TestSectionsDropWhatProducedNothing(t *testing.T) {
	res := &agentcontext.ContextResult{Slots: []agentcontext.SlotResult{
		{Name: "failed", Section: "## Failed", Content: "partial", Err: errors.New("unreachable")},
		{Name: "empty", Section: "## Empty", Content: ""},
		{Name: "blank", Section: "## Blank", Content: "   \n\t"},
	}}

	got, err := slots.Sections(res)
	if err != nil {
		t.Fatalf("slots.Sections(): %v", err)
	}
	for name := range got {
		if got[name] != "" {
			t.Errorf("slots.Sections()[%q] = %q, want nothing at all", name, got[name])
		}
	}
}

// TestSectionsKeysAreTheDeclaredSlots pins the invariant a caller outside this
// package now reads meaning into: every declared slot gets a key, including the
// ones that produced nothing.
//
// bootdir.Unfilled distinguishes a slot the manifest declared and that then
// filled nothing — worth warning about — from a marker naming a slot nobody
// declared, which is not, and it can only do so because an absent key means
// undeclared and nothing else. Drop a key here for a slot that produced nothing
// and that warning goes silent two stages later with nothing to point at it.
// TestSectionsDropWhatProducedNothing asserts over the keys it finds and so
// would pass on an empty map; this asserts the keys are there.
func TestSectionsKeysAreTheDeclaredSlots(t *testing.T) {
	res := &agentcontext.ContextResult{Slots: []agentcontext.SlotResult{
		{Name: "filled", Section: "## Filled", Content: "content"},
		{Name: "failed", Section: "## Failed", Content: "partial", Err: errors.New("unreachable")},
		{Name: "empty", Section: "## Empty", Content: ""},
		{Name: "blank", Section: "## Blank", Content: "  \n\t"},
		{Name: "sectionless", Content: "bare"},
	}}

	got, err := slots.Sections(res)
	if err != nil {
		t.Fatalf("slots.Sections(): %v", err)
	}
	for _, name := range []string{"filled", "failed", "empty", "blank", "sectionless"} {
		if _, declared := got[name]; !declared {
			t.Errorf("slots.Sections() has no key for the declared slot %q: %v", name, got)
		}
	}
	if _, declared := got["never-declared"]; declared {
		t.Error("slots.Sections() invented a key for a slot the manifest did not declare")
	}
	if len(got) != 5 {
		t.Errorf("slots.Sections() returned %d keys for 5 declared slots: %v", len(got), got)
	}
}

// TestSectionsRefuseTwoSlotsOfOneName covers the ambiguity a template makes
// reachable. A marker addresses a slot by name, so a repeated name names two
// sections and cairn cannot say which the marker meant.
func TestSectionsRefuseTwoSlotsOfOneName(t *testing.T) {
	res := &agentcontext.ContextResult{Slots: []agentcontext.SlotResult{
		{Name: "repo", Section: "## First", Content: "a"},
		{Name: "repo", Section: "## Second", Content: "b"},
	}}

	if _, err := slots.Sections(res); !errors.Is(err, slots.ErrSlotName) {
		t.Fatalf("slots.Sections() = %v, want slots.ErrSlotName", err)
	}
}

// TestSectionsOfNothing covers a profile that declared no slots at all: an
// empty map, so a template's markers substitute away rather than the caller
// having to check.
func TestSectionsOfNothing(t *testing.T) {
	got, err := slots.Sections(nil)
	if err != nil {
		t.Fatalf("slots.Sections(nil): %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("slots.Sections(nil) = %v, want an empty map", got)
	}
}

// TestSectionsRenderBareContentWhenNoSectionIsDeclared is what makes a slot
// usable as a substitution value rather than only as a list entry.
//
// The library falls back to the slot's name as the heading, which was right
// when the assembled output was a list of sections and every slot wanted one.
// In a template the template supplies the structure: a slot carrying prose with
// its own headings inside it would otherwise get a bare "role" line above it,
// which is a heading nobody declared.
func TestSectionsRenderBareContentWhenNoSectionIsDeclared(t *testing.T) {
	res := &agentcontext.ContextResult{Slots: []agentcontext.SlotResult{
		{Name: "role", Content: "# Engineer\n\nYou implement one task.\n"},
		{Name: "repo", Section: "## Repository", Content: "on branch main"},
	}}

	got, err := slots.Sections(res)
	if err != nil {
		t.Fatalf("Sections(): %v", err)
	}
	if want := "# Engineer\n\nYou implement one task."; got["role"] != want {
		t.Errorf("Sections()[\"role\"] = %q, want the content and no heading", got["role"])
	}
	if strings.Contains(got["role"], "\nrole\n") || strings.HasPrefix(got["role"], "role") {
		t.Errorf("Sections()[\"role\"] carries the slot name as a heading: %q", got["role"])
	}
	// A declared section still renders exactly as it did, heading included, so
	// it still vanishes with the content it heads.
	if want := "## Repository\non branch main"; got["repo"] != want {
		t.Errorf("Sections()[\"repo\"] = %q, want %q", got["repo"], want)
	}
}

// TestASectionlessSlotThatProducedNothingStillRendersNothing keeps the (A)
// property intact for the new shape: dropping the heading must not drop the
// rule that came with it.
func TestASectionlessSlotThatProducedNothingStillRendersNothing(t *testing.T) {
	res := &agentcontext.ContextResult{Slots: []agentcontext.SlotResult{
		{Name: "failed", Content: "partial", Err: errors.New("unreachable")},
		{Name: "empty", Content: "  \n"},
	}}

	got, err := slots.Sections(res)
	if err != nil {
		t.Fatalf("Sections(): %v", err)
	}
	for name, section := range got {
		if section != "" {
			t.Errorf("Sections()[%q] = %q, want nothing at all", name, section)
		}
	}
}

// TestDeterministicAssemblyResolvesOnlyWhatACheckCanSurvive covers the rule the
// installed layer runs under. A check re-renders and diffs against disk, so a
// source whose value can differ between two renders of one profile would report
// drift on every run.
func TestDeterministicAssemblyResolvesOnlyWhatACheckCanSurvive(t *testing.T) {
	spec := profile.Spec{profile.SpecKeySlots: []byte(`[
		{"name":"prose","source":{"kind":"inline","inline":{"content":"shared prose"}}},
		{"name":"repo", "source":{"kind":"cmd","cmd":{"run":"git status"}}},
		{"name":"memory","source":{"kind":"http_json","http_json":{"url":"http://x/recall"}}}
	]`)}

	res, err := slots.Assemble(context.Background(), spec, slots.Options{Deterministic: true})
	if err != nil {
		t.Fatalf("Assemble(): %v", err)
	}
	sections, err := slots.Sections(res)
	if err != nil {
		t.Fatalf("Sections(): %v", err)
	}
	if sections["prose"] != "shared prose" {
		t.Errorf("the inline slot resolved to %q, want its content", sections["prose"])
	}
	for _, name := range []string{"repo", "memory"} {
		if sections[name] != "" {
			t.Errorf("the %s slot resolved to %q in a deterministic assembly", name, sections[name])
		}
	}

	// And the caller can say which ones it left alone, rather than the operator
	// meeting one puzzling empty section per marker.
	skipped, err := slots.Nondeterministic(spec)
	if err != nil {
		t.Fatalf("Nondeterministic(): %v", err)
	}
	if len(skipped) != 2 {
		t.Fatalf("Nondeterministic() = %v, want the two non-static slots", skipped)
	}
	for _, want := range []string{`"repo" (cmd)`, `"memory" (http_json)`} {
		if !slices.Contains(skipped, want) {
			t.Errorf("Nondeterministic() = %v, does not name %s", skipped, want)
		}
	}
}

// TestDeterministicKindsAreTheOnesWhoseAnswerTheOperatorControls pins the list
// itself. A kind added to it that reads live state would make every check
// report drift; one removed that reads a file would make an installed template
// useless again.
func TestDeterministicKindsAreTheOnesWhoseAnswerTheOperatorControls(t *testing.T) {
	got := slots.DeterministicKinds()
	want := []agentcontext.SlotSourceKind{
		agentcontext.SlotSourceKindStaticFile,
		agentcontext.SlotSourceKindStaticDir,
		agentcontext.SlotSourceKindInline,
		agentcontext.SlotSourceKindRoleSummary,
	}
	if !slices.Equal(got, want) {
		t.Errorf("DeterministicKinds() = %v, want %v", got, want)
	}
	for _, absent := range []agentcontext.SlotSourceKind{
		agentcontext.SlotSourceKindCmd,
		agentcontext.SlotSourceKindHTTPText,
		agentcontext.SlotSourceKindHTTPJSON,
	} {
		if slices.Contains(got, absent) {
			t.Errorf("DeterministicKinds() carries %q, which reads live state", absent)
		}
	}
}
