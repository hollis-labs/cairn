package bootdir_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/bootdir"
)

// markRepo makes dir the root of a git working tree the cheap way: the marker
// is all [bootdir.CheckRoot] looks at, so a real `git init` would add a
// subprocess, a dependency on git being installed, and the operator's own
// gitconfig — for no additional coverage of a check that reads one entry name.
func markRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mark %s as a repository: %v", dir, err)
	}
	return dir
}

// markLinkedRepo makes dir the root of a working tree whose ".git" is a FILE
// rather than a directory. That is what git writes for a linked worktree and
// for a submodule, and a check that only looked for a directory would call
// such a tree "no repository" — silently, and in the permissive direction.
func markLinkedRepo(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	body := "gitdir: " + filepath.Join(t.TempDir(), ".git", "worktrees", "wt") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(body), 0o644); err != nil {
		t.Fatalf("mark %s as a linked worktree: %v", dir, err)
	}
	return dir
}

func mkdirAll(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	return dir
}

// TestCheckRootRefusesAForeignRepository covers the hazard itself, in both
// forms the marker takes. The boot root does not exist yet, which is the
// ordinary case: cairn creates it, and a check that waited for it would run
// after the damage was already configured.
func TestCheckRootRefusesAForeignRepository(t *testing.T) {
	for name, mark := range map[string]func(*testing.T, string) string{
		"a .git directory": markRepo,
		"a .git file":      markLinkedRepo,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			scopeDir := markRepo(t, filepath.Join(root, "scope"))
			foreign := mark(t, filepath.Join(root, "foreign"))
			bootRoot := filepath.Join(foreign, "runtime", "boot")

			err := bootdir.CheckRoot(bootRoot, scopeDir)
			if !errors.Is(err, bootdir.ErrForeignRepository) {
				t.Fatalf("CheckRoot(%q, %q) = %v, want ErrForeignRepository", bootRoot, scopeDir, err)
			}
			// Both halves, because either alone leaves the operator guessing
			// at the other: the repository the session's shell would have
			// answered with, and the scope everything else in the boot
			// directory reports.
			for _, want := range []string{foreign, scopeDir} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal %q does not name %q", err, want)
				}
			}
		})
	}
}

// TestCheckRootAllowsTheScopesOwnRepository covers the normal case, which must
// not trip: a boot root in the scope's own working tree aims the session's git
// at the repository the session is actually working on.
//
// Both shapes are covered, because the scope is a directory rather than a
// repository and the two are routinely not the same directory. A scope one
// level down from its repository root is an ordinary monorepo checkout.
func TestCheckRootAllowsTheScopesOwnRepository(t *testing.T) {
	t.Run("the scope is the repository root", func(t *testing.T) {
		scopeDir := markRepo(t, filepath.Join(t.TempDir(), "repo"))
		bootRoot := filepath.Join(scopeDir, "runtime", "boot")
		if err := bootdir.CheckRoot(bootRoot, scopeDir); err != nil {
			t.Errorf("CheckRoot(%q, %q) = %v, want no refusal", bootRoot, scopeDir, err)
		}
	})

	t.Run("the scope is inside the repository", func(t *testing.T) {
		repo := markRepo(t, filepath.Join(t.TempDir(), "repo"))
		scopeDir := mkdirAll(t, filepath.Join(repo, "services", "api"))
		bootRoot := filepath.Join(repo, "runtime", "boot")
		if err := bootdir.CheckRoot(bootRoot, scopeDir); err != nil {
			t.Errorf("CheckRoot(%q, %q) = %v, want no refusal", bootRoot, scopeDir, err)
		}
	})

	t.Run("the repository is a linked worktree", func(t *testing.T) {
		scopeDir := markLinkedRepo(t, filepath.Join(t.TempDir(), "repo-wt"))
		bootRoot := filepath.Join(scopeDir, "runtime", "boot")
		if err := bootdir.CheckRoot(bootRoot, scopeDir); err != nil {
			t.Errorf("CheckRoot(%q, %q) = %v, want no refusal", bootRoot, scopeDir, err)
		}
	})
}

// TestCheckRootAllowsARootOutsideAnyRepository covers what the default does,
// and it is the case the check has to be silent about: nothing to contradict,
// nothing to say.
func TestCheckRootAllowsARootOutsideAnyRepository(t *testing.T) {
	root := t.TempDir()
	scopeDir := markRepo(t, filepath.Join(root, "scope"))
	bootRoot := filepath.Join(root, "state", "cairn", "boot")
	if err := bootdir.CheckRoot(bootRoot, scopeDir); err != nil {
		t.Errorf("CheckRoot(%q, %q) = %v, want no refusal", bootRoot, scopeDir, err)
	}
}

// TestCheckRootScopeInNoRepository covers a scope that is not a checkout. The
// boot root is still inside a repository the session's git would find, and the
// scope is still not it — so the refusal stands, and has to phrase what it
// expected without inventing a repository for the scope.
func TestCheckRootScopeInNoRepository(t *testing.T) {
	root := t.TempDir()
	scopeDir := mkdirAll(t, filepath.Join(root, "workspace"))
	foreign := markRepo(t, filepath.Join(root, "foreign"))
	bootRoot := filepath.Join(foreign, "boot")

	err := bootdir.CheckRoot(bootRoot, scopeDir)
	if !errors.Is(err, bootdir.ErrForeignRepository) {
		t.Fatalf("CheckRoot(%q, %q) = %v, want ErrForeignRepository", bootRoot, scopeDir, err)
	}
	if !strings.Contains(err.Error(), "inside no repository") {
		t.Errorf("the refusal %q does not say the scope is in no repository", err)
	}
}

// TestCheckRootEmptyScope covers the declared limit: with no scope there is no
// repository the boot root could contradict, and nothing the message could
// name as expected.
func TestCheckRootEmptyScope(t *testing.T) {
	bootRoot := filepath.Join(markRepo(t, filepath.Join(t.TempDir(), "repo")), "boot")
	for _, scopeDir := range []string{"", "   "} {
		if err := bootdir.CheckRoot(bootRoot, scopeDir); err != nil {
			t.Errorf("CheckRoot(%q, %q) = %v, want no refusal", bootRoot, scopeDir, err)
		}
	}
}

// TestCheckRootNearestRepositoryWins covers nesting. A submodule inside the
// scope's own repository is a different working tree on a different commit,
// and a shell started inside it is in the submodule — so a boot root there is
// foreign even though an outer walk would have found the scope's repository.
func TestCheckRootNearestRepositoryWins(t *testing.T) {
	repo := markRepo(t, filepath.Join(t.TempDir(), "repo"))
	scopeDir := mkdirAll(t, filepath.Join(repo, "work"))
	sub := markLinkedRepo(t, filepath.Join(repo, "vendor", "sub"))
	bootRoot := filepath.Join(sub, "boot")

	err := bootdir.CheckRoot(bootRoot, scopeDir)
	if !errors.Is(err, bootdir.ErrForeignRepository) {
		t.Fatalf("CheckRoot(%q, %q) = %v, want ErrForeignRepository", bootRoot, scopeDir, err)
	}
	if !strings.Contains(err.Error(), sub) {
		t.Errorf("the refusal %q names something other than the nearest repository %q", err, sub)
	}
}

// TestCheckRootThroughASymlink covers the reason the walk resolves symlinks:
// git searches the physical ancestry, because a shell's getcwd has already
// resolved them. A lexical walk up from the link would climb an ancestry no
// git command ever looks at, and report no repository where git finds one.
func TestCheckRootThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege this test will not ask for")
	}
	root := t.TempDir()
	scopeDir := markRepo(t, filepath.Join(root, "scope"))
	foreign := markRepo(t, filepath.Join(root, "foreign"))
	mkdirAll(t, filepath.Join(foreign, "state"))

	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(foreign, "state"), link); err != nil {
		t.Fatalf("link %s: %v", link, err)
	}

	bootRoot := filepath.Join(link, "boot")
	err := bootdir.CheckRoot(bootRoot, scopeDir)
	if !errors.Is(err, bootdir.ErrForeignRepository) {
		t.Fatalf("CheckRoot(%q, %q) = %v, want ErrForeignRepository", bootRoot, scopeDir, err)
	}
}

// TestCheckRootEmptyRoot covers the one input this check declines to judge.
// An empty root is Location.Dir's refusal to make, and it names it better.
func TestCheckRootEmptyRoot(t *testing.T) {
	scopeDir := markRepo(t, filepath.Join(t.TempDir(), "repo"))
	if err := bootdir.CheckRoot("", scopeDir); err != nil {
		t.Errorf("CheckRoot with an empty root = %v, want no refusal", err)
	}
}
