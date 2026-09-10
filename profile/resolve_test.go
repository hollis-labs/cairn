package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// errFakeNotFound stands in for the catalog's ErrProfileNotFound. The cascade
// must propagate whatever its loader reports, so the tests use a sentinel of
// their own rather than depending on package catalog.
var errFakeNotFound = errors.New("fake loader: no such profile")

// fakeLoader is an in-memory [Loader]: the profiles a test declares, by id.
type fakeLoader map[string]*Profile

func (f fakeLoader) Profile(_ context.Context, id string) (*Profile, error) {
	p, ok := f[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errFakeNotFound, id)
	}
	return p, nil
}

// nilLoader violates the [Loader] contract by reporting neither a profile nor
// an error.
type nilLoader struct{}

func (nilLoader) Profile(context.Context, string) (*Profile, error) { return nil, nil }

// ctxKey is the type of the value the context-propagation test plants.
type ctxKey struct{}

// ctxLoader records the context value it is called with, so a test can prove
// the caller's context reaches every load rather than being dropped.
type ctxLoader struct {
	inner fakeLoader
	saw   []string
}

func (c *ctxLoader) Profile(ctx context.Context, id string) (*Profile, error) {
	v, _ := ctx.Value(ctxKey{}).(string)
	c.saw = append(c.saw, v)
	return c.inner.Profile(ctx, id)
}

func spec(t *testing.T, kv map[string]string) Spec {
	t.Helper()
	s := Spec{}
	for k, v := range kv {
		if !json.Valid([]byte(v)) {
			t.Fatalf("spec key %q: fixture is not valid JSON: %s", k, v)
		}
		s[k] = json.RawMessage(v)
	}
	return s
}

func resolveOK(t *testing.T, l Loader, id string) *Resolved {
	t.Helper()
	got, err := Resolve(t.Context(), l, id)
	if err != nil {
		t.Fatalf("Resolve(%q) = error %v, want no error", id, err)
	}
	return got
}

func TestResolveSingleProfile(t *testing.T) {
	t.Parallel()

	l := fakeLoader{"solo": {
		ID:          "solo",
		Name:        "Solo",
		Description: "stands alone",
		Provider:    ProviderClaude,
		Model:       "opus",
		Body:        "the body",
		Spec:        spec(t, map[string]string{"skills": `["review"]`}),
	}}

	got := resolveOK(t, l, "solo")

	if got.ID != "solo" {
		t.Errorf("ID = %q, want %q", got.ID, "solo")
	}
	if !slices.Equal(got.Chain, []string{"solo"}) {
		t.Errorf("Chain = %v, want [solo]", got.Chain)
	}
	if got.Name != "Solo" || got.Description != "stands alone" {
		t.Errorf("Name/Description = %q/%q", got.Name, got.Description)
	}
	if got.Provider != ProviderClaude || got.Model != "opus" {
		t.Errorf("Provider/Model = %q/%q", got.Provider, got.Model)
	}
	if len(got.Bodies) != 1 || got.Bodies[0].ID != "solo" || got.Bodies[0].Text != "the body" {
		t.Errorf("Bodies = %+v, want one body of solo's", got.Bodies)
	}
	if string(got.Spec["skills"]) != `["review"]` {
		t.Errorf("Spec[skills] = %s", got.Spec["skills"])
	}
}

func TestResolveClosestWinsPerField(t *testing.T) {
	t.Parallel()

	// Each field is declared at a different depth, and two of them are
	// declared twice, so every assertion below distinguishes closest-wins from
	// root-wins and from leaf-only.
	l := fakeLoader{
		"root": {
			ID: "root", Name: "root name", Description: "root desc",
			Provider: ProviderCodex, Model: "root model",
		},
		"mid": {
			ID: "mid", Extends: "root",
			Description: "mid desc", Model: "mid model",
		},
		"leaf": {
			ID: "leaf", Extends: "mid",
			Model: "leaf model",
		},
	}

	got := resolveOK(t, l, "leaf")

	if !slices.Equal(got.Chain, []string{"root", "mid", "leaf"}) {
		t.Errorf("Chain = %v, want [root mid leaf]", got.Chain)
	}
	for _, tc := range []struct {
		field, got, want string
	}{
		{"Name", got.Name, "root name"},              // declared only at the root
		{"Description", got.Description, "mid desc"}, // root and mid; mid is closer
		{"Provider", got.Provider.String(), "codex"}, // declared only at the root
		{"Model", got.Model, "leaf model"},           // all three; the leaf is closest
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if got.ID != "leaf" {
		t.Errorf("ID = %q, want leaf", got.ID)
	}
}

func TestResolveEmptyFieldInheritsRatherThanBlanking(t *testing.T) {
	t.Parallel()

	l := fakeLoader{
		"root": {
			ID: "root", Name: "root name", Description: "root desc",
			Provider: ProviderClaude, Model: "root model",
			Spec: spec(t, map[string]string{"skills": `["review"]`}),
		},
		// Declares nothing at all: an empty field means "not declared".
		"leaf": {ID: "leaf", Extends: "root"},
	}

	got := resolveOK(t, l, "leaf")

	if got.Name != "root name" || got.Description != "root desc" {
		t.Errorf("Name/Description = %q/%q, want the root's", got.Name, got.Description)
	}
	if got.Provider != ProviderClaude || got.Model != "root model" {
		t.Errorf("Provider/Model = %q/%q, want the root's", got.Provider, got.Model)
	}
	if string(got.Spec["skills"]) != `["review"]` {
		t.Errorf("Spec[skills] = %s, want the root's", got.Spec["skills"])
	}
}

// TestResolveUnkeyedSpecKeyWinsWhole covers the half of the cascade that did
// not change: a key that is not a keyed collection is taken whole, unread, and
// the ancestor's value is gone.
//
// The three keys are chosen to cover the shapes a merge rule could plausibly
// be inferred from and must not be: an object (spec.subagent), a scalar, and a
// list of strings nested inside an opaque object (subagent's "tools"), which
// is the exact shape spec.skills has and composes by.
func TestResolveUnkeyedSpecKeyWinsWhole(t *testing.T) {
	t.Parallel()

	l := fakeLoader{
		"root": {ID: "root", Spec: spec(t, map[string]string{
			"subagent":   `{"description":"root","tools":["Read","Grep"],"kept":1}`,
			"skills_dir": `"/root/skills"`,
			"files":      `{"root.md":"root"}`,
		})},
		"leaf": {ID: "leaf", Extends: "root", Spec: spec(t, map[string]string{
			"subagent":   `{"tools":["Write"]}`,
			"skills_dir": `"/leaf/skills"`,
		})},
	}

	got := resolveOK(t, l, "leaf")

	// The object is replaced, not merged, and the list inside it is replaced,
	// not unioned. spec.subagent is opaque and stays opaque.
	if s := string(got.Spec[SpecKeySubagent]); s != `{"tools":["Write"]}` {
		t.Errorf("Spec[subagent] = %s, want {\"tools\":[\"Write\"]} — an unkeyed object must not be merged in", s)
	}
	if s := string(got.Spec[SpecKeySkillsDir]); s != `"/leaf/skills"` {
		t.Errorf("Spec[skills_dir] = %s, want the leaf's", s)
	}
	// A key the leaf does not declare is inherited whole.
	if s := string(got.Spec[SpecKeyFiles]); s != `{"root.md":"root"}` {
		t.Errorf("Spec[files] = %s, want the root's", s)
	}

	// The stored profiles are untouched, and the merged map is the resolved
	// profile's own.
	got.Spec["scribbled"] = json.RawMessage(`true`)
	if _, ok := l["leaf"].Spec["scribbled"]; ok {
		t.Error("writing to Resolved.Spec reached the stored profile's Spec")
	}
	if s := string(l["root"].Spec[SpecKeySubagent]); s != `{"description":"root","tools":["Read","Grep"],"kept":1}` {
		t.Errorf("the root profile's Spec[subagent] changed to %s", s)
	}
}

func TestResolveUnknownSpecKeyCascadesLikeAKnownOne(t *testing.T) {
	t.Parallel()

	const carried = `{"nested":{"deep":[1,2,{"three":true}]},"unicode":"café"}`

	l := fakeLoader{
		"root": {ID: "root", Spec: spec(t, map[string]string{
			"skills_dir":   `"/root/skills"`,
			"telemetry":    `{"sink":"root","kept":true}`,
			"root_only":    carried,
			"also_unknown": `["a","b"]`,
		})},
		"mid": {ID: "mid", Extends: "root"},
		"leaf": {ID: "leaf", Extends: "mid", Spec: spec(t, map[string]string{
			"skills_dir":   `"/leaf/skills"`,
			"telemetry":    `{"sink":"leaf"}`,
			"also_unknown": `["c"]`,
		})},
	}

	got := resolveOK(t, l, "leaf")

	// The unknown key behaves exactly like the unkeyed rendered one beside it:
	// taken whole, never looked inside.
	if s := string(got.Spec["telemetry"]); s != `{"sink":"leaf"}` {
		t.Errorf("Spec[telemetry] = %s, want the leaf's — an unknown object must not be merged", s)
	}
	if s := string(got.Spec[SpecKeySkillsDir]); s != `"/leaf/skills"` {
		t.Errorf("Spec[skills_dir] = %s, want the leaf's", s)
	}
	// A list of strings is the shape spec.skills composes by. An unknown key
	// wearing it is still replaced, because what is keyed is a table and not a
	// guess about JSON.
	if s := string(got.Spec["also_unknown"]); s != `["c"]` {
		t.Errorf("Spec[also_unknown] = %s, want [\"c\"] — an unknown list must not be unioned", s)
	}
	// An unknown key nothing overrides survives the walk byte for byte.
	if s := string(got.Spec["root_only"]); s != carried {
		t.Errorf("Spec[root_only] = %s, want %s", s, carried)
	}
	if len(got.Spec) != 4 {
		t.Errorf("Spec has %d keys (%v), want 4", len(got.Spec), specKeys(got.Spec))
	}
}

func TestResolveSpecKeySetToNullOverrides(t *testing.T) {
	t.Parallel()

	l := fakeLoader{
		"root": {ID: "root", Spec: spec(t, map[string]string{
			"slots": `[{"name":"role","source":{"kind":"inline","inline":{"content":"hi"}}}]`,
		})},
		"leaf": {ID: "leaf", Extends: "root", Spec: spec(t, map[string]string{"slots": `null`})},
	}

	got := resolveOK(t, l, "leaf")

	if s := string(got.Spec[SpecKeySlots]); s != "null" {
		t.Errorf("Spec[slots] = %s, want null", s)
	}
	slots, err := got.Spec.Slots()
	if err != nil {
		t.Fatalf("Slots() = error %v", err)
	}
	if len(slots) != 0 {
		t.Errorf("Slots() = %v, want none", slots)
	}
}

func TestResolveSpecIsNeverNil(t *testing.T) {
	t.Parallel()

	l := fakeLoader{
		"root": {ID: "root"},
		"leaf": {ID: "leaf", Extends: "root"},
	}

	got := resolveOK(t, l, "leaf")

	if got.Spec == nil {
		t.Fatal("Spec is nil for a chain that declares no manifest")
	}
	if len(got.Spec) != 0 {
		t.Errorf("Spec = %v, want empty", got.Spec)
	}
}

// TestResolveBodiesCarryTheChainInFoldOrder pins that a body is carried
// rather than composed: every declared body arrives in fold order, tagged with
// the profile that wrote it, and nothing here decides what composing them
// means.
//
// It replaces a test that pinned a concatenation. The concatenation was real
// and was never rendered by anything — see profile.Resolved.Bodies — so what
// it pinned was the shape of a value cairn computed and dropped. Composing
// bodies is the render engine's, and it composes them by section rather than
// by joining them: see package template.
func TestResolveBodiesCarryTheChainInFoldOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		bodies []string // root first
		want   []Body   // fold order
	}{
		{
			name:   "ancestor first",
			bodies: []string{"root body", "mid body", "leaf body"},
			want: []Body{
				{ID: "p0", Text: "root body"},
				{ID: "p1", Text: "mid body"},
				{ID: "p2", Text: "leaf body"},
			},
		},
		{
			name:   "a profile that declared none contributes nothing",
			bodies: []string{"root body", "", "leaf body"},
			want:   []Body{{ID: "p0", Text: "root body"}, {ID: "p2", Text: "leaf body"}},
		},
		{
			name:   "a whitespace-only body counts as none",
			bodies: []string{"root body", "  \n\t ", "leaf body"},
			want:   []Body{{ID: "p0", Text: "root body"}, {ID: "p2", Text: "leaf body"}},
		},
		{
			name:   "an all-empty chain carries no body at all",
			bodies: []string{"", "", ""},
			want:   nil,
		},
		{
			name:   "surrounding blank lines are the file's formatting",
			bodies: []string{"\n\nroot body\n\n\n", "\nmid body\n", "leaf body\n"},
			want: []Body{
				{ID: "p0", Text: "root body"},
				{ID: "p1", Text: "mid body"},
				{ID: "p2", Text: "leaf body"},
			},
		},
		{
			name:   "blank lines inside a body are the author's content",
			bodies: []string{"root para\n\nroot para two", "leaf body"},
			want: []Body{
				{ID: "p0", Text: "root para\n\nroot para two"},
				{ID: "p1", Text: "leaf body"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			l := fakeLoader{}
			var prev string
			var leaf string
			for i, body := range tc.bodies {
				id := fmt.Sprintf("p%d", i)
				l[id] = &Profile{ID: id, Extends: prev, Body: body}
				prev, leaf = id, id
			}

			got := resolveOK(t, l, leaf)

			if !slices.Equal(got.Bodies, tc.want) {
				t.Errorf("Bodies = %+v, want %+v", got.Bodies, tc.want)
			}
		})
	}
}

func TestResolveAbstractIsTheLeafsOwn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                       string
		rootAbstract, leafAbstract bool
		want                       bool
	}{
		{name: "abstract root, concrete leaf", rootAbstract: true, leafAbstract: false, want: false},
		{name: "concrete root, abstract leaf", rootAbstract: false, leafAbstract: true, want: true},
		{name: "both abstract", rootAbstract: true, leafAbstract: true, want: true},
		{name: "neither abstract", rootAbstract: false, leafAbstract: false, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			l := fakeLoader{
				"root": {ID: "root", Abstract: tc.rootAbstract},
				"leaf": {ID: "leaf", Extends: "root", Abstract: tc.leafAbstract},
			}

			// Resolving an abstract profile is never itself an error: install
			// resolves one, and only a direct boot has reason to object.
			got := resolveOK(t, l, "leaf")

			if got.Abstract != tc.want {
				t.Errorf("Abstract = %v, want %v", got.Abstract, tc.want)
			}
		})
	}
}

func TestResolveAbstractLeafResolvesWithoutError(t *testing.T) {
	t.Parallel()

	l := fakeLoader{"base": {ID: "base", Abstract: true, Name: "Base", Body: "shared"}}

	got := resolveOK(t, l, "base")

	if !got.Abstract {
		t.Error("Abstract = false, want true")
	}
	if len(got.Bodies) != 1 || got.Bodies[0].Text != "shared" || got.Name != "Base" {
		t.Errorf("Bodies/Name = %+v/%q, want [shared]/Base", got.Bodies, got.Name)
	}
}

func TestResolveCycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		loader  fakeLoader
		id      string
		wantIDs string
	}{
		{
			name:    "a profile extending itself",
			loader:  fakeLoader{"ouroboros": {ID: "ouroboros", Extends: "ouroboros"}},
			id:      "ouroboros",
			wantIDs: "ouroboros -> ouroboros",
		},
		{
			name: "a three profile loop",
			loader: fakeLoader{
				"a": {ID: "a", Extends: "b"},
				"b": {ID: "b", Extends: "c"},
				"c": {ID: "c", Extends: "a"},
			},
			id:      "a",
			wantIDs: "a -> b -> c -> a",
		},
		{
			name: "a chain that walks into a loop",
			loader: fakeLoader{
				"leaf": {ID: "leaf", Extends: "a"},
				"a":    {ID: "a", Extends: "b"},
				"b":    {ID: "b", Extends: "a"},
			},
			id:      "leaf",
			wantIDs: "leaf -> a -> b -> a",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Resolve(t.Context(), tc.loader, tc.id)

			if got != nil {
				t.Errorf("Resolve returned %+v, want nil alongside the error", got)
			}
			if !errors.Is(err, ErrCycle) {
				t.Fatalf("Resolve error = %v, want one wrapping ErrCycle", err)
			}
			if !strings.Contains(err.Error(), tc.wantIDs) {
				t.Errorf("Resolve error = %q, want it to name the walk order %q", err, tc.wantIDs)
			}
		})
	}
}

func TestResolveMissingAncestorNamesTheReferencingProfile(t *testing.T) {
	t.Parallel()

	l := fakeLoader{
		"leaf": {ID: "leaf", Extends: "mid"},
		"mid":  {ID: "mid", Extends: "ghost"},
	}

	_, err := Resolve(t.Context(), l, "leaf")

	if !errors.Is(err, errFakeNotFound) {
		t.Fatalf("Resolve error = %v, want the loader's error propagated", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"ghost"`) {
		t.Errorf("Resolve error = %q, want it to name the missing profile", msg)
	}
	if !strings.Contains(msg, `"mid"`) {
		t.Errorf("Resolve error = %q, want it to name the profile that referenced it", msg)
	}
}

func TestResolveMissingLeaf(t *testing.T) {
	t.Parallel()

	_, err := Resolve(t.Context(), fakeLoader{}, "absent")

	if !errors.Is(err, errFakeNotFound) {
		t.Fatalf("Resolve error = %v, want the loader's error propagated", err)
	}
	if !strings.Contains(err.Error(), `"absent"`) {
		t.Errorf("Resolve error = %q, want it to name the profile asked for", err)
	}
	// Nothing referenced the leaf, so there is nobody to blame for it.
	if strings.Contains(err.Error(), "extended by") {
		t.Errorf("Resolve error = %q, want no referencing profile named", err)
	}
}

func TestResolveNilLoader(t *testing.T) {
	t.Parallel()

	_, err := Resolve(t.Context(), nil, "leaf")

	if !errors.Is(err, ErrNilLoader) {
		t.Fatalf("Resolve error = %v, want one wrapping ErrNilLoader", err)
	}
}

func TestResolveLoaderReturningNothing(t *testing.T) {
	t.Parallel()

	_, err := Resolve(t.Context(), nilLoader{}, "leaf")

	if !errors.Is(err, ErrNilProfile) {
		t.Fatalf("Resolve error = %v, want one wrapping ErrNilProfile", err)
	}
}

func TestResolvePassesTheCallersContext(t *testing.T) {
	t.Parallel()

	l := &ctxLoader{inner: fakeLoader{
		"root": {ID: "root"},
		"leaf": {ID: "leaf", Extends: "root"},
	}}
	ctx := context.WithValue(t.Context(), ctxKey{}, "carried")

	if _, err := Resolve(ctx, l, "leaf"); err != nil {
		t.Fatalf("Resolve = error %v", err)
	}

	if !slices.Equal(l.saw, []string{"carried", "carried"}) {
		t.Errorf("loader saw context values %v, want the caller's on every load", l.saw)
	}
}

func TestResolveDeepChain(t *testing.T) {
	t.Parallel()

	// The walk is bounded by the cycle check alone, so a chain longer than any
	// arbitrary limit resolves.
	const depth = 500

	l := fakeLoader{}
	for i := range depth {
		id := fmt.Sprintf("p%d", i)
		var extends string
		if i > 0 {
			extends = fmt.Sprintf("p%d", i-1)
		}
		l[id] = &Profile{ID: id, Extends: extends, Model: id}
	}
	leaf := fmt.Sprintf("p%d", depth-1)

	got := resolveOK(t, l, leaf)

	if len(got.Chain) != depth {
		t.Errorf("Chain has %d ids, want %d", len(got.Chain), depth)
	}
	if got.Chain[0] != "p0" || got.Chain[depth-1] != leaf {
		t.Errorf("Chain runs %q..%q, want p0..%s", got.Chain[0], got.Chain[depth-1], leaf)
	}
	if got.Model != leaf {
		t.Errorf("Model = %q, want the leaf's %q", got.Model, leaf)
	}
}

// specKeys returns s's keys sorted, for a readable failure message.
func specKeys(s Spec) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// TestResolveCompositionFoldsPartsAfterTheChain covers the whole of what a
// composition is: one fold over the target's chain followed by each part's,
// under the rules the extends cascade already had.
func TestResolveCompositionFoldsPartsAfterTheChain(t *testing.T) {
	ctx := context.Background()
	l := fakeLoader{
		"base": {ID: "base", Abstract: true, Name: "Base", Provider: "claude", Model: "opus",
			Body: "base prose", Spec: spec(t, map[string]string{"skills": `["base-skill"]`})},
		"engineer": {ID: "engineer", Extends: "base", Name: "Engineer", Model: "sonnet",
			Body: "engineer prose", Spec: spec(t, map[string]string{"skills": `["code-review"]`})},
		"docs-only": {ID: "docs-only", Name: "Docs only",
			Spec: spec(t, map[string]string{"skills": `["docs-review"]`})},
		// A part that is abstract and extends something, so both of the
		// properties a part is allowed to have are exercised at once.
		"fragment": {ID: "fragment", Extends: "docs-only", Abstract: true,
			Spec: spec(t, map[string]string{"subagents": `["reviewer"]`})},
	}

	got, err := ResolveComposition(ctx, l, "engineer", []string{"docs-only", "fragment"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// The chain is the fold order: the target's, then whatever each part adds
	// to it. docs-only appears once — the second chain reaches it again, and a
	// profile already folded is not folded a second time.
	want := []string{"base", "engineer", "docs-only", "fragment"}
	if !slices.Equal(got.Chain, want) {
		t.Errorf("the chain is %v, want %v", got.Chain, want)
	}
	if got.ID != "engineer" {
		t.Errorf("the composition resolved as %q, want the target it was booted by", got.ID)
	}
	// The keyed collection unions across every contributor, which is the
	// cascade's own rule and not a second one.
	if s := string(got.Spec["skills"]); s != `["base-skill","code-review","docs-review"]` {
		t.Errorf("the composed skills are %s", s)
	}
	// Abstract is the target's leaf. A part exists to be merged rather than
	// booted, so letting one decide this would refuse the case composition
	// exists for.
	if got.Abstract {
		t.Error("an abstract part made the composition abstract")
	}
	// Every declared body arrives in fold order, the chain's ahead of the
	// parts', each tagged with the profile that wrote it. What a later body
	// does to an earlier one is the render engine's question and not this
	// one's.
	if len(got.Bodies) < 2 || got.Bodies[0].Text != "base prose" || got.Bodies[1].Text != "engineer prose" {
		t.Errorf("Bodies = %+v, want base's then engineer's first", got.Bodies)
	}
}

// TestResolveCompositionNamesThePartThatFailed pins that a part which cannot
// be walked is reported as a part rather than as the profile being booted.
func TestResolveCompositionNamesThePartThatFailed(t *testing.T) {
	l := fakeLoader{"engineer": {ID: "engineer"}}
	_, err := ResolveComposition(context.Background(), l, "engineer", []string{"nope"})
	if !errors.Is(err, errFakeNotFound) {
		t.Fatalf("a part that is not there resolved: %v", err)
	}
	for _, want := range []string{`"engineer"`, `"nope"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not name %s: %v", want, err)
		}
	}
}

// TestResolveIsACompositionWithNoParts pins that the plain resolution is the
// composition's own path and not a second one beside it.
func TestResolveIsACompositionWithNoParts(t *testing.T) {
	ctx := context.Background()
	l := fakeLoader{
		"base":     {ID: "base", Abstract: true, Spec: spec(t, map[string]string{"skills": `["a"]`})},
		"engineer": {ID: "engineer", Extends: "base", Spec: spec(t, map[string]string{"skills": `["b"]`})},
	}
	plain, err := Resolve(ctx, l, "engineer")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	composed, err := ResolveComposition(ctx, l, "engineer", nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !slices.Equal(plain.Chain, composed.Chain) ||
		string(plain.Spec["skills"]) != string(composed.Spec["skills"]) {
		t.Errorf("resolving with no parts differs from Resolve: %+v and %+v", plain, composed)
	}
}

// TestAPartDoesNotRevertWhatTheTargetOverrode is the reproduction, asserted on
// the two fields it silently changed.
//
// base is abstract and declares a name and a model; target extends base and
// overrides both; part extends base and says nothing about either. Folding
// part's whole chain put base back in front of target, so adding a part that
// mentions neither field changed both — the operator asked for one thing to be
// added and two unrelated things reverted.
//
// The rule that prevents it is that no profile is ever folded after one of its
// own descendants. This is the test that fails if somebody reads the skip in
// ResolveComposition as an optimization and removes it.
func TestAPartDoesNotRevertWhatTheTargetOverrode(t *testing.T) {
	ctx := context.Background()
	l := fakeLoader{
		"base":   {ID: "base", Abstract: true, Name: "BASE NAME", Model: "base-model"},
		"target": {ID: "target", Extends: "base", Name: "TARGET NAME", Model: "target-model"},
		"part":   {ID: "part", Extends: "base", Description: "what the part adds"},
	}

	plain, err := Resolve(ctx, l, "target")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	composed, err := ResolveComposition(ctx, l, "target", []string{"part"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if composed.Name != plain.Name || composed.Model != plain.Model {
		t.Errorf("composing a part changed the target's own overrides: name %q -> %q, model %q -> %q",
			plain.Name, composed.Name, plain.Model, composed.Model)
	}
	if composed.Name != "TARGET NAME" || composed.Model != "target-model" {
		t.Errorf("the composition resolved to name %q and model %q", composed.Name, composed.Model)
	}
	// What the part does add still arrives — the skip drops the ancestor that
	// was already folded, never the part itself.
	if composed.Description != "what the part adds" {
		t.Errorf("the part contributed nothing: description is %q", composed.Description)
	}
	// And the shared ancestor is in the chain once, at the position its own
	// descendant put it.
	if want := []string{"base", "target", "part"}; !slices.Equal(composed.Chain, want) {
		t.Errorf("the chain is %v, want %v", composed.Chain, want)
	}
}

// TestAPartDoesNotRevertWhatAnEarlierPartOverrode is the same inversion one
// step over, and the reason the skip is cumulative rather than against the
// target's chain alone.
//
// Two parts share an ancestor with each other and not with the target. Skipping
// only what the target's chain folded would put that ancestor between the two
// parts, reverting whatever the first one overrode — the identical failure, in
// the case that does not get reported first.
func TestAPartDoesNotRevertWhatAnEarlierPartOverrode(t *testing.T) {
	ctx := context.Background()
	l := fakeLoader{
		"target": {ID: "target", Name: "TARGET NAME"},
		"shared": {ID: "shared", Abstract: true, Model: "shared-model",
			Spec: spec(t, map[string]string{"skills": `["shared-skill"]`})},
		"first":  {ID: "first", Extends: "shared", Model: "first-model"},
		"second": {ID: "second", Extends: "shared", Description: "the second part"},
	}

	got, err := ResolveComposition(ctx, l, "target", []string{"first", "second"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Model != "first-model" {
		t.Errorf("the model is %q, want the first part's override to stand", got.Model)
	}
	if want := []string{"target", "shared", "first", "second"}; !slices.Equal(got.Chain, want) {
		t.Errorf("the chain is %v, want %v", got.Chain, want)
	}
	// The shared ancestor's own contribution is in the composition once, from
	// where the first part folded it.
	if s := string(got.Spec["skills"]); s != `["shared-skill"]` {
		t.Errorf("the composed skills are %s", s)
	}
}
