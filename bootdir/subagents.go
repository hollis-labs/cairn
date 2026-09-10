package bootdir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chrispian/cairn/profile"
	"gopkg.in/yaml.v3"
)

// SubagentFileExt is the extension a definition is planted with. The file's
// stem is the profile id, which is also what [SubagentNameKey] is forced to,
// so the two cannot drift apart.
const SubagentFileExt = ".md"

// SubagentNameKey is the frontmatter key naming the definition. Cairn writes
// the profile id there and refuses a declaration that says something else —
// see [renderSubagent].
const SubagentNameKey = "name"

// SubagentBodyKey is the one key of a subagent declaration that is not
// frontmatter: its value is the markdown below the fence. See [renderSubagent]
// for why the body is declared there rather than taken from the profile's own
// body field.
const SubagentBodyKey = "body"

// subagentFence opens and closes the frontmatter block.
const subagentFence = "---"

// ErrSubagentID reports an id that cannot name a definition file: it is empty,
// it is "." or "..", it holds a path separator or whitespace, or the manifest
// names it twice.
var ErrSubagentID = errors.New("invalid subagent id")

// ErrSubagentDeclaration reports a profile's subagent declaration that cannot
// be rendered: it is not a JSON object, it repeats a key, it declares a name
// that is not the profile's id, or its body is not a string.
var ErrSubagentDeclaration = errors.New("invalid subagent declaration")

// Subagent is one definition to render: the profile id a booting profile named
// under spec.subagents, and that profile's own declaration of what the
// definition holds.
//
// The declaration is carried rather than the profile it came from, and that is
// the whole of what crosses over: rendering a definition needs no other field
// of the profile it names, so no other field reaches the boot directory of the
// profile that named it.
type Subagent struct {
	// ID is the profile id. It is the definition file's stem and the value
	// [SubagentNameKey] is written with.
	ID string

	// Declaration is the named profile's resolved spec.subagent, a JSON
	// object. It is opaque: cairn transcribes it and reads only the two keys
	// it has to — see [renderSubagent].
	Declaration json.RawMessage
}

// renderSubagents returns one definition per profile the booting profile named
// under spec.subagents.
//
// The definitions arrive on the instance already resolved — see
// [Instance].Subagents. Looking one up means reading another profile out of
// the catalog and walking its extends chain, and a renderer does no I/O.
//
// A profile naming no subagents renders nothing and reports no error, and so
// does one whose definitions this tree has nowhere to plant. The
// output is deterministic: definitions in the order the resolved spec.subagents
// carries them — the order the manifest names them where one profile declares
// the key, key order where the cascade composed it — and within each,
// [SubagentNameKey] followed by the declaration's own key order.
func renderSubagents(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	if len(inst.Subagents) == 0 {
		return nil, nil
	}
	// Dropped rather than refused, for the reason a prompt is — see
	// [Undeclared]. The two are one decision: they are the same refusal over
	// two collections, and changing one would have moved the wall rather than
	// removed it.
	dir := strings.TrimSpace(inst.Layout.SubagentsDir)
	if dir == "" {
		return nil, nil
	}

	seen := make(map[string]struct{}, len(inst.Subagents))
	files := make([]File, 0, len(inst.Subagents))
	for _, sub := range inst.Subagents {
		id := strings.TrimSpace(sub.ID)
		if err := checkSubagentID(id); err != nil {
			return nil, err
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("%w: spec.%s names %q twice",
				ErrSubagentID, profile.SpecKeySubagents, id)
		}
		seen[id] = struct{}{}

		content, err := renderSubagent(id, sub.Declaration)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: path.Join(dir, id+SubagentFileExt), Content: content})
	}
	return files, nil
}

// renderSubagent returns one definition file: the declaration as frontmatter,
// and its body key as the markdown below the fence.
//
// # Everything is transcribed, two keys are read
//
// The declaration is the named profile's own, and cairn has no concept the
// harness's frontmatter is made of — no tool surface, no permission model, no
// depth. It transcribes what the profile wrote, JSON value by JSON value, and
// a key it has never heard of survives exactly like one it has. Transcription
// rather than a byte copy is forced by the destination: the manifest is JSON
// and frontmatter is YAML, so the values are carried by value where
// spec.settings is carried by byte.
//
// [SubagentNameKey] is the exception, and it is forced rather than trusted.
// The harness resolves a definition by its name field and not by its filename,
// so a name that is not the file's stem plants a definition under a name
// nothing named — and a declaration with no name at all is dropped by the
// harness with no diagnostic, which is the one failure here that is silent.
// A declaration that names something other than its own profile is refused
// rather than overwritten, because overwriting it would discard something the
// operator wrote.
//
// # The body
//
// [SubagentBodyKey] is lifted out of the frontmatter and written below the
// fence, which is where the harness reads a definition's prompt from.
//
// It is the declaration's own key rather than the named profile's cascaded
// body, and that is a decision about duplication. A subagent dispatched inside
// a boot directory is handed that directory's CLAUDE.md — verified against
// Claude Code 2.1.246, whose subagent queries carry the project instruction
// block unless the definition sets omitClaudeMd, which only its built-in
// definitions do. Cairn's CLAUDE.md includes AGENTS.md, so every ancestor body
// in the booting profile's cascade already reaches the subagent. Rendering the
// named profile's cascade here would put those same paragraphs in the
// definition a second time, and would put an ancestor's persona — "you
// implement one task end to end" — into a definition for a profile that
// reviews.
//
// Taking the named profile's own row body instead would mean reading a field
// that had skipped the cascade, in a package whose input is a [profile.Resolved]
// that carries no such thing. The declaration is where the definition is
// declared, so the body is declared there too, and a descendant that restates
// spec.subagent restates the body with it rather than inheriting one half of a
// definition from one profile and the other half from another.
//
// A declaration with no body renders frontmatter and nothing else. What a
// subagent is told is content, and content is not cairn's to require.
func renderSubagent(id string, declared json.RawMessage) ([]byte, error) {
	keys, values, err := jsonObjectFields(declared)
	if err != nil {
		return nil, fmt.Errorf("%w: spec.%s of profile %q: %w",
			ErrSubagentDeclaration, profile.SpecKeySubagent, id, err)
	}
	if err := checkSubagentName(id, values[SubagentNameKey]); err != nil {
		return nil, err
	}
	body, err := subagentBody(id, values[SubagentBodyKey])
	if err != nil {
		return nil, err
	}

	front := &yaml.Node{Kind: yaml.MappingNode}
	front.Content = append(front.Content, yamlString(SubagentNameKey), yamlString(id))
	for _, key := range keys {
		if key == SubagentNameKey || key == SubagentBodyKey {
			continue
		}
		value, err := yamlValue(values[key])
		if err != nil {
			return nil, fmt.Errorf("%w: spec.%s of profile %q: %q: %w",
				ErrSubagentDeclaration, profile.SpecKeySubagent, id, key, err)
		}
		front.Content = append(front.Content, yamlString(key), value)
	}

	var out bytes.Buffer
	out.WriteString(subagentFence + "\n")
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(front); err != nil {
		return nil, fmt.Errorf("%w: spec.%s of profile %q: %w",
			ErrSubagentDeclaration, profile.SpecKeySubagent, id, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("%w: spec.%s of profile %q: %w",
			ErrSubagentDeclaration, profile.SpecKeySubagent, id, err)
	}
	out.WriteString(subagentFence + "\n")
	if body = strings.Trim(body, "\r\n"); body != "" {
		out.WriteString("\n" + body + "\n")
	}
	return out.Bytes(), nil
}

// checkSubagentName reports a declared name that is not id. An absent name is
// no error: it is the ordinary case, and cairn writes the id there.
func checkSubagentName(id string, declared json.RawMessage) error {
	if len(bytes.TrimSpace(declared)) == 0 {
		return nil
	}
	var name string
	if err := json.Unmarshal(declared, &name); err != nil {
		return fmt.Errorf("%w: spec.%s of profile %q declares a %q that is not a string: %w",
			ErrSubagentDeclaration, profile.SpecKeySubagent, id, SubagentNameKey, err)
	}
	if name == id {
		return nil
	}
	return fmt.Errorf(
		"%w: spec.%s of profile %q declares %q %q, and a definition is resolved by that name rather than by its filename",
		ErrSubagentDeclaration, profile.SpecKeySubagent, id, SubagentNameKey, name)
}

// subagentBody returns the declared body. An absent key is an empty body; a
// value that is not a string is refused, because the alternative is planting
// something nobody wrote as a subagent's prompt.
func subagentBody(id string, declared json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(declared)) == 0 {
		return "", nil
	}
	var body string
	if err := json.Unmarshal(declared, &body); err != nil {
		return "", fmt.Errorf("%w: spec.%s of profile %q declares a %q that is not a string: %w",
			ErrSubagentDeclaration, profile.SpecKeySubagent, id, SubagentBodyKey, err)
	}
	return body, nil
}

// checkSubagentID rejects an id that cannot name one file in the definitions
// directory. An id holding a separator would reach outside it, and one holding
// whitespace cannot be the name the harness resolves.
func checkSubagentID(id string) error {
	switch {
	case id == "":
		return fmt.Errorf("%w: spec.%s holds an empty id", ErrSubagentID, profile.SpecKeySubagents)
	case id == "." || id == "..":
		return fmt.Errorf("%w: spec.%s holds %q", ErrSubagentID, profile.SpecKeySubagents, id)
	case strings.ContainsRune(id, '/'), strings.ContainsRune(id, filepath.Separator):
		return fmt.Errorf("%w: %q holds a path separator, so it does not name one definition file",
			ErrSubagentID, id)
	case strings.ContainsAny(id, " \t\r\n\v\f"):
		return fmt.Errorf("%w: %q holds whitespace", ErrSubagentID, id)
	}
	return nil
}

// subagentIDs returns the ids of subs, for a diagnostic that has to say what
// the manifest named.
func subagentIDs(subs []Subagent) []string {
	ids := make([]string, 0, len(subs))
	for _, sub := range subs {
		ids = append(ids, sub.ID)
	}
	return ids
}

// jsonObjectFields returns a JSON object's keys in the order they were written
// and its values by key.
//
// The order is kept because it is the operator's: a definition is a file they
// read, and sorting their keys would rewrite it for no gain, since the bytes
// it was declared as are fixed and so is the order they are walked in.
//
// A repeated key is refused. encoding/json would let the last one win, and a
// key that silently loses is a line of the operator's declaration that never
// reaches the file.
func jsonObjectFields(raw json.RawMessage) ([]string, map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if delim, ok := open.(json.Delim); !ok || delim != '{' {
		return nil, nil, fmt.Errorf("the declaration is %s, and a subagent declaration is an object",
			jsonShape(raw))
	}

	var keys []string
	values := make(map[string]json.RawMessage)
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, nil, fmt.Errorf("a key of the declaration is %v, which is not a string", token)
		}
		if _, repeated := values[key]; repeated {
			return nil, nil, fmt.Errorf("the key %q is declared twice", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, nil, err
		}
		keys = append(keys, key)
		values[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	return keys, values, nil
}

// jsonShape names what a JSON value is, for an error that has to say what
// arrived where an object was wanted.
func jsonShape(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "empty"
	}
	switch trimmed[0] {
	case '[':
		return "a list"
	case '"':
		return "a string"
	case 't', 'f':
		return "a boolean"
	case 'n':
		return "null"
	default:
		return "a number"
	}
}

// yamlValue returns a JSON value as the YAML node it is written as.
//
// Scalars other than strings carry their JSON text unchanged and no tag, so
// that a number is written exactly as the manifest spelled it and YAML's own
// resolution decides what it is. A string is tagged, so that "true" and "6"
// arrive at the harness as the strings they were declared as rather than as a
// boolean and an integer.
func yamlValue(raw json.RawMessage) (*yaml.Node, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("the value is empty")
	}
	switch trimmed[0] {
	case '{':
		keys, values, err := jsonObjectFields(trimmed)
		if err != nil {
			return nil, err
		}
		node := &yaml.Node{Kind: yaml.MappingNode}
		for _, key := range keys {
			value, err := yamlValue(values[key])
			if err != nil {
				return nil, fmt.Errorf("%q: %w", key, err)
			}
			node.Content = append(node.Content, yamlString(key), value)
		}
		return node, nil
	case '[':
		var elements []json.RawMessage
		if err := json.Unmarshal(trimmed, &elements); err != nil {
			return nil, err
		}
		node := &yaml.Node{Kind: yaml.SequenceNode}
		for i, element := range elements {
			value, err := yamlValue(element)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			node.Content = append(node.Content, value)
		}
		return node, nil
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		return yamlString(text), nil
	case 't', 'f', 'n':
		if !bytes.Equal(trimmed, []byte("true")) &&
			!bytes.Equal(trimmed, []byte("false")) &&
			!bytes.Equal(trimmed, []byte("null")) {
			return nil, fmt.Errorf("%s is not a JSON value", trimmed)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Value: string(trimmed)}, nil
	default:
		if _, err := strconv.ParseFloat(string(trimmed), 64); err != nil {
			return nil, fmt.Errorf("%s is not a JSON value", trimmed)
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Value: string(trimmed)}, nil
	}
}

// yamlString returns text as a YAML string node, tagged so that a value which
// reads as a number or a boolean is still written as a string.
func yamlString(text string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: text}
}
