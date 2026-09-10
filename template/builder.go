package template

import "strings"

// builder assembles a rendered document and applies the collapse rule.
//
// It exists because the rule cannot be applied a tag at a time. Whether a
// vanished tag takes its line with it depends on what else is on that line,
// and whether it takes a blank line with it depends on what survived either
// side — so the text is accumulated as segments that remember which of them
// came from a tag, and the rule runs once at [builder.finish].
//
// Two segments and not three: a tag that rendered text is indistinguishable
// from authored text once it is in the document, and treating it as its own
// kind would let a filled tag's own newlines be collapsed as though a tag had
// vanished there.
type builder struct {
	segs []segment
}

// segment is one contribution to the document: text, and whether a tag
// produced it.
type segment struct {
	text string

	// vanished marks a tag that rendered nothing. It is the only thing the
	// collapse rule reads, and it is why an empty tag is recorded at all
	// rather than simply not appended: a tag that produced no text is a
	// position in the document, and the rule is about that position.
	vanished bool
}

// text appends literal template text.
func (b *builder) text(s string) {
	if s == "" {
		return
	}
	b.segs = append(b.segs, segment{text: s})
}

// tag appends what one tag rendered. An empty result is recorded as a
// vanished position rather than dropped.
func (b *builder) tag(s string) {
	if s == "" {
		b.segs = append(b.segs, segment{vanished: true})
		return
	}
	b.segs = append(b.segs, segment{text: s})
}

// line is one line of the assembled document, and whether a tag on it
// rendered nothing.
type line struct {
	text     string
	vanished bool
}

// finish applies the collapse rule and returns the document with one trailing
// newline, or the empty string when nothing survived.
//
// The rule is stated on [Render]. What is worth saying here is why it is two
// passes rather than one: the first decides which lines are gone, and the
// second decides what the removals did to the blank space around them. One
// pass cannot do the second, because whether a blank line survives depends on
// a line that has not been read yet.
func (b *builder) finish() string {
	lines := b.lines()

	out := make([]string, 0, len(lines))
	// collapse is set by a removal and cleared by the next line with content
	// on it. While it is set, a blank line that would sit against another
	// blank line — or against the start of the document — is the gap the
	// removal left rather than anything an author wrote.
	collapse := false
	for _, ln := range lines {
		text := ln.text
		if ln.vanished {
			// The tag's own trailing whitespace goes with the tag. Only on a
			// line a tag vanished from: trimming every line would eat
			// markdown's two-space hard break, which is content.
			text = strings.TrimRight(text, " \t")
		}
		blank := strings.TrimSpace(text) == ""
		switch {
		case ln.vanished && blank:
			collapse = true
		case blank:
			if collapse && (len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == "") {
				continue
			}
			out = append(out, text)
		default:
			out = append(out, text)
			collapse = false
		}
	}

	// Leading and trailing blank lines are the file's formatting rather than
	// the author's content, and are trimmed for the reason a profile's body
	// already is when the catalog reads it.
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// lines splits the accumulated segments into lines, carrying each vanished
// tag's position onto the line it stood on.
func (b *builder) lines() []line {
	out := []line{{}}
	for _, seg := range b.segs {
		if seg.vanished {
			out[len(out)-1].vanished = true
			continue
		}
		parts := strings.Split(seg.text, "\n")
		for i, part := range parts {
			if i > 0 {
				out = append(out, line{})
			}
			out[len(out)-1].text += part
		}
	}
	return out
}
