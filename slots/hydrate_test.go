package slots_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/chrispian/cairn/slots"
	"github.com/chrispian/cairn/template"
)

// writeText writes a fixture file the hydrator reads.
func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// sourcesOf parses text and returns its inline sources.
func sourcesOf(t *testing.T, name, text string) []template.Source {
	t.Helper()
	doc, err := template.Parse(name, text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return template.Sources(doc)
}

func TestHydrateSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeText(t, dir+"/note.md", "the file said this\n")

	srcs := sourcesOf(t, "p", ""+
		"{{ cmd: echo the command said this }}\n"+
		"{{ file: "+dir+"/note.md }}\n"+
		"{{ inline: the inline said this }}\n")

	got, failures, err := slots.HydrateSources(ctx, srcs, slots.Options{})
	if err != nil {
		t.Fatalf("HydrateSources: %v", err)
	}
	if len(failures) != 0 {
		t.Errorf("failures = %v, want none", failures)
	}
	for i, want := range []string{"the command said this", "the file said this", "the inline said this"} {
		if text := got[srcs[i].ID]; !strings.Contains(text, want) {
			t.Errorf("source %d = %q, want it to carry %q", i, text, want)
		}
	}
}

// A source that fails is recorded and does not fail the hydration, which is a
// slot's answer rather than a file's: a missing section is degraded context,
// and the operator hears about it.
func TestHydrateSourcesReportsAFailureWithoutFailing(t *testing.T) {
	ctx := context.Background()
	srcs := sourcesOf(t, "p", "{{ file: "+t.TempDir()+"/never-written.md }}\n")

	got, failures, err := slots.HydrateSources(ctx, srcs, slots.Options{})
	if err != nil {
		t.Fatalf("HydrateSources: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want one", failures)
	}
	// The document and the line, because a diagnostic about a template has to
	// say where in the template.
	if !strings.Contains(failures[0], "p: line 1") {
		t.Errorf("failure = %q, want it to name the document and line", failures[0])
	}
	if _, resolved := got[srcs[0].ID]; resolved {
		t.Error("a failed source produced text")
	}
}

// A kind nothing resolves is refused rather than rendered as nothing. It is
// the opposite of a source that resolved to nothing: an unknown kind is a word
// that will never resolve on any machine.
func TestHydrateSourcesRefusesAnUnknownKind(t *testing.T) {
	ctx := context.Background()
	srcs := sourcesOf(t, "p", "{{ tesseract: recall cairn }}\n")

	_, _, err := slots.HydrateSources(ctx, srcs, slots.Options{})
	if !errors.Is(err, slots.ErrSourceKind) {
		t.Fatalf("HydrateSources = %v, want ErrSourceKind", err)
	}
	if !strings.Contains(err.Error(), `"cmd"`) {
		t.Errorf("the diagnostic does not name the kinds: %v", err)
	}
}

// The installed layer resolves only the kinds whose answer changes when the
// operator changes something, and names the ones it skipped.
func TestHydrateSourcesDeterministic(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeText(t, dir+"/note.md", "read from disk\n")
	srcs := sourcesOf(t, "p", "{{ cmd: echo ran a command }}\n{{ file: "+dir+"/note.md }}\n")

	got, _, err := slots.HydrateSources(ctx, srcs, slots.Options{Deterministic: true})
	if err != nil {
		t.Fatalf("HydrateSources: %v", err)
	}
	if _, ran := got[srcs[0].ID]; ran {
		t.Error("a deterministic hydration ran a cmd source")
	}
	if text := got[srcs[1].ID]; !strings.Contains(text, "read from disk") {
		t.Errorf("the static source did not resolve: %q", text)
	}

	skipped, err := slots.NondeterministicSources(srcs, nil)
	if err != nil {
		t.Fatalf("NondeterministicSources: %v", err)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "cmd") {
		t.Errorf("NondeterministicSources = %v, want the cmd source alone", skipped)
	}
}

// A source with no argument is refused: an inline source is KIND: ARG, and a
// kind with nothing after the colon names nothing to read.
func TestHydrateSourcesRefusesAnEmptyArgument(t *testing.T) {
	ctx := context.Background()
	srcs := sourcesOf(t, "p", "{{ cmd: }}\n")

	if _, _, err := slots.HydrateSources(ctx, srcs, slots.Options{}); !errors.Is(err, slots.ErrSourceKind) {
		t.Fatalf("HydrateSources = %v, want ErrSourceKind", err)
	}
}

// Nothing to hydrate makes no calls and is not an error.
func TestHydrateSourcesWithNone(t *testing.T) {
	got, failures, err := slots.HydrateSources(context.Background(), nil, slots.Options{})
	if err != nil || got != nil || failures != nil {
		t.Errorf("HydrateSources(nil) = %v, %v, %v", got, failures, err)
	}
}
