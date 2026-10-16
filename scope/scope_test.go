package scope_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cairn/scope"
)

// TestContainsDirectly covers the cases every filesystem can exhibit: the
// scope itself, something inside it, something beside it, and a path that does
// not exist yet — which is the ordinary case, since the boot directory is
// checked before it is created.
func TestContainsDirectly(t *testing.T) {
	root := t.TempDir()
	scopeDir := filepath.Join(root, "repo")
	beside := filepath.Join(root, "elsewhere")
	for _, dir := range []string{scopeDir, beside, filepath.Join(scopeDir, "nested")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	cases := []struct {
		name      string
		candidate string
		want      bool
	}{
		{"the scope itself", scopeDir, true},
		{"a directory inside it", filepath.Join(scopeDir, "nested"), true},
		{"a directory that does not exist yet, inside it", filepath.Join(scopeDir, "a", "b", "c"), true},
		{"a sibling", beside, false},
		{"a directory that does not exist yet, outside it", filepath.Join(beside, "a", "b"), false},
		{"the parent", root, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scope.Contains(scopeDir, tc.candidate)
			if err != nil {
				t.Fatalf("Contains(%q, %q): %v", scopeDir, tc.candidate, err)
			}
			if got != tc.want {
				t.Errorf("Contains(%q, %q) = %v, want %v", scopeDir, tc.candidate, got, tc.want)
			}
		})
	}
}

// TestContainsEmptyScope covers the empty scope, which declares no target and
// therefore contains nothing.
func TestContainsEmptyScope(t *testing.T) {
	got, err := scope.Contains("", t.TempDir())
	if err != nil {
		t.Fatalf("Contains with an empty scope: %v", err)
	}
	if got {
		t.Error("the empty scope reported that it contains a directory")
	}
}

// TestContainsBlankCandidate covers a candidate that names no directory. It is
// an error rather than a false, because answering "not inside" to a question
// that was never well formed is the permissive direction.
func TestContainsBlankCandidate(t *testing.T) {
	if _, err := scope.Contains(t.TempDir(), "   "); err == nil {
		t.Error("a blank candidate was accepted")
	}
}

// TestContainsMissingScope covers a scope the filesystem cannot identify. It
// contains nothing — which is exactly why Parse refuses one.
func TestContainsMissingScope(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	got, err := scope.Contains(missing, filepath.Join(missing, "inside"))
	if err != nil {
		t.Fatalf("Contains with a missing scope: %v", err)
	}
	if got {
		t.Error("a scope that does not exist reported that it contains a directory")
	}
}

// TestContainsThroughSymlink is the first of the three spelling axes: one
// directory reached by two paths, where a prefix comparison of the strings
// says "not inside" and the filesystem says "inside". Symlinks are the axis
// every platform can exhibit, so this one never skips.
func TestContainsThroughSymlink(t *testing.T) {
	t.Log(`axis "symlink" is reachable on this host`)

	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "nested"), 0o755); err != nil {
		t.Fatalf("create %s: %v", real, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("this host cannot create symlinks: %v", err)
	}

	// The scope was parsed from the real path; the candidate arrives spelled
	// through the link. No comparison of the two strings sees the containment.
	got, err := scope.Contains(real, filepath.Join(link, "nested"))
	if err != nil {
		t.Fatalf("Contains through a symlink: %v", err)
	}
	if !got {
		t.Errorf("%s is inside %s, reached through the symlink %s, and was reported as outside",
			filepath.Join(link, "nested"), real, link)
	}
}

// TestContainsFoldedCase is the second spelling axis. An ordinary Linux volume
// does not fold letter case, so this assertion cannot execute there; APFS
// does, natively, with no fixture machinery. The probe is what keeps the skip
// honest — see the skip census in .github/workflows/check.yml.
func TestContainsFoldedCase(t *testing.T) {
	root := t.TempDir()
	scopeDir := filepath.Join(root, "Repo")
	if err := os.MkdirAll(filepath.Join(scopeDir, "Nested"), 0o755); err != nil {
		t.Fatalf("create %s: %v", scopeDir, err)
	}
	folded := filepath.Join(root, "repo", "nested")
	if _, err := os.Stat(folded); err != nil {
		t.Skip("this filesystem does not fold letter case, so it cannot exhibit the defect")
	}
	t.Log(`axis "case" is reachable on this host`)

	got, err := scope.Contains(scopeDir, folded)
	if err != nil {
		t.Fatalf("Contains with a case-folded candidate: %v", err)
	}
	if !got {
		t.Errorf("%s and %s are one directory on this volume, and the candidate was reported as outside %s",
			folded, filepath.Join(scopeDir, "Nested"), scopeDir)
	}
}

// TestContainsFoldedUnicodeForm is the third spelling axis. The two names
// below are the same grapheme in NFC and NFD; a volume that normalizes Unicode
// form reaches one directory by both, and a byte comparison of the strings
// sees two.
func TestContainsFoldedUnicodeForm(t *testing.T) {
	const (
		nfc = "café"  // é as one code point
		nfd = "café" // e + combining acute
	)
	root := t.TempDir()
	scopeDir := filepath.Join(root, nfc)
	if err := os.MkdirAll(filepath.Join(scopeDir, "nested"), 0o755); err != nil {
		t.Fatalf("create %s: %v", scopeDir, err)
	}
	other := filepath.Join(root, nfd, "nested")
	if _, err := os.Stat(other); err != nil {
		t.Skip("this filesystem does not fold Unicode form, so it cannot exhibit the defect")
	}
	t.Log(`axis "unicode-form" is reachable on this host`)

	got, err := scope.Contains(scopeDir, other)
	if err != nil {
		t.Fatalf("Contains with a form-folded candidate: %v", err)
	}
	if !got {
		t.Errorf("%s and %s are one directory on this volume, and the candidate was reported as outside %s",
			other, filepath.Join(scopeDir, "nested"), scopeDir)
	}
}

// TestCheckBootDir covers the one validation Cairn performs on a scope: the
// boot directory must never land at or inside it.
func TestCheckBootDir(t *testing.T) {
	root := t.TempDir()
	scopeDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(scopeDir, 0o755); err != nil {
		t.Fatalf("create %s: %v", scopeDir, err)
	}

	inside := []string{scopeDir, filepath.Join(scopeDir, "runtime", "boot", "eng", "s1")}
	for _, dir := range inside {
		if err := scope.CheckBootDir(scopeDir, dir); !errors.Is(err, scope.ErrInsideScope) {
			t.Errorf("CheckBootDir(%q, %q) = %v, want ErrInsideScope", scopeDir, dir, err)
		}
	}

	outside := filepath.Join(root, "runtime", "boot", "eng", "s1")
	if err := scope.CheckBootDir(scopeDir, outside); err != nil {
		t.Errorf("CheckBootDir(%q, %q) = %v, want nil", scopeDir, outside, err)
	}

	// The empty scope declares no target, so it refuses nothing.
	if err := scope.CheckBootDir("", filepath.Join(root, "anything")); err != nil {
		t.Errorf("CheckBootDir with an empty scope = %v, want nil", err)
	}
}

// TestParse covers scope resolution: the empty scope, tilde expansion, the
// refusal of a scope that does not exist, and symlink resolution.
func TestParse(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("create %s: %v", real, err)
	}
	// A t.TempDir() on macOS is itself reached through /var -> /private/var,
	// so the expected value is the resolved form of the path, not the path.
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("resolve %s: %v", real, err)
	}

	t.Run("the empty scope", func(t *testing.T) {
		got, err := scope.Parse("   ", root)
		if err != nil || got != "" {
			t.Errorf("Parse of a blank scope = %q, %v; want \"\", nil", got, err)
		}
	})

	t.Run("tilde expansion", func(t *testing.T) {
		got, err := scope.Parse("~/real", root)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got != resolvedReal {
			t.Errorf("Parse(\"~/real\") = %q, want %q", got, resolvedReal)
		}
	})

	t.Run("a scope that does not exist", func(t *testing.T) {
		_, err := scope.Parse(filepath.Join(root, "gone"), root)
		if !errors.Is(err, scope.ErrNotDirectory) {
			t.Errorf("Parse of a missing directory = %v, want ErrNotDirectory", err)
		}
	})

	t.Run("a scope that is a file", func(t *testing.T) {
		file := filepath.Join(root, "file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
		if _, err := scope.Parse(file, root); !errors.Is(err, scope.ErrNotDirectory) {
			t.Errorf("Parse of a file = %v, want ErrNotDirectory", err)
		}
	})

	t.Run("symlinks resolve", func(t *testing.T) {
		link := filepath.Join(root, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("this host cannot create symlinks: %v", err)
		}
		got, err := scope.Parse(link, root)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if got != resolvedReal {
			t.Errorf("Parse(%q) = %q, want the symlink's target %q", link, got, resolvedReal)
		}
	})

	t.Run("a tilde with no home", func(t *testing.T) {
		if _, err := scope.Parse("~/anything", ""); !errors.Is(err, scope.ErrNoHome) {
			t.Errorf("Parse with no home = %v, want ErrNoHome", err)
		}
	})
}
