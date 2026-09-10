package install

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/profile"
)

// hostileUmask sets a umask that would strip every bit past the owner's from a
// file created with it, and restores the process's own on the way out.
//
// It is what makes the mode assertions below mean something: under the usual
// 022 the modes cairn writes survive whether or not anything chmods them, so a
// test run under it would pass against a write that had dropped the chmod.
//
// The umask is process-wide, so the test that sets it must not call
// t.Parallel: the go tool releases parallel tests only once every sequential
// one has returned, which is what keeps this from reaching a test in the
// external test package beside it.
func hostileUmask(t *testing.T) {
	t.Helper()
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })
}

// installed reads one installed file by its slash-separated path inside the
// root.
func installed(t *testing.T, root Root, rel string) []byte {
	t.Helper()
	path, err := root.Path(rel)
	if err != nil {
		t.Fatalf("resolve %q inside %s: %v", rel, root, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the installed %s: %v", rel, err)
	}
	return content
}

// rootEntries returns the names of everything directly inside the root.
func rootEntries(t *testing.T, root Root) []string {
	t.Helper()
	entries, err := os.ReadDir(root.Dir())
	if err != nil {
		t.Fatalf("read the install root %s: %v", root, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// TestInstallWritesWhatRenderReturned checks the two halves agree: every file
// the render produced is on disk with those exact bytes, and the result's
// manifest is the render's paths in render order.
func TestInstallWritesWhatRenderReturned(t *testing.T) {
	lay := fullyDeclaredLayer(t)
	rendered, err := Render(lay)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	result, err := Install(lay)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result == nil {
		t.Fatal("Install returned no result and no error")
	}
	if result.Root != lay.Root.Dir() {
		t.Errorf("Result.Root is %q, want %q", result.Root, lay.Root.Dir())
	}
	if want := renderedPaths(rendered); !slices.Equal(result.Files, want) {
		t.Errorf("Result.Files is\n\t%v\nwant\n\t%v", result.Files, want)
	}
	for _, f := range rendered {
		if got := installed(t, lay.Root, f.Path); !bytes.Equal(got, f.Content) {
			t.Errorf("%s holds %q, want %q", f.Path, got, f.Content)
		}
	}
}

// TestInstallWritesModesPastTheUmask checks that the mode a file is installed
// with is the mode the render asked for, and not whatever the shell cairn was
// launched from left of it. A skill's executable bit is load-bearing: a script
// that arrives without it is a skill that fails halfway through instead of at
// boot.
func TestInstallWritesModesPastTheUmask(t *testing.T) {
	hostileUmask(t)
	lay := fullyDeclaredLayer(t)
	if _, err := Install(lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for rel, want := range map[string]fs.FileMode{
		".claude/AGENTS.md":                   bootdir.DefaultFileMode,
		".claude/CLAUDE.md":                   bootdir.DefaultFileMode,
		".claude/settings.json":               bootdir.DefaultFileMode,
		".claude/skills/code-review/SKILL.md": bootdir.DefaultFileMode,
		".claude/skills/code-review/run.sh":   bootdir.ExecFileMode,
	} {
		path, err := lay.Root.Path(rel)
		if err != nil {
			t.Fatalf("resolve %q: %v", rel, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat the installed %s: %v", rel, err)
		}
		if got := info.Mode().Perm(); got != want.Perm() {
			t.Errorf("%s installed with mode %v, want %v — the umask reached it", rel, got, want.Perm())
		}
	}
}

// TestInstallCreatesTheDirectoriesTheRenderNeeds checks that a skill's
// references subdirectory arrives as a directory rather than as a missing
// parent the write fails on.
func TestInstallCreatesTheDirectoriesTheRenderNeeds(t *testing.T) {
	lay := fullyDeclaredLayer(t)
	if _, err := Install(lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, rel := range []string{".claude", ".claude/skills/code-review", ".claude/skills/code-review/references"} {
		path, err := lay.Root.Path(rel)
		if err != nil {
			t.Fatalf("resolve %q: %v", rel, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", rel)
		}
	}
	if got := string(installed(t, lay.Root, ".claude/skills/code-review/references/style.md")); got != "prefer clarity\n" {
		t.Errorf("the skill's reference holds %q", got)
	}
}

// TestInstallGeneratedMarkerSurvivesToDisk checks the marker round-trips
// through the write. It is the line an operator opening the installed file
// sees, so it is worth asserting where they would read it rather than only in
// the render.
func TestInstallGeneratedMarkerSurvivesToDisk(t *testing.T) {
	lay := fullyDeclaredLayer(t)
	if _, err := Install(lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	content := string(installed(t, lay.Root, ".claude/AGENTS.md"))
	if want := GeneratedMarker("base") + "\n\n"; !strings.HasPrefix(content, want) {
		t.Errorf("the installed .claude/AGENTS.md opens\n%s\nwant it to open with\n%s", content, want)
	}
	pointer := string(installed(t, lay.Root, ".claude/CLAUDE.md"))
	if want := "@" + "AGENTS.md" + "\n"; pointer != want {
		t.Errorf("the installed .claude/CLAUDE.md holds %q, want the template's own text %q", pointer, want)
	}
}

// TestInstallRoundTripsHTMLSpecialCharactersInSettings holds the settings
// document to the operator's exact characters across the whole install path.
//
// [json.Marshal] escapes "<", ">" and "&" into their \u00XX forms by default,
// so anything on this path that re-marshalled a merged [profile.Spec]
// would silently rewrite a settings document containing a shell pipeline, a
// glob, or a comparison. The catalog builds every manifest value with escaping
// off for the same reason, and the cascade re-encodes a merged one the same
// way. This is where the mistake gets caught if it is ever reintroduced.
//
// The render is laid out, so the comparison is against the document laid out
// and the escape check is against the characters. Laying a document out moves
// whitespace between tokens and never touches what is inside a string, which
// is exactly the difference this test is drawing.
func TestInstallRoundTripsHTMLSpecialCharactersInSettings(t *testing.T) {
	const settings = `{"hooks": {"PreToolUse": "a < b && c > d"}}`
	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderClaude,
		Spec:     fixtureSpec(t, `{"settings": {"claude": `+settings+`}}`),
	})
	if _, err := Install(lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := string(installed(t, lay.Root, ".claude/settings.json"))
	if want := string(bootdir.IndentJSON([]byte(settings))); got != want {
		t.Fatalf("the installed settings hold\n\t%s\nwant\n\t%s", got, want)
	}
	for _, escaped := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(got, escaped) {
			t.Errorf("the installed settings hold %s: something on the install path "+
				"re-marshalled the spec and HTML-escaped it", escaped)
		}
	}
}

// TestInstallAbstractProfile holds plan §7 on the write path too: the
// installed layer is normally rendered from the abstract root of the cascade,
// so installing one has to work and not only render.
func TestInstallAbstractProfile(t *testing.T) {
	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Name:     "Base",
		Abstract: true,
		Provider: profile.ProviderClaude,
		Bodies:   []profile.Body{{ID: "base", Text: "the abstract root"}},
	})
	result, err := Install(lay)
	if err != nil {
		t.Fatalf("Install an abstract profile: %v", err)
	}
	want := []string{".claude/AGENTS.md", ".claude/CLAUDE.md"}
	if !slices.Equal(result.Files, want) {
		t.Fatalf("Install an abstract profile wrote %v, want %v", result.Files, want)
	}
	if content := string(installed(t, lay.Root, ".claude/AGENTS.md")); !strings.Contains(content, "the instruction file") {
		t.Errorf("the installed instruction file lost the template's content:\n%s", content)
	}
}

// TestInstallFailedRenderWritesNothing checks the ordering that makes a
// half-installed layer impossible: everything is rendered before anything is
// written, so a manifest error leaves the root exactly as it was — with no
// provider directory, and no staging directory either.
func TestInstallFailedRenderWritesNothing(t *testing.T) {
	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderClaude,
		Spec: fixtureSpec(t, fmt.Sprintf(
			`{"settings": {"claude": {"model": "opus"}}, "install": {"skills": ["code-review"]}, "skills_dir": %s}`,
			strconv.Quote(filepath.Join(t.TempDir(), "absent")))),
	})
	before := rootEntries(t, lay.Root)

	if _, err := Install(lay); !errors.Is(err, bootdir.ErrSkillsSource) {
		t.Fatalf("Install with a missing skills directory = %v, want bootdir.ErrSkillsSource", err)
	}
	if after := rootEntries(t, lay.Root); !slices.Equal(after, before) {
		t.Errorf("the install root holds %v after a failed render, want %v", after, before)
	}
}

// TestInstallLeavesTheStagingDirectoryNowhere checks the staging directory is
// cleaned up on the success path. It sits inside the install root, which is
// the operator's home, so one left behind is litter in the most visible
// directory on the machine.
func TestInstallLeavesTheStagingDirectoryNowhere(t *testing.T) {
	lay := fullyDeclaredLayer(t)
	if _, err := Install(lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, name := range rootEntries(t, lay.Root) {
		if strings.HasPrefix(name, strings.TrimSuffix(StagingPattern, "*")) {
			t.Errorf("the install root still holds the staging directory %q", name)
		}
	}
}

// TestInstallLeavesTheOperatorsOwnFilesAlone checks that installing into a
// root that already holds things cairn did not render touches none of them.
// The install root is a home directory, and almost everything in it is
// somebody else's.
func TestInstallLeavesTheOperatorsOwnFilesAlone(t *testing.T) {
	lay := fullyDeclaredLayer(t)
	foreign := map[string]string{
		".zshrc":                "export PATH=/usr/local/bin:$PATH\n",
		".claude/todos.json":    "[]\n",
		".claude/projects/x.md": "notes\n",
	}
	for rel, content := range foreign {
		path, err := lay.Root.Path(rel)
		if err != nil {
			t.Fatalf("resolve %q: %v", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create the directory for %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	if _, err := Install(lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for rel, want := range foreign {
		if got := string(installed(t, lay.Root, rel)); got != want {
			t.Errorf("%s holds %q after an install, want %q", rel, got, want)
		}
	}
}

// TestInstallRemovesNothingAPreviousInstallLeft states the boundary in a test
// so that a later reader does not add a sweep here.
//
// A file cairn rendered once and no longer renders stays where it is.
// Reporting it is `cairn install --check`'s job, and removing it is the
// operator's: cairn does not delete out of a home directory on the strength of
// its own bookkeeping.
func TestInstallRemovesNothingAPreviousInstallLeft(t *testing.T) {
	skillsDir := t.TempDir()
	fixtureSkill(t, skillsDir, "code-review", map[string]string{"SKILL.md": "# Code review\n"})

	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderClaude,
		Spec: fixtureSpec(t, fmt.Sprintf(`{"install": {"skills": ["code-review"]}, "skills_dir": %s}`,
			strconv.Quote(skillsDir))),
	})
	if _, err := Install(lay); err != nil {
		t.Fatalf("the first Install: %v", err)
	}

	// The profile stops declaring the skill, and the settings document it
	// never declared stays undeclared.
	lay.Profile.Spec = nil
	result, err := Install(lay)
	if err != nil {
		t.Fatalf("the second Install: %v", err)
	}
	if slices.Contains(result.Files, ".claude/skills/code-review/SKILL.md") {
		t.Fatalf("the second install still rendered the skill: %v", result.Files)
	}
	if got := string(installed(t, lay.Root, ".claude/skills/code-review/SKILL.md")); got != "# Code review\n" {
		t.Errorf("the orphaned skill holds %q; install removed or rewrote it, which is "+
			"`install --check`'s finding to report and the operator's to act on", got)
	}
}

// TestInstallOverwritesItsOwnOutput checks that installing twice leaves the
// second render's bytes, so an operator re-running the command after editing a
// profile gets what they changed.
func TestInstallOverwritesItsOwnOutput(t *testing.T) {
	lay := fixtureLayer(t, profile.Resolved{
		ID:       "base",
		Provider: profile.ProviderClaude,
		Bodies:   []profile.Body{{ID: "base", Text: "first"}},
	})
	if _, err := Install(lay); err != nil {
		t.Fatalf("the first Install: %v", err)
	}
	// The template is what the instruction file is rendered from now, so
	// changing the profile means changing that.
	lay.Templates["AGENTS.md"] = "second\n"
	if _, err := Install(lay); err != nil {
		t.Fatalf("the second Install: %v", err)
	}
	content := string(installed(t, lay.Root, ".claude/AGENTS.md"))
	if strings.Contains(content, "first") || !strings.Contains(content, "second") {
		t.Errorf("the installed instruction file holds\n%s\nwant the second render", content)
	}
}

// TestInstallWithoutAProfile checks a layer carrying no resolved profile is
// reported rather than dereferenced.
func TestInstallWithoutAProfile(t *testing.T) {
	root, err := NewRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewRoot on a temporary directory: %v", err)
	}
	for name, lay := range map[string]*Layer{
		"a nil layer": nil,
		"no profile":  {Root: root},
	} {
		if _, err := Install(lay); !errors.Is(err, ErrNoProfile) {
			t.Errorf("Install with %s = %v, want ErrNoProfile", name, err)
		}
	}
}

// TestInstallChecksTheRootBeforeRendering covers the three ways a root can be
// unusable. Cairn never creates the install root: a missing one means it
// resolved the wrong path, which is a thing to report rather than to make.
func TestInstallChecksTheRootBeforeRendering(t *testing.T) {
	resolved := profile.Resolved{ID: "base", Provider: profile.ProviderClaude}

	if _, err := Install(&Layer{Profile: &resolved}); !errors.Is(err, ErrNoRoot) {
		t.Errorf("Install with the zero root = %v, want ErrNoRoot", err)
	}

	missing, err := NewRoot(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	if _, err := Install(&Layer{Root: missing, Profile: &resolved}); !errors.Is(err, ErrRootNotFound) {
		t.Errorf("Install into a missing root = %v, want ErrRootNotFound", err)
	}
	if _, err := os.Stat(missing.Dir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Install created the missing root; cairn never creates one")
	}

	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	notDir, err := NewRoot(file)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	if _, err := Install(&Layer{Root: notDir, Profile: &resolved}); !errors.Is(err, ErrRootNotDirectory) {
		t.Errorf("Install into a file = %v, want ErrRootNotDirectory", err)
	}
}

// TestWriteFilesWritesNothingForAnEmptyRender checks the write does not create
// a staging directory it has no use for.
func TestWriteFilesWritesNothingForAnEmptyRender(t *testing.T) {
	root, err := NewRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewRoot on a temporary directory: %v", err)
	}
	if err := writeFiles(root, nil); err != nil {
		t.Fatalf("writeFiles with no files: %v", err)
	}
	if entries := rootEntries(t, root); len(entries) != 0 {
		t.Errorf("the install root holds %v, want nothing", entries)
	}
}

// TestWriteFilesRejectsAPathOutsideTheRoot checks that a destination is
// resolved through [Root.Path] before anything is staged, so a path that
// escapes fails the write before it has begun rather than part way through it.
func TestWriteFilesRejectsAPathOutsideTheRoot(t *testing.T) {
	root, err := NewRoot(t.TempDir())
	if err != nil {
		t.Fatalf("NewRoot on a temporary directory: %v", err)
	}
	files := []File{
		{Path: ".claude/AGENTS.md", Content: []byte("first\n")},
		{Path: "../escaped.md", Content: []byte("second\n")},
	}
	if err := writeFiles(root, files); !errors.Is(err, ErrRootRelativePath) {
		t.Fatalf("writeFiles with an escaping path = %v, want ErrRootRelativePath", err)
	}
	if entries := rootEntries(t, root); len(entries) != 0 {
		t.Errorf("the install root holds %v after a rejected write, want nothing", entries)
	}
}

// TestWriteModeSubstitutesTheDefault checks the zero mode means the default a
// boot directory plants with, so the two layers write the same file the same
// way.
func TestWriteModeSubstitutesTheDefault(t *testing.T) {
	if got := writeMode(File{}); got != bootdir.DefaultFileMode {
		t.Errorf("writeMode of the zero mode = %v, want %v", got, bootdir.DefaultFileMode)
	}
	if got := writeMode(File{Mode: bootdir.ExecFileMode}); got != bootdir.ExecFileMode {
		t.Errorf("writeMode of %v = %v", bootdir.ExecFileMode, got)
	}
}

// TestInstallRefusesAnOccupiedDestinationBeforeMovingAnything covers the
// failure a per-file move would otherwise deliver half way through.
//
// A rename onto a directory fails. Without the pre-flight, the files ahead of
// it in render order would already have moved, leaving the operator with part
// of one layer and part of another. A refusal is recoverable and half an
// installed layer is not.
func TestInstallRefusesAnOccupiedDestinationBeforeMovingAnything(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		occupy  func(t *testing.T, dest string)
		wantsIn string
	}{
		{
			name: "a directory",
			occupy: func(t *testing.T, dest string) {
				if err := os.MkdirAll(dest, 0o755); err != nil {
					t.Fatalf("create %s: %v", dest, err)
				}
			},
			wantsIn: "a directory",
		},
		{
			name: "a symbolic link",
			occupy: func(t *testing.T, dest string) {
				target := filepath.Join(t.TempDir(), "linked.md")
				if err := os.WriteFile(target, []byte("linked\n"), 0o644); err != nil {
					t.Fatalf("write %s: %v", target, err)
				}
				if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
					t.Fatalf("create %s: %v", filepath.Dir(dest), err)
				}
				if err := os.Symlink(target, dest); err != nil {
					t.Skipf("this platform cannot create a symbolic link: %v", err)
				}
			},
			wantsIn: "a symbolic link",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lay := fullyDeclaredLayer(t)
			rootDir := lay.Root.Dir()

			// The instruction file is first in render order, so occupying the
			// settings path proves the refusal happens before any move rather
			// than at the occupied file's turn.
			dest := filepath.Join(rootDir, ".claude", "settings.json")
			tc.occupy(t, dest)

			_, err := Install(lay)
			if !errors.Is(err, ErrDestinationOccupied) {
				t.Fatalf("Install = %v, want ErrDestinationOccupied", err)
			}
			if !strings.Contains(err.Error(), tc.wantsIn) {
				t.Errorf("the error does not say what is in the way: %v", err)
			}

			// Nothing ahead of it in render order moved.
			for _, rel := range []string{
				path.Join(".claude", "AGENTS.md"),
				path.Join(".claude", "CLAUDE.md"),
			} {
				if _, err := os.Lstat(filepath.Join(rootDir, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s was written before the install refused: %v", rel, err)
				}
			}
			// And no staging directory was left behind.
			entries, err := os.ReadDir(rootDir)
			if err != nil {
				t.Fatalf("read %s: %v", rootDir, err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".cairn-install-") {
					t.Errorf("a staging directory was left behind: %s", e.Name())
				}
			}
		})
	}
}

// TestInstallNamesTheComponentInTheWay covers the diagnostic for a path that
// cannot be walked because something above it is a file.
//
// The raw Lstat error names the leaf — a path that does not exist — and says
// it is not a directory, which reads like nonsense to whoever has to act on
// it. The component actually in the way is the one worth naming.
func TestInstallNamesTheComponentInTheWay(t *testing.T) {
	t.Parallel()
	lay := fullyDeclaredLayer(t)
	rootDir := lay.Root.Dir()

	blocker := filepath.Join(rootDir, ".claude", "skills")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(blocker), err)
	}
	if err := os.WriteFile(blocker, []byte("a file where a directory belongs\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", blocker, err)
	}

	_, err := Install(lay)
	if !errors.Is(err, ErrDestinationOccupied) {
		t.Fatalf("Install = %v, want ErrDestinationOccupied", err)
	}
	if !strings.Contains(err.Error(), blocker) {
		t.Errorf("the error does not name the component in the way (%s): %v", blocker, err)
	}
	if !strings.Contains(err.Error(), "is a file") {
		t.Errorf("the error does not say what is wrong with it: %v", err)
	}

	// Nothing moved: the refusal came before the first rename.
	for _, rel := range []string{
		path.Join(".claude", "AGENTS.md"),
		path.Join(".claude", "CLAUDE.md"),
		path.Join(".claude", "settings.json"),
	} {
		if _, err := os.Lstat(filepath.Join(rootDir, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was written before the install refused: %v", rel, err)
		}
	}
}
