package bootdir

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// EnvBootRoot names the environment variable that overrides where boot
// directories are planted. It wins over [DefaultRootRel] and loses to the
// command-line flag.
const EnvBootRoot = "CAIRN_BOOT_ROOT"

// DefaultRootRel is the boot root relative to the operator's home directory:
// XDG's state location, which is where machine-local runtime state belongs and
// where the rest of the portfolio already puts it.
//
// A boot directory is the agent's working directory, so every git command the
// agent runs resolves against whatever repository contains the boot root. The
// previous default, "dev/agent-os/runtime/boot", named a path that on the
// machine it was written for sat inside a checkout, and pointed every agent's
// shell at a repository that was not its scope — while the slots in that same
// agent's boot.md, which resolve against the scope, reported the right one. A
// default has to be somewhere cairn can justify while knowing nothing about
// the machine: below home, under a name reserved for state, and not somewhere
// a repository plausibly lives.
//
// Fixed rather than read from $XDG_STATE_HOME, which cairn could pass in as
// easily as it passes $XDG_CONFIG_HOME to catalog.DefaultRoot. Honoring it
// fixes nothing this default was wrong about, and a state home pointed at a
// checkout reopens exactly the same hole. That guard exists now — [CheckRoot],
// which refuses a boot root inside a repository that is not the scope and does
// not care which knob pointed it there — so what this constant still waits on
// is the pass that gives every path cairn resolves one documented precedence,
// rather than inventing this one's alone. [EnvBootRoot] is what an operator
// who wants it somewhere else reaches for meanwhile, and [CheckRoot] holds it
// to the same rule the default already satisfies.
const DefaultRootRel = ".local/state/cairn/boot"

// SessionLayout is the time layout of a generated session segment. It sorts
// lexically in chronological order and carries no separator a filesystem
// treats specially.
const SessionLayout = "20060102T150405Z"

// ErrNoHome reports that the boot root fell back to the home directory and no
// home directory is known.
var ErrNoHome = errors.New("home directory unknown")

// ErrLocation reports a boot-directory location that cannot be built: an empty
// or non-absolute root, or a name or session that is not a single safe path
// segment.
var ErrLocation = errors.New("invalid boot directory location")

// Location names where one boot directory goes: the root every boot directory
// is planted below, the profile it was materialized for, and the
// segment that makes this materialization distinct from the last one.
type Location struct {
	// Root is the absolute directory boot directories are planted below.
	Root string

	// Name is the profile id this boot directory was materialized
	// for. It must be a single path segment.
	Name string

	// Session distinguishes this materialization from another of the same
	// name. It must be a single path segment. See [NewSession].
	Session string
}

// Dir returns the absolute boot directory l names, reporting [ErrLocation]
// when any part of it could name something other than a directory two levels
// below the root.
func (l Location) Dir() (string, error) {
	root := strings.TrimSpace(l.Root)
	if root == "" {
		return "", fmt.Errorf("%w: the boot root is empty", ErrLocation)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: the boot root %q is not absolute", ErrLocation, root)
	}
	if err := checkSegment("name", l.Name); err != nil {
		return "", err
	}
	if err := checkSegment("session", l.Session); err != nil {
		return "", err
	}
	return filepath.Join(root, l.Name, l.Session), nil
}

// DefaultRoot returns the boot root: the value of [EnvBootRoot] when it is
// set, and [DefaultRootRel] below home otherwise. It reports [ErrNoHome] only
// when it actually needs a home.
//
// Both inputs are passed rather than read so that nothing here consults the
// process environment on its own.
func DefaultRoot(env, home string) (string, error) {
	if root := strings.TrimSpace(env); root != "" {
		return root, nil
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("%w: set %s to say where boot directories go", ErrNoHome, EnvBootRoot)
	}
	return filepath.Join(home, filepath.FromSlash(DefaultRootRel)), nil
}

// NewSession returns a session segment for a materialization at now: a UTC
// timestamp and a short random suffix, so that two boots in the same second
// do not collide.
//
// entropy is the random source; nil means [crypto/rand.Reader].
func NewSession(now time.Time, entropy io.Reader) (string, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	var suffix [3]byte
	if _, err := io.ReadFull(entropy, suffix[:]); err != nil {
		return "", fmt.Errorf("read entropy for a session segment: %w", err)
	}
	return now.UTC().Format(SessionLayout) + "-" + hex.EncodeToString(suffix[:]), nil
}

// checkSegment rejects anything that is not one safe directory name. The
// character set is deliberately narrow: these segments are joined onto a path
// Cairn then creates, and a name a shell or a filesystem reads specially would
// be a surprise nobody asked for.
func checkSegment(what, seg string) error {
	switch seg {
	case "":
		return fmt.Errorf("%w: the %s is empty", ErrLocation, what)
	case ".", "..":
		return fmt.Errorf("%w: the %s is %q", ErrLocation, what, seg)
	}
	for _, r := range seg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("%w: the %s %q holds %q, which cannot be part of a directory name",
				ErrLocation, what, seg, r)
		}
	}
	return nil
}
