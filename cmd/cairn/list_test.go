package main

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chrispian/cairn/catalog"
)

// TestList covers the command that answers "what can I boot" against a
// directory of files. It replaces a SQL query the conductor profile ran against
// the store, so the question is the same one.
//
// What it enumerates changed with the catalog. Bindings were the first block
// and the widest — a name, the profile it booted, where it worked, and the
// parts, skills and prompts it composed — and they retired with `--save-as`,
// because composing for a launch is the launcher's. What is left is what the
// bundle actually holds: the profiles, the abstract ones apart from them, and
// the layouts a profile's `{{ extends }}` can name.
func TestList(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)
	mustMkdir(t, filepath.Join(bundle, catalog.TemplatesDir, catalog.LayoutsDir))
	for _, name := range []string{"agent", "terse"} {
		writeFile(t, filepath.Join(bundle, catalog.TemplatesDir, catalog.LayoutsDir, name+".md"),
			"{{ yield charter }}\n", 0o644)
	}

	list := func(t *testing.T, args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := run(ctx, append([]string{"list"}, append(args, "--profile", bundle)...), &stdout, &stderr); err != nil {
			t.Fatalf("list: %v\nstderr: %s", err, stderr.String())
		}
		if stderr.Len() > 0 {
			t.Errorf("list wrote to stderr with nothing to report:\n%s", stderr.String())
		}
		return stdout.String()
	}

	t.Run("no line ends in whitespace", func(t *testing.T) {
		// writeRows' own promise, asserted against the document rather than
		// read off the function. This render is planted into a boot file, where
		// a run of invisible spaces is a diff, and adding a column is exactly
		// the change that breaks it.
		for _, line := range strings.Split(list(t), "\n") {
			if line != strings.TrimRight(line, " ") {
				t.Errorf("a line ends in spaces, which is a diff in a planted file: %q", line)
			}
		}
	})

	t.Run("the layouts a profile can extend are listed", func(t *testing.T) {
		// The block that replaced bindings, and it exists for the same reason
		// the listing does: a layout is addressed by a bare name inside a
		// template, and a bundle with no way to enumerate them is a directory
		// the author has to `ls` to find out what `{{ extends }}` may say.
		out := list(t)
		names := rowIDs(blockOf(t, out, "Layouts"))
		if !slices.Equal(names, []string{"agent", "terse"}) {
			t.Errorf("Layouts block lists %v, want agent and terse:\n%s", names, out)
		}
	})

	t.Run("an abstract profile is listed apart from the bootable ones", func(t *testing.T) {
		out := list(t)
		bootable := rowIDs(blockOf(t, out, "Profiles"))
		abstract := rowIDs(blockOf(t, out, "Abstract profiles"))
		if slices.Contains(bootable, "base") {
			t.Errorf("an abstract profile is listed as bootable: %v", bootable)
		}
		if !slices.Contains(abstract, "base") {
			t.Errorf("the abstract profile is not listed at all:\n%s", out)
		}
	})

	t.Run("the bundle's own path is not printed", func(t *testing.T) {
		// Deliberate, and load-bearing twice: the listing is planted into a
		// boot file, where an absolute path would be the one line of a render
		// that differs between two checkouts, and the operator running this at
		// a terminal just typed the flag that chose the bundle.
		if out := list(t); strings.Contains(out, bundle) {
			t.Errorf("the listing names the bundle it read:\n%s", out)
		}
	})

	t.Run("a block with nothing in it renders nothing", func(t *testing.T) {
		// install.Report's rule: a bundle with no abstract profiles should not
		// have to scroll past a heading saying so.
		bare := filepath.Join(home, "bare")
		writeProfile(t, bare, bundleProfile{ID: "only", Name: "Only", Provider: "claude"})

		var stdout, stderr bytes.Buffer
		if err := run(ctx, []string{"list", "--profile", bare}, &stdout, &stderr); err != nil {
			t.Fatalf("list: %v\nstderr: %s", err, stderr.String())
		}
		for _, absent := range []string{"Abstract profiles", "Layouts"} {
			if strings.Contains(stdout.String(), absent) {
				t.Errorf("a bundle with no %s printed the heading anyway:\n%s", absent, stdout.String())
			}
		}
		if !strings.Contains(stdout.String(), "Profiles (1)") {
			t.Errorf("the one block with something in it is missing:\n%s", stdout.String())
		}
	})

	t.Run("it takes no target", func(t *testing.T) {
		// A listing is of the catalog. One profile is what `cairn show` is for,
		// and a target silently ignored would read as a filter that does not
		// filter.
		var stdout, stderr bytes.Buffer
		err := run(ctx, []string{"list", "engineer", "--profile", bundle}, &stdout, &stderr)
		if err == nil {
			t.Fatal("list with a target reported success")
		}
		if !strings.Contains(err.Error(), "engineer") {
			t.Errorf("the refusal does not name what it was given: %v", err)
		}
	})
}

// rowIDs returns the first column of every row of a block, dropping the note
// under the heading. It is a whole-field read rather than a substring one:
// "base2" carries "base", and an assertion that could not tell them apart
// would pass on a listing that put an abstract profile among the bootable
// ones.
func rowIDs(block string) []string {
	var out []string
	for i, line := range strings.Split(block, "\n") {
		fields := strings.Fields(line)
		if i == 0 || len(fields) == 0 {
			continue
		}
		out = append(out, fields[0])
	}
	return out
}

// blockOf returns the lines under a heading, up to the blank line that ends it.
func blockOf(t *testing.T, out, heading string) string {
	t.Helper()
	var block []string
	in := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, heading+" ("):
			in = true
		case in && strings.TrimSpace(line) == "":
			return strings.Join(block, "\n")
		case in:
			block = append(block, line)
		}
	}
	if !in {
		t.Fatalf("the listing has no %q block:\n%s", heading, out)
	}
	return strings.Join(block, "\n")
}
