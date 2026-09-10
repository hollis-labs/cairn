package slots

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/chrispian/cairn/profile"
	"github.com/chrispian/cairn/template"
	"github.com/hollis-labs/agentkit/agentcontext"
)

// ErrSourceKind reports an inline template source whose kind nothing here
// resolves.
//
// It is refused rather than rendered as nothing, which is the opposite of what
// a source that resolves to nothing gets. The two are different mistakes: a
// command that answered with no output is a fact about this materialization,
// and a kind cairn cannot resolve is a word that will never resolve on any
// machine, in a document an agent reads as instructions.
var ErrSourceKind = errors.New("invalid inline source kind")

// SourceAlias is the one spelling this package accepts that is not an
// agentcontext kind name.
//
// `file` reads better in a template than `static_file` and is what the design
// wrote, so it is accepted. One alias and not a table of them: an alias is a
// second name for a thing that already has one, and a template engine whose
// vocabulary has two halves is one where an author has to know which half
// they are in.
const SourceAlias = "file"

// HydrateSources resolves every inline source a template declares, keyed by
// [template.Source].ID for [template.Options].Sources.
//
// This is the hydrate half of the seam, and it is why the render half can be
// pure: a template's `{{ cmd: … }}` runs a command HERE, in a package the
// caller reached deliberately, and package template never learns that
// commands exist. Handing a renderer a callback that could resolve one would
// spend that guarantee for the convenience of one call site.
//
// # A failed source does not fail the render
//
// It records the failure and renders nothing in its place, which is a slot's
// answer rather than a file's. The reasoning is the one
// [DropUnresolved] already carries: a missing section is degraded context and
// an agent that needs the data asks its tools, where a missing FILE is a hole
// at a path the profile promised and nothing downstream notices. An inline
// source is a section, so it gets a section's answer — and because a body
// that empties itself renders no file at all, a whole document failing is
// still visible.
//
// Every failure is returned in failures for the caller to report. Silence is
// the one thing this must not do: the section is absent from the file and
// nothing in the file says so.
//
// A kind nothing resolves is the exception and is an error — see
// [ErrSourceKind].
func HydrateSources(ctx context.Context, sources []template.Source, opts Options) (
	text map[string]string, failures []string, err error) {

	if len(sources) == 0 {
		return nil, nil, nil
	}
	// The installed layer resolves only the kinds whose answer changes when
	// the operator changes something, for the reason [Assemble] does: a check
	// re-renders and diffs against disk, so a `{{ cmd: git status }}` in a
	// shared body would report drift on every run. A source left out is
	// reported by [NondeterministicSources] rather than here, because the
	// caller is the one that knows which layer it is rendering.
	if opts.Deterministic {
		allowed := DeterministicKinds()
		kept := make([]template.Source, 0, len(sources))
		for _, src := range sources {
			source, err := sourceFor(src, opts.Env)
			if err != nil {
				return nil, nil, err
			}
			if slices.Contains(allowed, source.Kind) {
				kept = append(kept, src)
			}
		}
		if len(kept) == 0 {
			return nil, nil, nil
		}
		sources = kept
	}

	req := agentcontext.ContextRequest{
		Workdir:    opts.Workdir,
		Provenance: opts.Provenance,
		Slots:      make([]agentcontext.SlotSpec, 0, len(sources)),
	}
	for _, src := range sources {
		source, err := sourceFor(src, opts.Env)
		if err != nil {
			return nil, nil, err
		}
		// Named by the source's id rather than by anything an author wrote:
		// two identical sources in one document are two sources, and a name
		// taken from the text would collapse them.
		req.Slots = append(req.Slots, agentcontext.SlotSpec{Name: src.ID, Source: source})
	}

	provider := opts.Provider
	if provider == nil {
		if provider, err = defaultProvider(); err != nil {
			return nil, nil, err
		}
	}
	result, err := provider.Assemble(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("hydrate inline template sources: %w", err)
	}
	if result == nil {
		return nil, nil, nil
	}

	byID := make(map[string]template.Source, len(sources))
	for _, src := range sources {
		byID[src.ID] = src
	}
	text = make(map[string]string, len(result.Slots))
	for _, s := range result.Slots {
		src := byID[s.Name]
		if s.Err != nil {
			failures = append(failures, fmt.Sprintf("%s: line %d: %s did not resolve: %v",
				src.Document, src.Line, src.Raw, s.Err))
			continue
		}
		text[s.Name] = strings.TrimRight(s.Content, "\n")
	}
	return text, failures, nil
}

// sourceFor builds one [agentcontext.SlotSource] from a template's inline
// source.
//
// The argument goes into whichever field that kind reads, and this is the
// whole of the mapping: an inline source is one line, so it carries the one
// parameter that names what to read and takes the resolver's own defaults for
// everything else. A source that needs a timeout, a header or a glob is a
// spec.slots member, which is the shape that has fields.
func sourceFor(src template.Source, look Expander) (agentcontext.SlotSource, error) {
	kind := src.Kind
	if kind == SourceAlias {
		kind = string(agentcontext.SlotSourceKindStaticFile)
	}
	arg := src.Arg
	if arg == "" {
		return agentcontext.SlotSource{}, fmt.Errorf(
			"%w: %s: line %d: %s names no argument, and a source is KIND: ARG",
			ErrSourceKind, src.Document, src.Line, src.Raw)
	}

	out := agentcontext.SlotSource{Kind: agentcontext.SlotSourceKind(kind)}
	switch out.Kind {
	case agentcontext.SlotSourceKindCmd:
		out.Cmd.Run = arg
	case agentcontext.SlotSourceKindStaticFile:
		out.StaticFile.Path = profile.ExpandEnv(arg, look)
	case agentcontext.SlotSourceKindStaticDir:
		out.StaticDir.Path = profile.ExpandEnv(arg, look)
	case agentcontext.SlotSourceKindInline:
		out.Inline.Content = arg
	case agentcontext.SlotSourceKindHTTPText:
		out.HTTPText.URL = profile.ExpandEnv(arg, look)
	case agentcontext.SlotSourceKindHTTPJSON:
		out.HTTPJSON.URL = profile.ExpandEnv(arg, look)
	case agentcontext.SlotSourceKindRoleSummary:
		out.RoleSummary.Path = profile.ExpandEnv(arg, look)
	default:
		return agentcontext.SlotSource{}, fmt.Errorf(
			"%w: %s: line %d: %s declares the kind %q; the kinds are %s",
			ErrSourceKind, src.Document, src.Line, src.Raw, src.Kind, inlineKinds())
	}
	return out, nil
}

// NondeterministicSources returns the inline sources a deterministic
// hydration leaves alone, each named with the kind that excluded it.
//
// It is a question rather than an error beside a usable result, exactly as
// [Nondeterministic] is: the render is complete and correct, and the sections
// left out are a fact the operator has to be told.
func NondeterministicSources(sources []template.Source, look Expander) ([]string, error) {
	allowed := DeterministicKinds()
	var out []string
	for _, src := range sources {
		source, err := sourceFor(src, look)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(allowed, source.Kind) {
			out = append(out, fmt.Sprintf("%s line %d (%s)", src.Document, src.Line, source.Kind))
		}
	}
	slices.Sort(out)
	return out, nil
}

// inlineKinds renders the kinds an inline source may declare, for a
// diagnostic. It is [wiredKinds] plus the one alias, so a kind the resolver
// map gains turns up here rather than in a second list to keep in step.
func inlineKinds() string {
	quoted := make([]string, 0, len(wiredKinds)+1)
	for _, k := range wiredKinds {
		quoted = append(quoted, fmt.Sprintf("%q", string(k)))
	}
	quoted = append(quoted, fmt.Sprintf("%q (for %q)", SourceAlias, agentcontext.SlotSourceKindStaticFile))
	slices.Sort(quoted)
	return strings.Join(quoted, ", ")
}
