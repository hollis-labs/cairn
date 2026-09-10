package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/catalog"
)

// TestShow covers the command that answers "what does this profile actually
// resolve to". It is the mitigation the keyed merge owes the operator — a
// value composed from three profiles reads exactly like a value one profile
// wrote — so what is asserted here is that the document says which profiles
// contributed, and not only what came out.
func TestShow(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	show := func(t *testing.T, args ...string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if err := run(ctx, append([]string{"show"}, append(args, "--profile", bundle)...), &stdout, &stderr); err != nil {
			t.Fatalf("show %v: %v\nstderr: %s", args, err, stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	t.Run("an abstract profile shows", func(t *testing.T) {
		// `cairn boot` refuses one and this does not. The abstract root is
		// where the settings, the templates and the standing skill set live,
		// which makes it the profile most worth reading.
		out, _ := show(t, "base")
		for _, want := range []string{
			"profile       base",
			"chain         base",
			"abstract      true",
			"spec.settings",
			"spec.templates",
			"somethingCairnHasNeverHeardOf", // an unknown key shows like any other
		} {
			if !strings.Contains(out, want) {
				t.Errorf("show base does not carry %q:\n%s", want, out)
			}
		}
	})

	t.Run("the chain is walked ancestor-first", func(t *testing.T) {
		out, errOut := show(t, "engineer")
		if !strings.Contains(out, "chain         base -> engineer") {
			t.Errorf("the chain is not reported ancestor-first:\n%s", out)
		}
		// The document is the whole output. Everything a boot reports on
		// stderr — a slot that failed, a marker nothing filled — belongs to
		// rendering, and nothing here renders.
		if errOut != "" {
			t.Errorf("show wrote to stderr with nothing to report:\n%s", errOut)
		}
	})

	t.Run("a profile declaring nothing prints an empty spec", func(t *testing.T) {
		writeProfile(t, bundle, bundleProfile{ID: "bare", Name: "Bare", Provider: "claude"})

		out, _ := show(t, "bare")
		if !strings.HasSuffix(out, "\nspec (0 keys)\n") {
			t.Errorf("an empty manifest does not end the document with its count:\n%s", out)
		}
		// No note under a heading with nothing under it, matching
		// install.Report, where a section with no entries renders nothing.
		if strings.Contains(out, "The names beside a key") {
			t.Errorf("an empty manifest carried the section note anyway:\n%s", out)
		}
	})

	t.Run("a key names every profile that declares it", func(t *testing.T) {
		// The single most important assertion in this file, so it is made on
		// the whole line: the key and the names have to be the same fact.
		// Asserting that both strings appear somewhere in the document would
		// pass on a document that never associated them, and a suffix match
		// would pass on "base, engineer" while claiming to reject it.
		out, _ := show(t, "engineer")
		for key, want := range map[string]string{
			"templates": "base, engineer", // both declare it: a composition
			"skills":    "engineer",       // the leaf alone
			"settings":  "base",           // the root alone
		} {
			got, ok := declarersOf(out, key)
			if !ok {
				t.Errorf("spec.%s was not printed:\n%s", key, out)
				continue
			}
			if got != want {
				t.Errorf("spec.%s is declared by %q, want %q", key, got, want)
			}
		}
	})

	t.Run("a merged value carries what both profiles declared", func(t *testing.T) {
		// The labels being right is not the same as the value being composed.
		// base declares AGENTS.md and CLAUDE.md; engineer restates all three
		// of its own. What proves a merge happened is that the merged object
		// carries a destination the leaf never mentioned.
		out, _ := show(t, "engineer")
		templates := valueOf(t, out, "templates").(map[string]any)
		for _, dest := range []string{"AGENTS.md", "CLAUDE.md", "boot.md"} {
			if _, ok := templates[dest]; !ok {
				t.Errorf("the merged templates lost %s: %v", dest, templates)
			}
		}
	})

	t.Run("a value one profile declares is that profile's own declaration", func(t *testing.T) {
		// The promise the section note makes. Indentation is whitespace, so
		// compacting what was printed has to give back exactly what the
		// catalog read out of the file — the property bootdir.RenderSettings
		// depends on.
		out, _ := show(t, "base")
		cat, err := catalog.Open(bundle)
		if err != nil {
			t.Fatalf("open the catalog: %v", err)
		}
		declared, err := cat.Profile(ctx, "base")
		if err != nil {
			t.Fatalf("load base: %v", err)
		}
		for key, raw := range declared.Spec {
			var printed, want bytes.Buffer
			if err := json.Compact(&printed, []byte(rawValueOf(t, out, key))); err != nil {
				t.Fatalf("spec.%s: the printed value is not JSON: %v", key, err)
			}
			if err := json.Compact(&want, raw); err != nil {
				t.Fatalf("spec.%s: the declared value is not JSON: %v", key, err)
			}
			if printed.String() != want.String() {
				t.Errorf("spec.%s was re-spelled:\n printed  %s\n declared %s", key, printed.String(), want.String())
			}
		}
	})

	t.Run("the merged spec is laid out with its keys sorted", func(t *testing.T) {
		out, _ := show(t, "engineer")
		var keys []string
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "spec.") {
				keys = append(keys, strings.Fields(line)[0])
			}
		}
		if len(keys) == 0 {
			t.Fatalf("no manifest keys were printed:\n%s", out)
		}
		if !slices.IsSorted(keys) {
			t.Errorf("the manifest keys are not sorted: %v", keys)
		}
		// Laid out rather than printed raw: a nested value the catalog carries
		// on one line arrives here across several, indented under its key.
		if !strings.Contains(out, "\n  {\n    \"") {
			t.Errorf("a nested value was not indented for reading:\n%s", out)
		}
	})

	t.Run("the scope reported is the one boot would work in", func(t *testing.T) {
		// Symlinks resolved, because that is what a boot resolves — scope.Parse
		// canonicalizes so that two spellings of one directory arrive as one
		// path, and on macOS the test's own temp root is behind one.
		declared := canonical(t, scopeDir)

		// The flag declares it, and the flag is the only source there is: a
		// binding used to carry a default and showing the bare name reported
		// it. Bindings retired, so a scope belongs to the launch.
		out, _ := show(t, "engineer", "--scope", scopeDir)
		if !strings.Contains(out, "scope         "+declared+"\n") {
			t.Errorf("the scope given is not reported as %s:\n%s", declared, out)
		}
		// With no flag there is no scope, and the field says so by carrying
		// nothing.
		out, _ = show(t, "base")
		if !strings.Contains(out, "\nscope\n") {
			t.Errorf("a profile with no --scope reports one anyway:\n%s", out)
		}
		// A second --scope is the same one flag, reported the same way.
		other := filepath.Join(home, "elsewhere")
		mustMkdir(t, other)
		out, _ = show(t, "engineer", "--scope", other)
		if !strings.Contains(out, "scope         "+canonical(t, other)+"\n") {
			t.Errorf("--scope was not reported:\n%s", out)
		}
		if strings.Contains(out, "scope         "+declared+"\n") {
			t.Errorf("--scope did not override the binding's:\n%s", out)
		}
	})

	t.Run("a scope that does not resolve is reported, not refused", func(t *testing.T) {
		// scope.ErrNotDirectory exists to keep the containment guard
		// answerable, and this command runs that guard on nothing. Refusing
		// the whole document over a directory nothing is about to be written
		// into would make show least usable exactly when something is wrong.
		var stdout, stderr bytes.Buffer
		err := run(ctx, []string{
			"show", "engineer", "--profile", bundle,
			"--scope", filepath.Join(home, "no-such-directory"),
		}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("show with an unresolvable scope = %v, want the document anyway", err)
		}
		if !strings.Contains(stdout.String(), "spec.templates") {
			t.Errorf("the document was not printed:\n%s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "\nscope\n") {
			t.Errorf("a scope that did not resolve was reported as one:\n%s", stdout.String())
		}
		// The operator hears about it where an operator reads, with the value
		// they wrote ahead of the error that says what it expanded to.
		if !strings.Contains(stderr.String(), "no-such-directory") ||
			!strings.Contains(stderr.String(), "did not resolve") {
			t.Errorf("stderr does not report the scope that did not resolve:\n%s", stderr.String())
		}
	})

	t.Run("show takes exactly one target", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if err := run(ctx, []string{"show", "--profile", bundle}, &out, &errOut); err == nil {
			t.Error("show with no target reported success")
		}
		if err := run(ctx, []string{"show", "base", "engineer", "--profile", bundle}, &out, &errOut); err == nil {
			t.Error("show with two targets reported success")
		}
		if err := run(ctx, []string{"show", "nobody", "--profile", bundle}, &out, &errOut); err == nil ||
			!strings.Contains(err.Error(), "nobody") {
			t.Errorf("show of an unknown target = %v, want a refusal naming it", err)
		}
	})
}

// TestAReadThatFindsNothingWritesNothing is the half of `show` and
// `install --check`'s contract that a reader cannot check by looking at the
// output, and it is the contract T07c was opened for.
//
// It is unqualified now, and it was not. Both commands promised in their own
// help to write nothing, and both opened a database to do it: store.Open
// created the file, its parent directories and the schema when they were
// absent, and did it before the target was looked up — so a command pointed at
// a path that named nothing conjured a directory tree and an empty database,
// and then failed. The catalog reads a directory and creates none.
//
// HOME is pointed at the fixture so that every default a write would take —
// the boot root under $HOME, and the installed layer's root, which is $HOME —
// lands inside the tree this walks. Without that the defaults resolve to the
// operator's real home and the assertion covers a directory nothing was ever
// going to touch.
func TestAReadThatFindsNothingWritesNothing(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	t.Setenv("HOME", home)
	t.Setenv("CAIRN_BOOT_ROOT", "")

	t.Run("a show that succeeds renders nothing", func(t *testing.T) {
		before := pathsUnder(t, home)
		for _, target := range []string{"base", "engineer"} {
			var stdout, stderr bytes.Buffer
			if err := run(ctx, []string{"show", target, "--profile", bundle}, &stdout, &stderr); err != nil {
				t.Fatalf("show %s: %v\nstderr: %s", target, err, stderr.String())
			}
			if stdout.Len() == 0 {
				t.Fatalf("show %s printed nothing", target)
			}
		}
		if after := pathsUnder(t, home); !slices.Equal(before, after) {
			t.Errorf("show left something behind:\nbefore %v\nafter  %v", before, after)
		}
		// Named individually as well, because the paths above are only as
		// strong as the tree they cover. The boot root comes from the constant
		// rather than a literal: a literal left behind by a change to the
		// default would go on passing while asserting nothing.
		for _, rel := range []string{".claude", filepath.FromSlash(bootdir.DefaultRootRel)} {
			if _, err := os.Stat(filepath.Join(home, rel)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("show created %s: %v", rel, err)
			}
		}
	})

	// The case that used to write. A bundle two directories deep inside a
	// temp root that does not exist: nothing may appear at any level of it,
	// and the assertion is on the outermost segment so that a command creating
	// only the parent is caught too.
	t.Run("a read of a bundle that is not there creates none of it", func(t *testing.T) {
		for _, args := range [][]string{
			{"show", "nobody"},
			{"install", "base", "--check", "--root", t.TempDir()},
		} {
			outer := filepath.Join(t.TempDir(), "made")
			absent := filepath.Join(outer, "up", "bundle")

			var stdout, stderr bytes.Buffer
			err := run(ctx, append(args, "--profile", absent), &stdout, &stderr)
			if err == nil {
				t.Fatalf("%s against a bundle that is not there reported success", args[0])
			}
			if !strings.Contains(err.Error(), absent) {
				t.Errorf("the %s refusal does not name the bundle: %v", args[0], err)
			}
			nothingUnder(t, outer)
		}
	})
}

// declarersOf returns the profiles named beside spec.<key> in a printed
// document, and whether the key was printed at all.
func declarersOf(out, key string) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "spec."+key+" "); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// rawValueOf returns the JSON text printed under spec.<key>, with the margin
// every line of it sits in removed.
func rawValueOf(t *testing.T, out, key string) string {
	t.Helper()
	var body []string
	found := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "spec."+key+" "):
			found = true
		case found && strings.HasPrefix(line, "spec."):
			return strings.Join(body, "\n")
		case found && strings.HasPrefix(line, "  "):
			body = append(body, strings.TrimPrefix(line, "  "))
		}
	}
	if !found {
		t.Fatalf("spec.%s was not printed:\n%s", key, out)
	}
	return strings.Join(body, "\n")
}

// valueOf returns the value printed under spec.<key>, decoded.
func valueOf(t *testing.T, out, key string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(rawValueOf(t, out, key)), &v); err != nil {
		t.Fatalf("spec.%s did not print valid JSON: %v", key, err)
	}
	return v
}

// canonical is a directory as scope.Parse would report it, which is the form
// every scope reaches the output in.
func canonical(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return resolved
}

// pathsUnder returns every path beneath dir, sorted, with sqlite's own
// sidecars left out: opening the database is a read this command has to make,
// and a journal appearing beside it is not cairn writing anything.
func pathsUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if strings.HasSuffix(rel, suffix) {
				return nil
			}
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	slices.Sort(out)
	return out
}

// TestShowJSON covers the machine-readable form of the same document. What is
// asserted here is mostly not "the JSON is correct" but "the JSON says what
// the prose says": the two forms are one document, and a consumer sourcing a
// list from --json is relying on that rather than on this file having listed
// every key twice.
//
// The rest is the contract [showReport] publishes. Every key emitted on every
// call, an absent value spelled null, and — the one a consumer would build a
// wrong UI on — provenance per key and never per member.
func TestShowJSON(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	// A profile of its own rather than one extending base: it is the only way
	// to reach a chain that declares no provider, since base declares one and
	// everything else in the fixture inherits it.
	writeProfile(t, bundle, bundleProfile{ID: "bare", Name: "Bare"})

	invoke := func(t *testing.T, args ...string) (string, string, error) {
		t.Helper()
		argv := append([]string{"show"}, args...)
		argv = append(argv, "--profile", bundle)
		var stdout, stderr bytes.Buffer
		err := run(ctx, argv, &stdout, &stderr)
		return stdout.String(), stderr.String(), err
	}
	showJSON := func(t *testing.T, args ...string) (showReport, string, string) {
		t.Helper()
		out, errOut, err := invoke(t, append(args, "--json")...)
		if err != nil {
			t.Fatalf("show %v --json: %v\nstderr: %s", args, err, errOut)
		}
		// One object and nothing else, so `cairn show x --json | jq` is one
		// value and `$(...)` is parseable. A decoder that stops after the
		// first value would accept a document with prose after it, so what is
		// asserted is that there is no second value.
		dec := json.NewDecoder(strings.NewReader(out))
		var report showReport
		if err := dec.Decode(&report); err != nil {
			t.Fatalf("show %v --json is not one JSON object: %v\n%s", args, err, out)
		}
		if dec.More() {
			t.Fatalf("show %v --json printed something after the object:\n%s", args, out)
		}
		return report, out, errOut
	}

	t.Run("every key is emitted on every call", func(t *testing.T) {
		// The key set is the contract, so it is asserted on the object as it
		// was printed and not on the struct it decodes into: a struct takes a
		// document missing half its keys without complaint, which is exactly
		// the regression this is here to catch.
		want := []string{
			"profile", "chain", "name", "description", "provider",
			"model", "abstract", "scope", "profile_root", "spec",
		}
		slices.Sort(want)
		for _, target := range []string{"base", "engineer", "bare", "nosettings", "grantsonly"} {
			_, out, _ := showJSON(t, target)
			var printed map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &printed); err != nil {
				t.Fatalf("show %s --json: %v", target, err)
			}
			if got := slices.Sorted(maps.Keys(printed)); !slices.Equal(got, want) {
				t.Errorf("show %s --json carries %v, want exactly %v", target, got, want)
			}
		}
	})

	t.Run("a value cairn does not have is null", func(t *testing.T) {
		// base declares no description and no --scope was given, so two of the
		// keys above are absent values rather than empty ones. Asserted on the
		// printed bytes as well as the decoded struct, because "" and null
		// both decode a *string the test would then have to distinguish.
		report, out, _ := showJSON(t, "base")
		for _, want := range []string{`"description": null`, `"scope": null`} {
			if !strings.Contains(out, want) {
				t.Errorf("show base --json does not carry %s:\n%s", want, out)
			}
		}
		if report.Description != nil || report.Scope != nil {
			t.Errorf("an absent description or scope decoded to a value: %v %v",
				report.Description, report.Scope)
		}
		// And a chain that declares no provider at all, which is a thing this
		// command shows and `cairn boot` refuses.
		bare, out, _ := showJSON(t, "bare")
		if bare.Provider != nil {
			t.Errorf("a profile declaring no provider reports %q", *bare.Provider)
		}
		if !strings.Contains(out, `"provider": null`) {
			t.Errorf("show bare --json does not spell an undeclared provider null:\n%s", out)
		}
	})

	t.Run("the scalar fields say what the document says", func(t *testing.T) {
		report, _, _ := showJSON(t, "engineer", "--scope", scopeDir)
		if report.Profile != "engineer" {
			t.Errorf("profile is %q, want engineer", report.Profile)
		}
		if want := []string{"base", "engineer"}; !slices.Equal(report.Chain, want) {
			t.Errorf("chain is %v, want %v — ancestor-first", report.Chain, want)
		}
		if report.Abstract {
			t.Error("a concrete profile reports abstract true")
		}
		if report.Name == nil || *report.Name != "Engineer" {
			t.Errorf("name is %v, want Engineer", report.Name)
		}
		// Inherited, not declared: the closest value in the chain, which for
		// both of these is the root's.
		if report.Provider == nil || *report.Provider != "claude" {
			t.Errorf("provider is %v, want claude", report.Provider)
		}
		if report.Model == nil || *report.Model != "opus" {
			t.Errorf("model is %v, want opus", report.Model)
		}
		if report.ProfileRoot != bundle {
			t.Errorf("profile_root is %q, want %q", report.ProfileRoot, bundle)
		}
		// Spelled exactly as bootReport.Scope spells it — absolute and
		// symlink-resolved — because a launcher showing one and booting into
		// the other would be worse than one that showed nothing.
		if declared := canonical(t, scopeDir); report.Scope == nil || *report.Scope != declared {
			t.Errorf("scope is %v, want %s", report.Scope, declared)
		}
		if abstract, _, _ := showJSON(t, "base"); !abstract.Abstract {
			t.Error("an abstract profile reports abstract false")
		}
	})

	t.Run("the two forms name the same contributors", func(t *testing.T) {
		// The assertion this file exists for, made against the prose rather
		// than against a list written out here: the whole reason a consumer
		// may source a list from --json is that it cannot disagree with what
		// an operator checked by eye. A hard-coded expectation would go on
		// passing while the two drifted apart.
		prose, _, err := invoke(t, "engineer")
		if err != nil {
			t.Fatalf("show engineer: %v", err)
		}
		report, _, _ := showJSON(t, "engineer", "--scope", scopeDir)
		if len(report.Spec) == 0 {
			t.Fatal("show engineer --json carried an empty manifest")
		}
		for key, entry := range report.Spec {
			want, ok := declarersOf(prose, key)
			if !ok {
				t.Errorf("spec.%s is in the object and not in the document", key)
				continue
			}
			if got := strings.Join(entry.Contributors, ", "); got != want {
				t.Errorf("spec.%s is contributed by %q in the object and %q in the document",
					key, got, want)
			}
		}
		// And the other direction, so a key the object dropped is caught too.
		for _, line := range strings.Split(prose, "\n") {
			key, ok := strings.CutPrefix(line, "spec.")
			if !ok {
				continue
			}
			key = strings.Fields(key)[0]
			if _, ok := report.Spec[key]; !ok {
				t.Errorf("spec.%s is in the document and not in the object", key)
			}
		}
	})

	t.Run("the manifest arrives as the cascade holds it", func(t *testing.T) {
		// The promise the prose section note makes, made again here because
		// this is the form a program reads: encoding a json.RawMessage moves
		// whitespace and changes nothing else, so compacting what was printed
		// gives back the bytes the catalog read out of the file.
		report, _, _ := showJSON(t, "base")
		cat, err := catalog.Open(bundle)
		if err != nil {
			t.Fatalf("open the catalog: %v", err)
		}
		declared, err := cat.Profile(ctx, "base")
		if err != nil {
			t.Fatalf("load base: %v", err)
		}
		for key, raw := range declared.Spec {
			entry, ok := report.Spec[key]
			if !ok {
				t.Errorf("spec.%s was not carried", key)
				continue
			}
			var printed, want bytes.Buffer
			if err := json.Compact(&printed, entry.Value); err != nil {
				t.Fatalf("spec.%s: the carried value is not JSON: %v", key, err)
			}
			if err := json.Compact(&want, raw); err != nil {
				t.Fatalf("spec.%s: the declared value is not JSON: %v", key, err)
			}
			if printed.String() != want.String() {
				t.Errorf("spec.%s was re-spelled:\n carried  %s\n declared %s",
					key, printed.String(), want.String())
			}
		}
	})

	t.Run("provenance is per key and the shape does not suggest otherwise", func(t *testing.T) {
		// A consumer will build a UI on whatever this object appears to
		// promise, so the entry's key set is pinned: two siblings, the value
		// whole and the contributors of the key. Anything keyed by something
		// inside the value would be per-member provenance, which is a change
		// to the cascade and not to this command.
		_, out, _ := showJSON(t, "engineer")
		var printed struct {
			Spec map[string]map[string]json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal([]byte(out), &printed); err != nil {
			t.Fatalf("show engineer --json: %v", err)
		}
		want := []string{"contributors", "value"}
		for key, entry := range printed.Spec {
			if got := slices.Sorted(maps.Keys(entry)); !slices.Equal(got, want) {
				t.Errorf("spec.%s carries %v, want exactly %v", key, got, want)
			}
		}
		// spec.templates is the case the distinction is about: two profiles
		// declare it, and what the object says is that the key came from both
		// — not which of them supplied AGENTS.md.
		var got bytes.Buffer
		if err := json.Compact(&got, entryOf(t, printed.Spec, "templates")["contributors"]); err != nil {
			t.Fatalf("spec.templates contributors are not JSON: %v", err)
		}
		if got.String() != `["base","engineer"]` {
			t.Errorf("spec.templates contributors are %s, want both profiles", got.String())
		}
	})

	t.Run("a flag is a contributor the chain cannot name", func(t *testing.T) {
		// --with lands in the chain and names itself there. --skill, --prompt
		// and --set do not, so without them here the one command whose job is
		// attribution would credit the profile with what was typed at the
		// terminal.
		report, _, _ := showJSON(t, "engineer", "--skill", "qhealth", "--set", "note=one-off")
		for key, want := range map[string][]string{
			"skills": {"engineer", "--skill"},
			"slots":  {"engineer", "--set"},
		} {
			if got := report.Spec[key].Contributors; !slices.Equal(got, want) {
				t.Errorf("spec.%s is contributed by %v, want %v", key, got, want)
			}
		}
		// Additive, and the value proves the merge rather than the label: the
		// profile's own skill is still there beside the one the flag added.
		var skills []string
		if err := json.Unmarshal(report.Spec["skills"].Value, &skills); err != nil {
			t.Fatalf("spec.skills is not a list: %v", err)
		}
		for _, want := range []string{"code-review", "qhealth"} {
			if !slices.Contains(skills, want) {
				t.Errorf("spec.skills is %v, want it to carry %s", skills, want)
			}
		}
	})

	t.Run("an empty manifest is an object and not null", func(t *testing.T) {
		// Not an exception to the null rule but the rule applied: null is for
		// a value cairn does not have, and a profile declaring nothing has an
		// empty manifest rather than no manifest.
		report, out, _ := showJSON(t, "bare")
		if report.Spec == nil || len(report.Spec) != 0 {
			t.Errorf("a profile declaring nothing carries %v, want an empty manifest", report.Spec)
		}
		if !strings.Contains(out, `"spec": {}`) {
			t.Errorf("an empty manifest was not printed as an object:\n%s", out)
		}
	})

	t.Run("a cleared key is a declared null and not an absent key", func(t *testing.T) {
		// The two are different facts and a consumer treating them as one
		// would render an inherited settings document for a profile that went
		// to the trouble of saying it has none.
		report, _, _ := showJSON(t, "nosettings")
		entry, ok := report.Spec["settings"]
		if !ok {
			t.Fatalf("a cleared key was dropped from the manifest: %v", slices.Sorted(maps.Keys(report.Spec)))
		}
		if string(entry.Value) != "null" {
			t.Errorf("spec.settings is %s, want the null the profile declared", entry.Value)
		}
		if !slices.Equal(entry.Contributors, []string{"base", "nosettings"}) {
			t.Errorf("spec.settings is contributed by %v, want both profiles", entry.Contributors)
		}
	})

	t.Run("a scope that did not resolve is null here and named on stderr", func(t *testing.T) {
		// --json does not move the seam the prose form draws. The document is
		// stdout, the facts about the resolution are stderr, and a consumer
		// reading only stdout gets one object either way.
		absent := filepath.Join(home, "no-such-directory")
		report, out, errOut := showJSON(t, "engineer", "--scope", absent)
		if report.Scope != nil {
			t.Errorf("a scope that did not resolve was reported as %q", *report.Scope)
		}
		if !strings.Contains(out, `"scope": null`) {
			t.Errorf("stdout does not spell the unresolved scope null:\n%s", out)
		}
		if !strings.Contains(errOut, "no-such-directory") || !strings.Contains(errOut, "did not resolve") {
			t.Errorf("stderr does not report the scope that did not resolve:\n%s", errOut)
		}
	})

	t.Run("without --json stdout stays the document laid out for reading", func(t *testing.T) {
		// The seam --json was added beside, and the reason it is a flag rather
		// than a change: a caller that wants the document keeps getting
		// exactly the document.
		out, _, err := invoke(t, "engineer")
		if err != nil {
			t.Fatalf("show engineer: %v", err)
		}
		if strings.HasPrefix(strings.TrimSpace(out), "{") {
			t.Errorf("show without --json printed JSON:\n%s", out)
		}
		if !strings.Contains(out, "chain         base -> engineer") {
			t.Errorf("show without --json is not the document:\n%s", out)
		}
	})

	t.Run("one resolution prints one document", func(t *testing.T) {
		// Keys are sorted and the bytes are stable, so a consumer may diff two
		// calls and read a difference as a change to the profile. Go's encoder
		// sorts a map's keys, which is what makes the manifest's order the
		// prose document's sorted order rather than the map's iteration order.
		_, first, _ := showJSON(t, "engineer")
		_, second, _ := showJSON(t, "engineer")
		if first != second {
			t.Errorf("two calls printed different documents:\n%s\n%s", first, second)
		}
		var keys []string
		for _, line := range strings.Split(first, "\n") {
			if key, ok := strings.CutPrefix(strings.TrimSpace(line), `"`); ok {
				if name, _, found := strings.Cut(key, `":`); found && strings.HasPrefix(line, "    \"") {
					keys = append(keys, name)
				}
			}
		}
		if len(keys) == 0 {
			t.Fatalf("no manifest keys were printed:\n%s", first)
		}
		if !slices.IsSorted(keys) {
			t.Errorf("the manifest keys are not sorted: %v", keys)
		}
	})
}

// entryOf returns one manifest entry from a partially decoded document,
// failing the test when the key is absent rather than returning a nil map
// whose reads would each fail somewhere less usefully.
func entryOf(t *testing.T, spec map[string]map[string]json.RawMessage, key string) map[string]json.RawMessage {
	t.Helper()
	entry, ok := spec[key]
	if !ok {
		t.Fatalf("spec.%s was not carried: %v", key, slices.Sorted(maps.Keys(spec)))
	}
	return entry
}
