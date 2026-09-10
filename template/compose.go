package template

import (
	"errors"
	"fmt"
	"strings"
)

// ErrLayout reports a layout a chain names and the bundle does not hold, or a
// layout chain that arrives back at a layout it already walked.
var ErrLayout = errors.New("layout not found")

// ErrLayoutCycle reports a layout chain that closed a loop. It is separate
// from [ErrLayout] because a missing layout is an authoring mistake in one
// file and a cycle is a mistake across several.
var ErrLayoutCycle = errors.New("layout extends cycle")

// LayoutLoader is what a chain's `{{ extends }}` is resolved through: one
// layout by name, parsed.
//
// It is an interface for the reason [github.com/chrispian/cairn/profile.Loader]
// is one: this package reads nothing off disk, so where a layout comes from —
// a bundle directory, a test's map, one day something else — is the caller's.
type LayoutLoader interface {
	Layout(name string) (*Document, error)
}

// LayoutFunc adapts a function to [LayoutLoader].
type LayoutFunc func(name string) (*Document, error)

// Layout implements [LayoutLoader].
func (f LayoutFunc) Layout(name string) (*Document, error) { return f(name) }

// maxLayoutDepth bounds a layout chain. It is a backstop and not a design
// limit: the cycle guard below catches a loop, and this catches a chain long
// enough that walking it is the symptom rather than the cause.
const maxLayoutDepth = 32

// Frame is what a render actually renders: the outermost document of a chain,
// and every document whose sections fill it, weakest first.
//
// The two are separated because they are two questions. Which document
// supplies the shape is one answer; which documents supply the content is a
// list, and the list is longer than the chain of files — a layout may carry a
// default section, and a composed part may override one.
type Frame struct {
	// Render is the document whose nodes are walked. It is the outermost
	// layout of the chain, or the rendering profile itself when nothing
	// extends anything.
	Render *Document

	// Sections are the documents whose `{{ section }}` blocks fill it,
	// weakest first: the layouts outermost to innermost, then the profile
	// chain ancestor-first, then whatever a composition added.
	Sections []*Document
}

// Compose resolves a profile chain and its layouts into one [Frame].
//
// docs is the profile cascade, ancestor-first and ending with whatever a
// composition added — exactly [github.com/chrispian/cairn/profile.Resolved]'s
// own fold order. A nil member is skipped, because a profile with no body is
// an ordinary member of a cascade.
//
// # Which document renders
//
// The closest document that declares `{{ extends }}`; failing that, the
// closest document with content OUTSIDE its sections; failing that, none, and
// the render produces nothing.
//
// Every clause is closest-wins, which is the only rule cairn's cascade has.
// The first exists because `{{ extends }}` is a document saying "I am the
// shape of a whole file". The second because a document without it is content
// that still has to land somewhere.
//
// The third is the one that earns its keep, and it is what a FRAGMENT is: a
// document holding nothing but section declarations declares content and no
// shape, so it is never the frame. Without that clause a composed part —
// `--with docs-only`, whose whole job is to contribute one section — would
// become the document, because it is closer than the profile being booted and
// closest-wins would hand it the shape. A part that means to supply the shape
// says so with `{{ extends }}`.
//
// The consequence worth stating is the one that changes an old behaviour: a
// body no longer concatenates. It used to — ancestor-first, "because the
// persona is additive" — and the concatenation was never rendered, so nothing
// depended on it. Under named sections the additive case has a better
// spelling: two sections and a layout that yields both, which is what the
// charter and lens split already produced. A body that means to add writes a
// section; a body that means to replace writes a body. Neither needs an
// exception to closest-wins.
//
// A document whose prose is dropped by that rule is reported through
// [Options].Report rather than passed over. An author who wrote a paragraph
// and got no paragraph has to hear which document won.
//
// # How layouts compose
//
// A layout may itself declare `{{ extends }}`, and the chain is walked
// outward: the OUTERMOST layout is the frame, and every layout inside it
// contributes sections. So a layout's default section is overridden by a
// layout closer to the profile, and both are overridden by the profile — one
// ordering, applied to files of two kinds.
func Compose(docs []*Document, layouts LayoutLoader, report func(string)) (Frame, error) {
	present := make([]*Document, 0, len(docs))
	for _, d := range docs {
		if d == nil || len(d.Nodes) == 0 {
			continue
		}
		present = append(present, d)
	}
	if len(present) == 0 {
		return Frame{}, nil
	}

	// The frame, by the three clauses above. present is ancestor-first, so the
	// closest candidate is the last.
	var rendering *Document
	for i := len(present) - 1; i >= 0 && rendering == nil; i-- {
		if present[i].Extends != "" {
			rendering = present[i]
		}
	}
	for i := len(present) - 1; i >= 0 && rendering == nil; i-- {
		if hasShape(present[i].Nodes) {
			rendering = present[i]
		}
	}
	if rendering == nil {
		// Every document is a fragment. There is content and no shape, so
		// there is no document to render — and the sections are reported as
		// unyielded by the caller rather than silently dropped.
		return Frame{Sections: present}, nil
	}

	// Said out loud rather than left to be noticed: a document whose prose is
	// not the shape of the output, and which declared no section to carry it,
	// contributed nothing an author can see.
	if report != nil {
		for _, d := range present {
			if d == rendering {
				continue
			}
			if prose := strings.TrimSpace(textOutsideSections(d.Nodes)); prose != "" {
				report(fmt.Sprintf(
					"template: %s: prose outside a %s is not rendered — %s supplies the document's shape; "+
						"put it in a %s the layout yields",
					d.Name, sectionVerb, rendering.Name, sectionVerb))
			}
		}
	}

	frame := Frame{Render: rendering, Sections: present}
	if rendering.Extends == "" {
		return frame, nil
	}
	// A chain that extends nothing needs no loader, and one that extends
	// something needs a real one. Saying so here is what keeps the absence a
	// diagnostic rather than a nil dereference three frames down.
	if layouts == nil {
		return Frame{}, fmt.Errorf("%s extends layout %q: %w: no layout loader was supplied",
			rendering.Name, rendering.Extends, ErrLayout)
	}

	// The layout chain, innermost first as it is walked.
	var chain []*Document
	seen := map[string]bool{}
	for name := rendering.Extends; name != ""; {
		if seen[name] {
			return Frame{}, fmt.Errorf("%w: %s: %s", ErrLayoutCycle, rendering.Name, cyclePath(chain, name))
		}
		seen[name] = true
		if len(chain) >= maxLayoutDepth {
			return Frame{}, fmt.Errorf("%w: %s: a layout chain is at most %d deep",
				ErrLayoutCycle, rendering.Name, maxLayoutDepth)
		}
		lay, err := layouts.Layout(name)
		if err != nil {
			return Frame{}, fmt.Errorf("%s extends layout %q: %w", rendering.Name, name, err)
		}
		if lay == nil {
			return Frame{}, fmt.Errorf("%s extends layout %q: %w", rendering.Name, name, ErrLayout)
		}
		chain = append(chain, lay)
		name = lay.Extends
	}

	// The outermost layout is the frame, and the layouts fill it weakest
	// first — outermost to innermost — ahead of the profile chain.
	frame.Render = chain[len(chain)-1]
	ordered := make([]*Document, 0, len(chain)+len(present))
	for i := len(chain) - 1; i >= 0; i-- {
		ordered = append(ordered, chain[i])
	}
	frame.Sections = append(ordered, present...)
	return frame, nil
}

// hasShape reports whether a document holds anything outside its sections —
// which is what makes it a candidate frame rather than a fragment.
//
// A tag counts and whitespace does not. That distinction is the whole of it: a
// layout whose entire content is `{{ yield charter }}` has shape, and a
// fragment that declares two sections with a blank line between them has
// none. Testing for TEXT outside sections instead would call a one-tag layout
// a fragment and render nothing at all — which is exactly what it did before
// this function existed.
func hasShape(nodes []Node) bool {
	for _, n := range nodes {
		if n.Kind == NodeSection {
			continue
		}
		if n.Kind != NodeText {
			return true
		}
		if strings.TrimSpace(n.Text) != "" {
			return true
		}
	}
	return false
}

// textOutsideSections returns the literal text of nodes that are not sections,
// which is the prose a document loses when it is not the one that renders.
func textOutsideSections(nodes []Node) string {
	var b strings.Builder
	for _, n := range nodes {
		if n.Kind == NodeText {
			b.WriteString(n.Text)
		}
	}
	return b.String()
}

// cyclePath renders a layout chain and the name that closed the loop.
func cyclePath(chain []*Document, closed string) string {
	names := make([]string, 0, len(chain)+1)
	for _, d := range chain {
		names = append(names, d.Name)
	}
	return strings.Join(append(names, closed), " -> ")
}
