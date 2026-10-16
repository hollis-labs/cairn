package bootdir

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/profile"
)

// treeInstance returns an instance whose manifest copies dest from source.
func treeInstance(t *testing.T, home string, trees map[string]string) *Instance {
	t.Helper()
	encoded, err := json.Marshal(trees)
	if err != nil {
		t.Fatalf("encode the trees manifest: %v", err)
	}
	inst := testInstance(t, profile.Resolved{
		ID:   "engineer",
		Spec: testSpec(t, `{"`+profile.SpecKeyTrees+`": `+string(encoded)+`}`),
	})
	inst.Home = home
	return inst
}

// TestTreesCopyADirectoryWhole is what this key exists for, and the reason a
// static_dir source is not the answer: that resolver concatenates the files it
// finds into one string, which is right for a slot and destroys a directory.
func TestTreesCopyADirectoryWhole(t *testing.T) {
	source := t.TempDir()
	writeTreeFile(t, source, "index.md", "# docs\n", 0o644)
	writeTreeFile(t, source, "engineering/process.md", "read it\n", 0o644)
	writeTreeFile(t, source, "bin/check.sh", "#!/bin/sh\n", 0o755)

	files, err := renderTrees(treeInstance(t, "", map[string]string{"docs": source}))
	if err != nil {
		t.Fatalf("renderTrees(): %v", err)
	}
	want := []string{"docs/bin/check.sh", "docs/engineering/process.md", "docs/index.md"}
	if got := filePaths(files); !slices.Equal(got, want) {
		t.Fatalf("copied %v, want %v with its shape intact", got, want)
	}
	if got := string(fileByPath(t, files, "docs/engineering/process.md").Content); got != "read it\n" {
		t.Errorf("the nested file holds %q, want its own bytes", got)
	}
	// A script that arrives without its executable bit fails halfway through
	// rather than at boot.
	if got := fileByPath(t, files, "docs/bin/check.sh").Mode; got != ExecFileMode {
		t.Errorf("the script planted with mode %v, want %v", got, ExecFileMode)
	}
}

// TestTreesAreAbsentWhenNoneAreDeclared covers the ordinary profile.
func TestTreesAreAbsentWhenNoneAreDeclared(t *testing.T) {
	files, err := renderTrees(testInstance(t, profile.Resolved{ID: "engineer"}))
	if err != nil {
		t.Fatalf("renderTrees(): %v", err)
	}
	if len(files) != 0 {
		t.Errorf("a profile declaring no trees rendered %v", filePaths(files))
	}
}

// TestTreesExpandALeadingTilde covers the one home-relative form a profile may
// write. A source directory is always somewhere on the operator's machine, and
// writing it out in full in every profile is how it goes stale.
//
// The home it expands against is the instance's, not the process's, which is
// also why this test does not touch the environment.
func TestTreesExpandALeadingTilde(t *testing.T) {
	home := t.TempDir()
	writeTreeFile(t, filepath.Join(home, "docs"), "x.md", "content\n", 0o644)

	files, err := renderTrees(treeInstance(t, home, map[string]string{"docs": "~/docs"}))
	if err != nil {
		t.Fatalf("renderTrees(): %v", err)
	}
	if got := filePaths(files); !slices.Equal(got, []string{"docs/x.md"}) {
		t.Errorf("copied %v, want the tilde expanded against the instance's home", got)
	}
}

// TestTreesRefuseASourceTheyCannotCopy covers every way a declared source is
// unusable. Each is refused by name rather than skipped: a tree a profile
// declared and cairn did not plant is a hole nothing downstream can notice.
func TestTreesRefuseASourceTheyCannotCopy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	writeTreeFile(t, filepath.Dir(file), "not-a-directory", "x\n", 0o644)

	for name, source := range map[string]string{
		"nothing at all":         "",
		"a relative path":        "docs/engineering",
		"a path that is not one": filepath.Join(t.TempDir(), "never-created"),
		"a file":                 file,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := renderTrees(treeInstance(t, "", map[string]string{"docs": source}))
			if !errors.Is(err, ErrTreeSource) {
				t.Fatalf("renderTrees() = %v, want ErrTreeSource", err)
			}
			if !strings.Contains(err.Error(), "docs") {
				t.Errorf("the refusal %q does not name the destination", err)
			}
		})
	}
}

// TestTreesCollideWithEveryOtherArtifact covers the guarantee that came free.
// A tree emits one file per file, so a destination that lands where something
// else already renders is the duplicate-path refusal every other artifact gets.
func TestTreesCollideWithEveryOtherArtifact(t *testing.T) {
	source := t.TempDir()
	writeTreeFile(t, source, "guide.md", "from the tree\n", 0o644)

	inst := treeInstance(t, "", map[string]string{"docs": source})
	inst.Templates = map[string]string{"docs/guide.md": "from a template\n"}

	if _, err := Render(inst); !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("Render() = %v, want ErrDuplicatePath", err)
	}
}

// writeTreeFile writes one file under dir, creating the directories above it.
func writeTreeFile(t *testing.T, dir, rel, content string, mode fs.FileMode) {
	t.Helper()
	dest := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("create the directory for %s: %v", dest, err)
	}
	if err := os.WriteFile(dest, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", dest, err)
	}
	// os.WriteFile's mode is masked by the umask, and the executable bit is a
	// property under test.
	if err := os.Chmod(dest, mode); err != nil {
		t.Fatalf("set the mode on %s: %v", dest, err)
	}
}

// TestTreesExpandAVariable is the gap this key had longest. A tree source is
// nothing but "point at a path", which is the manifest value most likely to
// want a variable, and until this it was the one place hardcoding was
// mandatory.
func TestTreesExpandAVariable(t *testing.T) {
	root := t.TempDir()
	writeTreeFile(t, filepath.Join(root, "engineering"), "process.md", "read it\n", 0o644)

	inst := treeInstance(t, "", map[string]string{"docs": "$ROOT/engineering"})
	inst.Env = func(name string) string {
		if name == "ROOT" {
			return root
		}
		return ""
	}

	files, err := renderTrees(inst)
	if err != nil {
		t.Fatalf("renderTrees(): %v", err)
	}
	if got := filePaths(files); !slices.Equal(got, []string{"docs/process.md"}) {
		t.Errorf("copied %v, want the variable expanded", got)
	}
}

// TestATreeSourceExpandsVariablesBeforeTheTilde covers the order, which is the
// only part of it that is a decision. A variable holding a home-relative path
// gets its tilde expanded too; expanding the tilde first would leave one in the
// middle of the result.
func TestATreeSourceExpandsVariablesBeforeTheTilde(t *testing.T) {
	home := t.TempDir()
	writeTreeFile(t, filepath.Join(home, "agents", "docs"), "x.md", "content\n", 0o644)

	inst := treeInstance(t, home, map[string]string{"docs": "$AGENT_HOME/docs"})
	inst.Env = func(string) string { return "~/agents" }

	files, err := renderTrees(inst)
	if err != nil {
		t.Fatalf("renderTrees(): %v", err)
	}
	if got := filePaths(files); !slices.Equal(got, []string{"docs/x.md"}) {
		t.Errorf("copied %v, want the tilde inside the variable expanded too", got)
	}
}

// TestAnUnsetVariableIsNamedAsTheOperatorWroteIt is the diagnostic this had to
// get right.
//
// "$ROOT/docs" with ROOT unset expands to "/docs", which is absolute and passes
// every check but the last. An error quoting only "/docs" sends the operator
// looking for a path they never wrote instead of at the variable they did not
// set, so the message carries both.
func TestAnUnsetVariableIsNamedAsTheOperatorWroteIt(t *testing.T) {
	inst := treeInstance(t, "", map[string]string{"docs": "$ROOT/never-created"})
	inst.Env = func(string) string { return "" }

	_, err := renderTrees(inst)
	if !errors.Is(err, ErrTreeSource) {
		t.Fatalf("renderTrees() = %v, want ErrTreeSource", err)
	}
	for _, want := range []string{"$ROOT/never-created", "/never-created"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
}

// TestNoEnvironmentLeavesTheOperatorsTextAlone states what an instance carrying
// no environment does. Not the process environment: a renderer reads nothing
// outside the instance it was handed, which is the same rule that carries the
// operator's home rather than looking it up.
func TestNoEnvironmentLeavesTheOperatorsTextAlone(t *testing.T) {
	inst := treeInstance(t, "", map[string]string{"docs": "$ROOT/docs"})

	_, err := renderTrees(inst)
	if !errors.Is(err, ErrTreeSource) {
		t.Fatalf("renderTrees() = %v, want ErrTreeSource", err)
	}
	if !strings.Contains(err.Error(), "$ROOT/docs") {
		t.Errorf("the refusal %q does not quote what the operator wrote", err)
	}
}
