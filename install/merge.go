package install

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/chrispian/cairn/bootdir"
)

// mergeSettingsDocument returns the settings document an install writes where
// one already exists: every key the render declares carries the render's
// value, and every key it does not carries the name and value bytes that were
// found.
//
// It is the settings artifact's [Renderer.Merge], and it is the whole of what
// "cairn claims settings.json by key" means. ~/.claude/settings.json is a file
// the harness and the operator write too — the live one held a `model` that no
// cairn profile declares, and that profiles/base.md in agent-setup declines to
// declare on purpose — and a render-and-overwrite install deleted it. A key
// cairn never rendered is not cairn's to remove.
//
// # Depth
//
// The rule applies at every level, not only the top one. A key cairn declares
// carries cairn's value; a key it does not stands, wherever it sits. That
// matters most one level down: cairn declares permissions.defaultMode and, from
// T11, permissions.additionalDirectories, while the harness writes the
// operator's user-scope rules into permissions beside them. Replacing the
// whole permissions object because cairn declares two of its keys would be the
// same deletion this function exists to stop, one level lower and in the key
// that decides what a session may do.
//
// The cost is stated rather than hidden: cairn can no longer remove a nested
// key it stopped declaring, because nothing distinguishes one from a key the
// harness wrote. That is the same leftover this whole change accepts at the top
// level, and it is the price of not deleting what cairn does not own.
//
// # Spelling and order
//
// Nothing is re-encoded. Both the keys and the values are copied as raw bytes
// from whichever document they came from, so a string holding "<" survives
// here for the same reason it survives [bootdir.IndentJSON] — see
// TestInstallRoundTripsHTMLSpecialCharactersInSettings, which is the test that
// catches a re-marshal on this path.
//
// What survives is every token, not the layout around them. The assembled
// document is laid out again on the way out, so whitespace inside a
// multi-line value the operator wrote moves even though none of its
// characters do. That is [bootdir.IndentJSON] doing to their keys exactly what
// it already does to cairn's.
//
// The order is the order found on disk, with the keys only the render declares
// after it. Cairn does not reorder a file it only partly owns: a document whose
// declared values all agree merges to itself byte for byte, so the check stays
// quiet and the install is a no-op. What that forgives, and it is the only
// thing, is a declared key moved: two documents differing only in where a key
// sits now read as the same. A changed value, a removed key, a respelled number
// and a duplicated key are all still findings — the last three because a
// document this cannot read member by member is not merged at all.
//
// # Failure is the render
//
// Anything that is not one readable JSON object on both sides returns the
// render untouched: a document that is not JSON, one that is not an object,
// one holding the same key twice. The install then overwrites it and the check
// reports it modified, which is the right answer for a file that is not the
// document cairn wrote — and refusing to guess is what keeps the duplicate-key
// case from being quietly collapsed in the file that carries the permission
// mode.
//
// The result is laid out by [bootdir.IndentJSON], the way
// [bootdir.RenderSettings] lays out the document it composes, so what an
// install writes is a document at rest and not an assembly.
func mergeSettingsDocument(rendered, existing []byte) []byte {
	merged, ok := mergeJSONObjects(rendered, existing)
	if !ok {
		return rendered
	}
	return bootdir.IndentJSON(merged)
}

func mergeSettingsArtifact(rendered, existing []byte) ([]byte, error) {
	return mergeSettingsDocument(rendered, existing), nil
}

// jsonMember is one member of a JSON object as it was written: the quoted key
// and the value, both raw.
//
// The key is kept quoted rather than decoded because it is written back out.
// A decoded key re-encoded by Go's encoder acquires < for "<" and loses
// whatever escape the operator spelled it with, and the settings document is
// one cairn promises not to re-spell.
type jsonMember struct {
	// name is the member's decoded name, which is what two documents are
	// matched on. Two spellings of one key — "a" and "\u0061" — name the same
	// member, and matching on the bytes would write both into one object.
	name string

	// key is the member's name including its quotes, exactly as found.
	key []byte

	// value is the member's value, exactly as found.
	value json.RawMessage
}

// jsonObject reads raw as a JSON object, member by member in the order they
// were written, reporting false for anything that is not exactly one.
//
// Refused: a value that is not an object, trailing content after it, and an
// object declaring the same key twice. The duplicate is the one worth naming.
// Go's decoder resolves it silently — the last value wins and the document
// loses a member — and doing that to the operator's file would be cairn
// editing a document it cannot read. A merge that refuses leaves the render to
// overwrite it and the check to report it, which is what an operator can act
// on.
//
// The raw key is recovered from the input offsets rather than re-encoded from
// the decoded token: between the end of the previous token and the end of the
// key there can only be whitespace, one comma, and the quoted key itself.
func jsonObject(raw []byte) ([]jsonMember, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var members []jsonMember
	seen := make(map[string]struct{})
	for dec.More() {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, ok := tok.(string)
		if !ok {
			return nil, false
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, false
		}
		seen[name] = struct{}{}
		key := quotedKey(raw[start:dec.InputOffset()])
		if key == nil {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		members = append(members, jsonMember{name: name, key: key, value: value})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return members, true
}

// quotedKey returns the quoted key inside the span between two decoder
// offsets, or nil when the span holds anything else.
//
// The span is whitespace, at most one comma, whitespace, and the key. It is
// verified rather than assumed: this hands raw bytes back into a document, and
// a span that is not what it is expected to be should fail the merge rather
// than be written.
func quotedKey(span []byte) []byte {
	key := bytes.TrimSpace(span)
	if len(key) > 0 && key[0] == ',' {
		key = bytes.TrimSpace(key[1:])
	}
	if len(key) < 2 || key[0] != '"' || key[len(key)-1] != '"' {
		return nil
	}
	return key
}

// mergeJSONObjects composes the render over what was found, reporting false
// when either is not one readable JSON object.
func mergeJSONObjects(rendered, existing []byte) ([]byte, bool) {
	want, ok := jsonObject(rendered)
	if !ok {
		return nil, false
	}
	found, ok := jsonObject(existing)
	if !ok {
		return nil, false
	}
	return mergeMembers(want, found), true
}

// mergeMembers writes the merged object: every member found, in the order it
// was found, carrying the render's value where the render declares that key,
// and then every member only the render declares.
//
// Two objects at one key compose — that is the depth [mergeSettingsDocument]
// describes. Anything else at a declared key is replaced by the render, which
// is what makes a declared value cairn's: an array, a scalar and a null all
// take the render's spelling, and none of them is composed with what was there.
func mergeMembers(rendered, found []jsonMember) []byte {
	declared := make(map[string]json.RawMessage, len(rendered))
	for _, m := range rendered {
		declared[m.name] = m.value
	}
	kept := make(map[string]struct{}, len(found))

	var buf bytes.Buffer
	buf.WriteByte('{')
	write := func(key []byte, value []byte) {
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	for _, m := range found {
		value, ours := declared[m.name]
		if !ours {
			write(m.key, m.value)
			continue
		}
		kept[m.name] = struct{}{}
		write(m.key, mergeValue(value, m.value))
	}
	for _, m := range rendered {
		if _, already := kept[m.name]; already {
			continue
		}
		write(m.key, m.value)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// mergeValue composes one declared member's value over the value found at that
// key: member by member when both are objects, and the render's own bytes
// otherwise.
func mergeValue(rendered, existing json.RawMessage) []byte {
	want, ok := jsonObject(rendered)
	if !ok {
		return rendered
	}
	found, ok := jsonObject(existing)
	if !ok {
		return rendered
	}
	return mergeMembers(want, found)
}
