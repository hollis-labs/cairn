package bootdir_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/bootdir"
)

// TestDefaultRootPrecedence pins the two properties of the boot root that a
// caller depends on and a reader of the constant cannot check.
//
// The default is where an agent's working directory ends up, and a default
// that lands inside a repository hands the agent's shell a repository that is
// not its scope. That is not something a unit test can assert in general, so
// what is asserted here is the shape it rests on: relative to home, below a
// path reserved for state, and overridable.
func TestDefaultRootPrecedence(t *testing.T) {
	home := filepath.FromSlash("/home/op")

	t.Run("the environment wins", func(t *testing.T) {
		root, err := bootdir.DefaultRoot("  /elsewhere/boot  ", home)
		if err != nil {
			t.Fatalf("DefaultRoot: %v", err)
		}
		if want := "/elsewhere/boot"; root != want {
			t.Errorf("DefaultRoot with %s set = %q, want %q", bootdir.EnvBootRoot, root, want)
		}
	})

	t.Run("otherwise below home", func(t *testing.T) {
		root, err := bootdir.DefaultRoot("", home)
		if err != nil {
			t.Fatalf("DefaultRoot: %v", err)
		}
		want := filepath.Join(home, filepath.FromSlash(bootdir.DefaultRootRel))
		if root != want {
			t.Errorf("DefaultRoot = %q, want %q", root, want)
		}
		if filepath.IsAbs(bootdir.DefaultRootRel) {
			t.Errorf("DefaultRootRel = %q, which is absolute and so ignores home", bootdir.DefaultRootRel)
		}
		// Asserted on the resolved root, not on how the constant is spelled:
		// ".local/state/../../dev/whatever" reads as being below the state
		// directory and joins to somewhere else entirely.
		state := filepath.Join(home, ".local", "state") + string(filepath.Separator)
		if !strings.HasPrefix(root, state) {
			t.Errorf("DefaultRoot = %q, which does not resolve below %q", root, state)
		}
	})

	t.Run("no home is only an error when it is needed", func(t *testing.T) {
		if _, err := bootdir.DefaultRoot("/elsewhere/boot", ""); err != nil {
			t.Errorf("DefaultRoot with a root and no home = %v, want no error", err)
		}
		_, err := bootdir.DefaultRoot("", "  ")
		if !errors.Is(err, bootdir.ErrNoHome) {
			t.Errorf("DefaultRoot with nothing = %v, want ErrNoHome", err)
		}
		if err != nil && !strings.Contains(err.Error(), bootdir.EnvBootRoot) {
			t.Errorf("DefaultRoot with nothing = %v, which does not name %s", err, bootdir.EnvBootRoot)
		}
	})

	// The doc comment claims both inputs are passed rather than read. A
	// process environment that disagrees with the arguments is the only way to
	// tell the difference.
	t.Run("the process environment is not consulted", func(t *testing.T) {
		t.Setenv(bootdir.EnvBootRoot, "/from/the/process")
		t.Setenv("HOME", "/from/the/process")
		root, err := bootdir.DefaultRoot("", home)
		if err != nil {
			t.Fatalf("DefaultRoot: %v", err)
		}
		if want := filepath.Join(home, filepath.FromSlash(bootdir.DefaultRootRel)); root != want {
			t.Errorf("DefaultRoot = %q, want %q — it read the process environment", root, want)
		}
	})
}
