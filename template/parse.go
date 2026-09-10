package template

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrSyntax reports a tag this package cannot act on: an unclosed tag, a verb
// it does not have, a name that cannot be one, a section that is never closed,
// an `{{ end }}` closing nothing, or a document declaring `{{ extends }}`
// twice.
//
// A malformed tag is refused rather than left in place, for the reason the
// marker design refused one: the delimiters are this engine's namespace, so a
// tag inside them that cairn does not understand is a mistake in a template
// and never somebody else's syntax — and leaving it alone would plant the
// tag's own text in a file an agent reads as instructions.
var ErrSyntax = errors.New("invalid template syntax")

// openTag and closeTag are the tag delimiters.
const (
	openTag  = "{{"
	closeTag = "}}"
)

// namePattern is what a section, layout or value may be called. It is
// deliberately narrow: a name is an identifier an author types twice — once
// where the content is declared and once where it renders — and a character
// that has to be escaped, quoted or trimmed to match is a name that will one
// day not match.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Parse reads one template document. name is what the document is called in a
// diagnostic and in a [Source.ID] — a profile's id, or a layout's name.
//
// Text outside a tag is carried byte for byte. Nothing is validated about it,
// and nothing about where a tag sits is validated either: a tag inside a
// fenced code block is a tag like any other, so a document that means to show
// this syntax has to avoid writing a live one. That limitation is the same one
// the marker design carried and it is stated rather than fixed, because the
// alternative is a parser that has to understand markdown to render it.
func Parse(name, text string) (*Document, error) {
	doc := &Document{Name: strings.TrimSpace(name)}
	if doc.Name == "" {
		return nil, fmt.Errorf("%w: a document needs a name", ErrSyntax)
	}

	// The open section stack. A section may not nest — see [openSection] —
	// so this is one deep at most, and it is a stack anyway because that is
	// what makes the diagnostic for an unclosed section name the right one.
	var stack []*Node
	// nodes points at the list the next node is appended to: the document's
	// own, or the innermost open section's body.
	appendNode := func(n Node) {
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			top.Body = append(top.Body, n)
			return
		}
		doc.Nodes = append(doc.Nodes, n)
	}

	line := 1
	sources := 0
	rest := text
	for rest != "" {
		start := strings.Index(rest, openTag)
		if start < 0 {
			appendNode(Node{Kind: NodeText, Text: rest, Line: line})
			break
		}
		if start > 0 {
			lead := rest[:start]
			appendNode(Node{Kind: NodeText, Text: lead, Line: line})
			line += strings.Count(lead, "\n")
			rest = rest[start:]
		}

		// A tag ends at the first close on the same line. Scanning no further
		// than the newline is what keeps an unclosed tag a one-line mistake:
		// the alternative finds a close three paragraphs down and reports the
		// error against text nobody thinks of as a tag.
		limit := len(rest)
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			limit = nl
		}
		end := strings.Index(rest[:limit], closeTag)
		if end < 0 {
			return nil, fmt.Errorf("%w: line %d: %q is never closed — a tag is %s…%s and is one line",
				ErrSyntax, line, firstLine(rest), openTag, closeTag)
		}
		raw := rest[:end+len(closeTag)]
		body := strings.TrimSpace(rest[len(openTag):end])
		rest = rest[end+len(closeTag):]

		node, err := parseTag(raw, body, line, doc, &sources)
		if err != nil {
			return nil, err
		}
		switch {
		case node.Kind == NodeExtends:
			// Lifted onto the document rather than kept in place: it renders
			// nothing, so it has no position worth keeping.
		case node.Kind == NodeSection && node.Body == nil && isOpen(body):
			if err := openSection(&stack, node, doc); err != nil {
				return nil, err
			}
		case node.Kind == NodeText && body == endVerb:
			if len(stack) == 0 {
				return nil, fmt.Errorf("%w: line %d: %s closes nothing — there is no open %s",
					ErrSyntax, line, raw, sectionVerb)
			}
			done := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			// Appended once it is closed, so a section's own body is complete
			// before anything can read it.
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.Body = append(top.Body, *done)
			} else {
				doc.Nodes = append(doc.Nodes, *done)
			}
		default:
			appendNode(node)
		}
		line += strings.Count(raw, "\n")
	}

	if len(stack) > 0 {
		unclosed := stack[len(stack)-1]
		return nil, fmt.Errorf("%w: %s: %s %s on line %d is never closed by %s%s %s",
			ErrSyntax, doc.Name, sectionVerb, unclosed.Name, unclosed.Line, openTag, endVerb, closeTag)
	}
	return doc, nil
}

// The verbs, spelled once. A verb is cairn's and a name is the author's, so
// this list is the whole of the vocabulary this package ships.
const (
	extendsVerb = "extends"
	sectionVerb = "section"
	yieldVerb   = "yield"
	valueVerb   = "value"
	parentVerb  = "parent"
	endVerb     = "end"
)

// verbs renders the vocabulary for a diagnostic.
func verbs() string {
	return fmt.Sprintf("%s NAME, %s NAME…%s, %s NAME, %s NAME, %s, or KIND: ARG for a source",
		extendsVerb, sectionVerb, endVerb, yieldVerb, valueVerb, parentVerb)
}

// isOpen reports whether a tag body opens a section.
func isOpen(body string) bool {
	return strings.HasPrefix(body, sectionVerb+" ") || body == sectionVerb
}

// openSection pushes an opening section onto the stack, refusing a nested one.
//
// A section may not nest. It is a substitution target and not a scope: a
// section inside a section would mean a name resolving differently depending
// on where it was read, which is the property that turns a template into a
// program. What an author reaching for it wants is two sections and a layout
// that yields both.
func openSection(stack *[]*Node, node Node, doc *Document) error {
	if len(*stack) > 0 {
		outer := (*stack)[len(*stack)-1]
		return fmt.Errorf("%w: %s: line %d: %s %s opens inside %s %s, and a section does not nest — "+
			"declare two sections and let the layout yield both",
			ErrSyntax, doc.Name, node.Line, sectionVerb, node.Name, sectionVerb, outer.Name)
	}
	held := node
	held.Body = []Node{}
	*stack = append(*stack, &held)
	return nil
}

// parseTag reads one tag's body into a node.
//
// A source is recognized before a verb, and by one rule: the text before the
// first colon is a single bare word. That is what lets a kind be anything —
// `{{ cmd: … }}` today, `{{ tesseract: … }}` the day something resolves one —
// without this package holding a list of them. A verb holds no colon, so the
// two cannot be confused.
func parseTag(raw, body string, line int, doc *Document, sources *int) (Node, error) {
	if body == "" {
		return Node{}, fmt.Errorf("%w: %s: line %d: %s is empty; a tag is %s",
			ErrSyntax, doc.Name, line, raw, verbs())
	}

	if kind, arg, ok := splitSource(body); ok {
		id := sourceID(doc.Name, *sources)
		*sources++
		return Node{Kind: NodeSource, Name: kind, Arg: arg, ID: id, Raw: raw, Line: line}, nil
	}

	fields := strings.Fields(body)
	verb := fields[0]

	if verb == endVerb || verb == parentVerb {
		if len(fields) != 1 {
			return Node{}, fmt.Errorf("%w: %s: line %d: %s takes no name",
				ErrSyntax, doc.Name, line, verb)
		}
		if verb == parentVerb {
			return Node{Kind: NodeParent, Raw: raw, Line: line}, nil
		}
		// An `{{ end }}` is returned as a text node the caller recognizes by
		// its body: it is a delimiter rather than content, and giving it a
		// kind of its own would put a node in the tree that never renders.
		return Node{Kind: NodeText, Raw: raw, Line: line}, nil
	}

	if len(fields) != 2 {
		return Node{}, fmt.Errorf("%w: %s: line %d: %s names %s, and a tag is a verb and one name; the tags are %s",
			ErrSyntax, doc.Name, line, raw, countedFields(fields), verbs())
	}
	name := fields[1]
	if !namePattern.MatchString(name) {
		return Node{}, fmt.Errorf("%w: %s: line %d: %q is not a name — a name starts with a letter or digit "+
			"and holds letters, digits, %q, %q and %q",
			ErrSyntax, doc.Name, line, name, "_", ".", "-")
	}

	switch verb {
	case extendsVerb:
		if doc.Extends != "" {
			return Node{}, fmt.Errorf("%w: %s: line %d: the document already extends %q, and a document composes into one layout",
				ErrSyntax, doc.Name, line, doc.Extends)
		}
		doc.Extends = name
		return Node{Kind: NodeExtends, Name: name, Raw: raw, Line: line}, nil
	case sectionVerb:
		return Node{Kind: NodeSection, Name: name, Raw: raw, Line: line}, nil
	case yieldVerb:
		return Node{Kind: NodeYield, Name: name, Raw: raw, Line: line}, nil
	case valueVerb:
		return Node{Kind: NodeValue, Name: name, Raw: raw, Line: line}, nil
	default:
		return Node{}, fmt.Errorf("%w: %s: line %d: %s declares the verb %q; the tags are %s",
			ErrSyntax, doc.Name, line, raw, verb, verbs())
	}
}

// splitSource reports whether body is an inline source, and splits it.
//
// The test is that the text before the first colon is one bare word. A verb
// never holds a colon and a kind is always one word, so nothing that is a verb
// can be read as a source and nothing that is a source can be read as a verb.
func splitSource(body string) (kind, arg string, ok bool) {
	i := strings.IndexByte(body, ':')
	if i <= 0 {
		return "", "", false
	}
	kind = strings.TrimSpace(body[:i])
	if kind == "" || strings.ContainsAny(kind, " \t") {
		return "", "", false
	}
	return kind, strings.TrimSpace(body[i+1:]), true
}

// countedFields describes what a malformed tag held, so a diagnostic can say
// what was wrong with it rather than only that something was.
func countedFields(fields []string) string {
	switch len(fields) {
	case 0:
		return "nothing"
	case 1:
		return fmt.Sprintf("only %q", fields[0])
	default:
		return fmt.Sprintf("%d words", len(fields))
	}
}

// firstLine returns text up to its first newline, for quoting an unclosed tag
// without quoting the rest of the document.
func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}
