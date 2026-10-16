package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/cairn/catalog"
	"github.com/hollis-labs/cairn/profile"
)

// envProfileRoot names the environment variable a profile bundle's root
// reaches a manifest through, and the one cairn reads a bundle from when no
// flag names one. It loses to the command-line flag, as every other variable
// cairn reads a path from does.
//
// It is spelled here rather than in a library because nothing below the
// composition root knows it exists: a profile writes "$CAIRN_PROFILE_ROOT/..."
// in a value that names somewhere to read from, and the expander that answers
// it is an ordinary [profile.Expander] handed down from here. Expansion did
// not have to learn a new name, and did not — and neither did the catalog,
// which is handed a directory rather than the name of a variable.
const envProfileRoot = "CAIRN_PROFILE_ROOT"

// envXDGConfigHome names the XDG base-directory variable for user
// configuration, which the default bundle sits under.
const envXDGConfigHome = "XDG_CONFIG_HOME"

// profileFlagUsage is the one description of --profile, so that the four
// commands that take it describe it identically.
const profileFlagUsage = "the profile bundle the catalog is read from, seeded as $" + envProfileRoot

// errProfileBundle reports a --profile that does not name an existing
// directory.
var errProfileBundle = errors.New("--profile does not name a profile bundle")

// resolveProfileRoot turns --profile's value into the absolute directory
// [envProfileRoot] is seeded with. An empty value returns the empty string,
// and [bundleRoot] is where the environment and the default are read instead.
//
// Everything below is done to the flag and to nothing else. A bundle root
// arriving through the variable is passed through exactly as it was exported —
// unabsolutized, unrewritten — because it is not cairn's value to rewrite, and
// the guarantees here stop at the command line.
//
// Absolute, and made so here. The bundle root is a prefix on paths cairn then
// resolves somewhere else entirely — a slot's static path resolves against the
// instance's scope, not against the shell the operator typed the flag in — so
// a relative --profile would name one directory to the operator and a
// different one to every value that expands it. Here, where the operator's own
// working directory is still the right thing to resolve against, is the only
// place that can be settled.
//
// A leading "~/" expands and a "$VAR" does not. The flag came through a shell,
// which has already expanded what it was going to; expanding again would
// re-read a value the operator may have quoted deliberately. The tilde
// survives quoting and shells routinely hand it through, so it is the one form
// left to deal with — [profile.ExpandPath] with a nil lookup is exactly that
// pair.
//
// Symlinks are not resolved, where scope.Parse does resolve them. That
// difference is the difference in what the two values are for. A scope is
// compared against a directory about to be written, and os.SameFile needs a
// canonical path for the comparison to mean anything. A bundle root is a prefix
// glued onto paths the operator wrote, and canonicalizing it would put a
// spelling they never used into every diagnostic about a path that failed —
// which is the loss [profile.QuotedExpansion] exists to prevent.
//
// A bundle that does not exist is refused rather than seeded, here and again
// in [catalog.Open]. Twice is not redundant: this one names the flag, and
// [catalog.Open] names whichever bundle the command ended up reading —
// flag, variable or default. It is install.Root.Check's reasoning about the
// install root: a missing directory here is not a starting state, it is a sign
// cairn was pointed somewhere wrong.
func resolveProfileRoot(raw, home string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	expanded, err := profile.ExpandPath(trimmed, home, nil)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("absolute path of the profile bundle %q: %w", raw, err)
	}
	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w: %s", errProfileBundle, abs)
	case err != nil:
		return "", fmt.Errorf("stat the profile bundle %s: %w", abs, err)
	case !info.IsDir():
		return "", fmt.Errorf("%w: %s is not a directory", errProfileBundle, abs)
	}
	return abs, nil
}

// bundleRoot returns the directory the catalog is read from: the flag, then
// [envProfileRoot], then $XDG_CONFIG_HOME/agents, then ~/.config/agents.
//
// It is one value and it answers two questions — where the profiles are, and
// what $CAIRN_PROFILE_ROOT expands to — because since the catalog became the
// store those are the same question. Resolving them separately is how a
// command ends up reading a profile out of one directory and expanding its
// paths against another.
//
// The variable and the default are passed through as they are. Only the flag
// is absolutized and checked, for the reason [resolveProfileRoot] gives; every
// path here still reaches [catalog.Open], which refuses a bundle that is not
// there and names it.
func bundleRoot(flagValue, home string) (string, error) {
	root, err := resolveProfileRoot(flagValue, home)
	if err != nil {
		return "", err
	}
	if root != "" {
		return root, nil
	}
	return catalog.DefaultRoot(os.Getenv(envProfileRoot), os.Getenv(envXDGConfigHome), home)
}

// environment returns the lookup a manifest's variables are expanded against:
// the process environment, with [envProfileRoot] answered by the bundle this
// command read its profile out of.
//
// One value threaded from here is the whole of what --profile does. Cairn
// already expands $VAR and ${VAR} in every manifest value that names somewhere
// to read from, and already carries the lookup down from the composition root
// rather than letting anything below reach for the process's own — so a bundle
// root is a variable like any other, and seeding it needed no new expansion,
// no new manifest key and no new path rule.
//
// It is seeded on every run, including the one where no flag was given. The
// bundle is not optional any more — a command that read a profile read it out
// of a directory — so a manifest that names $CAIRN_PROFILE_ROOT resolves
// against the bundle in play rather than against whatever the shell happened
// to export, or against nothing at all.
func environment(profileRoot string) profile.Expander {
	return func(name string) string {
		if name == envProfileRoot {
			return profileRoot
		}
		return os.Getenv(name)
	}
}
