package bootdir

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/profile"
)

// TestFilesRenderSortedAndVerbatim states both halves of the escape hatch. The
// order is sorted because a manifest holds these in a map, which has none, and
// a rendering has to be the same twice. The content is untouched because cairn
// does not know what any of it is for.
func TestFilesRenderSortedAndVerbatim(t *testing.T) {
	inst := testInstance(t, profile.Resolved{ID: "reviewer"})
	inst.Files = map[string]string{
		"notes/zulu.md":   "zulu\n",
		"alpha.md":        "alpha, with no trailing newline",
		"notes/alpha.md":  "nested alpha\n",
		"deeply/nested/x": "not markdown at all\r\n\tand not reformatted\n",
		"b.md":            "",
	}

	files, err := renderFiles(inst)
	if err != nil {
		t.Fatalf("renderFiles(): %v", err)
	}
	want := []string{"alpha.md", "b.md", "deeply/nested/x", "notes/alpha.md", "notes/zulu.md"}
	if got := filePaths(files); !slices.Equal(got, want) {
		t.Fatalf("rendered %v, want %v", got, want)
	}
	for path, content := range inst.Files {
		if got := string(fileByPath(t, files, path).Content); got != content {
			t.Errorf("%s holds %q, want %q", path, got, content)
		}
	}
}

// TestFilesArriveResolvedRatherThanFromTheManifest states where the content
// comes from, which is the
// which is the half of this renderer that is easy to get wrong.
//
// A files value may be a slot source rather than a literal, and resolving one
// runs a command or makes a request — which a renderer may not do. So the
// manifest is read in the composition root and the instance carries the
// answer. A renderer that read spec.files itself would plant the JSON of a
// source object as though it were the file's content.
func TestFilesArriveResolvedRatherThanFromTheManifest(t *testing.T) {
	inst := testInstance(t, profile.Resolved{
		ID:   "reviewer",
		Spec: testSpec(t, `{"files": {"tasks/task.md": {"kind":"cmd","cmd":{"run":"torque task get T-1"}}}}`),
	})
	inst.Files = map[string]string{"tasks/task.md": "# T-1\n\nin progress\n"}

	files, err := renderFiles(inst)
	if err != nil {
		t.Fatalf("renderFiles(): %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("rendered %v, want the one resolved file", filePaths(files))
	}
	if got := string(files[0].Content); got != inst.Files["tasks/task.md"] {
		t.Errorf("the file holds %q, want the resolved content %q", got, inst.Files["tasks/task.md"])
	}
}

// TestFilesAreAbsentWhenNoneAreDeclared covers the instance carrying none,
// whatever the manifest said — a manifest declaring only sources that all
// resolved to nothing is the same case here as one declaring no files at all.
func TestFilesAreAbsentWhenNoneAreDeclared(t *testing.T) {
	for _, resolved := range []map[string]string{nil, {}} {
		inst := testInstance(t, profile.Resolved{ID: "quiet", Spec: testSpec(t, `{"files": {"a.md": "x"}}`)})
		inst.Files = resolved

		files, err := renderFiles(inst)
		if err != nil {
			t.Fatalf("renderFiles() with %v resolved: %v", resolved, err)
		}
		if len(files) != 0 {
			t.Errorf("renderFiles() with %v resolved rendered %v, want nothing", resolved, filePaths(files))
		}
	}
}

// TestFilesRefuseAPathThatCannotNameItself covers the one bad path [Render]
// cannot report usefully: its diagnostic quotes the offending path, and an
// empty one leaves nothing to quote.
func TestFilesRefuseAPathThatCannotNameItself(t *testing.T) {
	inst := testInstance(t, profile.Resolved{ID: "reviewer"})
	inst.Files = map[string]string{"": "x"}

	_, err := renderFiles(inst)
	if !errors.Is(err, ErrArtifactPath) {
		t.Fatalf("renderFiles() error = %v, want ErrArtifactPath", err)
	}
	if !strings.Contains(err.Error(), profile.SpecKeyFiles) {
		t.Errorf("the error %q does not name the manifest key it came from", err)
	}
}

// TestFilesLeavePathSafetyToRender states where the second opinion lives. The
// renderer emits the path the manifest declared; [Render] is what decides
// whether it can name something inside the boot directory, and it asks that of
// every renderer.
func TestFilesLeavePathSafetyToRender(t *testing.T) {
	for _, bad := range []string{"../escape.md", "/absolute.md", "notes/../../escape.md"} {
		inst := testInstance(t, profile.Resolved{ID: "reviewer"})
		inst.Files = map[string]string{bad: "x"}

		if _, err := renderFiles(inst); err != nil {
			t.Errorf("renderFiles() with %q returned %v; path safety is Render's question", bad, err)
		}
		_, err := Render(inst)
		if !errors.Is(err, ErrArtifactPath) {
			t.Errorf("Render() with %q returned error %v, want ErrArtifactPath", bad, err)
		}
		if err != nil && !strings.Contains(err.Error(), bad) {
			t.Errorf("the error %q does not name %q", err, bad)
		}
	}
}
