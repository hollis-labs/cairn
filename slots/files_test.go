package slots_test

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/profile"
	"github.com/hollis-labs/cairn/slots"
	"github.com/hollis-labs/libs/harness/core/agentcontext"
)

// TestResolveFilesCarriesBothForms is the manifest key's whole contract in one
// profile: a literal is planted as written, and a source is resolved at
// materialization the way a slot is. Both land at the path the manifest named,
// which is the point — a boot directory is a set of paths a profile promised.
func TestResolveFilesCarriesBothForms(t *testing.T) {
	t.Parallel()

	onDisk := filepath.Join(t.TempDir(), "process.md")
	if err := os.WriteFile(onDisk, []byte("# implement\n\nread the diff first\n"), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}

	spec := manifest(t, filed(`{
	  "notes/scratch.md":       "a literal, planted as written\n",
	  "tasks/T-1/task.md":      {"kind":"cmd","cmd":{"run":"printf '# T-1\\nin progress\\n'"}},
	  "tasks/T-1/process.md":   {"kind":"static_file","static_file":{"path":"`+onDisk+`"}},
	  "tasks/T-1/task.json":    {"kind":"inline","inline":{"content":"{\"id\":\"T-1\"}"}}
	}`))

	got, err := slots.ResolveFiles(t.Context(), spec, slots.Options{})
	if err != nil {
		t.Fatalf("ResolveFiles(): %v", err)
	}
	want := map[string]string{
		"notes/scratch.md":     "a literal, planted as written\n",
		"tasks/T-1/task.md":    "# T-1\nin progress\n",
		"tasks/T-1/process.md": "# implement\n\nread the diff first\n",
		"tasks/T-1/task.json":  `{"id":"T-1"}`,
	}
	if !maps.Equal(got, want) {
		t.Errorf("ResolveFiles() =\n%#v\nwant\n%#v", got, want)
	}
}

// TestAFileSourceThatFailsFailsTheBoot is the deliberate opposite of a slot,
// and the reason the two are not one mechanism.
//
// A slot that does not resolve leaves a section out of boot.md and the agent
// asks its tools instead. A file that does not resolve leaves a hole at a path
// the profile promised, and nothing downstream can notice: whatever reads that
// path finds nothing there and has no way to tell "the profile never declared
// it" from "the command that fills it fell over".
func TestAFileSourceThatFailsFailsTheBoot(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "never-written.md")
	cases := map[string]string{
		"a file that is not there":      `{"kind":"static_file","static_file":{"path":"` + missing + `"}}`,
		"a command that exits non-zero": `{"kind":"cmd","cmd":{"run":"exit 3"}}`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			spec := manifest(t, filed(`{
			  "notes/fine.md":        "this one is a literal and resolves",
			  "tasks/T-1/task.md":    `+source+`
			}`))

			got, err := slots.ResolveFiles(t.Context(), spec, slots.Options{})
			if !errors.Is(err, slots.ErrFileSource) {
				t.Fatalf("ResolveFiles() = %v, want ErrFileSource", err)
			}
			if got != nil {
				t.Errorf("ResolveFiles() returned %v alongside the error, want nothing to plant", got)
			}
			// The path is the whole point of the diagnostic: the library's own
			// error knows the resolver's problem and not where the content was
			// going.
			if !strings.Contains(err.Error(), `"tasks/T-1/task.md"`) {
				t.Errorf("the error does not name the path it was going to write: %v", err)
			}
			if !strings.Contains(err.Error(), "spec."+profile.SpecKeyFiles) {
				t.Errorf("the error does not name the manifest key it came from: %v", err)
			}
		})
	}
}

// TestAFileSourceThatResolvesEmptyPlantsAnEmptyFile states the line between
// the rule above and this one. The resolver was reached and it answered; that
// the answer was empty is content, and content is a black box here.
//
// This is why the request declares its slots non-required: the library's
// Required flag fails an assembly on an empty result as well as on a failed
// one, and conflating the two would turn a task list that is legitimately
// empty into a boot that will not start.
func TestAFileSourceThatResolvesEmptyPlantsAnEmptyFile(t *testing.T) {
	t.Parallel()

	spec := manifest(t, filed(`{"tasks/none.md": {"kind":"cmd","cmd":{"run":"true"}}}`))

	got, err := slots.ResolveFiles(t.Context(), spec, slots.Options{})
	if err != nil {
		t.Fatalf("ResolveFiles() = %v, want an empty file rather than a refusal", err)
	}
	if want := map[string]string{"tasks/none.md": ""}; !maps.Equal(got, want) {
		t.Errorf("ResolveFiles() = %#v, want %#v", got, want)
	}
}

// TestTheFilesKindDiagnosticNamesThePath covers the YAML habit at the other
// place a source can be written. An operator who moves a working source out of
// the slots key and into a files entry carries the habit with it, and the
// library's answer — "unknown slot kind" — names neither the key nor the path.
func TestTheFilesKindDiagnosticNamesThePath(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ entry, wants string }{
		"a source copied out of a YAML profile": {
			entry: `{"type":"inline","inline":{"content":"x"}}`,
			wants: `"type"`,
		},
		"a source with no kind at all": {
			entry: `{"inline":{"content":"x"}}`,
			wants: `declares no "kind"`,
		},
		"a kind that is not a kind": {
			entry: `{"kind":"sqlite"}`,
			wants: `"sqlite"`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// A provider that must never run: a manifest this shape is refused
			// before anything is resolved.
			provider := &fakeProvider{err: errors.New("provider must not be called")}
			spec := manifest(t, filed(`{"tasks/T-1/task.md": `+tc.entry+`}`))

			_, err := slots.ResolveFiles(t.Context(), spec, slots.Options{Provider: provider})
			if !errors.Is(err, slots.ErrSlotKind) {
				t.Fatalf("ResolveFiles() = %v, want ErrSlotKind", err)
			}
			if !strings.Contains(err.Error(), `spec.files entry "tasks/T-1/task.md"`) {
				t.Errorf("the error does not name the entry: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the error does not carry %q: %v", tc.wants, err)
			}
			if provider.calls != 0 {
				t.Errorf("the provider was called %d times for a manifest that cannot resolve", provider.calls)
			}
		})
	}
}

// TestResolveFilesRefusesAValueThatIsNeitherFormByName covers the manifest
// whose entry is a number, a list, or a null. Coercing any of those silently
// would plant bytes nobody wrote at a path a profile promised.
func TestResolveFilesRefusesAValueThatIsNeitherFormByName(t *testing.T) {
	t.Parallel()

	for _, value := range []string{`42`, `["a","b"]`, `true`, `null`} {
		spec := manifest(t, filed(`{"notes/wrong.md": `+value+`}`))

		_, err := slots.ResolveFiles(t.Context(), spec, slots.Options{})
		if err == nil {
			t.Fatalf("ResolveFiles() accepted the value %s", value)
		}
		if !strings.Contains(err.Error(), "notes/wrong.md") {
			t.Errorf("the error for %s does not name the path: %v", value, err)
		}
	}
}

// TestResolveFilesMakesNoCallWhenThereIsNothingToResolve covers the two cases
// that reach no resolver: a manifest with no files key, and one whose entries
// are all literals. Cairn does not run an operator's commands to discover that
// it had none to run.
func TestResolveFilesMakesNoCallWhenThereIsNothingToResolve(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		manifest string
		want     map[string]string
	}{
		"the key is absent": {manifest: `{"slots": []}`},
		"the key is null":   {manifest: filed(`null`)},
		"the key is empty":  {manifest: filed(`{}`)},
		"only literals":     {manifest: filed(`{"a.md":"one","b.md":""}`), want: map[string]string{"a.md": "one", "b.md": ""}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider := &fakeProvider{err: errors.New("provider must not be called")}

			got, err := slots.ResolveFiles(t.Context(), manifest(t, tc.manifest), slots.Options{Provider: provider})
			if err != nil {
				t.Fatalf("ResolveFiles(): %v", err)
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("ResolveFiles() = %#v, want %#v", got, tc.want)
			}
			if provider.calls != 0 {
				t.Errorf("the provider was called %d times, want never", provider.calls)
			}
		})
	}
}

// TestResolveFilesBuildsTheSameRequestTwice pins the shape handed to the
// provider: one slot per sourced path, named by that path, in sorted order,
// none of them required, and no budget.
//
// The order is load-bearing because a map has none. The slots are not required
// because the library fails an assembly on a required slot that resolved
// empty, and an empty file is not a failure here. The budget is absent because
// one would silently truncate a file rather than a section.
func TestResolveFilesBuildsTheSameRequestTwice(t *testing.T) {
	t.Parallel()

	spec := manifest(t, filed(`{
	  "zulu.md":  {"kind":"inline","inline":{"content":"z"}},
	  "alpha.md": {"kind":"inline","inline":{"content":"a"}},
	  "mike.md":  {"kind":"inline","inline":{"content":"m"}},
	  "lit.md":   "a literal takes no slot"
	}`))

	opts := slots.Options{
		Workdir: "/somewhere",
		// A limit is set on purpose: the file request must not carry it.
		Limits:   agentcontext.Limits{MaxBytes: 8},
		Provider: &fakeProvider{result: &agentcontext.ContextResult{}},
	}
	for i := range 4 {
		provider := &fakeProvider{result: &agentcontext.ContextResult{Slots: []agentcontext.SlotResult{
			{Name: "alpha.md", Content: "a"},
			{Name: "mike.md", Content: "m"},
			{Name: "zulu.md", Content: "z"},
		}}}
		opts.Provider = provider

		if _, err := slots.ResolveFiles(t.Context(), spec, opts); err != nil {
			t.Fatalf("ResolveFiles() on pass %d: %v", i, err)
		}
		names := make([]string, 0, len(provider.req.Slots))
		for _, s := range provider.req.Slots {
			names = append(names, s.Name)
			if s.Required {
				t.Errorf("slot %q was declared required; an empty file is not a failure", s.Name)
			}
		}
		if want := []string{"alpha.md", "mike.md", "zulu.md"}; !slices.Equal(names, want) {
			t.Errorf("pass %d requested %v, want %v", i, names, want)
		}
		if provider.req.Workdir != opts.Workdir {
			t.Errorf("request Workdir = %q, want %q", provider.req.Workdir, opts.Workdir)
		}
		if (provider.req.Limits != agentcontext.Limits{}) {
			t.Errorf("request Limits = %+v, want none: a budget truncates a file", provider.req.Limits)
		}
	}
}

// TestResolveFilesRefusesAPromisedPathTheProviderSkipped covers the provider
// that answered for fewer slots than it was asked about. Filling the map from
// the results alone would leave the path silently unwritten, which is the hole
// this function exists to prevent.
func TestResolveFilesRefusesAPromisedPathTheProviderSkipped(t *testing.T) {
	t.Parallel()

	spec := manifest(t, filed(`{
	  "a.md": {"kind":"inline","inline":{"content":"a"}},
	  "b.md": {"kind":"inline","inline":{"content":"b"}}
	}`))
	provider := &fakeProvider{result: &agentcontext.ContextResult{
		Slots: []agentcontext.SlotResult{{Name: "a.md", Content: "a"}},
	}}

	_, err := slots.ResolveFiles(t.Context(), spec, slots.Options{Provider: provider})
	if !errors.Is(err, slots.ErrFileSource) {
		t.Fatalf("ResolveFiles() = %v, want ErrFileSource", err)
	}
	if !strings.Contains(err.Error(), "b.md") {
		t.Errorf("the error does not name the path that was skipped: %v", err)
	}
}

// filed wraps a files object in the manifest an operator stores.
func filed(raw string) string {
	return `{"` + profile.SpecKeyFiles + `": ` + raw + `}`
}
