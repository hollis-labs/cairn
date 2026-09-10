package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/template"
)

// bodyBundle writes a minimal bundle whose profiles carry bodies rather than
// spec.templates, plus one layout, and returns the bundle directory.
//
// It declares no template for the instruction artifact at all, which is the
// point: under the new engine the profile's body IS that document, and a
// profile that declared both would be refused — see
// TestBootRefusesTwoInstructionDocuments.
func bodyBundle(t *testing.T, home string, base, leaf string) string {
	t.Helper()
	bundle := filepath.Join(home, "bundle")
	mustMkdir(t, filepath.Join(bundle, "templates", "layouts"))
	writeFile(t, filepath.Join(bundle, "templates", "layouts", "agent.md"),
		"# {{ value profile }}\n\n{{ yield charter }}\n\n{{ yield direction }}\n\n{{ yield context }}\n", 0o644)
	writeProfile(t, bundle, bundleProfile{
		ID: "base", Abstract: true, Name: "Base", Provider: "claude", Body: base,
		Spec: map[string]string{"templates": `{"CLAUDE.md": "@AGENTS.md\n"}`},
	})
	writeProfile(t, bundle, bundleProfile{
		ID: "worker", Extends: "base", Name: "Worker", Body: leaf,
	})
	return bundle
}

// TestBootRendersTheProfileBodyAndNothingElseCarriesIt is the regression test
// for a defect this design closed: a profile's body was folded through the
// cascade and rendered by nothing at all, in either layer, while `cairn show`
// claimed `cairn boot` rendered it.
//
// It boots a profile whose whole document is its body and asserts the document
// is on disk. It also asserts where it is NOT: a body is the instruction
// artifact and is not prose cairn sprinkles through the tree.
func TestBootRendersTheProfileBodyAndNothingElseCarriesIt(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := bodyBundle(t, home,
		"{{ extends agent }}\n\n{{ section charter }}The floor every profile stands on.{{ end }}\n",
		"{{ extends agent }}\n\n{{ section charter }}You do the work.{{ end }}\n")

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "worker", "--profile", bundle,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}
	dir := strings.TrimSpace(stdout.String())

	agents := read(t, dir, "AGENTS.md")
	if !strings.Contains(agents, "You do the work.") {
		t.Errorf("AGENTS.md does not carry the profile's body:\n%s", agents)
	}
	// The leaf's section replaced the ancestor's at that name, which is the
	// cascade's only rule applied to a section.
	if strings.Contains(agents, "The floor every profile stands on.") {
		t.Errorf("AGENTS.md carries the ancestor's section, which the leaf replaced:\n%s", agents)
	}
	// The layout supplied the shape, so a value the layout names is filled.
	if !strings.Contains(agents, "# worker") {
		t.Errorf("AGENTS.md did not render the layout's value tag:\n%s", agents)
	}
	// Two holes the chain declared nothing for took their blank lines with
	// them: empty collapses, so the document has no run of blank lines.
	if strings.Contains(agents, "\n\n\n") {
		t.Errorf("AGENTS.md carries the gap an unfilled hole left:\n%q", agents)
	}
	// The pointer is still a template, and it still points at a document that
	// is now rendered from somewhere else.
	if pointer := read(t, dir, "CLAUDE.md"); !strings.Contains(pointer, "@AGENTS.md") {
		t.Errorf("CLAUDE.md = %q", pointer)
	}
}

// TestBootRendersInlineSourcesInTheBody pins the hydrate/render seam end to
// end: a tag carries its own source, the source runs at boot, and its output
// is in the document.
func TestBootRendersInlineSourcesInTheBody(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	note := filepath.Join(home, "note.md")
	writeFile(t, note, "the file source resolved\n", 0o644)
	bundle := bodyBundle(t, home, "",
		"{{ extends agent }}\n\n"+
			"{{ section charter }}You do the work.{{ end }}\n\n"+
			"{{ section context }}{{ cmd: echo the cmd source resolved }}\n\n{{ file: "+note+" }}{{ end }}\n")

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "worker", "--profile", bundle,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}
	agents := read(t, strings.TrimSpace(stdout.String()), "AGENTS.md")
	for _, want := range []string{"the cmd source resolved", "the file source resolved"} {
		if !strings.Contains(agents, want) {
			t.Errorf("AGENTS.md does not carry %q:\n%s", want, agents)
		}
	}
}

// A source that fails leaves no section and is reported, which is a slot's
// answer rather than a file's: the boot still happens.
func TestBootReportsAnInlineSourceThatFailed(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := bodyBundle(t, home, "",
		"{{ extends agent }}\n\n{{ section charter }}You do the work.{{ end }}\n\n"+
			"{{ section context }}## Repository\n\n{{ file: "+filepath.Join(home, "never-written.md")+" }}{{ end }}\n")

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "worker", "--profile", bundle,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "did not resolve") {
		t.Errorf("stderr does not report the failed source:\n%s", stderr.String())
	}
	// The heading is the author's and stays: a source that produced nothing
	// takes its own tag and not the prose written around it.
	agents := read(t, strings.TrimSpace(stdout.String()), "AGENTS.md")
	if !strings.Contains(agents, "You do the work.") {
		t.Errorf("the boot lost the rest of the document over one failed source:\n%s", agents)
	}
}

// TestBootRefusesTwoInstructionDocuments pins the refusal rather than a
// precedence. A rule like "the body wins" would leave the losing document in
// the profile, edited and committed and never taking effect, in the one file
// that tells an agent what it is.
func TestBootRefusesTwoInstructionDocuments(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	writeProfile(t, bundle, bundleProfile{
		ID: "both", Name: "Both", Provider: "claude",
		Body: "the body's document\n",
		Spec: map[string]string{"templates": `{"AGENTS.md": "the template's document\n"}`},
	})

	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{
		"boot", "both", "--profile", bundle,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr)
	if !errors.Is(err, bootdir.ErrInstructionArtifact) {
		t.Fatalf("boot = %v, want ErrInstructionArtifact", err)
	}
	// The diagnostic names both, because the fix is to remove one and the
	// reader has to know which two.
	for _, want := range []string{"body", "AGENTS.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic does not name %q: %v", want, err)
		}
	}
	nothingUnder(t, filepath.Join(home, "runtime"))
}

// A layout the bundle does not hold names the file cairn looked for and what
// the bundle does hold, which is what an author who mistyped a name needs.
func TestBootReportsAMissingLayout(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := bodyBundle(t, home, "", "{{ extends agnet }}\n{{ section charter }}x{{ end }}\n")

	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{
		"boot", "worker", "--profile", bundle,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr)
	if !errors.Is(err, template.ErrLayout) {
		t.Fatalf("boot = %v, want ErrLayout", err)
	}
	for _, want := range []string{"agnet", `"agent"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic does not name %q: %v", want, err)
		}
	}
}

// The installed layer renders the same body through the same function, so an
// operator cannot install one instruction document and boot another. Its
// inline sources are restricted to the deterministic kinds, for the reason its
// slots are: a check re-renders and diffs against disk.
func TestInstallRendersTheProfileBody(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	root := filepath.Join(home, "root")
	mustMkdir(t, root)
	bundle := bodyBundle(t, home,
		"{{ extends agent }}\n\n{{ section charter }}The installed floor.{{ end }}\n\n"+
			"{{ section context }}{{ cmd: echo this must not run here }}{{ end }}\n", "")

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{"install", "base", "--profile", bundle, "--root", root},
		&stdout, &stderr); err != nil {
		t.Fatalf("install: %v\nstderr: %s", err, stderr.String())
	}
	agents := read(t, root, filepath.Join(".claude", "AGENTS.md"))
	if !strings.Contains(agents, "The installed floor.") {
		t.Errorf(".claude/AGENTS.md does not carry the body:\n%s", agents)
	}
	if strings.Contains(agents, "this must not run here") {
		t.Errorf("the installed layer ran a cmd source:\n%s", agents)
	}
	if !strings.Contains(stderr.String(), "resolves no inline source") {
		t.Errorf("stderr does not say which sources this layer skipped:\n%s", stderr.String())
	}
	// A check against the layer just written reports no drift, which is the
	// property the restriction exists for.
	stdout.Reset()
	stderr.Reset()
	if err := run(ctx, []string{"install", "base", "--profile", bundle, "--root", root, "--check"},
		&stdout, &stderr); err != nil {
		t.Fatalf("install --check straight after an install reports drift: %v\n%s", err, stdout.String())
	}
}

// The example bundle is cairn's public shape, so its new-engine profile is
// booted here rather than only described.
func TestBootTheExampleBundlesTemplateProfile(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := exampleBundle(t, home)
	scopeDir := filepath.Join(home, "scope")
	mustMkdir(t, scopeDir)

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "documentarian", "--profile", bundle, "--scope", scopeDir,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot documentarian: %v\nstderr: %s", err, stderr.String())
	}
	agents := read(t, strings.TrimSpace(stdout.String()), "AGENTS.md")
	for _, want := range []string{
		"documentarian",          // the layout's {{ value profile }}
		"You write the document", // its charter section
		"The tree, as it stands", // its context section
		scopeDir,                 // the layout's {{ value scope }}
	} {
		if !strings.Contains(agents, want) {
			t.Errorf("the rendered document does not carry %q:\n%s", want, agents)
		}
	}
	// A hole the profile filled nothing for is gone, and so is its gap.
	if strings.Contains(agents, "\n\n\n") {
		t.Errorf("the rendered document carries an unfilled hole's gap:\n%q", agents)
	}
}

// Composing a fragment onto a new-engine profile fills the layout's remaining
// hole, which is the whole composition story in one boot.
func TestBootTheExampleBundleComposesAFragmentIntoTheLayout(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := exampleBundle(t, home)
	scopeDir := filepath.Join(home, "scope")
	mustMkdir(t, scopeDir)

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "documentarian", "--with", "docs-only", "--profile", bundle, "--scope", scopeDir,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot documentarian --with docs-only: %v\nstderr: %s", err, stderr.String())
	}
	agents := read(t, strings.TrimSpace(stdout.String()), "AGENTS.md")
	if !strings.Contains(agents, "Write user-facing documentation only") {
		t.Errorf("the part's section did not reach the layout's hole:\n%s", agents)
	}
	// The part contributed a section and never the shape: the profile's own
	// charter is still there.
	if !strings.Contains(agents, "You write the document") {
		t.Errorf("the part took over the document's shape:\n%s", agents)
	}
}

// TestBootDropsContentTheTreeCannotPlantAndSaysSo is the warn-and-drop at the
// command, over the tree that actually has nowhere for either collection.
//
// The refusal it replaces made every Codex boot of every agent-setup profile
// fail, because one inherited `prompts:` in base reached all of them, and the
// only way to clear it was a per-provider null in the catalog — provider
// knowledge, in the component that owns none of it. See bootdir.Undeclared.
func TestBootDropsContentTheTreeCannotPlantAndSaysSo(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	scopeDir := filepath.Join(home, "scope")
	mustMkdir(t, scopeDir)

	prompts := filepath.Join(home, "prompts")
	mustMkdir(t, prompts)
	writeFile(t, filepath.Join(prompts, "report.md"), "# report\n", 0o644)

	writeProfile(t, bundle, bundleProfile{
		ID: "helper", Name: "Helper", Provider: "claude",
		Spec: map[string]string{"subagent": `{"description": "reviews a diff"}`},
	})
	writeProfile(t, bundle, bundleProfile{
		ID: "worker", Name: "Worker", Provider: "claude",
		Spec: map[string]string{
			"prompts":     `["report"]`,
			"prompts_dir": `"` + prompts + `"`,
			"subagents":   `["helper"]`,
			"templates":   `{"AGENTS.md": "# worker\n"}`,
		},
	})

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "worker", "--profile", bundle, "--provider", "codex", "--scope", scopeDir,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot into the codex tree: %v\nstderr: %s", err, stderr.String())
	}

	// The boot happened, and the rest of it is there. That is the change: the
	// refusal planted nothing at all.
	dir := strings.TrimSpace(stdout.String())
	if got := read(t, dir, "AGENTS.md"); !strings.Contains(got, "# worker") {
		t.Errorf("the instruction document is missing:\n%s", got)
	}

	report := stderr.String()
	// Both collections, because a profile that declares both should hear about
	// both rather than about whichever is checked first.
	for _, want := range []string{"prompts", "report", "subagents", "helper", "codex"} {
		if !strings.Contains(report, want) {
			t.Errorf("stderr does not name %q:\n%s", want, report)
		}
	}
	// And it says the boot is fine, which a refusal never had to. Without this
	// the line reads like a failure that did not fail, and the operator goes
	// looking for a directory that is sitting there complete.
	if !strings.Contains(report, "the boot directory is complete") {
		t.Errorf("stderr does not say the boot is otherwise complete:\n%s", report)
	}
}
