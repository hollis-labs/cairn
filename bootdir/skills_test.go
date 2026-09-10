package bootdir

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/chrispian/cairn/profile"
)

// skillsInstance returns an instance declaring names, copied from source.
//
// The fixtures are built under the test's own temporary directory rather than
// stored under testdata. A skill tree is a .claude/skills directory holding
// SKILL.md files, which is exactly the shape an agent harness scans the
// working tree for, so the fixtures are kept out of the working tree
// altogether.
func skillsInstance(t *testing.T, source string, names ...string) *Instance {
	t.Helper()
	manifest := map[string]any{profile.SpecKeySkills: names}
	if source != "" {
		manifest[profile.SpecKeySkillsDir] = source
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode the skills manifest: %v", err)
	}
	return testInstance(t, profile.Resolved{ID: "reviewer", Spec: testSpec(t, string(encoded))})
}

// TestSkillsArePlantedAsDirectories is the reason cairn copies a tree instead
// of resolving a skill to one file. A skill is a directory — an entry file,
// references beside it, and scripts that have to still be executable — and
// flattening it would drop everything but the entry file, silently.
func TestSkillsArePlantedAsDirectories(t *testing.T) {
	source := t.TempDir()
	writeSkillTree(t, source, "code-review", map[string]string{
		SkillFileName:             "# code review\n",
		"references/checklist.md": "- read the diff\n",
		"references/deep/note.md": "a nested reference\n",
		"scripts/run.sh":          "#!/bin/sh\necho reviewing\n",
	}, "scripts/run.sh")
	writeSkillTree(t, source, "capture-decision", map[string]string{
		SkillFileName: "# capture decision\n",
	})

	inst := skillsInstance(t, source, "code-review", "capture-decision")
	files, err := RenderSkills(inst)
	if err != nil {
		t.Fatalf("RenderSkills(): %v", err)
	}

	want := []string{
		".claude/skills/code-review/SKILL.md",
		".claude/skills/code-review/references/checklist.md",
		".claude/skills/code-review/references/deep/note.md",
		".claude/skills/code-review/scripts/run.sh",
		".claude/skills/capture-decision/SKILL.md",
	}
	got := filePaths(files)
	if len(got) != len(want) {
		t.Fatalf("rendered %v, want %v", got, want)
	}
	for _, path := range want {
		if !slices.Contains(got, path) {
			t.Errorf("nothing was rendered at %s; the rendering holds %v", path, got)
		}
	}

	entry := fileByPath(t, files, ".claude/skills/code-review/SKILL.md")
	if string(entry.Content) != "# code review\n" {
		t.Errorf("SKILL.md holds %q, want %q", entry.Content, "# code review\n")
	}
	if entry.Mode != DefaultFileMode {
		t.Errorf("SKILL.md is mode %v, want %v", entry.Mode, DefaultFileMode)
	}
	script := fileByPath(t, files, ".claude/skills/code-review/scripts/run.sh")
	if script.Mode != ExecFileMode {
		t.Errorf("the script is mode %v, want %v — a skill's script has to still run",
			script.Mode, ExecFileMode)
	}
	reference := fileByPath(t, files, ".claude/skills/code-review/references/checklist.md")
	if reference.Mode != DefaultFileMode {
		t.Errorf("a reference is mode %v, want %v", reference.Mode, DefaultFileMode)
	}
}

// TestSkillsAreOrderedDeterministically states the property a rendering has to
// hold whatever the filesystem hands back: skills in the order the manifest
// declares them, files inside each in one stable order.
func TestSkillsAreOrderedDeterministically(t *testing.T) {
	source := t.TempDir()
	writeSkillTree(t, source, "zulu", map[string]string{
		SkillFileName: "z\n", "b.md": "b\n", "a.md": "a\n", "references/y.md": "y\n",
	})
	writeSkillTree(t, source, "alpha", map[string]string{SkillFileName: "a\n"})

	inst := skillsInstance(t, source, "zulu", "alpha")
	first, err := RenderSkills(inst)
	if err != nil {
		t.Fatalf("RenderSkills(): %v", err)
	}
	want := []string{
		".claude/skills/zulu/SKILL.md",
		".claude/skills/zulu/a.md",
		".claude/skills/zulu/b.md",
		".claude/skills/zulu/references/y.md",
		".claude/skills/alpha/SKILL.md",
	}
	if got := filePaths(first); !slices.Equal(got, want) {
		t.Fatalf("rendered %v, want %v", got, want)
	}
	for i := range 8 {
		again, err := RenderSkills(inst)
		if err != nil {
			t.Fatalf("RenderSkills() on render %d: %v", i, err)
		}
		if got := filePaths(again); !slices.Equal(got, want) {
			t.Fatalf("render %d produced %v, want %v", i, got, want)
		}
	}
}

// TestSkillsAreCopiedNotLinked is why the planted layer exists at all. The
// bytes are read at render time and carried in a [File], so a boot directory
// cannot reference the skill source: editing the source afterwards leaves
// every already-rendered instance as it was.
func TestSkillsAreCopiedNotLinked(t *testing.T) {
	source := t.TempDir()
	writeSkillTree(t, source, "code-review", map[string]string{SkillFileName: "the original body\n"})

	files, err := RenderSkills(skillsInstance(t, source, "code-review"))
	if err != nil {
		t.Fatalf("RenderSkills(): %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "code-review", SkillFileName),
		[]byte("rewritten after rendering\n"), 0o644); err != nil {
		t.Fatalf("rewrite the source skill: %v", err)
	}
	if got := string(files[0].Content); got != "the original body\n" {
		t.Errorf("the rendered skill is %q, want the bytes read at render time", got)
	}
}

// TestSkillsRefuseWhatAHarnessWouldNotLoad collects every way a declared skill
// stops the render. Each of them would otherwise produce a boot directory an
// agent reads as complete while a skill it was promised is missing or inert.
func TestSkillsRefuseWhatAHarnessWouldNotLoad(t *testing.T) {
	source := t.TempDir()
	writeSkillTree(t, source, "code-review", map[string]string{SkillFileName: "# code review\n"})
	writeSkillTree(t, source, "no-entry-file", map[string]string{"references/only.md": "no entry file\n"})
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0o755); err != nil {
		t.Fatalf("create an empty skill directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "a-file-not-a-directory"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write a file where a skill directory would be: %v", err)
	}

	tests := []struct {
		name    string
		declare []string
		source  string
		want    error
		names   []string
	}{
		{
			name:    "a skill with no entry file",
			declare: []string{"no-entry-file"},
			source:  source,
			want:    ErrSkillContent,
			names:   []string{`"no-entry-file"`, SkillFileName, filepath.Join(source, "no-entry-file")},
		},
		{
			name:    "a skill directory holding nothing",
			declare: []string{"empty"},
			source:  source,
			want:    ErrSkillContent,
			names:   []string{`"empty"`, filepath.Join(source, "empty")},
		},
		{
			name:    "a skill that is a file",
			declare: []string{"a-file-not-a-directory"},
			source:  source,
			want:    ErrSkillContent,
			names:   []string{`"a-file-not-a-directory"`},
		},
		{
			name:    "a skill that is not there",
			declare: []string{"code-review", "no-such-skill"},
			source:  source,
			want:    ErrSkillNotFound,
			names:   []string{`"no-such-skill"`, filepath.Join(source, "no-such-skill")},
		},
		{
			name:    "a name holding a path separator",
			declare: []string{"../elsewhere/code-review"},
			source:  source,
			want:    ErrSkillName,
			names:   []string{`"../elsewhere/code-review"`},
		},
		{
			name:    "an empty name",
			declare: []string{""},
			source:  source,
			want:    ErrSkillName,
		},
		{
			name:    "a name declared twice",
			declare: []string{"code-review", "code-review"},
			source:  source,
			want:    ErrSkillName,
			names:   []string{`"code-review"`},
		},
		{
			name:    "skills declared with no skills_dir",
			declare: []string{"code-review"},
			source:  "",
			want:    ErrSkillsSource,
			names:   []string{`"code-review"`, profile.SpecKeySkillsDir},
		},
		{
			name:    "a skills_dir that is not absolute",
			declare: []string{"code-review"},
			source:  "relative/skills",
			want:    ErrSkillsSource,
			names:   []string{`"relative/skills"`},
		},
		{
			name:    "a skills_dir that is not there",
			declare: []string{"code-review"},
			source:  filepath.Join(source, "no-such-directory"),
			want:    ErrSkillsSource,
			names:   []string{filepath.Join(source, "no-such-directory")},
		},
		{
			name:    "a skills_dir that is a file",
			declare: []string{"code-review"},
			source:  filepath.Join(source, "a-file-not-a-directory"),
			want:    ErrSkillsSource,
			names:   []string{filepath.Join(source, "a-file-not-a-directory")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := skillsInstance(t, tt.source, tt.declare...)

			files, err := RenderSkills(inst)
			if !errors.Is(err, tt.want) {
				t.Fatalf("RenderSkills() error = %v, want %v; it rendered %v", err, tt.want, filePaths(files))
			}
			for _, want := range tt.names {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error %q does not name %s", err, want)
				}
			}
		})
	}
}

// TestSkillsRefuseSomethingThatIsNotAFile keeps a directory symlink out of a
// planted skill. A symlink to a regular file is read through and copied by
// value, which is the point; anything else would be planted as a file that is
// not one.
func TestSkillsRefuseSomethingThatIsNotAFile(t *testing.T) {
	source := t.TempDir()
	writeSkillTree(t, source, "code-review", map[string]string{SkillFileName: "# code review\n"})
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(source, "code-review", "linked")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	_, err := RenderSkills(skillsInstance(t, source, "code-review"))
	if !errors.Is(err, ErrTreeContent) {
		t.Fatalf("RenderSkills() error = %v, want ErrTreeContent", err)
	}
	// The refusal names the link and what it points at. A copier that walks a
	// docs tree will meet this far more often than one that walks a skill, and
	// "not a regular file" without a path is a message that sends the operator
	// looking.
	for _, want := range []string{"linked", elsewhere} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
}

// TestTreeCopyRefusesADanglingSymlink is the other half of the symlink rule,
// and the one a skills package almost never hits. A link whose target was
// removed is refused by the same sentinel rather than surfacing as a bare stat
// failure, so a caller can tell it from a filesystem going wrong.
func TestTreeCopyRefusesADanglingSymlink(t *testing.T) {
	source := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "never-written.md"),
		filepath.Join(source, "dangling.md")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	_, err := CopyTree(source, "docs")
	if !errors.Is(err, ErrTreeContent) {
		t.Fatalf("CopyTree() error = %v, want ErrTreeContent", err)
	}
	if !strings.Contains(err.Error(), "dangling.md") {
		t.Errorf("the refusal %q does not name the link", err)
	}
}

// TestTreeCopyFollowsALinkToAFile records a property rather than guarding a
// decision. A symlink to a regular file is copied by value even when its target
// lies outside the source directory. That is what the skills copier has always
// done; narrowing it while generalizing the copier would be a behaviour change
// smuggled into a feature.
func TestTreeCopyFollowsALinkToAFile(t *testing.T) {
	source := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target.md")
	if err := os.WriteFile(outside, []byte("from outside the source\n"), 0o644); err != nil {
		t.Fatalf("write the target: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "link.md")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	files, err := CopyTree(source, "docs")
	if err != nil {
		t.Fatalf("CopyTree(): %v", err)
	}
	if got := string(fileByPath(t, files, "docs/link.md").Content); got != "from outside the source\n" {
		t.Errorf("the link planted %q, want its target's bytes", got)
	}
}

// TestSkillsExpandALeadingTilde covers the one form a profile is allowed to
// write a home-relative skills directory in. Cairn ships no skills, so the
// directory is always somewhere on the operator's machine, and writing it out
// in full in every profile is how it goes stale.
//
// The home it expands against is the instance's, not the process's: a renderer
// consults nothing outside the instance it was handed, which is also why this
// test does not have to touch the environment to exercise the expansion.
func TestSkillsExpandALeadingTilde(t *testing.T) {
	home := t.TempDir()
	source := filepath.Join(home, "skills")
	writeSkillTree(t, source, "code-review", map[string]string{SkillFileName: "# code review\n"})

	inst := skillsInstance(t, "~/skills", "code-review")
	inst.Home = home
	files, err := RenderSkills(inst)
	if err != nil {
		t.Fatalf("RenderSkills() with a home-relative skills_dir: %v", err)
	}
	if got := filePaths(files); !slices.Equal(got, []string{".claude/skills/code-review/SKILL.md"}) {
		t.Errorf("rendered %v, want the one skill under %s", got, ".claude/skills")
	}

	// With no home on the instance there is nothing to expand against, and the
	// failure says which key could not be resolved rather than resolving
	// somewhere unexpected.
	inst.Home = ""
	if _, err := RenderSkills(inst); !errors.Is(err, ErrSkillsSource) {
		t.Errorf("RenderSkills() with no home = %v, want ErrSkillsSource", err)
	}
}

// TestSkillsAreAbsentWhenNoneAreDeclared covers the profile that declares no
// skills at all, including one that declares a directory and nothing to copy
// out of it.
func TestSkillsAreAbsentWhenNoneAreDeclared(t *testing.T) {
	for _, manifest := range []string{"", `{}`, `{"skills": []}`, `{"skills": null}`,
		`{"skills": [], "skills_dir": "/nowhere"}`} {
		inst := testInstance(t, profile.Resolved{ID: "quiet", Spec: testSpec(t, manifest)})

		files, err := RenderSkills(inst)
		if err != nil {
			t.Fatalf("RenderSkills() with manifest %q: %v", manifest, err)
		}
		if len(files) != 0 {
			t.Errorf("RenderSkills() with manifest %q rendered %v, want nothing", manifest, filePaths(files))
		}
	}
}

// TestSkillsRefuseALayoutWithNoSkillsDirectory covers a layout that declares
// nowhere to plant a skill while the profile declares one to plant.
func TestSkillsRefuseALayoutWithNoSkillsDirectory(t *testing.T) {
	source := t.TempDir()
	writeSkillTree(t, source, "code-review", map[string]string{SkillFileName: "# code review\n"})
	inst := skillsInstance(t, source, "code-review")
	inst.Layout.SkillsDir = ""

	if _, err := RenderSkills(inst); !errors.Is(err, ErrProviderLayout) {
		t.Fatalf("RenderSkills() error = %v, want ErrProviderLayout", err)
	}
}

// TestSkillFileModeMapsOnlyTheExecutableBit states what survives the copy: a
// file that could be run stays runnable, and every other mode becomes the
// default, so a stray 0600 in the source cannot plant a skill the harness
// cannot read.
func TestSkillFileModeMapsOnlyTheExecutableBit(t *testing.T) {
	tests := []struct {
		source fs.FileMode
		want   fs.FileMode
	}{
		{0o644, DefaultFileMode},
		{0o600, DefaultFileMode},
		{0o400, DefaultFileMode},
		{0o755, ExecFileMode},
		{0o700, ExecFileMode},
		{0o111, ExecFileMode},
	}
	for _, tt := range tests {
		if got := treeFileMode(tt.source); got != tt.want {
			t.Errorf("treeFileMode(%v) = %v, want %v", tt.source, got, tt.want)
		}
	}
}

// TestSkillsExpandAVariable closes the same gap trees had. A skills directory
// is the other bare path a manifest declares, and an operator who learns that
// a slot's path takes a variable will write one here too.
func TestSkillsExpandAVariable(t *testing.T) {
	root := t.TempDir()
	writeSkillTree(t, root, "code-review", map[string]string{SkillFileName: "# code review\n"})

	inst := skillsInstance(t, "$SKILLS_ROOT", "code-review")
	inst.Env = func(name string) string {
		if name == "SKILLS_ROOT" {
			return root
		}
		return ""
	}

	files, err := RenderSkills(inst)
	if err != nil {
		t.Fatalf("RenderSkills(): %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("rendered %v, want the variable expanded", filePaths(files))
	}
}

// TestSkillsNameAnUnsetVariableAsItWasWritten is the diagnostic, for the same
// reason a tree's is: an error quoting only the expansion sends the operator
// looking for a path they never wrote.
func TestSkillsNameAnUnsetVariableAsItWasWritten(t *testing.T) {
	inst := skillsInstance(t, "$SKILLS_ROOT/skills", "code-review")
	inst.Env = func(string) string { return "" }

	_, err := RenderSkills(inst)
	if !errors.Is(err, ErrSkillsSource) {
		t.Fatalf("RenderSkills() = %v, want ErrSkillsSource", err)
	}
	if !strings.Contains(err.Error(), "$SKILLS_ROOT/skills") {
		t.Errorf("the refusal %q does not quote what the operator wrote", err)
	}
}

// TestInstallSkillsAreTheOtherKeyAndTheSameRender pins the split of one key
// into two: the boot directory's skills and the installed layer's are separate
// declarations, and neither renderer reads the other's.
//
// The render itself is shared, so what is checked here is only what a shared
// body cannot check for itself — which list each entry point read.
func TestInstallSkillsAreTheOtherKeyAndTheSameRender(t *testing.T) {
	source := t.TempDir()
	writeSkillTree(t, source, "booted", map[string]string{SkillFileName: "# booted\n"})
	writeSkillTree(t, source, "installed", map[string]string{
		SkillFileName:        "# installed\n",
		"references/note.md": "beside it\n",
	})

	inst := testInstance(t, profile.Resolved{ID: "base", Spec: testSpec(t, `{
		"skills":     ["booted"],
		"install":    {"skills": ["installed"]},
		"skills_dir": `+strconv.Quote(source)+`
	}`)})

	booted, err := RenderSkills(inst)
	if err != nil {
		t.Fatalf("RenderSkills(): %v", err)
	}
	if want := []string{".claude/skills/booted/SKILL.md"}; !slices.Equal(filePaths(booted), want) {
		t.Errorf("RenderSkills() = %v, want %v", filePaths(booted), want)
	}

	installed, err := RenderInstallSkills(inst)
	if err != nil {
		t.Fatalf("RenderInstallSkills(): %v", err)
	}
	want := []string{
		".claude/skills/installed/SKILL.md",
		".claude/skills/installed/references/note.md",
	}
	if !slices.Equal(filePaths(installed), want) {
		t.Errorf("RenderInstallSkills() = %v, want %v", filePaths(installed), want)
	}
}

// TestInstallSkillsPlantNothingIntoABootDirectory is the net effect the split
// exists for. A profile holding only the installed set renders nothing beside
// a boot directory, which is what makes the next stage's keyed merge a no-op
// for skills rather than a union of every profile's.
func TestInstallSkillsPlantNothingIntoABootDirectory(t *testing.T) {
	source := t.TempDir()
	writeSkillTree(t, source, "installed", map[string]string{SkillFileName: "# installed\n"})

	inst := testInstance(t, profile.Resolved{ID: "base", Spec: testSpec(t, `{
		"install":    {"skills": ["installed"]},
		"skills_dir": `+strconv.Quote(source)+`
	}`)})

	files, err := RenderSkills(inst)
	if err != nil {
		t.Fatalf("RenderSkills(): %v", err)
	}
	if len(files) != 0 {
		t.Errorf("RenderSkills() rendered %v; install.skills is not a boot directory's", filePaths(files))
	}
}

// TestInstallSkillsNameTheirOwnKeyInEveryRefusal is the diagnostic the shared
// body has to carry. The two renders differ in one thing an operator can act
// on — which declaration they read — so an error that named "spec.skills" for
// a set written under install would send them to edit a key they never wrote.
func TestInstallSkillsNameTheirOwnKeyInEveryRefusal(t *testing.T) {
	source := t.TempDir()
	writeSkillTree(t, source, "installed", map[string]string{SkillFileName: "# installed\n"})

	tests := map[string]struct {
		manifest string
		layout   string
		want     error
	}{
		"no skills directory": {
			manifest: `{"install": {"skills": ["installed"]}}`,
			want:     ErrSkillsSource,
		},
		"an empty name": {
			manifest: `{"install": {"skills": [""]}, "skills_dir": ` + strconv.Quote(source) + `}`,
			want:     ErrSkillName,
		},
		"the same name twice": {
			manifest: `{"install": {"skills": ["installed","installed"]}, "skills_dir": ` + strconv.Quote(source) + `}`,
			want:     ErrSkillName,
		},
		"a skill that is not there": {
			manifest: `{"install": {"skills": ["absent"]}, "skills_dir": ` + strconv.Quote(source) + `}`,
			want:     ErrSkillNotFound,
		},
		"nowhere to plant it": {
			manifest: `{"install": {"skills": ["installed"]}, "skills_dir": ` + strconv.Quote(source) + `}`,
			layout:   "-",
			want:     ErrProviderLayout,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			inst := testInstance(t, profile.Resolved{ID: "base", Spec: testSpec(t, tc.manifest)})
			if tc.layout == "-" {
				inst.Layout.SkillsDir = ""
			}
			_, err := RenderInstallSkills(inst)
			if !errors.Is(err, tc.want) {
				t.Fatalf("RenderInstallSkills() = %v, want %v", err, tc.want)
			}
			if got := err.Error(); !strings.Contains(got, "spec.install.skills") {
				t.Errorf("the refusal %q does not name the key it read", got)
			}
		})
	}
}
