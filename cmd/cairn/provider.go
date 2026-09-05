package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chrispian/cairn/profile"
)

// providerFlagUsage is the one description of --provider, so that the three
// commands that take it describe it identically.
//
// It names the default rather than leaving it to be inferred, because the
// default is the whole reason the flag is not disruptive: every profile in a
// bundle already declares a provider, so a command run without this renders
// exactly what it rendered before the flag existed.
const providerFlagUsage = "the harness this materializes into, which selects the layout it is " +
	"written as and the spec.settings document written into it; defaults to the provider the " +
	"profile declares"

// selectProvider returns the harness a materialization targets, and how a
// diagnostic should say where that target came from.
//
// The flag wins and the profile is the default, which is the precedence every
// other value in cairn resolves by. What is new is that the target is now a
// choice at all: spec.settings holds a document per provider, and access,
// slots, templates and skills are neutral, so one profile is materializable
// into more than one harness and something has to say which. That something is
// the operator, at the terminal, per materialization — a provider is a
// materialization target rather than a property of the content.
//
// A value that names no harness cairn knows is refused here, ahead of any
// layout lookup, and the two refusals are deliberately different answers. This
// one means the word is not a provider; [bootdir.LayoutFor] and the installed
// layer's own lookup mean it is a provider and cairn cannot render it yet. An
// operator who typed "cluade" and an operator who typed "opencode" have
// different problems, and one message for both would serve neither.
//
// The returned name is what a refusal further down should quote: the flag when
// a flag chose the target, and otherwise the profile, which is the file the
// reader would have to edit. Without it a `--provider codex` would be reported
// as "profile "engineer": unsupported provider", sending the reader to a file
// that says claude.
func selectProvider(flagValue string, declared profile.Provider, profileID string) (profile.Provider, string, error) {
	value := strings.TrimSpace(flagValue)
	if value == "" {
		return declared, fmt.Sprintf("profile %q", profileID), nil
	}
	p := profile.Provider(value)
	if !p.Valid() {
		return "", "", fmt.Errorf("--provider %q names no harness cairn knows; it knows %s",
			value, profile.ProviderList())
	}
	return p, "--provider " + strconv.Quote(value), nil
}
