package main

import (
	"context"
	"fmt"
	"io"

	"github.com/chrispian/cairn/catalog"
	"github.com/chrispian/cairn/profile"
	"github.com/chrispian/cairn/slots"
	"github.com/chrispian/cairn/template"
)

// renderDocument renders a resolved profile's body chain into the one
// instruction document both layers plant.
//
// It is the composition root's job and it is here for the reason every other
// resolution is: hydrating an inline source runs a command, reads a file or
// makes a request, and neither a renderer nor package template may do any of
// those. What goes down to a layer is text.
//
// Rendered once and handed to both layers by the caller, so that
// `cairn install` and `cairn boot` cannot write two different instruction
// documents out of one profile.
//
// A profile whose chain declares no body renders the empty string, and the
// tree plants no instruction file for it. That is the promise cairn's identity
// rests on: nothing here requires a profile to have a document, because
// nothing here can recognize one.
//
// Everything the render reports — a value naming nothing, a source that came
// back empty, prose that did not render, a section nobody yields — goes to
// stderr as it happens. None of it fails the render, and none of it is
// silent: the document is written either way, and an operator has no other
// way to learn that a block is missing from the file an agent reads.
func renderDocument(ctx context.Context, cat *catalog.Catalog, resolved *profile.Resolved,
	values map[string]string, opts slots.Options, stderr io.Writer) (string, error) {

	docs := make([]*template.Document, 0, len(resolved.Bodies))
	for _, body := range resolved.Bodies {
		doc, err := template.Parse(body.ID, body.Text)
		if err != nil {
			return "", err
		}
		docs = append(docs, doc)
	}
	if len(docs) == 0 {
		return "", nil
	}

	sources := template.Sources(docs...)
	hydrated, failures, err := slots.HydrateSources(ctx, sources, opts)
	if err != nil {
		return "", fmt.Errorf("profile %q: %w", resolved.ID, err)
	}
	for _, line := range failures {
		_, _ = fmt.Fprintf(stderr, "cairn: %s\n", line)
	}
	// The sources this layer did not run at all, named once rather than as
	// one puzzling empty block per tag.
	if opts.Deterministic {
		skipped, err := slots.NondeterministicSources(sources, opts.Env)
		if err != nil {
			return "", fmt.Errorf("profile %q: %w", resolved.ID, err)
		}
		if len(skipped) > 0 {
			_, _ = fmt.Fprintf(stderr,
				"cairn: the installed layer resolves no inline source at %s: it resolves only %s, "+
					"because a check re-renders and anything else would report drift on every run\n",
				joinNames(skipped), kindList(slots.DeterministicKinds()))
		}
	}

	result, err := template.Render(docs, template.Options{
		Layouts: layoutLoader(cat),
		Values:  values,
		Sources: hydrated,
		Report:  func(line string) { _, _ = fmt.Fprintf(stderr, "cairn: %s\n", line) },
	})
	if err != nil {
		return "", fmt.Errorf("profile %q: %w", resolved.ID, err)
	}
	return result.Text, nil
}

// layoutLoader resolves a template's `{{ extends }}` against the bundle,
// naming the file cairn looked for when there is none.
//
// The catalog holds a layout's text unparsed and this parses it, so the
// bundle stays a directory of files and the syntax stays in one package. A
// name the bundle has no file for reports what it looked for and what the
// bundle does hold, which is the diagnostic an author who mistyped a layout
// name needs.
func layoutLoader(cat *catalog.Catalog) template.LayoutLoader {
	return template.LayoutFunc(func(name string) (*template.Document, error) {
		text, ok := cat.LayoutText(name)
		if !ok {
			path, err := cat.LayoutPath(name)
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w: no file at %s; this bundle holds %s",
				template.ErrLayout, path, joinNames(cat.Layouts()))
		}
		return template.Parse(name, text)
	})
}

// joinNames renders a list for a diagnostic, saying so when it is empty.
func joinNames(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%q", n)
	}
	return out
}
