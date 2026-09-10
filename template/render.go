package template

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrUnknownName reports a `{{ value }}` naming something the instance does
// not carry.
//
// It is returned nowhere and is a sentinel for a report: a name cairn cannot
// fill renders nothing and the document is written anyway, for the reason the
// marker design settled on. A typo should degrade rather than fail — refusing
// lets one word in one document decide whether anything is written at all —
// and a template outlives the set of values any one build fills.
var ErrUnknownName = errors.New("no such value")

// Options is everything a render needs beyond the documents themselves.
//
// Every field is data. There is no I/O here and no hook that could do any: a
// source's text arrives already hydrated, a value arrives already computed,
// and a layout arrives already parsed. That is what makes a render pure, and
// it is the property the hydrate/render split exists to protect.
type Options struct {
	// Layouts resolves `{{ extends }}`. Nil is legal for a chain that extends
	// nothing and an error for one that does.
	Layouts LayoutLoader

	// Values fills `{{ value NAME }}`. A name absent from it renders nothing
	// and is reported.
	//
	// The map is the whole vocabulary. This package holds no list of the
	// values it fills, so what an instance carries is decided once by whoever
	// builds this map — which is what keeps a predefined set out of the
	// engine.
	Values map[string]string

	// Sources fills `{{ KIND: ARG }}`, keyed by [Source.ID]. A source absent
	// from it renders nothing and is reported: it is the shape a hydrator that
	// could not resolve one leaves behind, and a section that came back empty
	// is a fact about this materialization rather than a fault in the
	// template.
	Sources map[string]string

	// Report receives one line per thing an operator should hear about: a
	// value naming nothing, a source that produced no text, prose that did not
	// render. Nil discards them.
	//
	// It is a callback rather than a returned list because these are not
	// errors and are not ordered against each other in any way a caller should
	// read meaning into. What matters is that none of them is silent.
	Report func(string)
}

// Result is a render.
type Result struct {
	// Text is the rendered document, with one trailing newline, or empty when
	// everything in it collapsed.
	Text string

	// Sections are the section names the frame rendered, sorted. It is
	// provenance for a caller that wants to say what a document was built
	// from; nothing here reads it.
	Sections []string

	// Unused are the section names the chain declared and the frame never
	// yielded, sorted. A section nobody yields is content an author wrote and
	// no document carries, which is exactly the failure a marker design made
	// invisible.
	Unused []string
}

// Render renders one profile chain into one document.
//
// docs is the cascade ancestor-first, as [Compose] takes it. The chain's
// layouts are resolved, its sections are folded closest-wins, and the frame is
// walked once.
//
// # Empty collapses
//
// A tag that renders nothing renders nothing, and takes its whitespace with
// it. Precisely:
//
//   - Its own text goes, along with the trailing whitespace on its line.
//   - Its line goes too, when nothing else on that line is more than
//     whitespace. So a `{{ yield }}` alone on a line leaves no blank line
//     behind, and `- scope: {{ value scope }}` with no scope leaves
//     "- scope:".
//   - When removing lines runs two blank stretches together, the join
//     collapses to one blank line.
//   - Blank space no removal touched is left exactly as it was authored.
//
// That last clause is the one worth defending. Collapsing every run of blank
// lines would be simpler to state and would reflow a fenced code block in
// somebody's prose, which is a worse promise than this one makes. What an
// author needed to count newlines for was the gap a marker left when it filled
// nothing, and that gap is what the third clause removes.
//
// A document that collapses to nothing renders the empty string, and the
// caller writes no file. An empty document is a claim that a profile said
// something and meant nothing by it; an absent one is the profile not having
// declared it.
func Render(docs []*Document, opts Options) (Result, error) {
	frame, err := Compose(docs, opts.Layouts, opts.Report)
	if err != nil {
		return Result{}, err
	}

	sections, order, err := FoldSections(frame.Sections, opts)
	if err != nil {
		return Result{}, err
	}
	// Every document was a fragment: content and no shape. The sections are
	// reported so that an author who wrote one hears that nothing carries it,
	// which is the case a marker design made invisible.
	if frame.Render == nil {
		out := Result{Unused: order}
		slices.Sort(out.Unused)
		if opts.Report != nil {
			for _, name := range out.Unused {
				opts.Report(fmt.Sprintf(
					"template: %s declares %s %q and no document in the chain supplies a shape to yield it into",
					declarerOf(frame.Sections, name), sectionVerb, name))
			}
		}
		return out, nil
	}

	var b builder
	yielded := map[string]bool{}
	if err := renderNodes(&b, frame.Render.Nodes, sections, yielded, opts, frame.Render.Name); err != nil {
		return Result{}, err
	}

	out := Result{Text: b.finish()}
	for _, name := range order {
		if yielded[name] {
			out.Sections = append(out.Sections, name)
			continue
		}
		out.Unused = append(out.Unused, name)
	}
	slices.Sort(out.Sections)
	slices.Sort(out.Unused)
	if opts.Report != nil {
		for _, name := range out.Unused {
			opts.Report(fmt.Sprintf(
				"template: %s declares %s %q and %s yields it nowhere, so its content is not in the document",
				declarerOf(frame.Sections, name), sectionVerb, name, frame.Render.Name))
		}
	}
	return out, nil
}

// FoldSections folds every document's sections into one map, closest-wins,
// and returns the names in the order they were first declared.
//
// docs is weakest-first, so a later document's section replaces an earlier
// one's at that name.
//
// # Replace, and how to add instead
//
// Replace is the default because it is the only rule cairn's cascade has: a
// descendant's member of a keyed collection replaces the ancestor's member at
// that key and leaves the rest standing. A section is a member of a keyed
// collection, and making it the one that appends would be a second
// composition rule for a shape that already has one.
//
// It is also the direction that fails safe and the direction that is
// recoverable. A section that replaced when it meant to add produces a
// document with a paragraph missing, which a reader sees. One that appended
// when it meant to replace produces two personas stacked in one file, and both
// halves look deliberate. And a section that means to add can say so —
// `{{ parent }}` renders what the chain already had, and there is no tag that
// could subtract under the other default.
func FoldSections(docs []*Document, opts Options) (map[string]string, []string, error) {
	out := map[string]string{}
	var order []string
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		for _, n := range doc.Nodes {
			if n.Kind != NodeSection {
				continue
			}
			var b builder
			// The accumulated value is what `{{ parent }}` renders, read
			// before this section's own text replaces it.
			parent := out[n.Name]
			if err := renderSectionBody(&b, n.Body, parent, opts, doc.Name); err != nil {
				return nil, nil, err
			}
			if _, declared := out[n.Name]; !declared {
				order = append(order, n.Name)
			}
			out[n.Name] = strings.TrimRight(b.finish(), "\n")
		}
	}
	return out, order, nil
}

// renderSectionBody renders one section's nodes. A section's body may hold
// text, values, sources and `{{ parent }}`, and nothing else: a section is a
// substitution target and not a scope — see [openSection].
func renderSectionBody(b *builder, nodes []Node, parent string, opts Options, docName string) error {
	for _, n := range nodes {
		switch n.Kind {
		case NodeText:
			b.text(n.Text)
		case NodeParent:
			b.tag(parent)
		case NodeValue:
			b.tag(valueText(n, opts, docName))
		case NodeSource:
			b.tag(sourceText(n, opts, docName))
		default:
			return fmt.Errorf("%w: %s: line %d: a %s does not belong inside a %s",
				ErrSyntax, docName, n.Line, n.Kind, sectionVerb)
		}
	}
	return nil
}

// renderNodes walks the frame, recording which sections it yielded.
func renderNodes(b *builder, nodes []Node, sections map[string]string,
	yielded map[string]bool, opts Options, docName string) error {

	for _, n := range nodes {
		switch n.Kind {
		case NodeText:
			b.text(n.Text)
		case NodeValue:
			b.tag(valueText(n, opts, docName))
		case NodeSource:
			b.tag(sourceText(n, opts, docName))
		case NodeYield, NodeSection:
			// One case for both, and that is the point of a section being one
			// node: a `{{ section }}` in the document that renders is a yield
			// whose default the fold has already taken into account, so its
			// own body is never walked here. Walking it would render the
			// frame's own text in place of the descendant's override.
			yielded[n.Name] = true
			b.tag(sections[n.Name])
		case NodeParent:
			return fmt.Errorf("%w: %s: line %d: %s renders only inside a %s",
				ErrSyntax, docName, n.Line, parentVerb, sectionVerb)
		case NodeExtends:
			// Renders nothing; it is carried on the document.
		}
	}
	return nil
}

// valueText returns what a `{{ value }}` renders, reporting a name the
// instance does not carry.
//
// The name is read from the map's keys rather than from a list this package
// holds, which is what makes "cairn ships no vocabulary" a property rather
// than a promise. A name present and empty is silent: an instance with no
// declared scope is a fact about the instance, not something that went wrong.
func valueText(n Node, opts Options, docName string) string {
	text, ok := opts.Values[n.Name]
	if !ok {
		if opts.Report != nil {
			opts.Report(fmt.Sprintf("template: %s: line %d: %s: %v; the values are %s",
				docName, n.Line, n.Raw, ErrUnknownName, quotedKeys(opts.Values)))
		}
		return ""
	}
	return text
}

// sourceText returns what an inline source renders.
//
// A source absent from the map and a source that resolved to nothing are the
// same thing here and are reported the same way. They have to be: a hydrator
// that could not resolve one leaves no entry, one that resolved it to nothing
// leaves an empty one, and a renderer that treated them differently would be
// reading a failure off the shape of a map.
func sourceText(n Node, opts Options, docName string) string {
	text := opts.Sources[n.ID]
	if strings.TrimSpace(text) == "" {
		if opts.Report != nil {
			opts.Report(fmt.Sprintf("template: %s: line %d: %s produced no text, so nothing is in its place",
				docName, n.Line, n.Raw))
		}
		return ""
	}
	return text
}

// declarerOf names the last document to declare a section, for a diagnostic
// about one nothing yields.
func declarerOf(docs []*Document, name string) string {
	out := "the chain"
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		for _, n := range doc.Nodes {
			if n.Kind == NodeSection && n.Name == name {
				out = doc.Name
			}
		}
	}
	return out
}

// quotedKeys renders a value map's names for a diagnostic, sorted so that two
// runs say it the same way.
func quotedKeys(m map[string]string) string {
	if len(m) == 0 {
		return "none"
	}
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, fmt.Sprintf("%q", k))
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}
