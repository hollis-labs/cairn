package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hollis-labs/cairn/profile"
	"gopkg.in/yaml.v3"
)

// durationKey is the field name a [time.Duration] arrives under. Three of
// agentcontext's slot sources carry one — cmd, http_text and http_json — and
// all three spell it this way.
const durationKey = "timeout"

// decodeSpec converts a profile's YAML manifest into the JSON one cairn
// carries, one [json.RawMessage] per top-level key.
//
// Frontmatter is YAML because a profile is authored by hand; a spec is JSON
// because every shape it decodes into is a Go struct with JSON tags. The
// conversion is the whole of what this does, and it is why a slot written
// `kind:` in a profile lands as "kind" — the YAML habit that produces a slot
// with no kind at all in a hand-written manifest never reaches the operator.
//
// A key cairn has never heard of is converted like any other and carried. What
// this does not do is read one: nothing here validates a manifest key, and the
// only value it changes the type of is a duration — see [parseDuration].
func decodeSpec(node *yaml.Node) (profile.Spec, error) {
	out := profile.Spec{}
	if node.Tag == "!!null" {
		return out, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("the manifest is not a mapping")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		walk := &specWalk{durations: normalizesDurations(key.Value), expanding: map[*yaml.Node]bool{}}
		converted, err := walk.value(value, "spec."+key.Value)
		if err != nil {
			return nil, err
		}
		raw, err := encodeJSON(converted)
		if err != nil {
			return nil, fmt.Errorf("spec.%s: %w", key.Value, err)
		}
		out[key.Value] = raw
	}
	return out, nil
}

// normalizesDurations reports whether a duration written under a manifest key
// is translated, which is true of the keys whose values cairn hands to a
// library struct carrying a [time.Duration].
//
// It is a list of keys rather than "every field called timeout", and the
// difference matters in one direction. spec.settings, spec.subagent and every
// key cairn does not render are documents somebody else reads, and rewriting a
// string into a number inside one of them would hand the harness a value its
// own schema does not accept — silently, because a wrong duration is a wrong
// number and not an error. The seeder this replaces translated by field name
// alone and had that hazard; nothing in the portfolio had tripped it yet.
//
// The list being hardcoded is what makes it safe to hardcode: the two ways of
// being wrong about it fail differently. A key missing from it that should be
// here fails loudly and immediately — "5s" reaches a time.Duration field and
// encoding/json refuses to unmarshal a string into one, on the first boot, by
// name. A key here that should not be is the silent case above. So the list
// errs toward the failure that reports itself, and a key added to it later
// arrives because a decode error asked for it.
func normalizesDurations(specKey string) bool {
	switch specKey {
	case profile.SpecKeySlots, profile.SpecKeyFiles, profile.SpecKeyTemplates:
		return true
	}
	return false
}

// maxAliasHops bounds an alias chain. It is a guard and not a limit anybody
// should reach: aliases pointing at aliases are legal, and a document with
// this many of them in a row is not one an operator wrote.
const maxAliasHops = 64

// specWalk is one manifest key's conversion: whether a duration underneath it
// is translated, and which anchors are currently being expanded.
//
// The second is a cycle guard, and it is needed because [yaml.Unmarshal] into
// a [yaml.Node] happily builds the node tree for a self-referential anchor —
// `a: &x [*x]` parses, and only Decode refuses it with "anchor 'x' value
// contains itself". Nothing below Decode is walking that tree except this, so
// without the guard a profile nobody would write on purpose is a stack
// overflow rather than a diagnostic.
type specWalk struct {
	durations bool
	expanding map[*yaml.Node]bool
}

// value converts one YAML node into the Go value that JSON-encodes to what the
// manifest means. where names the node for a diagnostic, as a manifest path.
//
// A mapping becomes a map and not an ordered structure, so the encoder writes
// its keys sorted: two profiles declaring the same document declare the same
// bytes whatever order they were typed in.
func (w *specWalk) value(node *yaml.Node, where string) (any, error) {
	switch node.Kind {
	case yaml.AliasNode:
		if node.Alias == nil {
			return nil, fmt.Errorf("%s: line %d: the alias %q names no anchor",
				where, node.Line+frontmatterLineOffset, node.Value)
		}
		if w.expanding[node.Alias] {
			return nil, fmt.Errorf("%s: line %d: the anchor %q contains itself",
				where, node.Line+frontmatterLineOffset, node.Value)
		}
		w.expanding[node.Alias] = true
		defer delete(w.expanding, node.Alias)
		return w.value(node.Alias, where)

	case yaml.MappingNode:
		out := make(map[string]any, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("%s: line %d: a key has to be a scalar", where, key.Line+frontmatterLineOffset)
			}
			at := where + "." + key.Value
			// The alias is followed before the tag is read, so that a duration
			// written once as an anchor and used twice is translated at both
			// uses. Reading the tag off the alias node instead would see
			// "!!alias", leave the string alone, and hand a time.Duration
			// field a string — which fails, but fails as an unmarshal error
			// naming a Go type rather than as anything the operator wrote.
			if declared := deref(value); w.durations && key.Value == durationKey && declared.Tag == "!!str" {
				nanos, err := parseDuration(at, declared)
				if err != nil {
					return nil, err
				}
				out[key.Value] = nanos
				continue
			}
			converted, err := w.value(value, at)
			if err != nil {
				return nil, err
			}
			out[key.Value] = converted
		}
		return out, nil

	case yaml.SequenceNode:
		out := make([]any, 0, len(node.Content))
		for i, item := range node.Content {
			converted, err := w.value(item, fmt.Sprintf("%s[%d]", where, i))
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		return out, nil

	default:
		var scalar any
		if err := node.Decode(&scalar); err != nil {
			return nil, fmt.Errorf("%s: line %d: %w", where, node.Line+frontmatterLineOffset, err)
		}
		return scalar, nil
	}
}

// deref follows an alias to the node it names, and keeps following while that
// node is an alias too. The hop count is bounded because the tree can be
// genuinely cyclic — see [specWalk] — and this is the cheap half of that
// guard: a chain that runs out of hops stops on an alias node, which is not a
// scalar, so the caller simply does not treat it as a duration and
// [specWalk.value] refuses it by name a moment later.
func deref(node *yaml.Node) *yaml.Node {
	for hop := 0; node.Kind == yaml.AliasNode && node.Alias != nil && hop < maxAliasHops; hop++ {
		node = node.Alias
	}
	return node
}

// parseDuration turns a duration written the way Go writes one into the number
// of nanoseconds a [time.Duration] unmarshals from.
//
// This translation is the seeder's one irreplaceable job, moved from Python
// into Go. `json.Unmarshal` reads a [time.Duration] from a number of
// nanoseconds and not from "5s", and every duration in the portfolio's YAML is
// written "5s" — so without this the operator either writes 5000000000 or
// writes 5s and gets five nanoseconds. It is exactly the second one that makes
// this worth a function and a test: a wrong duration is a wrong number, not an
// error, and nothing downstream can tell it from a deliberate one.
//
// [time.ParseDuration] is the parser rather than a hand-written one, so what a
// profile may write is what Go writes: "5s", "300ms", "1m30s", "2h", and the
// compound forms in between. A duration written as a bare number is left as
// the number of nanoseconds it already is, which is what the manifest format
// means by one.
func parseDuration(where string, node *yaml.Node) (int64, error) {
	text := strings.TrimSpace(node.Value)
	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("%s: line %d: %q is not a duration — write it the way Go writes one: 5s, 300ms, 1m30s",
			where, node.Line+frontmatterLineOffset, node.Value)
	}
	return int64(d), nil
}

// encodeJSON serializes a converted manifest value with HTML escaping off, for
// the reason profile.encodeJSON has it off: "&", "<" and ">" are ordinary
// characters in a path, a matcher and a command line, and a manifest carries
// what the operator wrote rather than a re-spelling of it.
func encodeJSON(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}
