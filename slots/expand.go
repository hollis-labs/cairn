package slots

import (
	"fmt"

	"github.com/hollis-labs/cairn/profile"
	"github.com/hollis-labs/libs/harness/core/agentcontext"
)

// Expander is [profile.Expander]: the environment a manifest's variables are
// expanded against, supplied by the composition root so that nothing here
// reads the process environment on its own.
type Expander = profile.Expander

// expandSources returns specs with $VAR and ${VAR} expanded in the two fields
// that name somewhere to read from: a static path and an HTTP URL.
//
// It is those two and nothing else, deliberately. A profile has to say where a
// service lives without hardcoding a host that differs between machines, and
// without cairn growing a second configuration file to hold one. Expanding a
// command line as well would make the environment able to rewrite what runs,
// which is a larger promise than "say where to read from" — a cmd slot already
// runs through a shell that does its own expansion, so nothing is lost by
// leaving it alone.
//
// The specs are copied rather than rewritten: the caller's manifest is what a
// diagnostic quotes, and an error naming an expanded value would name something
// the operator never wrote.
func expandSources(specs []agentcontext.SlotSpec, look Expander) []agentcontext.SlotSpec {
	out := make([]agentcontext.SlotSpec, len(specs))
	copy(out, specs)
	for i := range out {
		out[i].Source = expandSource(out[i].Source, look)
	}
	return out
}

// expandSource is [expandSources] for one source.
func expandSource(src agentcontext.SlotSource, look Expander) agentcontext.SlotSource {
	src.StaticFile.Path = profile.ExpandEnv(src.StaticFile.Path, look)
	src.StaticDir.Path = profile.ExpandEnv(src.StaticDir.Path, look)
	src.RoleSummary.Path = profile.ExpandEnv(src.RoleSummary.Path, look)
	src.HTTPText.URL = profile.ExpandEnv(src.HTTPText.URL, look)
	src.HTTPJSON.URL = profile.ExpandEnv(src.HTTPJSON.URL, look)
	return src
}

// sourceValue returns what a source names to read from, and what that field is
// called in a manifest. A kind that names nothing — inline, cmd, skill_index —
// returns empty strings.
//
// It is the same set [expandSource] rewrites, read from one switch so that a
// field which gains expansion cannot gain it without also gaining a
// diagnostic, or the reverse.
func sourceValue(src agentcontext.SlotSource) (field, value string) {
	switch src.Kind {
	case agentcontext.SlotSourceKindStaticFile:
		return "static_file path", src.StaticFile.Path
	case agentcontext.SlotSourceKindStaticDir:
		return "static_dir path", src.StaticDir.Path
	case agentcontext.SlotSourceKindRoleSummary:
		return "role_summary path", src.RoleSummary.Path
	case agentcontext.SlotSourceKindHTTPText:
		return "http_text url", src.HTTPText.URL
	case agentcontext.SlotSourceKindHTTPJSON:
		return "http_json url", src.HTTPJSON.URL
	}
	return "", ""
}

// Expansions returns, for each slot or entry under key whose source names a
// value that expansion changed, a phrase naming what the operator wrote and
// what it became.
//
// It exists because the information is lost otherwise, and the loss is cairn's
// rather than the library's. Expansion happens here, before the request is
// built, so a resolver is handed "/process.md" and reports "/process.md" —
// which is exactly what it was asked for and all it can say. Only cairn ever
// held both forms, and a caller reporting a slot that failed has nothing to
// name the variable with unless it is given this.
//
// A source whose value expansion did not change is absent, so the common slot —
// one with no variable in it — reports exactly as it did before.
func Expansions(spec profile.Spec, key string, look Expander) (map[string]string, error) {
	sources, err := sourcesOf(spec, key)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(sources))
	for name, src := range sources {
		field, declared := sourceValue(src)
		if field == "" || declared == "" {
			continue
		}
		expanded := profile.ExpandEnv(declared, look)
		if expanded == declared {
			continue
		}
		out[name] = fmt.Sprintf("the %s is %s", field, profile.QuotedExpansion(declared, expanded))
	}
	return out, nil
}

// sourcesOf returns the sources under key, by the name each failure is
// reported against: a slot's name, or a files or templates entry's path.
func sourcesOf(spec profile.Spec, key string) (map[string]agentcontext.SlotSource, error) {
	out := make(map[string]agentcontext.SlotSource)
	if key == profile.SpecKeySlots {
		declared, err := spec.Slots()
		if err != nil {
			return nil, err
		}
		for _, slot := range declared {
			out[slot.Name] = slot.Source
		}
		return out, nil
	}
	declared, err := entriesOf(spec, key)
	if err != nil {
		return nil, err
	}
	for rel, entry := range declared {
		if entry.IsSource() {
			out[rel] = *entry.Source
		}
	}
	return out, nil
}
