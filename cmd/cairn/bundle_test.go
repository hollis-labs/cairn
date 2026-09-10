package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chrispian/cairn/catalog"
)

// bundleProfile is one profile file, as a test declares it. It is the file's
// frontmatter with the manifest held as JSON text per key, which is what the
// tests below were already written in and what a reader compares against the
// shapes in package profile.
//
// JSON is legal YAML — a flow mapping is a JSON object — so a manifest value
// goes into the frontmatter verbatim on one line. That is why these fixtures
// can carry the same manifests they carried when a test wrote rows.
type bundleProfile struct {
	ID          string
	Extends     string
	Abstract    bool
	Name        string
	Description string
	Provider    string
	Model       string
	Body        string

	// Spec maps a manifest key to the JSON its value is written as. A test
	// changing one key of a fixture rewrites the file with this map edited,
	// which is the file-backed equivalent of the read-modify-put these tests
	// used to do against the store.
	Spec map[string]string
}

// writeProfile writes p into the bundle's profiles directory, at the file name
// its id obliges — the catalog refuses a profile whose frontmatter and file
// name disagree, and a fixture that could disagree would be testing the
// fixture.
func writeProfile(t *testing.T, bundle string, p bundleProfile) {
	t.Helper()
	writeProfileAt(t, filepath.Join(bundle, catalog.ProfilesDir), p)
}

// writeNestedProfile writes p into the bundle's profiles/parts directory,
// which the catalog reads into the same flat namespace as the files above it.
// It differs from [writeProfile] in one argument and nothing else, which is
// the point: a nested profile is an ordinary profile, and a fixture that had
// to say otherwise would be testing a distinction cairn does not make.
//
// It is not [writePart], which writes a file OUTSIDE the bundle for the path
// form of --with. The two are the two halves of that flag: a catalog id that
// happens to live in a subdirectory, and a file that has no catalog entry at
// all.
func writeNestedProfile(t *testing.T, bundle string, p bundleProfile) {
	t.Helper()
	writeProfileAt(t, filepath.Join(bundle, catalog.ProfilesDir, catalog.PartsDir), p)
}

// writeProfileAt writes p into dir, at the file name its id obliges.
func writeProfileAt(t *testing.T, dir string, p bundleProfile) {
	t.Helper()
	var b strings.Builder
	b.WriteString("---\n")
	writeFrontmatter(&b, "id", p.ID)
	writeFrontmatter(&b, "extends", p.Extends)
	if p.Abstract {
		b.WriteString("abstract: true\n")
	}
	writeFrontmatter(&b, "name", p.Name)
	writeFrontmatter(&b, "description", p.Description)
	writeFrontmatter(&b, "provider", p.Provider)
	writeFrontmatter(&b, "model", p.Model)
	if len(p.Spec) > 0 {
		b.WriteString("spec:\n")
		for _, key := range slices.Sorted(maps.Keys(p.Spec)) {
			var compact bytes.Buffer
			if err := json.Compact(&compact, []byte(p.Spec[key])); err != nil {
				t.Fatalf("profile %q: spec.%s is not JSON: %v", p.ID, key, err)
			}
			fmt.Fprintf(&b, "  %s: %s\n", key, compact.String())
		}
	}
	b.WriteString("---\n")
	if p.Body != "" {
		b.WriteString("\n" + p.Body + "\n")
	}

	mustMkdir(t, dir)
	writeFile(t, filepath.Join(dir, p.ID+".md"), b.String(), 0o644)
}

// writeFrontmatter writes one scalar frontmatter line, and nothing at all for
// an empty value. The value goes through the JSON encoder so that a fixture
// carrying a colon or a leading marker character is still one YAML string.
func writeFrontmatter(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	quoted, err := json.Marshal(value)
	if err != nil {
		// Unreachable: the input is a Go string.
		panic(err)
	}
	fmt.Fprintf(b, "%s: %s\n", key, quoted)
}

// nothingUnder fails the test when anything exists at or under path. It is how
// "a read that finds nothing writes nothing" is asserted: the command is
// pointed at a path that does not exist, and this says whether it stayed that
// way.
func nothingUnder(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		var found []string
		_ = filepath.Walk(path, func(p string, _ os.FileInfo, err error) error {
			if err == nil {
				found = append(found, p)
			}
			return nil
		})
		t.Fatalf("a command that reads and finds nothing left something behind:\n  %s",
			strings.Join(found, "\n  "))
	}
}

// The helpers below moved here when saveas_test.go was deleted with
// `--save-as`. They are not about bindings: they copy the example bundle,
// boot it, and diff two planted trees, which is what a good half of this
// package's tests do.
//
// exampleBundle still copies rather than pointing at the checkout. Nothing
// writes into a bundle any more — that was --save-as's alone — but a test
// that mutates the repository's own example bundle is a test that fails the
// second time it runs, and the copy is what keeps that impossible rather than
// merely unlikely.

// A copy, because --save-as writes into the bundle and the example one is
// checked in. The two skills the tests add are skills the example bundle does
// not ship: the point of naming them is that they are ids the profiles never
// declared, so seeing them in a boot directory means the flag or the binding
// put them there.
func exampleBundle(t *testing.T, into string) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "examples", "bundle"))
	if err != nil {
		t.Fatalf("locate the example bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "profiles", "engineer.md")); err != nil {
		t.Fatalf("the example bundle is not where this test expects it: %v", err)
	}
	dst := filepath.Join(into, "bundle")
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy the example bundle: %v", err)
	}
	for _, id := range []string{"qhealth", "adr"} {
		mustMkdir(t, filepath.Join(dst, "skills", id))
		writeFile(t, filepath.Join(dst, "skills", id, "SKILL.md"),
			fmt.Sprintf("---\nname: %s\ndescription: A skill the example bundle does not ship.\n---\n", id),
			0o644)
	}
	return dst
}

// bootTree runs one boot and returns the tree it planted.
func bootTree(t *testing.T, ctx context.Context, bootRoot string, args ...string) map[string]string {
	t.Helper()
	return bootTreeErr(t, ctx, discard(), bootRoot, args...)
}

// bootTreeErr is [bootTree] with stderr handed in, for the tests that read it.
func bootTreeErr(t *testing.T, ctx context.Context, stderr io.Writer, bootRoot string, args ...string) map[string]string {
	t.Helper()
	var stdout bytes.Buffer
	var captured bytes.Buffer
	full := append([]string{"boot"}, args...)
	full = append(full, "--boot-root", bootRoot, "--session", "s")
	if err := run(ctx, full, &stdout, io.MultiWriter(stderr, &captured)); err != nil {
		t.Fatalf("boot %v: %v\nstderr: %s", args, err, captured.String())
	}
	return treeOf(t, strings.TrimSpace(stdout.String()))
}

// changedFiles names every path the two trees disagree about, present in one
// and absent from the other included.
func changedFiles(t *testing.T, a, b map[string]string) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, rel := range slices.Sorted(maps.Keys(a)) {
		if b[rel] != a[rel] {
			seen[rel] = true
		}
	}
	for _, rel := range slices.Sorted(maps.Keys(b)) {
		if a[rel] != b[rel] {
			seen[rel] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}
