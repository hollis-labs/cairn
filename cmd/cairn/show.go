package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/chrispian/cairn/catalog"
	"github.com/chrispian/cairn/profile"
	"github.com/chrispian/cairn/scope"
)

// specIndent is one level of the indentation a manifest value is shown at, and
// also the margin the value sits in under its key.
const specIndent = "  "

// columnGap separates a name from its value in the two-column blocks: the
// scalar fields, and each manifest key from the profiles that declare it.
const columnGap = 2

// specNote is the spec section's note, in the shape install.Report's sections
// use: a heading, then what the entries under it mean, then the entries.
//
// The names are the whole reason this command exists. A value composed from
// three profiles looks exactly like a value one profile wrote, and nothing
// else in this document could tell them apart — see docs/plan.md §3, which
// records that a profile can no longer be read without walking its chain.
//
// The count of names is not a detail: it decides what the value below can be
// trusted to be. One name and the value is that profile's own declaration,
// converted from the YAML it was authored in and never re-serialized after —
// the case that matters, because spec.settings reaches the harness's settings
// document with exactly those bytes and an operator will diff the two. Both
// are laid out at [specIndent], so that diff is about content. Two or more and
// it is a composition: profile.encodeJSON marshals a Go map, so the members
// are what the profiles declared and the order is the cascade's, at every
// depth. A note promising the same of both would be wrong for exactly the key
// it was written to explain.
//
// A name may also be a flag. --skill and --set contribute to a manifest key
// without being profiles and so without appearing in the chain, and a document
// that credited the profile with what was typed at the terminal would fail at
// exactly the thing this column is for. See composition.contributors.
const specNote = specIndent + "The names beside a key are the profiles in the chain that declare it, followed\n" +
	specIndent + "by any flag that contributed one. A single profile's name means the value below\n" +
	specIndent + "is that profile's own declaration, converted from YAML and laid out here, and\n" +
	specIndent + "otherwise untouched. Anything else is a composition: the members are what those\n" +
	specIndent + "contributors declared, and the order is not.\n"

// runShow resolves a target and prints what it resolves to — laid out for
// reading, or with --json as one object for a program. It is the mitigation
// docs/plan.md §3 names: a manifest key is composed member by member across
// the extends chain, so a profile can no longer be read by reading its own
// row.
//
// The two forms are one document and not two, which is why --json is a flag on
// this command rather than a command of its own. They are rendered from the
// same resolution, the same scope and the same attribution map — see the seam
// at the bottom of this function — so the machine-readable form cannot drift
// into answering a different question than the one an operator checked by eye.
// See [showReport], which carries what a consumer may rely on.
//
// What --json does not move is stderr. A scope that did not resolve and a part
// that contributed nothing are facts about the resolution rather than lines of
// the document, they are reported the same way in both forms, and a consumer
// reading stdout gets one object either way.
//
// It renders nothing and writes nothing. No boot directory, no installed
// layer, no temporary file, and nothing in the bundle it reads; the whole
// output is on stdout.
//
// That is now unqualified, and it was not always. Until the catalog became the
// store this command opened a database — creating the file, its parent
// directory and the schema when they were absent, and doing it before the
// target was looked up, so a show that then failed still left them. A read
// that finds nothing writes nothing: [catalog.Open] reads a directory and
// creates none, and a bundle that is not there is named rather than
// conjured.
//
// An abstract profile shows, following runInstall rather than runBoot: nothing
// here is run, and the abstract root is the profile most worth reading.
//
// --provider selects the harness the reported materialization targets, and it
// arrived here the moment there was something for it to select: spec.settings
// is one document per provider now, so a profile resolves into a different
// settings document depending on where it is being written. This command takes
// the flag for the reason it takes the composition flags — it is boot's
// preview, and a preview that could not be handed what the boot will be handed
// would be blind to exactly the thing the reader is checking.
//
// What it changes here is one reported field and no value below it. The spec
// this prints is the merged manifest, which carries every provider's document
// at once; selecting one is a rendering question, and this command renders
// nothing.
func runShow(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cairn show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		scopeFlag    = fs.String("scope", "", "the scope to report, as a path")
		jsonFlag     = fs.Bool("json", false, showJSONFlagUsage)
		providerFlag = fs.String("provider", "", providerFlagUsage)
		profileFlag  = fs.String("profile", "", profileFlagUsage)
	)
	// Every flag `cairn boot` composes with, and this is not a convenience.
	// The document below is the answer to "what will this resolve to", and a
	// preview that could not be handed the composition the boot will be handed
	// would be blind to precisely the part that makes a composition differ from
	// its base — which is the one thing the reader is checking.
	var compose composition
	compose.bind(fs)
	target, rest := splitTarget(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if target == "" {
		target = fs.Arg(0)
	} else if fs.NArg() > 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return fmt.Errorf("show takes one profile, and was given %q as well", fs.Arg(0))
	}
	if target == "" || fs.NArg() > 1 {
		_, _ = fmt.Fprint(stderr, usage)
		return errors.New("show takes exactly one profile")
	}

	home, _ := os.UserHomeDir()

	// The bundle is where the profile comes from, and it is also a reported
	// fact. It was only the second of those until the catalog became the
	// store, and both halves are still true: no value printed below is
	// expanded — spec is printed as the profiles declared it, which is the
	// promise [specNote] makes — so the bundle changes what is read and not how
	// any of it is spelled.
	//
	// A --profile that names nothing is refused, which is the opposite of what
	// an unresolvable --scope gets. The two are refused for different reasons.
	// A scope is one reported fact, and refusing the whole document over it
	// makes show least usable exactly when something is already wrong. The
	// bundle is the document.
	bundle, err := bundleRoot(*profileFlag, home)
	if err != nil {
		return err
	}

	cat, err := catalog.Open(bundle)
	if err != nil {
		return err
	}

	profileID, err := profileTarget(ctx, cat, target)
	if err != nil {
		return err
	}
	// The loader is kept, not discarded: a part read from a file is not in the
	// catalog, and [declaringProfiles] re-reads every profile in the chain to
	// say which of them declared each key. Handing it the catalog instead
	// would fail on exactly the composition this command exists to preview.
	resolved, loader, err := compose.resolve(ctx, cat, home, environment(bundle), profileID)
	if err != nil {
		return err
	}
	// The one thing this command reports outside its document, alongside a
	// scope that would not resolve. Both are facts about the resolution rather
	// than about a render, which is what this command does not do.
	compose.reportAbsorbedParts(stderr, resolved)

	// The resolved spec does not depend on scope — scope is an instance value
	// substituted at boot, not a thing the cascade composes. So the flag is
	// kept by making the scope one of the reported facts rather than an input
	// to them: what `cairn boot <target>` would work in, resolved through the
	// same path runBoot resolves it through. A flag that takes a value and
	// changes no output is worse than no flag.
	//
	// The flag is the scope's only source, here as in runBoot. A binding used
	// to carry a default and --scope used to override it; bindings retired,
	// so a scope belongs to the launch and there is nothing to override.
	scopeDir, err := scope.Parse(strings.TrimSpace(*scopeFlag), home)
	if err != nil {
		// Reported, not refused, and the rule it follows is the slot rule: a
		// resolution that fails costs the reader one fact, and a command that
		// refused the whole document over it would be least usable exactly
		// when something is already wrong.
		//
		// It is specifically not runBoot's refusal, because runBoot is
		// refusing something else. scope.ErrNotDirectory exists to keep
		// scope.CheckBootDir answerable — a scope the filesystem cannot
		// identify contains nothing, which would turn the one guard cairn has
		// into a no-op. This command runs that guard on nothing: it plants no
		// directory, so there is no write for an unidentifiable scope to leave
		// unguarded.
		//
		// The declared value leads the line, ahead of the error that names
		// what it expanded to, for the reason reportSlotFailures puts it there.
		_, _ = fmt.Fprintf(stderr, "cairn: scope %q did not resolve, so none is reported: %v\n",
			strings.TrimSpace(*scopeFlag), err)
		scopeDir = ""
	}

	// The harness a boot of this target would materialize into, reported the
	// way the scope is: a fact about the materialization rather than about the
	// cascade, resolved through the same call runBoot resolves it through, so
	// the preview and the boot cannot disagree about which document
	// spec.settings would yield.
	//
	// No layout is looked up, and that is the same call this command already
	// makes about a scope it cannot resolve. show renders nothing and writes
	// nothing, so there is no layout for it to need; a profile declaring a
	// harness cairn cannot yet render is a profile worth reading, and refusing
	// to print it would make show least usable exactly when something is
	// already wrong. `cairn boot` and `cairn install` are where a target with
	// no layout is refused, because they are the ones that would have to write
	// it.
	provider, _, err := selectProvider(*providerFlag, resolved.Provider, resolved.ID)
	if err != nil {
		return err
	}

	declared, err := declaringProfiles(ctx, loader, resolved)
	if err != nil {
		return err
	}
	// After the profiles, because a flag merges last. --with is already in the
	// chain and names itself there; these two are not.
	for key, flags := range compose.contributors() {
		declared[key] = append(declared[key], flags...)
	}

	// Whichever form it takes, this is the whole output of the command. The
	// two renderers take the same arguments and are handed the same values —
	// nothing above here is resolved twice, and nothing is resolved
	// differently for one of them — so --json changes how the document is
	// spelled and never what it says.
	// Only the form that was asked for is rendered. The one-liner that renders
	// both and keeps one is what `cairn boot` does, and it is right there
	// because the discarded branch is a path and a newline; here it is a whole
	// document, and building one to throw away would be this command doing the
	// work its own help says it does not.
	out := ""
	if *jsonFlag {
		if out, err = showJSONDocument(resolved, provider, scopeDir, cat.Root(), declared); err != nil {
			return fmt.Errorf("%s resolved but could not be described: %w", resolved.ID, err)
		}
	} else {
		out = showDocument(resolved, provider, scopeDir, cat.Root(), declared)
	}
	_, err = fmt.Fprint(stdout, out)
	return err
}

// declaringProfiles maps each manifest key to the profiles in the chain whose
// own spec declares it, ancestor-first.
//
// It re-reads the chain rather than asking the cascade, because the cascade
// keeps no record of where a value came from: [profile.Resolve] folds ancestor
// onto descendant and returns the composition. Reading the same rows a second
// time is the cheap half of the question.
//
// It is per key and not per member. Saying that spec.slots came from two
// profiles is not the same as saying which of them contributed the slot in
// front of you, and the second answer cannot be assembled out here without a
// second copy of profile.specMergers — the table that decides what is keyed
// and by what, which that package documents as the only place a key earns a
// merge rule. Provenance per member is a change to the cascade, not to its
// caller, and it is more than this command should carry.
func declaringProfiles(ctx context.Context, l profile.Loader, resolved *profile.Resolved) (map[string][]string, error) {
	out := make(map[string][]string, len(resolved.Spec))
	for _, id := range resolved.Chain {
		p, err := l.Profile(ctx, id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("loading profile %q: %w", id, profile.ErrNilProfile)
		}
		for key := range p.Spec {
			out[key] = append(out[key], id)
		}
	}
	return out, nil
}

// showDocument renders what `cairn show` prints: the chain and the scalar
// fields as one aligned block, then the merged manifest one key at a time.
//
// The fields are a fixed list and a field with no value prints its name and
// nothing after it. A varying set of lines would leave the reader unable to
// tell a field that resolved to nothing from a field this command forgot.
//
// The last two are the instance's rather than the cascade's: what a boot of
// this target would work in, and the bundle this profile was read out of,
// which is also what $CAIRN_PROFILE_ROOT expands to. Neither changes a single
// value above them — that is why they are at the bottom.
//
// The provider line is the instance's too, and it is not at the bottom. It
// keeps the position it held when it was purely a declared field, because it
// is one whenever no --provider was given, and moving a line an operator reads
// by position to record that it gained a second source would cost more than it
// said.
//
// Resolved.Bodies is the field left out, and it is left out rather than
// forgotten: a body is a whole persona long, and printing the chain of them
// would bury the manifest this command exists to show. `cairn boot` renders
// them, into the instruction artifact the tree names — see
// bootdir.instructionFile.
//
// This paragraph used to say `cairn boot` rendered a concatenated Body, and
// that was false: nothing rendered it, in either layer, and a profile's prose
// was read, folded and dropped. The claim outlived the behaviour it described
// because the field existed and looked used. Verified empirically before it
// was fixed, and now covered by a test that boots a profile with a body.
//
// The chain is the fold order, and it is printed because precedence is what a
// reader checks a composition for: the parts a --with added stand after the
// extends chain, each naming what it contributed. See
// profile.ResolveComposition.
//
// Keys are sorted, and so are the members of every collection the cascade
// composed — see profile.sortedValues, which says why that order is
// deterministic and why nothing may read meaning into it.
func showDocument(resolved *profile.Resolved, provider profile.Provider, scopeDir, profileRoot string,
	declared map[string][]string) string {

	var b strings.Builder

	fields := [][2]string{
		{"profile", resolved.ID},
		{"chain", strings.Join(resolved.Chain, " -> ")},
		{"name", resolved.Name},
		{"description", resolved.Description},
		{"provider", provider.String()},
		{"model", resolved.Model},
		{"abstract", strconv.FormatBool(resolved.Abstract)},
		{"scope", scopeDir},
		{"profile root", profileRoot},
	}
	width := 0
	for _, f := range fields {
		width = max(width, len(f[0]))
	}
	for _, f := range fields {
		writeField(&b, width, f[0], f[1])
	}

	keys := slices.Sorted(maps.Keys(resolved.Spec))
	fmt.Fprintf(&b, "\nspec (%d %s)\n", len(keys), plural(len(keys), "key"))
	if len(keys) == 0 {
		return b.String()
	}
	b.WriteString(specNote)

	width = 0
	for _, key := range keys {
		width = max(width, len(specLabel(key)))
	}
	for _, key := range keys {
		b.WriteString("\n")
		writeField(&b, width, specLabel(key), strings.Join(declared[key], ", "))
		b.WriteString(indentJSON(resolved.Spec[key]))
	}
	return b.String()
}

// writeField writes one name-and-value line, leaving no trailing space when
// the value is empty.
func writeField(b *strings.Builder, width int, name, value string) {
	if value == "" {
		b.WriteString(name + "\n")
		return
	}
	b.WriteString(pad(name, width) + value + "\n")
}

// specLabel is how a manifest key is named here, which is how the cascade's
// own diagnostics name one: profile.listByField and profile.decodeObject both
// report a key they could not compose as spec.<key>.
func specLabel(key string) string { return "spec." + key }

// pad returns s widened to width and followed by the column gap. width must be
// at least len(s), which every caller guarantees by computing it as the widest
// of the very names it then pads.
func pad(s string, width int) string {
	return s + strings.Repeat(" ", width-len(s)+columnGap)
}

// plural returns word, made plural unless n is one.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// indentJSON returns a manifest value laid out for reading, in the margin its
// key sits above.
//
// [json.Indent] is the whole transformation: it moves whitespace between
// tokens and changes nothing else, so key order, string spelling and number
// spelling all survive it. That is a narrower promise than re-encoding would
// allow, and it is the one [specNote] makes to the reader.
//
// A value that is not JSON is printed as it was declared. Every manifest value
// is JSON by construction — the catalog builds it from the YAML the operator
// wrote rather than accepting JSON text — and a merge composes valid JSON out
// of valid JSON, so this is unreachable through either. Showing what is there
// is this command's whole job, and refusing to print a value because it could
// not be laid out prettily would fail at exactly the moment the operator most
// needs to see it.
func indentJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, specIndent, specIndent); err != nil {
		return specIndent + string(raw) + "\n"
	}
	return specIndent + buf.String() + "\n"
}
