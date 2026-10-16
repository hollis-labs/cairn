package slots_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/profile"
	"github.com/hollis-labs/cairn/slots"
	"github.com/hollis-labs/libs/harness/core/agentcontext"
)

// manifest decodes a JSON literal into a profile.Spec, so a test declares its
// slots the way an operator does: as the stored rendering manifest.
func manifest(t *testing.T, raw string) profile.Spec {
	t.Helper()
	var spec profile.Spec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("fixture manifest: %v", err)
	}
	return spec
}

// fakeProvider records the request it was handed and returns a canned result.
type fakeProvider struct {
	calls  int
	req    agentcontext.ContextRequest
	result *agentcontext.ContextResult
	err    error
}

func (f *fakeProvider) Assemble(_ context.Context, req agentcontext.ContextRequest) (*agentcontext.ContextResult, error) {
	f.calls++
	f.req = req
	return f.result, f.err
}

func TestAssembleNoSlotsDeclared(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"key absent": `{"files": {"a.md": "x"}}`,
		"key null":   `{"slots": null}`,
		"empty list": `{"slots": []}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// A provider that must never run: with nothing declared there is
			// nothing to assemble.
			provider := &fakeProvider{err: errors.New("provider must not be called")}

			result, err := slots.Assemble(t.Context(), manifest(t, raw), slots.Options{Provider: provider})
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			if result != nil {
				t.Fatalf("Assemble returned %+v, want nil result", result)
			}
			if provider.calls != 0 {
				t.Fatalf("provider called %d times, want 0", provider.calls)
			}
		})
	}
}

func TestAssembleInlineSlotsKeepDeclaredOrder(t *testing.T) {
	t.Parallel()

	spec := manifest(t, `{"slots": [
		{"name": "first", "source": {"kind": "inline", "inline": {"content": "alpha-body"}}},
		{"name": "second", "source": {"kind": "inline", "inline": {"content": "beta-body"}}}
	]}`)

	result, err := slots.Assemble(t.Context(), spec, slots.Options{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if result == nil {
		t.Fatal("Assemble returned nil result, want two slots")
	}

	if got, want := len(result.Slots), 2; got != want {
		t.Fatalf("len(Slots) = %d, want %d", got, want)
	}
	if got, want := []string{result.Slots[0].Name, result.Slots[1].Name}, []string{"first", "second"}; got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("slot names = %v, want %v", got, want)
	}

	alpha := strings.Index(result.Rendered, "alpha-body")
	beta := strings.Index(result.Rendered, "beta-body")
	if alpha < 0 || beta < 0 {
		t.Fatalf("Rendered missing a slot body:\n%s", result.Rendered)
	}
	if alpha > beta {
		t.Fatalf("Rendered emitted the second slot before the first:\n%s", result.Rendered)
	}
}

func TestAssembleStaticFileResolvesAgainstWorkdir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cairn-note.md")
	if err := os.WriteFile(path, []byte("note-from-the-workdir"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	spec := manifest(t, `{"slots": [
		{"name": "note", "source": {"kind": "static_file", "static_file": {"path": "cairn-note.md"}}}
	]}`)

	result, err := slots.Assemble(t.Context(), spec, slots.Options{Workdir: dir})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if result == nil {
		t.Fatal("Assemble returned nil result, want one slot")
	}
	if !strings.Contains(result.Rendered, "note-from-the-workdir") {
		t.Fatalf("Rendered missing the file body:\n%s", result.Rendered)
	}
	if got := result.Slots[0].Provenance.Source; got != path {
		t.Fatalf("provenance source = %q, want %q", got, path)
	}

	// The same manifest without a workdir resolves the relative path against
	// the process working directory instead, where the fixture does not exist.
	// That failure is what proves Workdir did the work above.
	elsewhere, err := slots.Assemble(t.Context(), spec, slots.Options{})
	if err != nil {
		t.Fatalf("Assemble without Workdir: %v", err)
	}
	if elsewhere.Slots[0].Err == nil {
		t.Fatalf("Assemble without Workdir found the fixture anyway, source %q",
			elsewhere.Slots[0].Provenance.Source)
	}
}

func TestAssembleNonRequiredSlotFailureIsRecordedNotFatal(t *testing.T) {
	t.Parallel()

	spec := manifest(t, `{"slots": [
		{"name": "absent", "source": {"kind": "static_file", "static_file": {"path": "no-such-file.md"}}},
		{"name": "after", "source": {"kind": "inline", "inline": {"content": "still-assembled"}}}
	]}`)

	result, err := slots.Assemble(t.Context(), spec, slots.Options{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if result == nil {
		t.Fatal("Assemble returned nil result, want two slots")
	}
	if got, want := len(result.Slots), 2; got != want {
		t.Fatalf("len(Slots) = %d, want %d", got, want)
	}

	failed := result.Slots[0]
	if failed.Err == nil {
		t.Fatal("failed slot recorded no error on SlotResult.Err")
	}
	if !errors.Is(failed.Err, fs.ErrNotExist) {
		t.Fatalf("SlotResult.Err = %v, want it to wrap fs.ErrNotExist", failed.Err)
	}
	if !strings.Contains(result.Rendered, "still-assembled") {
		t.Fatalf("a non-required failure blocked the slots after it:\n%s", result.Rendered)
	}
}

func TestAssembleRequiredSlotFailureStaysComparable(t *testing.T) {
	t.Parallel()

	spec := manifest(t, `{"slots": [
		{"name": "absent", "required": true, "source": {"kind": "static_file", "static_file": {"path": "no-such-file.md"}}}
	]}`)

	result, err := slots.Assemble(t.Context(), spec, slots.Options{Workdir: t.TempDir()})
	if err == nil {
		t.Fatal("Assemble succeeded, want the required slot to fail it")
	}
	if result != nil {
		t.Fatalf("Assemble returned %+v alongside an error, want nil result", result)
	}
	if !errors.Is(err, agentcontext.ErrRequiredSlotFailed) {
		t.Fatalf("error = %v, want errors.Is ErrRequiredSlotFailed through the wrapping", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want the resolver's own cause to stay reachable", err)
	}
	if !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("error = %q, want it to name the profile manifest", err)
	}
}

func TestAssembleMalformedSlotsKey(t *testing.T) {
	t.Parallel()

	spec := manifest(t, `{"slots": {"name": "not-a-list"}}`)

	result, err := slots.Assemble(t.Context(), spec, slots.Options{})
	if err == nil {
		t.Fatal("Assemble succeeded on a malformed slots key, want an error")
	}
	if result != nil {
		t.Fatalf("Assemble returned %+v alongside an error, want nil result", result)
	}
	for _, want := range []string{"manifest", "slots"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestAssembleProviderOverride(t *testing.T) {
	t.Parallel()

	canned := &agentcontext.ContextResult{Rendered: "from-the-fake"}
	provider := &fakeProvider{result: canned}

	spec := manifest(t, `{"slots": [
		{"name": "only", "source": {"kind": "inline", "inline": {"content": "declared"}}}
	]}`)
	opts := slots.Options{
		Workdir:    "/some/scope",
		Limits:     agentcontext.Limits{MaxBytes: 4096, MaxTokens: 512},
		Provenance: agentcontext.ProvenanceInput{ProfileID: "p-1", Role: "backend"},
		Provider:   provider,
	}

	result, err := slots.Assemble(t.Context(), spec, opts)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if result != canned {
		t.Fatalf("Assemble returned %+v, want the fake's own result", result)
	}
	if provider.calls != 1 {
		t.Fatalf("provider called %d times, want 1", provider.calls)
	}

	req := provider.req
	if req.Workdir != opts.Workdir {
		t.Errorf("request Workdir = %q, want %q", req.Workdir, opts.Workdir)
	}
	if req.Limits != opts.Limits {
		t.Errorf("request Limits = %+v, want %+v", req.Limits, opts.Limits)
	}
	if !reflect.DeepEqual(req.Provenance, opts.Provenance) {
		t.Errorf("request Provenance = %+v, want %+v", req.Provenance, opts.Provenance)
	}
	if len(req.Slots) != 1 || req.Slots[0].Name != "only" {
		t.Fatalf("request Slots = %+v, want the manifest's one slot unchanged", req.Slots)
	}
	if got, want := req.Slots[0].Source.Inline.Content, "declared"; got != want {
		t.Errorf("request slot content = %q, want %q", got, want)
	}
}

func TestAssembleProviderErrorIsWrapped(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("provider said no")
	provider := &fakeProvider{err: sentinel}

	spec := manifest(t, `{"slots": [
		{"name": "only", "source": {"kind": "inline", "inline": {"content": "declared"}}}
	]}`)

	result, err := slots.Assemble(t.Context(), spec, slots.Options{Provider: provider})
	if result != nil {
		t.Fatalf("Assemble returned %+v alongside an error, want nil result", result)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want errors.Is the provider's own error", err)
	}
	if !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("error = %q, want it to name the profile manifest", err)
	}
}
