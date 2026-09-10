package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/catalog"
	"github.com/chrispian/cairn/profile"
)

// TestPartIsPath is the detection rule, written out as a table.
//
// It gets a test of its own rather than being covered only through the
// commands because of how it fails. Every other wire in a composition breaks
// loudly — a part that will not load, a chain that will not resolve — and this
// one resolves the wrong thing: a value taken for an id when it named a file
// finds a profile of that name and boots it, silently, and a value taken for a
// path when it named an id fails to open a file nobody meant. Neither says
// what happened.
func TestPartIsPath(t *testing.T) {
	for raw, want := range map[string]bool{
		// Names. A catalog id holds no separator — catalog.parseProfile
		// refuses one — so nothing in this half can also be a path.
		"docs-only":      false,
		"base":           false,
		"a_b.c-d":        false,
		"part-with-dots": false,

		// The ambiguous one, and the reason the not-found diagnostic below
		// exists. It resolves as a name.
		"x.md": false,

		// Paths.
		"./x.md":                         true,
		"../parts/x.md":                  true,
		"/abs/x.md":                      true,
		"parts/x.md":                     true,
		"~/parts/x.md":                   true,
		"~":                              true,
		"$CAIRN_PROFILE_ROOT/parts/x.md": true,
		"$PART":                          true,
		".hidden":                        true,
	} {
		if got := partIsPath(raw); got != want {
			t.Errorf("partIsPath(%q) = %v, want %v", raw, got, want)
		}
	}
}

// TestComposeWith covers --with in both of its forms, which is one flag and
// two resolutions: an id read out of the catalog, and a path read off disk.
func TestComposeWith(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	// A part in the catalog: an ordinary profile, declaring one key.
	writeProfile(t, bundle, bundleProfile{
		ID:   "docs-only",
		Name: "Docs only",
		Spec: map[string]string{"skills": `["docs-review"]`},
	})
	// A second, so the order of two parts can be read off the result. It is
	// named to sort BEFORE the first: a pair given in alphabetical order
	// cannot tell a list that preserved the order from one that sorted it,
	// which is the discrimination this fixture exists to provide.
	writeProfile(t, bundle, bundleProfile{
		ID:   "aardvark",
		Name: "Aardvark",
		Spec: map[string]string{"skills": `["from-aardvark"]`},
	})

	t.Run("a catalog id merges after the extends chain", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "engineer", "--with", "docs-only")
		if got, want := declarersOfOrFail(t, out, "skills"), "engineer, docs-only"; got != want {
			t.Errorf("spec.skills is declared by %q, want %q", got, want)
		}
		if got := skillsOf(t, out); !slicesEqual(got, []string{"code-review", "docs-review"}) {
			t.Errorf("the composed skills are %v, want the profile's and the part's", got)
		}
	})

	t.Run("the parts merge in the order they were given", func(t *testing.T) {
		// Order is precedence, so it has to survive the flag, the slice and
		// the fold. The pair is deliberately given in reverse alphabetical
		// order and then asserted both ways round: a list that sorted its
		// values, or reversed them, would pass one of these and fail the
		// other, where a pair in sorted order would pass a sort silently.
		for _, order := range [][2]string{{"docs-only", "aardvark"}, {"aardvark", "docs-only"}} {
			out := mustShow(t, ctx, bundle, "engineer", "--with", order[0], "--with", order[1])
			want := "base -> engineer -> " + order[0] + " -> " + order[1]
			if !strings.Contains(out, "chain         "+want) {
				t.Errorf("the chain is not the fold order %q:\n%s", want, out)
			}
		}
	})

	t.Run("a path names a file", func(t *testing.T) {
		part := writePart(t, home, "generated.md", `---
name: Generated
spec:
  skills: ["from-the-file"]
---
`)
		out := mustShow(t, ctx, bundle, "engineer", "--with", part)
		if !strings.Contains(out, "chain         base -> engineer -> "+part) {
			t.Errorf("the chain does not name the file the part was read from:\n%s", out)
		}
		if got := skillsOf(t, out); !slicesEqual(got, []string{"code-review", "from-the-file"}) {
			t.Errorf("the composed skills are %v, want the profile's and the file's", got)
		}
		// The document says the file contributed. A composed value that read
		// as the profile's own declaration is the failure `cairn show` exists
		// to prevent, and a part outside the catalog is the newest way to
		// cause it.
		if got, want := declarersOfOrFail(t, out, "skills"), "engineer, "+part; got != want {
			t.Errorf("spec.skills is declared by %q, want %q", got, want)
		}
	})

	// The pinned restriction that nobody wrote and everybody would infer.
	t.Run("extends on a path-loaded part resolves against the catalog", func(t *testing.T) {
		part := writePart(t, home, "inherits.md", `---
extends: docs-only
spec:
  subagents: ["reviewer"]
---
`)
		out := mustShow(t, ctx, bundle, "base2", "--with", part)
		if !strings.Contains(out, "chain         base -> base2 -> docs-only -> "+part) {
			t.Errorf("the part's extends did not resolve against the catalog:\n%s", out)
		}
		// And it brought the ancestor's contribution with it, which is what
		// resolving the chain before merging means.
		if got := skillsOf(t, out); !slicesEqual(got, []string{"docs-review"}) {
			t.Errorf("the composed skills are %v, want the catalog ancestor's", got)
		}
	})

	t.Run("a bare name that is not a profile says how to name a file", func(t *testing.T) {
		// The whole mitigation for the one ambiguity the detection rule
		// creates, asserted as the sentence rather than as its parts: an
		// operator who wrote x.md meaning a file has to be told the spelling
		// that would have worked.
		err := runShow(ctx, []string{"engineer", "--with", "x.md", "--profile", bundle}, discard(), discard())
		if err == nil {
			t.Fatal("a --with naming no profile was accepted")
		}
		want := `no profile named "x.md"; if you meant a file, write "./x.md"`
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic is %q, want it to carry %q", err, want)
		}
	})

	t.Run("a path that names no file is refused", func(t *testing.T) {
		err := runShow(ctx, []string{"engineer", "--with", "./nope.md", "--profile", bundle}, discard(), discard())
		if err == nil {
			t.Fatal("a --with naming no file was accepted")
		}
		// It quotes what the operator wrote, and it does not send them looking
		// for a profile of that name.
		if !strings.Contains(err.Error(), `--with "./nope.md"`) {
			t.Errorf("the diagnostic does not quote the value: %v", err)
		}
		if strings.Contains(err.Error(), "no profile named") {
			t.Errorf("a path was looked up as a catalog id: %v", err)
		}
	})

	t.Run("a variable in a part expands", func(t *testing.T) {
		// $CAIRN_PROFILE_ROOT is seeded from the bundle this command read, so
		// a part beside the profiles relocates with them.
		part := filepath.Join(bundle, "part.md")
		writeFile(t, part, "---\nspec:\n  skills: [\"from-the-bundle\"]\n---\n", 0o644)
		out := mustShow(t, ctx, bundle, "engineer", "--with", "$CAIRN_PROFILE_ROOT/part.md")
		if got := skillsOf(t, out); !slicesEqual(got, []string{"code-review", "from-the-bundle"}) {
			t.Errorf("the composed skills are %v, want the part beside the bundle to have loaded", got)
		}
	})

	t.Run("an abstract part does not make the composition abstract", func(t *testing.T) {
		// A part exists to be merged rather than booted, which is what
		// abstract means, so a boot that refused one would refuse the case
		// composition exists for. The target's own leaf is what decides.
		part := writePart(t, home, "fragment.md", "---\nabstract: true\nspec:\n  skills: [\"fragmentary\"]\n---\n")
		out := mustShow(t, ctx, bundle, "engineer", "--with", part)
		if !strings.Contains(out, "abstract      false") {
			t.Errorf("an abstract part made the composition abstract:\n%s", out)
		}
	})
}

// TestComposeSkill covers --skill in both of its spellings.
func TestComposeSkill(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	t.Run("the comma-separated and repeated forms compose", func(t *testing.T) {
		// Chrispian asked for the list form; the repeatable form is what every
		// other instance flag looks like. They are one flag, and a launcher
		// never has to decide which shape cairn wants.
		lists := mustShow(t, ctx, bundle, "engineer", "--skill", "qhealth,qstatus", "--skill", "adr")
		repeats := mustShow(t, ctx, bundle, "engineer",
			"--skill", "qhealth", "--skill", "qstatus", "--skill", "adr")
		if a, b := skillsOf(t, lists), skillsOf(t, repeats); !slicesEqual(a, b) {
			t.Errorf("the two spellings resolve differently: %v and %v", a, b)
		}
	})

	t.Run("it is additive, by id, and the profile's own skills stand", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "engineer", "--skill", "adr,code-review")
		got := skillsOf(t, out)
		// code-review is the profile's own and was also passed: one member,
		// because the collection is keyed by the id.
		if !slicesEqual(got, []string{"adr", "code-review"}) {
			t.Errorf("the composed skills are %v, want the union keyed by id", got)
		}
	})

	t.Run("the flag is named as a contributor", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "engineer", "--skill", "adr")
		if got, want := declarersOfOrFail(t, out, "skills"), "engineer, --skill"; got != want {
			t.Errorf("spec.skills is declared by %q, want %q", got, want)
		}
	})

	t.Run("a profile declaring none still receives them", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "base2", "--skill", "adr")
		if got := skillsOf(t, out); !slicesEqual(got, []string{"adr"}) {
			t.Errorf("the composed skills are %v, want the flag's alone", got)
		}
	})

	t.Run("a value naming no skill is refused", func(t *testing.T) {
		var l idList
		if err := l.Set(" , "); err == nil {
			t.Error("a --skill naming nothing was accepted")
		}
	})

	// The rule has to be in the help, because the flag reads like "choose the
	// skills" and is not that. There is no spelling anywhere in cairn for
	// removing a member of a collection keyed by its own id, and an operator
	// who learns that from the behaviour learns it by booting with one skill
	// too many.
	t.Run("the additive-only rule is in the help", func(t *testing.T) {
		for _, text := range []string{skillFlagUsage, usage} {
			if !strings.Contains(text, "Additive only") {
				t.Errorf("the help does not say the flag is additive only:\n%s", text)
			}
		}
	})
}

// TestComposeSet covers --set, which supplies an inline literal for a named
// slot at materialization.
func TestComposeSet(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	t.Run("it replaces the slot of that name", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "engineer", "--set", "note=user-facing docs only")
		note := slotNamed(t, out, "note")
		source, _ := note["source"].(map[string]any)
		inline, _ := source["inline"].(map[string]any)
		if got := source["kind"]; got != "inline" {
			t.Errorf("the slot's kind is %v, want inline", got)
		}
		if got := inline["content"]; got != "user-facing docs only" {
			t.Errorf("the slot's content is %v, want what was set", got)
		}
		// A member replaces whole, so the declared section goes with it. That
		// is how spec.slots composes for every other contributor, and --set
		// introduces no second rule.
		if _, kept := note["section"]; kept {
			t.Errorf("the replaced slot kept the declared section: %v", note)
		}
	})

	t.Run("the slots it does not name stand", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "engineer", "--set", "note=x")
		for _, name := range []string{"quiet", "memory"} {
			if slotNamed(t, out, name) == nil {
				t.Errorf("setting one slot dropped %q:\n%s", name, out)
			}
		}
	})

	t.Run("a slot the profile never declared is added", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "engineer", "--set", "direction=no API reference")
		if slotNamed(t, out, "direction") == nil {
			t.Errorf("a --set naming an undeclared slot added nothing:\n%s", out)
		}
	})

	t.Run("the flag is named as a contributor", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "engineer", "--set", "note=x")
		if got, want := declarersOfOrFail(t, out, "slots"), "engineer, --set"; got != want {
			t.Errorf("spec.slots is declared by %q, want %q", got, want)
		}
	})

	t.Run("a value with no = is refused, and an empty one is not", func(t *testing.T) {
		var l slotList
		if err := l.Set("note"); err == nil {
			t.Error(`a --set with no "=" was accepted`)
		}
		if err := l.Set("=x"); err == nil {
			t.Error("a --set naming no slot was accepted")
		}
		// A slot that stands for nothing renders nothing, which is a
		// legitimate way to silence one section for one materialization.
		if err := l.Set("note="); err != nil {
			t.Errorf("a --set with an empty value was refused: %v", err)
		}
	})
}

// TestComposeBoot is the end of the wire: what the flags resolve to is what
// the boot directory carries.
//
// `cairn show` is where most of the assertions above live because it prints
// the resolution, and a preview asserted alone would only prove the preview.
// This proves the boot.
func TestComposeBoot(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "runtime", "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	// A second skill for --skill to reach, so the flag is asserted against a
	// skill the profile does not already carry.
	mustMkdir(t, filepath.Join(skillsDir, "adr"))
	writeFile(t, filepath.Join(skillsDir, "adr", "SKILL.md"), "# adr\n", 0o644)
	seed(t, bundle, skillsDir, scopeDir)

	part := writePart(t, home, "docs-only.md", `---
name: Docs only
spec:
  files:
    "notes/direction.md": "docs only\n"
---
`)

	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{
		"boot", "engineer",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", "s1",
		"--with", part,
		"--skill", "adr",
		"--set", "note=the direction for this session",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}
	dir := strings.TrimSpace(stdout.String())

	// --with: the part's own files entry was planted.
	if got, want := read(t, dir, "notes/direction.md"), "docs only\n"; got != want {
		t.Errorf("the part's file holds %q, want %q", got, want)
	}
	// --with does not replace what the profile declared at another key.
	if _, err := os.Stat(filepath.Join(dir, "notes", "scratch.md")); err != nil {
		t.Errorf("composing a part dropped the profile's own file: %v", err)
	}
	// --skill: planted beside the profile's own.
	for _, rel := range []string{
		".claude/skills/adr/SKILL.md",
		".claude/skills/code-review/SKILL.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("the boot directory is missing %s: %v", rel, err)
		}
	}
	// --set: the inline literal reached the file the slot's marker stands in.
	if got := read(t, dir, "boot.md"); !strings.Contains(got, "the direction for this session") {
		t.Errorf("boot.md does not carry the slot that was set:\n%s", got)
	}
	if got := read(t, dir, "boot.md"); strings.Contains(got, "the standing note") {
		t.Errorf("boot.md still carries the slot the profile declared:\n%s", got)
	}
}

// mustShow runs `cairn show` against bundle and returns stdout.
func mustShow(t *testing.T, ctx context.Context, bundle, target string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"show", target}, args...)
	full = append(full, "--profile", bundle)
	if err := run(ctx, full, &stdout, &stderr); err != nil {
		t.Fatalf("show %v: %v\nstderr: %s", args, err, stderr.String())
	}
	return stdout.String()
}

// writePart writes a part outside the bundle, which is the whole reason --with
// takes a path: generated composition content has no home in profiles/.
func writePart(t *testing.T, home, name, text string) string {
	t.Helper()
	dir := filepath.Join(home, "parts")
	mustMkdir(t, dir)
	path := filepath.Join(dir, name)
	writeFile(t, path, text, 0o644)
	return path
}

// declarersOfOrFail reads the names beside a manifest key, failing the test
// when the key was not printed at all.
func declarersOfOrFail(t *testing.T, out, key string) string {
	t.Helper()
	got, ok := declarersOf(out, key)
	if !ok {
		t.Fatalf("spec.%s was not printed:\n%s", key, out)
	}
	return got
}

// skillsOf reads the composed skill ids out of a show document.
func skillsOf(t *testing.T, out string) []string {
	t.Helper()
	raw, ok := valueOf(t, out, "skills").([]any)
	if !ok {
		t.Fatalf("spec.skills is not a list:\n%s", out)
	}
	ids := make([]string, 0, len(raw))
	for _, v := range raw {
		ids = append(ids, v.(string))
	}
	return ids
}

// slotNamed reads one composed slot out of a show document, or nil when no
// slot of that name is there.
func slotNamed(t *testing.T, out, name string) map[string]any {
	t.Helper()
	raw, ok := valueOf(t, out, "slots").([]any)
	if !ok {
		t.Fatalf("spec.slots is not a list:\n%s", out)
	}
	for _, v := range raw {
		slot, _ := v.(map[string]any)
		if slot["name"] == name {
			return slot
		}
	}
	return nil
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// discard returns a writer for output a test does not read.
func discard() *bytes.Buffer { return &bytes.Buffer{} }

// TestMergeSkillsIsTheCascadesOwnRule pins that the flag reaches the manifest
// through the table of keyed collections rather than through a union written
// beside it. A second implementation of "compose by id" is how the two answers
// start to drift.
func TestMergeSkillsIsTheCascadesOwnRule(t *testing.T) {
	spec := profile.Spec{profile.SpecKeySkills: json.RawMessage(`["b","a"]`)}
	if err := (idList{"c", "a"}).mergeInto(spec, profile.SpecKeySkills, "--skill"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	want, err := profile.Merge(profile.SpecKeySkills,
		json.RawMessage(`["b","a"]`), json.RawMessage(`["c","a"]`))
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := string(spec[profile.SpecKeySkills]); got != string(want) {
		t.Errorf("--skill composed %s, want the key's own rule to give %s", got, want)
	}
}

// TestAFlagMayNameWhatTheCompositionAlreadyCarries covers the collision the
// flags exist to make cheap: a binding declares a skill, and the operator names
// it again because they did not read the binding first.
//
// It is asserted against a target that declares neither key, which is the shape
// that used to refuse. A merger only runs on two declared values, so a profile
// with no spec.skills leaves the flags' own list the whole value, unread — and
// an unfolded repeat in it reached bootdir, which refuses one. A target that
// declares the key never showed the defect at all, because the cascade's union
// folded the repeat away on the way past.
//
// Both flags, because they are one flag over two keys and a fold written for
// one of them would leave the other refusing.
func TestAFlagMayNameWhatTheCompositionAlreadyCarries(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := exampleBundle(t, home)
	scopeDir := filepath.Join(home, "scope")
	mustMkdir(t, scopeDir)

	// Neither key declared anywhere in this chain: base carries the two source
	// directories and nothing that names a skill or a prompt.
	writeFile(t, filepath.Join(bundle, "profiles", "bare.md"),
		"---\nid: bare\nextends: base\nname: Bare\n---\n", 0o644)
	// Two of each, so the fold has a position to get wrong as well as a
	// duplicate to remove.
	writeFile(t, filepath.Join(bundle, "bindings", "bare-binding.yaml"),
		"profile: bare\n"+
			"skills:\n  - capture-decision\n  - adr\n"+
			"prompts:\n  - handoff\n  - reset-scope\n"+
			fmt.Sprintf("scope: %s\n", scopeDir), 0o644)

	t.Run("a binding's id restated by the flag lands once, where it was", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "bare-binding",
			"--skill", "capture-decision", "--prompt", "handoff")
		// Once, and still first: the binding put it there, and the operator
		// restating it is not a request to move it behind the id it precedes.
		if got := skillsOf(t, out); !slicesEqual(got, []string{"capture-decision", "adr"}) {
			t.Errorf("the composed skills are %v, want the binding's folded once and in place", got)
		}
		if got := promptsOf(t, out); !slicesEqual(got, []string{"handoff", "reset-scope"}) {
			t.Errorf("the composed prompts are %v, want the binding's folded once and in place", got)
		}
	})

	t.Run("and the boot it used to refuse plants each once", func(t *testing.T) {
		// bootTree fails the test if the boot refuses, which is the claim.
		tree := bootTree(t, ctx, filepath.Join(home, "runtime"), "bare-binding",
			"--profile", bundle, "--skill", "capture-decision", "--prompt", "handoff")
		for _, rel := range []string{
			".claude/skills/capture-decision/SKILL.md",
			".claude/skills/adr/SKILL.md",
			".claude/commands/boot/handoff.md",
			".claude/commands/boot/reset-scope.md",
		} {
			if _, ok := tree[rel]; !ok {
				t.Errorf("the boot did not plant %s; the tree holds %v", rel, sortedPaths(tree))
			}
		}
	})

	t.Run("one command naming an id twice is the same collision", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "bare",
			"--skill", "adr,adr", "--prompt", "handoff", "--prompt", "handoff")
		if got := skillsOf(t, out); !slicesEqual(got, []string{"adr"}) {
			t.Errorf("the composed skills are %v, want the id once", got)
		}
		if got := promptsOf(t, out); !slicesEqual(got, []string{"handoff"}) {
			t.Errorf("the composed prompts are %v, want the id once", got)
		}
	})

	// Two spellings are two ids. A skill is a directory name and a prompt a
	// file name, so folding by anything looser than the bytes would be an
	// identity rule cairn has nowhere else — and would make one of two
	// directories a case-sensitive filesystem tells apart disappear.
	t.Run("ids that differ fold nothing", func(t *testing.T) {
		if got := (idList{"adr", "Adr", "adr"}).folded(); !slicesEqual(got, []string{"adr", "Adr"}) {
			t.Errorf("the folded ids are %v, want the two spellings kept and the repeat dropped", got)
		}
	})
}

// TestAManifestDeclaringOneIDTwiceIsStillRefused is the other half of the fold,
// and the half a fix could have taken out by mistake.
//
// The composition folds before the value is a manifest value. What bootdir
// guards is the manifest itself, where one skill declared twice is an authoring
// error in a file somebody wrote — and the way to find out is to be told, not
// to have cairn quietly render one of them. So the refusal stands, reached by a
// profile that declares the repeat with no flag anywhere near it.
func TestAManifestDeclaringOneIDTwiceIsStillRefused(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := exampleBundle(t, home)
	scopeDir := filepath.Join(home, "scope")
	mustMkdir(t, scopeDir)
	writeFile(t, filepath.Join(bundle, "profiles", "twice.md"),
		"---\nid: twice\nextends: base\nname: Twice\n"+
			"spec:\n  skills: [capture-decision, capture-decision]\n---\n", 0o644)
	writeFile(t, filepath.Join(bundle, "profiles", "twice-prompts.md"),
		"---\nid: twice-prompts\nextends: base\nname: Twice prompts\n"+
			"spec:\n  prompts: [handoff, handoff]\n---\n", 0o644)

	for _, tc := range []struct {
		id   string
		want error
	}{
		{"twice", bootdir.ErrSkillName},
		{"twice-prompts", bootdir.ErrPromptName},
	} {
		t.Run(tc.id, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(ctx, []string{"boot", tc.id, "--profile", bundle,
				"--scope", scopeDir,
				"--boot-root", filepath.Join(home, "runtime", tc.id), "--session", "s"},
				&stdout, &stderr)
			if !errors.Is(err, tc.want) {
				t.Fatalf("booting a manifest that declares one id twice gave %v, want %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), "twice") {
				t.Errorf("the refusal does not say what is wrong with the manifest: %v", err)
			}
		})
	}
}

// TestShowTakesEveryFlagBootComposesWith is the non-negotiable one, asserted
// as the flag sets rather than through a behaviour that could pass while one
// flag was missing.
//
// `cairn show` is the "what will this resolve to" preview. If boot accepts a
// part, a skill list or an inline slot and show does not, the preview is blind
// to precisely the part that makes a composition differ from its base — which
// is the one thing the reader is checking. It is also the piece most likely to
// be dropped as out of scope, which is why it is pinned here and not left to
// the tests that happen to use it.
func TestShowTakesEveryFlagBootComposesWith(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)
	writeProfile(t, bundle, bundleProfile{ID: "docs-only", Name: "Docs only"})

	// One invocation carrying all three, because the flags compose and a
	// command that took them one at a time would still be broken.
	out := mustShow(t, ctx, bundle, "engineer",
		"--with", "docs-only", "--skill", "adr,qhealth", "--set", "note=a direction")
	if !strings.Contains(out, "chain         base -> engineer -> docs-only") {
		t.Errorf("show did not compose the part:\n%s", out)
	}
	if got := skillsOf(t, out); !slicesEqual(got, []string{"adr", "code-review", "qhealth"}) {
		t.Errorf("show did not compose the skills: %v", got)
	}
	if slotNamed(t, out, "note")["source"].(map[string]any)["kind"] != "inline" {
		t.Errorf("show did not compose the set slot:\n%s", out)
	}
}

// TestInstallDoesNotCompose pins a scope fence that the task's own description
// got wrong, so the decision is recorded where it cannot go stale.
//
// The end-state CLI surface gives --with, --skill and --set to boot and show
// and gives install none of them. The reason is what install renders: the
// layer every session on the machine loads, from install.skills rather than
// from spec.skills, out of the abstract root of a cascade. A composition is an
// instance concern by construction, and there is no instance here to concern.
func TestInstallDoesNotCompose(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	for _, flag := range [][]string{
		{"--with", "base"},
		{"--skill", "adr"},
		{"--prompt", "handoff"},
		{"--set", "note=x"},
	} {
		args := append([]string{"install", "engineer", "--profile", bundle, "--root", t.TempDir()}, flag...)
		if err := run(ctx, args, discard(), discard()); err == nil {
			t.Errorf("cairn install accepted %s", flag[0])
		}
	}
}

// TestAPartDoesNotRevertTheTargetsOverrides is the reproduction as it was
// found: three profiles and two `cairn show` invocations, compared.
//
// Nearly every profile in a portfolio extends one abstract base, so a part will
// almost always share an ancestor with the target it is composed onto. Folding
// that ancestor a second time put it in front of the target and reverted the
// target's own overrides — a name and a model changed by adding a part that
// mentions neither. See profile.ResolveComposition for the rule that stops it.
func TestAPartDoesNotRevertTheTargetsOverrides(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)
	// The part's skill has to exist on disk, because this composition is
	// booted as well as shown.
	mustMkdir(t, filepath.Join(skillsDir, "from-the-part"))
	writeFile(t, filepath.Join(skillsDir, "from-the-part", "SKILL.md"), "# from the part\n", 0o644)

	// templates is here because it is the key that changes what the agent
	// reads. It is an objectByKey, so the ancestor's entry for a destination
	// replaces the leaf's — which against cairn's own examples/bundle flipped
	// AGENTS.md from engineer.md to base.md.
	writeProfile(t, bundle, bundleProfile{ID: "shared", Abstract: true, Provider: "claude",
		Name: "BASE NAME", Model: "base-model",
		Spec: map[string]string{
			"templates":  `{"AGENTS.md": "the shared base's instructions\n"}`,
			"skills_dir": `"` + skillsDir + `"`,
		}})
	writeProfile(t, bundle, bundleProfile{ID: "target", Extends: "shared",
		Name: "TARGET NAME", Model: "target-model",
		Spec: map[string]string{"templates": `{"AGENTS.md": "the target's own instructions\n"}`}})
	writeProfile(t, bundle, bundleProfile{ID: "part", Extends: "shared",
		Description: "what the part adds", Spec: map[string]string{"skills": `["from-the-part"]`}})

	plain := mustShow(t, ctx, bundle, "target")
	composed := mustShow(t, ctx, bundle, "target", "--with", "part")

	for _, field := range []string{"name          TARGET NAME", "model         target-model"} {
		if !strings.Contains(plain, field) {
			t.Fatalf("the fixture does not resolve to %q:\n%s", field, plain)
		}
		if !strings.Contains(composed, field) {
			t.Errorf("composing a part reverted the target's own override of %q:\n%s", field, composed)
		}
	}
	// The part still contributes what it adds. A composition that kept the
	// overrides by dropping the part would pass the assertions above and be
	// useless.
	if !strings.Contains(composed, "description   what the part adds") {
		t.Errorf("the part contributed nothing:\n%s", composed)
	}
	if got := skillsOf(t, composed); !slicesEqual(got, []string{"from-the-part"}) {
		t.Errorf("the composed skills are %v, want the part's", got)
	}
	// And the shared ancestor is folded once, where its own descendant put it.
	if !strings.Contains(composed, "chain         shared -> target -> part") {
		t.Errorf("the shared ancestor was folded twice:\n%s", composed)
	}

	// The keyed collection, which is the half that changes rendered content
	// rather than a reported field.
	templates := valueOf(t, composed, "templates").(map[string]any)
	if got := templates["AGENTS.md"]; got != "the target's own instructions\n" {
		t.Errorf("the composed AGENTS.md template is %q, want the target's own", got)
	}

	// And at the end of the wire, which is where the harm actually lands: the
	// agent is handed instructions from a profile it is not.
	bootRoot := filepath.Join(home, "runtime", "boot")
	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{"boot", "target", "--profile", bundle, "--boot-root", bootRoot,
		"--session", "s1", "--scope", scopeDir, "--with", "part"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}
	if got := read(t, strings.TrimSpace(stdout.String()), "AGENTS.md"); got != "the target's own instructions\n" {
		t.Errorf("the boot directory's AGENTS.md holds %q, want the target's own", got)
	}
}

// TestAPathPartIsNotHeldToItsFileName pins the difference between a part and a
// catalog entry, on the file a generator actually writes.
//
// The catalog is keyed by file name, so a bundled profile disagreeing with
// itself is a real ambiguity. A part has no such key. Holding it to the rule
// anyway would put back the friction --with <path> exists to remove: a
// launcher writing a one-off part to a tempfile would have to name the part
// after that tempfile's random basename.
func TestAPathPartIsNotHeldToItsFileName(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	part := writePart(t, home, "tachyon-session-4f2a9b.md", `---
id: docs-only
name: Docs only
spec:
  skills: ["adr"]
---
`)
	out := mustShow(t, ctx, bundle, "engineer", "--with", part)
	if got := skillsOf(t, out); !slicesEqual(got, []string{"adr", "code-review"}) {
		t.Errorf("the generated part did not compose: %v", got)
	}
	// The path stays the part's identity in the chain, whatever the file called
	// itself: it is what the operator would edit and what nothing else can
	// collide with.
	if !strings.Contains(out, "chain         base -> engineer -> "+part) {
		t.Errorf("the chain does not name the file the part was read from:\n%s", out)
	}
}

// TestAPathPartNamesItselfInEveryDiagnostic covers the three failures a
// path-named part can produce, each of which has to name the file rather than
// something the bundle does not hold.
func TestAPathPartNamesItselfInEveryDiagnostic(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	// A merge that fails. engineer declares spec.slots, so the part's own
	// slots reach a merger; a member with no name is one the keyed collection
	// cannot compose. The refusal comes out of package profile, which reports
	// it off the Profile's own ID — so this is what the ID overwrite buys, and
	// nothing else asserts it.
	t.Run("a merge that fails names the file", func(t *testing.T) {
		part := writePart(t, home, "unnameable.md",
			"---\nid: docs-only\nspec:\n  slots: [{source: {kind: inline, inline: {content: x}}}]\n---\n")
		err := runShow(ctx, []string{"engineer", "--with", part, "--profile", bundle}, discard(), discard())
		if err == nil {
			t.Fatal("a part whose slots cannot be composed was accepted")
		}
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the refusal does not name the file it came out of: %v", err)
		}
		// And specifically not the id the file happened to declare, which is
		// also the id of nothing in this bundle.
		if strings.Contains(err.Error(), `profile "docs-only"`) {
			t.Errorf("the refusal blames an id the bundle does not hold: %v", err)
		}
	})

	// A variable that is not set. Expansion happens before the open, so
	// without the guard this degrades to `open : no such file or directory` —
	// a diagnostic about a path nobody typed, which is the loss
	// profile.QuotedExpansion exists to prevent.
	t.Run("an expansion that came out empty names what was written", func(t *testing.T) {
		t.Setenv("CAIRN_T17_UNSET", "")
		err := runShow(ctx, []string{"engineer", "--with", "$CAIRN_T17_UNSET", "--profile", bundle},
			discard(), discard())
		if err == nil {
			t.Fatal("a --with whose variable is unset was accepted")
		}
		if !strings.Contains(err.Error(), `--with "$CAIRN_T17_UNSET"`) {
			t.Errorf("the refusal does not quote what was written: %v", err)
		}
		if !strings.Contains(err.Error(), "not set") {
			t.Errorf("the refusal does not say why there is no file: %v", err)
		}
	})
}

// TestAFlagsContributionIsNotRespelled pins that the two flags encode with HTML
// escaping off, which is what every other encoder in cairn does and what most
// of this repo's prose about carrying the operator's own text rests on.
//
// A --set value is prose an operator typed. Marshalling it with escaping on
// would turn "<" into "<" inside a slot's content, and the difference
// reaches the rendered section.
func TestAFlagsContributionIsNotRespelled(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	// The raw text as printed, not the decoded value: decoding would undo the
	// escaping this is asserting the absence of.
	out := mustShow(t, ctx, bundle, "base2", "--set", "note=read <docs> & nothing else")
	raw := rawValueOf(t, out, "slots")
	if !strings.Contains(raw, "read <docs> & nothing else") {
		t.Errorf("the value was re-spelled on its way into the manifest:\n%s", raw)
	}
	for _, escape := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(raw, escape) {
			t.Errorf("the value carries the HTML escape %s:\n%s", escape, raw)
		}
	}
}

// TestComposingDoesNotRespellAKeyOnlyOneProfileDeclares pins the promise
// specNote makes, under composition.
//
// `cairn show` tells the reader that one name beside a key means the value
// below is that profile's own declaration, converted from YAML and otherwise
// untouched, and profile.Resolve promises the same thing as byte identity. A
// fold that reached a shared ancestor twice broke both at once: the value went
// through a merger and came back re-encoded — Go's encoder sorts an object's
// keys — while the column still showed one name, so the document asserted
// something about bytes that were no longer there.
//
// It is the same defect from the reader's side that the dedupe fixes from the
// fold's, which is why it is pinned separately.
func TestComposingDoesNotRespellAKeyOnlyOneProfileDeclares(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	// Keys written out of alphabetical order, so a re-encode is visible: the
	// encoder sorts, and the operator did not.
	writeProfile(t, bundle, bundleProfile{ID: "root", Abstract: true, Provider: "claude",
		Spec: map[string]string{"settings": `{"zebra":1,"alpha":2}`}})
	writeProfile(t, bundle, bundleProfile{ID: "leaf", Extends: "root", Name: "Leaf"})
	writeProfile(t, bundle, bundleProfile{ID: "sibling", Extends: "root", Description: "a part"})

	plain := rawValueOf(t, mustShow(t, ctx, bundle, "leaf"), "settings")
	composed := mustShow(t, ctx, bundle, "leaf", "--with", "sibling")

	if got := rawValueOf(t, composed, "settings"); got != plain {
		t.Errorf("composing re-spelled a document only root declares:\n%s\nwant:\n%s", got, plain)
	}
	// And the column still says one profile, which is now true rather than
	// true-looking.
	if got, want := declarersOfOrFail(t, composed, "settings"), "root"; got != want {
		t.Errorf("spec.settings is declared by %q, want %q", got, want)
	}
}

// TestAFlagsContributionIsSpelledLikeAManifestValue covers what composedJSON
// produces, on the path where nothing else touches its bytes.
//
// A flag's contribution reaches a merger only when the profile declares the key
// too; when it does not, [profile.Merge] passes the bytes through and what the
// flag encoded is what `cairn show` lays out. So the encoder's two decisions
// are both load-bearing on exactly this path, and both are asserted here — the
// escaping, and the trailing newline the encoder adds and composedJSON removes.
//
// The newline is the one that looks cosmetic. It is not: a raw value ending in
// "\n" survives json.Indent, so the document grows a blank line the layout
// never put there, in the one command this change makes a fidelity claim about.
func TestAFlagsContributionIsSpelledLikeAManifestValue(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	// base2 declares no skills of its own, so --skill's bytes pass through
	// untouched rather than being re-encoded by a merge.
	out := mustShow(t, ctx, bundle, "base2", "--skill", "adr")

	// Every manifest key is one blank line from the one before it. Two means a
	// value carried a newline into a document that lays its own out.
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("the document carries a blank line the layout did not put there:\n%q", out)
	}
	// Read positively too, so the assertion above cannot pass by the block
	// having moved somewhere else entirely. The label's column width is the
	// document's business, so the lines under it are what is asserted.
	lines := strings.Split(out, "\n")
	at := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "spec.skills ") {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("spec.skills was not printed:\n%s", out)
	}
	want := []string{"  [", `    "adr"`, "  ]", "", "spec."}
	for n, w := range want {
		got := lines[at+1+n]
		if n == len(want)-1 {
			if !strings.HasPrefix(got, w) {
				t.Errorf("the line after the skills block is %q, want the next key", got)
			}
			continue
		}
		if got != w {
			t.Errorf("line %d of the skills block is %q, want %q", n+1, got, w)
		}
	}
}

// TestAWithValueIsTakenAsWritten covers the two things partList.Set does to a
// value before anything else sees it.
func TestAWithValueIsTakenAsWritten(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)
	writeProfile(t, bundle, bundleProfile{ID: "docs-only", Name: "Docs only",
		Spec: map[string]string{"skills": `["docs-review"]`}})

	// Surrounding whitespace is the shell's or the launcher's, not the
	// operator's, so it is removed before the value decides anything. Without
	// this a quoted argument fails the detection rule and then fails to be
	// found, naming a profile with spaces in it that nobody could have written.
	t.Run("surrounding whitespace is not part of the name", func(t *testing.T) {
		out := mustShow(t, ctx, bundle, "engineer", "--with", "  docs-only  ")
		if !strings.Contains(out, "chain         base -> engineer -> docs-only") {
			t.Errorf("a padded --with did not resolve:\n%s", out)
		}
	})

	// An empty value is refused where it was written, rather than becoming a
	// lookup for a profile named "". The flag says what is wrong with the
	// flag; "no profile named \"\"" describes a search nobody asked for.
	t.Run("an empty value is refused as an empty flag", func(t *testing.T) {
		err := runShow(ctx, []string{"engineer", "--with", "", "--profile", bundle}, discard(), discard())
		if err == nil {
			t.Fatal("an empty --with was accepted")
		}
		if !strings.Contains(err.Error(), "names no part") {
			t.Errorf("the refusal is %q, want it to name the empty flag", err)
		}
	})
}

// TestAWithThatContributedNothingSaysSo covers the one silent no-op the
// composition flags could otherwise have.
//
// A part the resolution has already reached is folded once where it first
// landed — Chrispian's ruling, and the thing that stops a composition reverting
// the target's overrides. The consequence is that an operator can type a flag
// that changes nothing. Cairn reports every other marker that stood for
// nothing, and an explicit flag deserves the same line.
//
// It is a report and not a refusal: the exit status stays 0 and stdout is
// untouched, because naming a part the resolution already covers is a
// legitimate way to make a composition explicit about what it rests on.
func TestAWithThatContributedNothingSaysSo(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	// Two parts sharing an ancestor the target does not have, for the shape an
	// operator is least likely to predict.
	writeProfile(t, bundle, bundleProfile{ID: "common", Abstract: true, Name: "Common",
		Spec: map[string]string{"skills": `["from-common"]`}})
	writeProfile(t, bundle, bundleProfile{ID: "carrier", Extends: "common", Name: "Carrier"})
	writeProfile(t, bundle, bundleProfile{ID: "adds", Name: "Adds",
		Spec: map[string]string{"skills": `["from-adds"]`}})

	show := func(t *testing.T, args ...string) (string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		full := append([]string{"show", "engineer"}, args...)
		if err := run(ctx, append(full, "--profile", bundle), &stdout, &stderr); err != nil {
			t.Fatalf("show %v: %v\nstderr: %s", args, err, stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	// The line is asserted as the WHOLE of stderr, not as a substring of it.
	// A substring match passes on a command that also printed three other
	// things, and — more to the point here — the silence assertions below are
	// only meaningful if this one pins the exact bytes.
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{{
		name: "a profile the target extends",
		args: []string{"--with", "base"},
		want: "cairn: --with base: already in the chain, contributed nothing\n",
	}, {
		name: "the target itself",
		args: []string{"--with", "engineer"},
		want: "cairn: --with engineer: already in the chain, contributed nothing\n",
	}, {
		name: "an ancestor an earlier part already brought",
		args: []string{"--with", "carrier", "--with", "common"},
		want: "cairn: --with common: already in the chain, contributed nothing\n",
	}, {
		name: "the same part twice",
		args: []string{"--with", "adds", "--with", "adds"},
		want: "cairn: --with adds: already in the chain, contributed nothing\n",
	}, {
		name: "one line per part, in the order they were given",
		args: []string{"--with", "base", "--with", "adds", "--with", "engineer"},
		want: "cairn: --with base: already in the chain, contributed nothing\n" +
			"cairn: --with engineer: already in the chain, contributed nothing\n",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut := show(t, tc.args...)
			if errOut != tc.want {
				t.Errorf("stderr is\n%q\nwant\n%q", errOut, tc.want)
			}
			// The document is unaffected: this is a fact about the
			// resolution, not a change to it.
			if !strings.Contains(out, "profile       engineer") {
				t.Errorf("the document did not print:\n%s", out)
			}
		})
	}

	// The half that keeps the line worth reading. A diagnostic that fires on
	// the ordinary case is noise, and noise is read past.
	t.Run("a part that contributed stays quiet", func(t *testing.T) {
		for _, args := range [][]string{
			{"--with", "adds"},
			{"--with", "carrier"},
			{"--with", "carrier", "--with", "adds"},
			{"--skill", "qhealth", "--set", "note=x"},
		} {
			if _, errOut := show(t, args...); errOut != "" {
				t.Errorf("show %v wrote to stderr with nothing to report:\n%s", args, errOut)
			}
		}
	})

	// A path part names what the operator wrote, not the expansion it was
	// loaded under — the id and the written form differ there, and a
	// diagnostic quotes what was typed.
	t.Run("a path part is named as it was written", func(t *testing.T) {
		part := writePart(t, home, "twice.md", "---\nspec:\n  skills: [\"x\"]\n---\n")
		t.Setenv("CAIRN_T17_PART", part)
		_, errOut := show(t, "--with", "$CAIRN_T17_PART", "--with", "$CAIRN_T17_PART")
		want := "cairn: --with $CAIRN_T17_PART: already in the chain, contributed nothing\n"
		if errOut != want {
			t.Errorf("stderr is\n%q\nwant\n%q", errOut, want)
		}
	})

	// And it is a report, so a boot still writes its directory and exits 0.
	t.Run("a boot still succeeds", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := run(ctx, []string{"boot", "engineer", "--profile", bundle,
			"--boot-root", filepath.Join(home, "runtime"), "--session", "s1", "--with", "base"},
			&stdout, &stderr)
		if err != nil {
			t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "cairn: --with base: already in the chain, contributed nothing\n") {
			t.Errorf("boot did not report the part that contributed nothing:\n%s", stderr.String())
		}
		if _, err := os.Stat(filepath.Join(strings.TrimSpace(stdout.String()), "AGENTS.md")); err != nil {
			t.Errorf("the boot directory was not written: %v", err)
		}
	})
}

// TestANestedProfileIsAnOrdinaryProfile covers profiles/parts/ from the
// commands' side.
//
// The catalog's own tests assert that the file is read into the flat id map.
// What is asserted here is the consequence that made it worth doing: every
// surface a root profile has, a nested one has, reached by the same bare id —
// so a bundle can organize its small reusable profiles into a directory
// without any of them becoming a second kind of thing that each command would
// have to learn.
func TestANestedProfileIsAnOrdinaryProfile(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	// The part's own skill, on disk: --save-as boots before it saves, and a
	// boot renders spec.skills into the directory rather than taking the
	// profile's word for it.
	mustMkdir(t, filepath.Join(skillsDir, "docs-review"))
	writeFile(t, filepath.Join(skillsDir, "docs-review", "SKILL.md"), "# docs review\n", 0o644)

	// The one difference from the fixture beside it in TestComposeWith: the
	// file goes one directory down. Everything below names it "docs-only".
	writeNestedProfile(t, bundle, bundleProfile{
		ID:      "docs-only",
		Extends: "base",
		Name:    "Docs only",
		Spec:    map[string]string{"skills": `["docs-review"]`},
	})

	t.Run("it is shown and composed by its bare id", func(t *testing.T) {
		// Directly: a part is bootable and inspectable on its own, which is
		// the property that makes "there is no fragment kind" true rather
		// than merely claimed.
		out := mustShow(t, ctx, bundle, "docs-only")
		if !strings.Contains(out, "chain         base -> docs-only") {
			t.Errorf("a nested profile does not resolve its own chain:\n%s", out)
		}
		// And as a part, where it lands in the chain after the target's and
		// names itself in the contributor column.
		out = mustShow(t, ctx, bundle, "engineer", "--with", "docs-only")
		if !strings.Contains(out, "chain         base -> engineer -> docs-only") {
			t.Errorf("a nested profile does not compose as a part:\n%s", out)
		}
		if got, want := declarersOfOrFail(t, out, "skills"), "engineer, docs-only"; got != want {
			t.Errorf("spec.skills is declared by %q, want %q", got, want)
		}
		if got := skillsOf(t, out); !slicesEqual(got, []string{"code-review", "docs-review"}) {
			t.Errorf("the composed skills are %v, want the profile's and the part's", got)
		}
	})

	t.Run("a binding names it, and boots it", func(t *testing.T) {
		// Both positions, because they are checked in different places: the
		// profile key is verified when the bundle is read, and a part is
		// resolved during the composition.
		writeBinding(t, bundle, "docs-direct", "docs-only", scopeDir)
		out := mustShow(t, ctx, bundle, "docs-direct")
		if !strings.Contains(out, "profile       docs-only") {
			t.Errorf("a binding does not boot a nested profile:\n%s", out)
		}

		writeFile(t, filepath.Join(bundle, "bindings", "docs.yaml"),
			"profile: engineer\nparts:\n  - docs-only\n", 0o644)
		out = mustShow(t, ctx, bundle, "docs")
		if !strings.Contains(out, "chain         base -> engineer -> docs-only") {
			t.Errorf("a binding's part did not resolve to the nested profile:\n%s", out)
		}
	})

	t.Run("--save-as records it by id, and the binding replays", func(t *testing.T) {
		// The rule --save-as enforces is that a composition holding a PATH is
		// refused, because a binding must be reproducible by name. A nested
		// profile is the case that rule was leaving stranded: it is catalogued
		// content that used to have no spelling but a path.
		t.Setenv("CAIRN_BOOT_ROOT", t.TempDir())
		var stdout, stderr bytes.Buffer
		err := run(ctx, []string{
			"boot", "engineer", "--profile", bundle, "--scope", scopeDir,
			"--with", "docs-only", "--save-as", "saved",
		}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("boot --save-as: %v\nstderr: %s", err, stderr.String())
		}
		saved, readErr := os.ReadFile(filepath.Join(bundle, "bindings", "saved.yaml"))
		if readErr != nil {
			t.Fatalf("read the saved binding: %v", readErr)
		}
		got := string(saved)
		if !strings.Contains(got, "parts:\n    - docs-only\n") &&
			!strings.Contains(got, "parts:\n  - docs-only\n") {
			t.Errorf("the saved binding does not name the part by id:\n%s", got)
		}
		// The one spelling that must NOT appear: a path to the file. That is
		// what --save-as refuses, and a nested part reached by id is how the
		// same content is saved instead.
		if strings.Contains(got, catalog.PartsDir+"/") {
			t.Errorf("the saved binding recorded a path:\n%s", got)
		}
		if out := mustShow(t, ctx, bundle, "saved"); !strings.Contains(out, "-> docs-only") {
			t.Errorf("the saved binding did not replay the part:\n%s", out)
		}
	})

	t.Run("the directory is not part of the name, and cairn says so", func(t *testing.T) {
		// The mistake profiles/parts/ creates. "parts/docs-only" holds a
		// separator, so the detection rule reads it as a path — correctly, and
		// unhelpfully, because the operator is looking straight at the file.
		var stdout, stderr bytes.Buffer
		err := run(ctx, []string{
			"show", "engineer", "--profile", bundle, "--with", catalog.PartsDir + "/docs-only",
		}, &stdout, &stderr)
		if err == nil {
			t.Fatal("a part named by its directory was accepted")
		}
		for _, want := range []string{"docs-only", "named by its id"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not carry %q: %v", want, err)
			}
		}
	})

	t.Run("the hint is offered only when there is a profile to name", func(t *testing.T) {
		// A guess that named a profile nobody declared would send the operator
		// looking for the wrong problem, so a path that resolves to no catalog
		// id gets the plain refusal.
		var stdout, stderr bytes.Buffer
		err := run(ctx, []string{
			"show", "engineer", "--profile", bundle, "--with", catalog.PartsDir + "/nobody",
		}, &stdout, &stderr)
		if err == nil {
			t.Fatal("a part naming nothing was accepted")
		}
		if strings.Contains(err.Error(), "named by its id") {
			t.Errorf("a path naming no profile was given the bare-id hint: %v", err)
		}
	})
}

// TestPromptFlagUsageMatchesTheLayout holds the one place a layout's prompt
// namespace is written out rather than read.
//
// A flag's help is one string and there are as many namespaces as there are
// trees, so --prompt spells Claude Code's. That is a duplicate of the tree, and
// this is what keeps it from becoming a stale one: a tree that moved its
// prompts directory fails here rather than telling an operator to type a
// command that is not there.
func TestPromptFlagUsageMatchesTheLayout(t *testing.T) {
	layout, err := bootdir.LayoutFor(profile.ProviderClaude)
	if err != nil {
		t.Fatalf("LayoutFor(%q): %v", profile.ProviderClaude, err)
	}
	if want := "/" + layout.PromptNamespace + ":<name>"; !strings.Contains(promptFlagUsage, want) {
		t.Errorf("--prompt's help does not say %q:\n%s", want, promptFlagUsage)
	}
}
