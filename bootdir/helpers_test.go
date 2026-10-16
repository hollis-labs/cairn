package bootdir

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/hollis-labs/cairn/profile"
)

// testLayout returns the claude layout, which is the only one implemented.
func testLayout(t *testing.T) Layout {
	t.Helper()
	layout, err := LayoutFor(profile.ProviderClaude)
	if err != nil {
		t.Fatalf("LayoutFor(%q): %v", profile.ProviderClaude, err)
	}
	return layout
}

// testSpec decodes a manifest written inline in a test into a [profile.Spec],
// so that a test declares the JSON an operator would have stored rather than a
// map of pre-marshalled values.
func testSpec(t *testing.T, manifest string) profile.Spec {
	t.Helper()
	if manifest == "" {
		return nil
	}
	var spec profile.Spec
	if err := json.Unmarshal([]byte(manifest), &spec); err != nil {
		t.Fatalf("decode the manifest %s: %v", manifest, err)
	}
	return spec
}

// testInstance returns an instance of the claude layout carrying resolved.
func testInstance(t *testing.T, resolved profile.Resolved) *Instance {
	t.Helper()
	return &Instance{
		Dir:     filepath.Join(t.TempDir(), "boot"),
		Layout:  testLayout(t),
		Profile: &resolved,
	}
}

// writeSkillTree writes one skill directory under root: files maps
// slash-separated paths inside the skill to their contents, and every path
// named in executable is written with its executable bit set.
func writeSkillTree(t *testing.T, root, name string, files map[string]string, executable ...string) {
	t.Helper()
	for rel, content := range files {
		dest := filepath.Join(root, name, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatalf("create the directory for %s: %v", dest, err)
		}
		mode := fs.FileMode(0o644)
		if slices.Contains(executable, rel) {
			mode = 0o755
		}
		if err := os.WriteFile(dest, []byte(content), mode); err != nil {
			t.Fatalf("write %s: %v", dest, err)
		}
		// os.WriteFile's mode is masked by the umask, and the executable bit
		// is the property under test.
		if err := os.Chmod(dest, mode); err != nil {
			t.Fatalf("set the mode on %s: %v", dest, err)
		}
	}
}

// fileByPath returns the rendered file at path.
func fileByPath(t *testing.T, files []File, path string) File {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no file was rendered at %q; the rendering holds %v", path, filePaths(files))
	return File{}
}

// filePaths returns the paths of files, in render order.
func filePaths(files []File) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}
