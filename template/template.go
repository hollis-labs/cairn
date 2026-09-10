// Package template is Cairn's render engine: a profile IS its template.
//
// A profile's frontmatter declares and its body is template content. The body
// carries named sections, the layout it composes into, and its own inline
// sources, so a profile, its template, its prose and the commands it runs are
// one file. Nothing here reads a profile off disk and nothing here runs a
// command: rendering is values in, text out.
//
// # The seam this package sits on
//
// materialize = hydrate → render → plant. This is render, and it is pure by
// construction. A document's inline sources are collected by [Sources] at
// parse time and handed back as data; whoever owns the dangerous half resolves
// them and passes the text in through [Options].Sources. So a template tree is
// shareable and a renderer cannot reach a command, a file or a URL — see
// [github.com/chrispian/cairn/slots], which is the half that can.
//
// # The syntax
//
//	{{ extends NAME }}        compose this document into the layout NAME
//	{{ section NAME }}…{{ end }}  declare content for NAME, and render it here
//	{{ yield NAME }}          render NAME here, declaring nothing
//	{{ value NAME }}          one value of the instance being materialized
//	{{ parent }}              inside a section: what the chain already had
//	{{ KIND: ARG }}           an inline source — {{ cmd: git status }}
//
// A tag is one line by construction: the scanner stops at a newline inside a
// tag, so an unclosed "{{" is reported at the line it was written on rather
// than swallowing the rest of the document.
//
// There is no conditional and no loop, and there is not going to be one. That
// is the property that keeps a template a substitution target rather than a
// program, and it is what lets [Render] promise that the same inputs produce
// the same bytes.
//
// # Named, not predefined
//
// Every name in the list above is the author's. Cairn ships no section names,
// no layout names and no source kinds: a section is whatever a layout yields,
// a layout is whatever the bundle holds, and a source kind is a word this
// package hands to a hydrator without reading it. What cairn owns is the six
// verbs, because a verb is the language and a name is the content.
//
// # Empty collapses
//
// A tag that renders nothing renders nothing, and takes its whitespace with
// it — see [Render] for the exact rule. This is the whole point of the
// engine's shape: it retires the class of rules a marker design needs, where
// an author counts blank lines around a marker because a marker that fills
// nothing leaves them behind.
package template

import "fmt"

// NodeKind is what one node of a parsed document is.
type NodeKind int

const (
	// NodeText is literal template text, carried byte for byte.
	NodeText NodeKind = iota

	// NodeExtends is `{{ extends NAME }}`: the layout this document composes
	// into. It renders nothing itself.
	NodeExtends

	// NodeSection is `{{ section NAME }}…{{ end }}`: content for NAME, and the
	// place NAME renders.
	//
	// It is one node and not two because declaring and yielding are one act.
	// A layout's `{{ yield charter }}` and a profile's `{{ section charter }}`
	// differ only in whether a default came with the hole — see [NodeYield].
	NodeSection

	// NodeYield is `{{ yield NAME }}`: the place NAME renders, with no default.
	NodeYield

	// NodeValue is `{{ value NAME }}`: one value of the instance.
	NodeValue

	// NodeParent is `{{ parent }}`: inside a section, the content the chain
	// already had for that section. It is how a section adds to an ancestor's
	// rather than replacing it — see [FoldSections].
	NodeParent

	// NodeSource is `{{ KIND: ARG }}`: an inline source. Its text arrives
	// already hydrated, keyed by [Source.ID].
	NodeSource
)

// String names a kind for a diagnostic.
func (k NodeKind) String() string {
	switch k {
	case NodeText:
		return "text"
	case NodeExtends:
		return "extends"
	case NodeSection:
		return "section"
	case NodeYield:
		return "yield"
	case NodeValue:
		return "value"
	case NodeParent:
		return "parent"
	case NodeSource:
		return "source"
	default:
		return fmt.Sprintf("node(%d)", int(k))
	}
}

// Node is one element of a parsed document.
type Node struct {
	// Kind is what this node is.
	Kind NodeKind

	// Text is the literal text of a [NodeText], and empty for every other
	// kind.
	Text string

	// Name is the section, layout or value a tag names, and the kind of a
	// [NodeSource] — the word before the colon.
	Name string

	// Arg is a [NodeSource]'s argument: everything after the colon, verbatim
	// and unexpanded. This package neither reads nor expands it.
	Arg string

	// Body is a [NodeSection]'s content. Empty for every other kind.
	Body []Node

	// ID is a [NodeSource]'s stable identity, matching the [Source.ID] the
	// hydrated text arrives under.
	ID string

	// Raw is the tag exactly as it was written, for a diagnostic that has to
	// quote it. Empty for a [NodeText].
	Raw string

	// Line is the 1-based line the node began on, for a diagnostic that has to
	// say where.
	Line int
}

// Document is one parsed template: a profile's body, or a layout.
//
// It is the parse of one file and nothing more. What it composes with, what
// its sections resolve to, and where its rendered text lands are all somebody
// else's question — see [Render] for the first two and the boot tree's layout
// document for the third.
type Document struct {
	// Name is what this document is called in a diagnostic: a profile's id, or
	// a layout's name. It is also what makes a [Source.ID] unique across a
	// chain.
	Name string

	// Nodes is the document in order.
	Nodes []Node

	// Extends is the layout this document composes into, from its
	// `{{ extends }}` tag, or empty for a document that declares none.
	//
	// It is lifted out of Nodes because it is a property of the document
	// rather than a place in it: a tag that renders nothing has no position
	// worth keeping. Declaring it twice is refused at parse.
	Extends string
}

// Source is one inline source a document declares, as data for a hydrator.
//
// Kind is not read here. A document may name any kind at all and this package
// carries the word through untouched: what `cmd` means, and whether it is
// allowed to mean anything, is the hydrator's to decide. That is the same
// promise the manifest makes about a key cairn has never heard of, and it is
// what keeps the predefined vocabulary out of the engine.
type Source struct {
	// ID is this source's stable identity: the document's name and the
	// source's position in it. The hydrated text is keyed by it.
	ID string

	// Kind is the word before the colon, as written.
	Kind string

	// Arg is everything after the colon, verbatim and unexpanded.
	Arg string

	// Raw is the tag as it was written, and Line is where, for a diagnostic
	// naming a source that would not resolve.
	Raw  string
	Line int

	// Document is the name of the document the source was written in, which a
	// diagnostic needs and [Source.ID] only encodes.
	Document string
}

// Sources returns every inline source in docs, in document and then document
// order, so that a hydrator can resolve them all before a render begins.
//
// A nil document contributes nothing rather than panicking: a chain is
// assembled from a profile cascade and a missing body is an ordinary member of
// one.
func Sources(docs ...*Document) []Source {
	var out []Source
	for _, doc := range docs {
		if doc == nil {
			continue
		}
		out = append(out, collectSources(doc.Nodes, doc.Name)...)
	}
	return out
}

// collectSources walks nodes, descending into section bodies, and returns the
// sources in the order they were written.
func collectSources(nodes []Node, docName string) []Source {
	var out []Source
	for _, n := range nodes {
		switch n.Kind {
		case NodeSource:
			out = append(out, Source{
				ID: n.ID, Kind: n.Name, Arg: n.Arg,
				Raw: n.Raw, Line: n.Line, Document: docName,
			})
		case NodeSection:
			out = append(out, collectSources(n.Body, docName)...)
		}
	}
	return out
}

// sourceID is a source's stable identity. The document's name and the
// source's ordinal within it are enough: a document is parsed once and the
// same parse is walked by the hydrator and the renderer, so the nth source is
// the nth source in both.
//
// Two sources written identically in one document therefore get two ids,
// which is what a hydrator wants — the same command in two places may
// legitimately be resolved twice, and collapsing them would be this package
// deciding that it knows the command is pure.
func sourceID(docName string, ordinal int) string {
	return fmt.Sprintf("%s#%d", docName, ordinal)
}
