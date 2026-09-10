package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chrispian/cairn/profile"
)

// TestABundleIsReadWhole covers the shape of a catalog: what a profile file
// carries.
//
// It is one test over one fixture rather than one per field, because the thing
// being asserted is that a directory of files produces the same values the
// database it replaces did. A field read wrong is a field missing from the
// whole.
func TestABundleIsReadWhole(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "base", `---
id: base
abstract: true
name: Base
description: The floor.
provider: claude
model: opus
spec:
  settings:
    permissions: { defaultMode: auto }
---

The standing prose.
`)
	writeProfileFile(t, root, "engineer", `---
id: engineer
extends: base
name: Engineer
provider: claude
---
`)
	cat, err := Open(root)
	if err != nil {
		t.Fatalf("open the bundle: %v", err)
	}
	if got := cat.Root(); got != root {
		t.Errorf("Root() = %q, want %q", got, root)
	}

	base, err := cat.Profile(context.Background(), "base")
	if err != nil {
		t.Fatalf("load base: %v", err)
	}
	if !base.Abstract || base.Name != "Base" || base.Description != "The floor." ||
		base.Provider != profile.ProviderClaude || base.Model != "opus" {
		t.Errorf("the frontmatter did not reach the profile: %+v", base)
	}
	if got, want := base.Body, "The standing prose.\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	// The manifest arrives as JSON, with its keys sorted, so two profiles
	// declaring one document declare the same bytes.
	if got, want := string(base.Spec["settings"]), `{"permissions":{"defaultMode":"auto"}}`; got != want {
		t.Errorf("spec.settings = %s, want %s", got, want)
	}

	if got := ids(cat.Profiles()); !slices.Equal(got, []string{"base", "engineer"}) {
		t.Errorf("Profiles() = %v, want base and engineer in order", got)
	}
}

// TestADurationIsTranslatedAsTheCatalogIsRead is the seeder's one
// irreplaceable job, and the one whose failure is a wrong number rather than
// an error.
//
// Go unmarshals a time.Duration from a number of nanoseconds, so "5s" left
// alone reaches the resolver as five nanoseconds — a slot that times out
// before it starts, on every boot, with nothing saying why. The assertion is
// therefore on the parsed [time.Duration] and not on the JSON: a test reading
// the number back out of the manifest would agree with itself about a number
// that is wrong.
//
// The compound form is here because it is where an accumulator gets it wrong:
// "1m30s" is two terms summed, and a parser that took the last one, or the
// first, would still pass every single-unit case.
func TestADurationIsTranslatedAsTheCatalogIsRead(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "timed", `---
id: timed
name: Timed
provider: claude
spec:
  slots:
    - name: quick
      source: { kind: cmd, cmd: { run: "true", timeout: 300ms } }
    - name: plain
      source: { kind: cmd, cmd: { run: "true", timeout: 5s } }
    - name: compound
      source: { kind: cmd, cmd: { run: "true", timeout: 1m30s } }
    - name: long
      source: { kind: cmd, cmd: { run: "true", timeout: 2h } }
    - name: nanoseconds
      source: { kind: cmd, cmd: { run: "true", timeout: 1500 } }
    - name: silent
      source: { kind: cmd, cmd: { run: "true" } }
---
`)
	cat, err := Open(root)
	if err != nil {
		t.Fatalf("open the bundle: %v", err)
	}
	p, err := cat.Profile(context.Background(), "timed")
	if err != nil {
		t.Fatalf("load the profile: %v", err)
	}
	slots, err := p.Spec.Slots()
	if err != nil {
		t.Fatalf("decode the slots: %v", err)
	}

	want := map[string]time.Duration{
		"quick":       300 * time.Millisecond,
		"plain":       5 * time.Second,
		"compound":    time.Minute + 30*time.Second,
		"long":        2 * time.Hour,
		"nanoseconds": 1500 * time.Nanosecond,
		"silent":      0,
	}
	if len(slots) != len(want) {
		t.Fatalf("decoded %d slots, want %d", len(slots), len(want))
	}
	for _, slot := range slots {
		if got := slot.Source.Cmd.Timeout; got != want[slot.Name] {
			t.Errorf("slot %q timeout = %s, want %s", slot.Name, got, want[slot.Name])
		}
	}
}

// TestADurationIsTranslatedUnderEveryKeyThatCarriesOne walks the whole list
// rather than the key that happens to be used.
//
// Every duration in the portfolio's bundle sits under spec.slots today, so a
// narrowing of the key set to spec.slots alone passes the rest of this suite
// and the golden gate as well. The other two keys carry an
// agentcontext.SlotSource just as slots does — a files entry and a template
// entry are both "a literal, or a source resolved at materialization" — and
// nothing but this asserts they were not forgotten.
func TestADurationIsTranslatedUnderEveryKeyThatCarriesOne(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "everywhere", `---
id: everywhere
name: Everywhere
provider: claude
spec:
  slots:
    - name: fromslots
      source: { kind: cmd, cmd: { run: "true", timeout: 5s } }
  files:
    "notes/from-files.md": { kind: cmd, cmd: { run: "true", timeout: 5s } }
  templates:
    "AGENTS.md": { kind: cmd, cmd: { run: "true", timeout: 5s } }
---
`)
	cat, err := Open(root)
	if err != nil {
		t.Fatalf("open the bundle: %v", err)
	}
	p, err := cat.Profile(context.Background(), "everywhere")
	if err != nil {
		t.Fatalf("load the profile: %v", err)
	}

	slots, err := p.Spec.Slots()
	if err != nil {
		t.Fatalf("decode spec.slots: %v", err)
	}
	if len(slots) != 1 || slots[0].Source.Cmd.Timeout != 5*time.Second {
		t.Errorf("spec.slots timeout = %v, want 5s", slots)
	}

	for key, entries := range map[string]func() (map[string]profile.FileEntry, error){
		"files":     p.Spec.Files,
		"templates": p.Spec.Templates,
	} {
		declared, err := entries()
		if err != nil {
			t.Fatalf("decode spec.%s: %v", key, err)
		}
		if len(declared) != 1 {
			t.Fatalf("spec.%s decoded %d entries, want 1", key, len(declared))
		}
		for at, entry := range declared {
			if entry.Source == nil {
				t.Fatalf("spec.%s %q is not a source", key, at)
			}
			if got := entry.Source.Cmd.Timeout; got != 5*time.Second {
				t.Errorf("spec.%s %q timeout = %s, want 5s", key, at, got)
			}
		}
	}
}

// TestADurationBehindAnAnchorIsTranslatedToo covers the one place the alias had
// to be followed before the tag was read.
//
// A value node for `timeout: *d` is an alias node tagged "!!alias", so a tag
// test taken off it leaves the string alone. The failure is loud — encoding/json
// refuses to unmarshal a string into a time.Duration — but it names a Go type
// rather than anything the operator wrote, and the seeder this replaces
// translated it, because PyYAML resolves an alias before the walk ever sees it.
func TestADurationBehindAnAnchorIsTranslatedToo(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "anchored", `---
id: anchored
name: Anchored
provider: claude
spec:
  slots:
    - name: first
      source: { kind: cmd, cmd: { run: "true", timeout: &short 300ms } }
    - name: second
      source: { kind: cmd, cmd: { run: "true", timeout: *short } }
---
`)
	cat, err := Open(root)
	if err != nil {
		t.Fatalf("open the bundle: %v", err)
	}
	p, err := cat.Profile(context.Background(), "anchored")
	if err != nil {
		t.Fatalf("load the profile: %v", err)
	}
	slots, err := p.Spec.Slots()
	if err != nil {
		t.Fatalf("decode the slots: %v", err)
	}
	for _, slot := range slots {
		if got := slot.Source.Cmd.Timeout; got != 300*time.Millisecond {
			t.Errorf("slot %q timeout = %s, want 300ms", slot.Name, got)
		}
	}
}

// TestAnAnchorThatContainsItselfIsRefusedRatherThanFollowed is a crash
// regression, and the crash is reachable from a file.
//
// yaml.Unmarshal into a yaml.Node builds the node tree for `&x [*x]` without
// complaint — only Decode refuses it, and this package walks the tree itself.
// So the guard has to be here, and what it owes the operator is the anchor's
// name.
func TestAnAnchorThatContainsItselfIsRefusedRatherThanFollowed(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "looped", `---
id: looped
name: Looped
provider: claude
spec:
  settings: &loop
    nested: *loop
---
`)
	_, err := Open(root)
	if err == nil {
		t.Fatal("a self-referential anchor was accepted")
	}
	for _, want := range []string{"loop", "contains itself"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %q: %v", want, err)
		}
	}
}

// TestAManifestValueIsNotRespelledIntoEscapes covers the encoder setting, which
// is invisible everywhere else.
//
// Go's JSON encoder rewrites "&", "<" and ">" as \u0026, \u003c and \u003e
// unless it is told not to. Those are ordinary characters in a path, a shell
// redirect and a settings matcher, and every one of them survives a decode —
// so a slot's `2>/dev/null` round-trips through agentcontext identically either
// way and no render would show the difference. spec.settings does not decode:
// its bytes are carried to the harness's settings document with json.Indent,
// which moves whitespace and nothing else. That is the one place the escapes
// would land, in a file nobody reads by hand.
func TestAManifestValueIsNotRespelledIntoEscapes(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "punctuated", `---
id: punctuated
name: Punctuated
provider: claude
spec:
  settings:
    matcher: "mcp__.*(a|b) & <c> > d"
---
`)
	cat, err := Open(root)
	if err != nil {
		t.Fatalf("open the bundle: %v", err)
	}
	p, err := cat.Profile(context.Background(), "punctuated")
	if err != nil {
		t.Fatalf("load the profile: %v", err)
	}
	got := string(p.Spec["settings"])
	if want := `{"matcher":"mcp__.*(a|b) & <c> > d"}`; got != want {
		t.Errorf("spec.settings = %s, want %s", got, want)
	}
	if strings.Contains(got, `\u00`) {
		t.Errorf("the manifest was re-spelled into escapes: %s", got)
	}
}

// TestADurationIsNotTranslatedInAnOpaqueDocument is the other half of the rule,
// and the half the seeder got wrong.
//
// It translated every field named "timeout" anywhere in the manifest.
// spec.settings and spec.subagent are documents a harness reads, not values
// cairn decodes, so rewriting a string into a number inside one hands the
// harness a value its own schema rejects — silently, for the same reason the
// test above exists.
func TestADurationIsNotTranslatedInAnOpaqueDocument(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "opaque", `---
id: opaque
name: Opaque
provider: claude
spec:
  settings:
    someHarnessKey: { timeout: 5s }
  subagent:
    timeout: 5s
---
`)
	cat, err := Open(root)
	if err != nil {
		t.Fatalf("open the bundle: %v", err)
	}
	p, err := cat.Profile(context.Background(), "opaque")
	if err != nil {
		t.Fatalf("load the profile: %v", err)
	}
	for key, want := range map[string]string{
		"settings": `{"someHarnessKey":{"timeout":"5s"}}`,
		"subagent": `{"timeout":"5s"}`,
	} {
		if got := string(p.Spec[key]); got != want {
			t.Errorf("spec.%s = %s, want %s — an opaque document was rewritten", key, got, want)
		}
	}
}

// TestADurationThatIsNotOneIsRefusedByName covers the failure the operator can
// act on. A duration cairn cannot parse is a refusal rather than a zero,
// because a zero means "use the resolver's default" and would look like a
// profile that declared nothing.
func TestADurationThatIsNotOneIsRefusedByName(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "wrong", `---
id: wrong
name: Wrong
provider: claude
spec:
  slots:
    - name: bad
      source: { kind: cmd, cmd: { run: "true", timeout: "five seconds" } }
---
`)
	_, err := Open(root)
	if err == nil {
		t.Fatal("a duration that is not one was accepted")
	}
	for _, want := range []string{"five seconds", "spec.slots", "1m30s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %q: %v", want, err)
		}
	}
}

// TestAnAbsentBundleIsNamedRatherThanCreated is the T07c contract at the level
// it is implemented: a read finds nothing, says what it was looking for, and
// leaves the filesystem alone.
func TestAnAbsentBundleIsNamedRatherThanCreated(t *testing.T) {
	outer := filepath.Join(t.TempDir(), "made")
	absent := filepath.Join(outer, "up", "bundle")

	_, err := Open(absent)
	if !errors.Is(err, ErrBundleNotFound) {
		t.Fatalf("Open(absent) = %v, want ErrBundleNotFound", err)
	}
	if !strings.Contains(err.Error(), absent) {
		t.Errorf("the refusal does not name the bundle: %v", err)
	}
	if _, err := os.Lstat(outer); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reading an absent bundle created %s: %v", outer, err)
	}
}

// TestADirectoryWithNoProfilesIsNotAnEmptyCatalog covers the case that reads as
// an empty catalog and is not one: cairn pointed at the parent of a bundle, at
// a checkout that has moved, or at a variable exported for something else. The
// diagnostic has to name the directory it looked in, because the operator's
// next question is which one that was.
func TestADirectoryWithNoProfilesIsNotAnEmptyCatalog(t *testing.T) {
	t.Run("no profiles directory", func(t *testing.T) {
		root := t.TempDir()
		_, err := Open(root)
		if !errors.Is(err, ErrNoProfilesDir) {
			t.Fatalf("Open = %v, want ErrNoProfilesDir", err)
		}
		if !strings.Contains(err.Error(), filepath.Join(root, ProfilesDir)) {
			t.Errorf("the refusal does not name the directory: %v", err)
		}
	})
	t.Run("a profiles directory holding no profile", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, ProfilesDir, "README.txt"), "not a profile\n")
		_, err := Open(root)
		if !errors.Is(err, ErrNoProfilesDir) {
			t.Fatalf("Open = %v, want ErrNoProfilesDir", err)
		}
	})
}

// TestAFrontmatterKeyCairnDoesNotKnowIsRefused covers the difference between
// the frontmatter and the manifest, which is the one place this package
// validates anything.
//
// A manifest key cairn has never heard of is carried untouched — that is the
// whole design of spec. A frontmatter key it has never heard of is a typo, and
// with the file as the only copy nothing downstream would notice that
// "descripton" left the profile without a description.
func TestAFrontmatterKeyCairnDoesNotKnowIsRefused(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "typo", `---
id: typo
name: Typo
descripton: the missing letter
provider: claude
spec:
  aKeyCairnHasNeverHeardOf: { carried: true }
---
`)
	_, err := Open(root)
	if err == nil {
		t.Fatal("a misspelled frontmatter key was accepted")
	}
	for _, want := range []string{"descripton", "description", "line 4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %q: %v", want, err)
		}
	}

	// And the manifest key beside it is carried, which is the contrast.
	writeProfileFile(t, root, "typo", `---
id: typo
name: Typo
provider: claude
spec:
  aKeyCairnHasNeverHeardOf: { carried: true }
---
`)
	cat, err := Open(root)
	if err != nil {
		t.Fatalf("open the bundle: %v", err)
	}
	p, err := cat.Profile(context.Background(), "typo")
	if err != nil {
		t.Fatalf("load the profile: %v", err)
	}
	if got, want := string(p.Spec["aKeyCairnHasNeverHeardOf"]), `{"carried":true}`; got != want {
		t.Errorf("an unknown manifest key = %s, want %s", got, want)
	}
}

// TestAProfileIdMustMatchItsFileName pins the two spellings of one fact
// together. An extends chain names a profile by id and a listing names it by
// file, so a bundle where they differ resolves one and lists the other.
func TestAProfileIdMustMatchItsFileName(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "engineer", "---\nid: enginer\nname: Engineer\nprovider: claude\n---\n")
	_, err := Open(root)
	if err == nil {
		t.Fatal("a profile whose id and file name disagree was accepted")
	}
	for _, want := range []string{"enginer", "engineer.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not carry %q: %v", want, err)
		}
	}
}

// TestAFileWithNoFrontmatterIsRefusedByName covers the two ways a profile file
// can be malformed at the fence. Both name the file, because the operator's
// question is which one.
func TestAFileWithNoFrontmatterIsRefusedByName(t *testing.T) {
	for name, text := range map[string]string{
		"no frontmatter at all": "# just prose\n",
		"an unclosed fence":     "---\nid: x\nname: X\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeProfileFile(t, root, "x", text)
			_, err := Open(root)
			if err == nil {
				t.Fatal("a malformed profile file was accepted")
			}
			if !strings.Contains(err.Error(), "x.md") {
				t.Errorf("the refusal does not name the file: %v", err)
			}
		})
	}
}

// TestABlockScalarKeepsItsTrailingNewline is a regression, and the bug it
// pins is invisible: a `body: |` in a subagent declaration is written into a
// definition file, and one newline shorter is a diff nobody reads.
//
// It happens when the line break before the closing fence is dropped along
// with the fence. A YAML document that stops without one clips the trailing
// newline off the last block scalar in it, and every other value in the file
// is unaffected.
func TestABlockScalarKeepsItsTrailingNewline(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "sub", `---
id: sub
name: Sub
provider: claude
spec:
  subagent:
    body: |
      Read the diff.
---
`)
	cat, err := Open(root)
	if err != nil {
		t.Fatalf("open the bundle: %v", err)
	}
	p, err := cat.Profile(context.Background(), "sub")
	if err != nil {
		t.Fatalf("load the profile: %v", err)
	}
	var declared struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(p.Spec["subagent"], &declared); err != nil {
		t.Fatalf("decode spec.subagent: %v", err)
	}
	if got, want := declared.Body, "Read the diff.\n"; got != want {
		t.Errorf("the block scalar is %q, want %q", got, want)
	}
}

// TestDefaultRoot covers the fallback chain, which is the database path's chain
// with the file name taken off the end.
func TestDefaultRoot(t *testing.T) {
	for _, c := range []struct {
		name                 string
		env, xdg, home, want string
		wantErr              bool
	}{
		{name: "the variable wins", env: "/bundles/here", xdg: "/cfg", home: "/home/op", want: "/bundles/here"},
		{name: "then XDG", xdg: "/cfg", home: "/home/op", want: filepath.Join("/cfg", DirName)},
		{name: "then home", home: "/home/op", want: filepath.Join("/home/op", ".config", DirName)},
		{name: "and no home is an error", wantErr: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := DefaultRoot(c.env, c.xdg, c.home)
			if c.wantErr {
				if !errors.Is(err, ErrNoHome) {
					t.Fatalf("DefaultRoot = %q, %v, want ErrNoHome", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("DefaultRoot: %v", err)
			}
			if got != c.want {
				t.Errorf("DefaultRoot = %q, want %q", got, c.want)
			}
		})
	}
}

// TestAMissingLookupIsNamedByKind covers the two not-found sentinels a caller
// branches on, and that each names the file it looked in.
func TestAMissingLookupIsNamedByKind(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "engineer", "---\nid: engineer\nname: Engineer\nprovider: claude\n---\n")
	cat, err := Open(root)
	if err != nil {
		t.Fatalf("open the bundle: %v", err)
	}

	if _, err := cat.Profile(context.Background(), "nobody"); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("Profile(nobody) = %v, want ErrProfileNotFound", err)
	}
}

// writeProfileFile writes one profile file into the bundle's profiles
// directory.
func writeProfileFile(t *testing.T, root, id, text string) {
	t.Helper()
	writeFile(t, filepath.Join(root, ProfilesDir, id+profileExt), text)
}

// writeFile writes text at path, creating the directories above it.
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ids returns the profile ids of a listing, in the order it came back.
func ids(profiles []profile.Profile) []string {
	out := make([]string, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, p.ID)
	}
	return out
}

// TestAProfileIdHoldsNoPathSeparator pins the constraint the composition
// flags' detection rule stands on.
//
// `cairn boot x --with <part>` decides between a catalog id and a path by
// whether the value holds a separator, and that is only sound while an id
// cannot hold one. It is true today because an id is a file stem, but that is
// an inference about where ids come from rather than a rule about ids, and an
// inference is not what a resolution split should rest on.
func TestAProfileIdHoldsNoPathSeparator(t *testing.T) {
	root := t.TempDir()
	writeProfileFile(t, root, "engineer", "---\nid: sub/dir\nname: Engineer\nprovider: claude\n---\n")
	_, err := Open(root)
	if !errors.Is(err, ErrProfileID) {
		t.Fatalf("a profile id holding a separator was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "sub/dir") {
		t.Errorf("the refusal does not name the id: %v", err)
	}
}

// TestReadProfileReadsAFileOutsideTheBundle covers the read a composition's
// path-named part goes through: the ordinary parser, on a file the catalog
// does not hold and never listed.
func TestReadProfileReadsAFileOutsideTheBundle(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "docs-only.md")
	writeFile(t, path, "---\nextends: base\nname: Docs only\nspec:\n  skills: [\"adr\"]\n---\n\nDocs.\n")
	p, err := ReadProfile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// The id is the file's own name, which is what a part declaring none gets
	// and what a generated part will always be.
	if p.ID != "docs-only" || p.Name != "Docs only" || p.Extends != "base" {
		t.Errorf("the part read as %+v", *p)
	}
	if got := string(p.Spec["skills"]); got != `["adr"]` {
		t.Errorf("the part's manifest is %s, want the declared list", got)
	}

	// Any extension, because a path names exactly one file and there is
	// nothing to tell a profile from a README.
	other := filepath.Join(dir, "part.txt")
	writeFile(t, other, "---\nname: Part\n---\n")
	if _, err := ReadProfile(other); err != nil {
		t.Errorf("a part spelled with another extension was refused: %v", err)
	}

	// A file that is not there names itself rather than being conjured.
	if _, err := ReadProfile(filepath.Join(dir, "nope.md")); err == nil {
		t.Error("a part that is not there was accepted")
	}

	// The file is NOT held to being named after the id it declares, which is
	// the one rule that does not carry over from the catalog.
	//
	// The catalog is keyed by file name, so a bundled file disagreeing with
	// itself is a real ambiguity about which spelling the bundle answers to.
	// Nothing here is keyed by anything. Applying the rule anyway would put
	// back exactly the friction --with <path> exists to remove: a generator
	// writing a part to a tempfile would have to embed that tempfile's random
	// basename as the part's id.
	generated := filepath.Join(dir, "tachyon-session-4f2a9b.md")
	writeFile(t, generated, "---\nid: docs-only\nname: Docs only\n---\n")
	p, err = ReadProfile(generated)
	if err != nil {
		t.Fatalf("a part whose id is not its file name was refused: %v", err)
	}
	if p.ID != "docs-only" {
		t.Errorf("the part's id is %q, want the one it declared", p.ID)
	}
	// The catalog still refuses the same file, because there the rule means
	// something. Asserted here so the two cannot quietly become one.
	if _, err := parseProfile("---\nid: docs-only\n---\n", "tachyon-session-4f2a9b.md"); err == nil {
		t.Error("the catalog accepted a profile whose id and file name disagree")
	}
}

// TestPartsIsReadIntoOneNamespace covers the one subdirectory of the profiles
// directory that is also read.
//
// What is asserted throughout is that nothing downstream can tell. A file
// under profiles/parts/ is an ordinary profile with an ordinary id, in the
// same flat map as the files beside it — so the interesting assertions are all
// about sameness, and the only new behaviour is the collision that flat
// directory made impossible.
func TestPartsIsReadIntoOneNamespace(t *testing.T) {
	const engineer = "---\nid: engineer\nname: Engineer\nprovider: claude\n---\n"
	const docsOnly = "---\nid: docs-only\nname: Docs only\nprovider: claude\n---\n"

	t.Run("a nested profile is catalogued under its bare id", func(t *testing.T) {
		root := t.TempDir()
		writeProfileFile(t, root, "engineer", engineer)
		writePartFile(t, root, "docs-only", docsOnly)

		cat, err := Open(root)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		// One namespace and one order. The listing is sorted by id, so a
		// nested profile sorts among the root ones rather than after them —
		// there is nowhere in the output where the two locations are visible.
		if got, want := ids(cat.Profiles()), []string{"docs-only", "engineer"}; !slices.Equal(got, want) {
			t.Errorf("the catalog lists %v, want %v", got, want)
		}
		// Looked up by the bare id, and by nothing else: an id holds no
		// separator, so the directory cannot be part of the name.
		if _, err := cat.Profile(t.Context(), "docs-only"); err != nil {
			t.Errorf("a nested profile is not reachable by its id: %v", err)
		}
		if _, err := cat.Profile(t.Context(), PartsDir+"/docs-only"); !errors.Is(err, ErrProfileNotFound) {
			t.Errorf("a nested profile answered to a path-ish id: %v", err)
		}
	})

	t.Run("two files claiming one id are refused, naming both", func(t *testing.T) {
		// The failure a flat directory made impossible: an id is its file's
		// stem, and one directory holds one docs-only.md. Two can.
		root := t.TempDir()
		writeProfileFile(t, root, "docs-only", docsOnly)
		writePartFile(t, root, "docs-only", docsOnly)

		_, err := Open(root)
		if !errors.Is(err, ErrDuplicateProfileID) {
			t.Fatalf("Open with a duplicate id = %v, want ErrDuplicateProfileID", err)
		}
		// Both paths, because the fix is to rename one of them and the reader
		// has to be told which two files are in question. Neither is quietly
		// preferred: a winner would leave the loser on disk, edited and
		// committed and never taking effect.
		for _, want := range []string{
			filepath.Join(root, ProfilesDir, "docs-only"+profileExt),
			filepath.Join(root, ProfilesDir, PartsDir, "docs-only"+profileExt),
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name %s: %v", want, err)
			}
		}
	})

	t.Run("only parts is read, and only one level of it", func(t *testing.T) {
		// Naming exactly one directory is what keeps the layout of the bundle
		// from becoming part of its meaning. The cost is that the others stay
		// ignored silently, which is asserted here rather than left to be
		// discovered: these are not broken profiles and are not reported as
		// any kind of profile at all.
		root := t.TempDir()
		writeProfileFile(t, root, "engineer", engineer)
		writeFile(t, filepath.Join(root, ProfilesDir, "roles", "hidden"+profileExt),
			"---\nid: hidden\nname: Hidden\nprovider: claude\n---\n")
		writeFile(t, filepath.Join(root, ProfilesDir, PartsDir, "deeper", "buried"+profileExt),
			"---\nid: buried\nname: Buried\nprovider: claude\n---\n")

		cat, err := Open(root)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got, want := ids(cat.Profiles()), []string{"engineer"}; !slices.Equal(got, want) {
			t.Errorf("the catalog lists %v, want %v — nothing but parts is read", got, want)
		}
	})

	t.Run("a parts symlink is not followed", func(t *testing.T) {
		// A bundle is a directory of files git reviews, and a link out of the
		// tree is content nobody reviewing the bundle can see. The entry's own
		// type is what decides it, which is why readProfiles reads parts
		// through the listing rather than by joining the name.
		root := t.TempDir()
		writeProfileFile(t, root, "engineer", engineer)
		outside := t.TempDir()
		writeFile(t, filepath.Join(outside, "docs-only"+profileExt), docsOnly)
		if err := os.Symlink(outside, filepath.Join(root, ProfilesDir, PartsDir)); err != nil {
			t.Skipf("symlinks are not available here: %v", err)
		}

		cat, err := Open(root)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got, want := ids(cat.Profiles()), []string{"engineer"}; !slices.Equal(got, want) {
			t.Errorf("the catalog lists %v, want %v — a symlinked parts was followed", got, want)
		}
	})

	t.Run("a bundle whose profiles are all nested is a bundle", func(t *testing.T) {
		root := t.TempDir()
		writePartFile(t, root, "docs-only", docsOnly)

		cat, err := Open(root)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got, want := ids(cat.Profiles()), []string{"docs-only"}; !slices.Equal(got, want) {
			t.Errorf("the catalog lists %v, want %v", got, want)
		}
	})

	t.Run("a bundle with a profile in neither place names both", func(t *testing.T) {
		// The "cairn was pointed somewhere that is not a bundle" diagnostic,
		// which now has two directories to have looked in and has to say so:
		// an operator whose only profiles are nested must not be told cairn
		// looked at the root and stopped.
		root := t.TempDir()
		writeFile(t, filepath.Join(root, ProfilesDir, "README.txt"), "not a profile\n")

		_, err := Open(root)
		if !errors.Is(err, ErrNoProfilesDir) {
			t.Fatalf("Open = %v, want ErrNoProfilesDir", err)
		}
		for _, want := range []string{
			filepath.Join(root, ProfilesDir),
			filepath.Join(root, ProfilesDir, PartsDir),
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not name %s: %v", want, err)
			}
		}
	})

	t.Run("a nested profile is held to the rules a root one is", func(t *testing.T) {
		// parseProfile, unchanged and reached by the same call. The id and the
		// file name are one fact written twice wherever the file sits, which
		// is also what makes the duplicate above the only collision possible.
		root := t.TempDir()
		writePartFile(t, root, "docs-only", "---\nid: something-else\nname: X\nprovider: claude\n---\n")

		_, err := Open(root)
		if err == nil {
			t.Fatal("a nested profile disagreeing with its file name was accepted")
		}
		if !strings.Contains(err.Error(), "rename one to match the other") {
			t.Errorf("the refusal is not the catalog's own: %v", err)
		}
	})
}

// writePartFile writes one profile file into the bundle's profiles/parts
// directory.
func writePartFile(t *testing.T, root, id, text string) {
	t.Helper()
	writeFile(t, filepath.Join(root, ProfilesDir, PartsDir, id+profileExt), text)
}
