package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/profile"
	"github.com/chrispian/cairn/scope"
	"github.com/chrispian/cairn/slots"
	goprovider "github.com/hollis-labs/go-providers/provider"
)

// TestBootEndToEnd is the MVP gate: cairn boot writes a directory that can be
// opened and read. Every assertion below reads what was actually written,
// because a contract asserted against the renderer that produced it is not
// asserted at all.
func TestBootEndToEnd(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "runtime", "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")

	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{
		"boot", "engineer",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", "s1",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}

	dir := strings.TrimSpace(stdout.String())
	want := filepath.Join(bootRoot, "engineer", "s1")
	if dir != want {
		t.Fatalf("boot printed %q, want %q", dir, want)
	}

	// The output contract, read off disk.
	for _, rel := range []string{
		"AGENTS.md",
		"CLAUDE.md",
		"boot.md",
		".mcp.json",
		".claude/settings.json",
		".claude/skills/code-review/SKILL.md",
		".claude/skills/code-review/references/checklist.md",
		".claude/skills/code-review/run.sh",
		"notes/scratch.md",
		"tasks/T-1/task.md",
		".claude/agents/reviewer.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("the boot directory is missing %s: %v", rel, err)
		}
	}

	// The pointer is a template like any other: what it holds is what the
	// profile wrote there, not a line cairn contributed.
	if got := read(t, dir, "CLAUDE.md"); got != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q, want the template's own text", got)
	}

	// Every block is one the profile put there, in the order the profile put
	// them in. Cairn composes no heading, no section, and no order of its own.
	agents := read(t, dir, "AGENTS.md")
	for _, want := range []string{
		"# engineer on claude",       // two value markers on one line
		"base persona",               // ancestor prose the leaf restated in its own template
		"engineer persona",           // and its own, after it
		scopeDir,                     // a value marker mid-line, substituted
		"## Note\nthe standing note", // a slot marker: heading and content together
	} {
		if !strings.Contains(agents, want) {
			t.Errorf("AGENTS.md does not carry %q:\n%s", want, agents)
		}
	}
	// A slot that resolved empty and one that failed both left nothing —
	// heading included, because the heading came back from the slot rather
	// than being written around the marker.
	for _, absent := range []string{"Quiet", "Memory", "cairn:"} {
		if strings.Contains(agents, absent) {
			t.Errorf("AGENTS.md carries %q, which nothing filled:\n%s", absent, agents)
		}
	}

	// The same slot, addressed by name from a second template. Nothing about
	// boot.md is special any more: it is a destination a profile named.
	if got := read(t, dir, "boot.md"); !strings.Contains(got, "the standing note") {
		t.Errorf("boot.md does not carry the slot it names:\n%s", got)
	}

	// A literal files entry is planted as written; a source entry is planted
	// with what it resolved to, at the same kind of path. The second is the
	// torque case — a task bundle rendered from state that is only true now.
	if got := read(t, dir, "notes/scratch.md"); got != "scratch\n" {
		t.Errorf("the literal files entry holds %q, want %q", got, "scratch\n")
	}
	if got, want := read(t, dir, "tasks/T-1/task.md"), "# T-1\nin progress\n"; got != want {
		t.Errorf("the sourced files entry holds %q, want the resolved %q", got, want)
	}

	var mcp struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, dir, ".mcp.json")), &mcp); err != nil {
		t.Fatalf(".mcp.json is not JSON: %v", err)
	}
	if got, ok := mcp.MCPServers["vanta"]; !ok || got.Command != "vanta-mcp" {
		t.Errorf(".mcp.json did not carry the declared server: %+v", mcp)
	}

	// Settings are the operator's own document, not Cairn's opinion of it.
	if got := read(t, dir, ".claude/settings.json"); !strings.Contains(got, `"whateverTheOperatorWrote"`) {
		t.Errorf("settings.json did not carry the operator's own value:\n%s", got)
	}

	// A subagent definition is the named profile's own declaration, and only
	// that. The parent neither narrowed nor widened it, and the named
	// profile's persona is not in it — that is what the declaration's own body
	// key is for.
	definition := read(t, dir, ".claude/agents/reviewer.md")
	for _, want := range []string{
		"---\nname: reviewer\n",
		"description: Fresh review with no shared context.",
		"model: sonnet",
		"- Read",
		"You review a diff and report what you found.",
	} {
		if !strings.Contains(definition, want) {
			t.Errorf("the definition does not carry %q:\n%s", want, definition)
		}
	}
	if strings.Contains(definition, "reviewer persona") || strings.Contains(definition, "base persona") {
		t.Errorf("the definition carries a cascaded body:\n%s", definition)
	}

	// A skill's executable bit is load-bearing: a script that arrives without
	// it is a skill that fails halfway through instead of at boot.
	info, err := os.Stat(filepath.Join(dir, ".claude", "skills", "code-review", "run.sh"))
	if err != nil {
		t.Fatalf("stat the planted script: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the planted run.sh lost its executable bit: mode %v", info.Mode().Perm())
	}
}

// TestBootJSONDescribesTheBootForALauncher asserts the contract a launcher
// reads, as a document rather than as fields on a struct: a launcher
// unmarshals bytes, so bytes are what this checks.
//
// It replaces a scrape. The reference launcher used to pull the scope out of
// the rendered AGENTS.md with sed, which made a launcher's access grant depend
// on a marker's position in a document written to be re-authored.
func TestBootJSONDescribesTheBootForALauncher(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "runtime", "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{
		"boot", "engineer",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", "s1",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("boot --json: %v\nstderr: %s", err, stderr.String())
	}

	// One object and nothing else, so `$(cairn boot x --json)` is parseable.
	dec := json.NewDecoder(strings.NewReader(stdout.String()))
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, stdout.String())
	}
	if dec.More() {
		t.Errorf("stdout carries more than the one object:\n%s", stdout.String())
	}

	// The key set is the contract. A key that came and went would make a
	// consumer handle two shapes for one meaning.
	want := []string{"boot_dir", "cwd_preference", "env_amendments", "profile_root", "project_dir_arg", "provider",
		"saved_binding_path", "saved_dropped_sets", "scope", "settings_path"}
	if got := slices.Sorted(maps.Keys(raw)); !slices.Equal(got, want) {
		t.Errorf("the document carries %v, want exactly %v", got, want)
	}
	// The two --save-as keys are emitted on a boot that saved nothing, which
	// is the half of "every key on every boot" a conditional would break.
	for _, key := range []string{"saved_binding_path", "saved_dropped_sets"} {
		if got := string(raw[key]); got != "null" {
			t.Errorf("%s = %s on a boot with no --save-as, want null", key, got)
		}
	}

	var report bootReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal the report: %v", err)
	}
	if got := filepath.Join(bootRoot, "engineer", "s1"); report.BootDir != got {
		t.Errorf("boot_dir = %q, want %q", report.BootDir, got)
	}
	if _, err := os.Stat(report.BootDir); err != nil {
		t.Errorf("boot_dir names a directory that is not there: %v", err)
	}
	if report.Provider != profile.ProviderClaude.String() {
		t.Errorf("provider = %q, want %q", report.Provider, profile.ProviderClaude)
	}
	// The bundle this boot was composed out of, which the boot directory does
	// not record anywhere. Without it a harness re-running cairn from inside
	// that directory falls back to ~/.config/agents and reads a different
	// bundle, silently and by the rules.
	if report.ProfileRoot != bundle {
		t.Errorf("profile_root = %q, want %q", report.ProfileRoot, bundle)
	}
	// The scope as the boot resolved it, symlinks and all, and not the raw
	// value the binding declared — a granted directory is matched against by
	// the harness, so the spelling matters.
	wantScope, err := scope.Parse(scopeDir, home)
	if err != nil {
		t.Fatalf("resolve the fixture scope: %v", err)
	}
	if report.Scope == nil || *report.Scope != wantScope {
		t.Errorf("scope = %v, want %q", deref(report.Scope), wantScope)
	}
	// The path a launcher passes to --settings, absolute and pointing at a
	// file that is really there. Naming one that is not is the failure this
	// key exists to remove.
	if report.SettingsPath == nil {
		t.Fatalf("settings_path is null, but the profile declares settings")
	}
	if got := filepath.Join(report.BootDir, ".claude", "settings.json"); *report.SettingsPath != got {
		t.Errorf("settings_path = %q, want %q", *report.SettingsPath, got)
	}
	if _, err := os.Stat(*report.SettingsPath); err != nil {
		t.Errorf("settings_path names a file that is not there: %v", err)
	}
	if report.CwdPreference != "boot_dir" {
		t.Errorf("cwd_preference = %q, want %q", report.CwdPreference, "boot_dir")
	}
	// The provider's own flag, split into argv here and left un-substituted.
	// Both halves matter and they fail differently: a consumer handed one
	// string has to substitute and split itself, and doing those in the wrong
	// order turns a scope with a space in it into two arguments — silently,
	// and only on the input nobody tests.
	layout, err := bootdir.LayoutFor(profile.ProviderClaude)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	if got := strings.Fields(layout.ProjectDirArg); !slices.Equal(report.ProjectDirArg, got) {
		t.Errorf("project_dir_arg = %v, want the layout's own pattern split into %v", report.ProjectDirArg, got)
	}
	if !slices.Contains(report.ProjectDirArg, projectDirPlaceholder) {
		t.Errorf("project_dir_arg = %v, want it to still carry %s", report.ProjectDirArg, projectDirPlaceholder)
	}
	for _, tok := range report.ProjectDirArg {
		// The property the split buys: no element needs quoting, so no
		// consumer can lose one to word splitting.
		if strings.ContainsAny(tok, " \t\n") {
			t.Errorf("project_dir_arg carries a token with whitespace in it: %q", tok)
		}
		if strings.Contains(tok, wantScope) {
			t.Errorf("project_dir_arg was substituted: %v", report.ProjectDirArg)
		}
	}
}

// TestBootJSONReportsAFlaglessProviderAsNull covers the third null, which no
// supported provider reaches: LayoutFor admits Claude Code alone and that
// adapter hardcodes a pattern. The rule is still a third of the contract, and
// without this it rests on nullable's shape and nothing else.
func TestBootJSONReportsAFlaglessProviderAsNull(t *testing.T) {
	layout := bootdir.Layout{Provider: profile.ProviderClaude}
	report, err := newBootReport("/boot/x", layout, "", "/bundle", nil, nil)
	if err != nil {
		t.Fatalf("newBootReport: %v", err)
	}
	if report.ProjectDirArg != nil {
		t.Errorf("project_dir_arg = %v, want nil for a provider declaring no flag", report.ProjectDirArg)
	}
	// nil and not [], which would read as "pass these zero arguments" rather
	// than "this harness takes no such flag" — and only the bytes say which.
	out, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"project_dir_arg":null`) {
		t.Errorf("project_dir_arg is not null in the document:\n%s", out)
	}
}

// TestBootJSONSpellsAPathTheOperatorCanRead is the escaping rule this repo
// already settled for the settings fragment, applied to the document that
// describes it.
//
// "&", "<" and ">" are legal in a directory name, and Go's default encoder
// escapes all three. A report spelling a scope in \u0026 while the settings
// file one directory over spells it plainly would be cairn contradicting
// itself about the same directory — and this document is one an operator reads
// at a terminal before any launcher parses it.
func TestBootJSONSpellsAPathTheOperatorCanRead(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "runtime", "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	awkward := filepath.Join(home, "r&d <x>")
	mustMkdir(t, awkward)

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "engineer",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", "s1",
		"--scope", awkward,
		"--json",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot --json: %v\nstderr: %s", err, stderr.String())
	}

	wantScope, err := scope.Parse(awkward, home)
	if err != nil {
		t.Fatalf("resolve the fixture scope: %v", err)
	}
	if !strings.Contains(stdout.String(), wantScope) {
		t.Errorf("the report does not spell the scope as it is on disk:\n%s", stdout.String())
	}
	for _, escape := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(stdout.String(), escape) {
			t.Errorf("the report carries %s:\n%s", escape, stdout.String())
		}
	}
	// The claim is agreement, so read the other document too: the settings
	// file cairn wrote one directory over already has escaping off.
	var report bootReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal the report: %v", err)
	}
	if report.SettingsPath == nil {
		t.Fatal("settings_path is null, but a scope was granted")
	}
	granted, err := os.ReadFile(*report.SettingsPath)
	if err != nil {
		t.Fatalf("read the settings document: %v", err)
	}
	if !strings.Contains(string(granted), wantScope) {
		t.Errorf("the two documents disagree about the same directory:\nreport: %s\nsettings: %s",
			stdout.String(), granted)
	}
}

// TestBootWithoutJSONPrintsTheBarePath guards the seam --json was added beside
// rather than through. A caller that wants a path keeps getting exactly a path.
func TestBootWithoutJSONPrintsTheBarePath(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "runtime", "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "engineer",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", "s1",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), filepath.Join(bootRoot, "engineer", "s1")+"\n"; got != want {
		t.Errorf("stdout = %q, want the bare path %q", got, want)
	}
}

// TestBootJSONReportsAnAbsentValueAsNull is the rule that keeps a launcher from
// interpolating nothing into argv.
//
// An empty string reaches `claude $FLAG "$VALUE"` as an empty argument and the
// launch is wrong with nothing reporting it; null forces the consumer to
// decide. The seeded profile has no binding, so no scope resolves, and clears
// its ancestor's settings, so with nothing to grant no settings document is
// written at all.
//
// Then the same profile with one directory to grant, which is the case that
// keeps the two nulls from being one fact. They are not equivalent and only
// one implication holds: a scope is itself a granted directory, so a non-null
// scope guarantees a settings document — the invariant that lets a launcher
// pass --settings and drop --add-dir — while a profile granting a directory it
// is not scoped to renders one with no scope at all.
func TestBootJSONReportsAnAbsentValueAsNull(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "runtime", "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "nosettings",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", "s1",
		"--json",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot --json: %v\nstderr: %s", err, stderr.String())
	}

	// Null in the bytes, not merely a zero value after unmarshalling: the
	// distinction this test is about is one only the document can carry.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	for _, key := range []string{"scope", "settings_path"} {
		got, present := raw[key]
		if !present {
			t.Errorf("%s is missing; an absent value is null and the key is always emitted", key)
			continue
		}
		if string(got) != "null" {
			t.Errorf("%s = %s, want null", key, got)
		}
	}

	var report bootReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal the report: %v", err)
	}
	// settings_path is null because the render produced no such file, which is
	// the claim worth reading off disk rather than off the layout.
	if _, err := os.Stat(filepath.Join(report.BootDir, ".claude", "settings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("settings_path is null but a settings document was written: %v", err)
	}

	// The mixed case. Nothing may read the two nulls as one.
	stdout.Reset()
	stderr.Reset()
	if err := run(ctx, []string{
		"boot", "grantsonly",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", "s2",
		"--json",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot grantsonly --json: %v\nstderr: %s", err, stderr.String())
	}
	var mixed bootReport
	if err := json.Unmarshal(stdout.Bytes(), &mixed); err != nil {
		t.Fatalf("unmarshal the report: %v", err)
	}
	if mixed.Scope != nil {
		t.Errorf("scope = %q, want null: the profile has no binding", *mixed.Scope)
	}
	if mixed.SettingsPath == nil {
		t.Fatal("settings_path is null, but spec.access.directories named one to grant")
	}
	if _, err := os.Stat(*mixed.SettingsPath); err != nil {
		t.Errorf("settings_path names a file that is not there: %v", err)
	}
}

// TestCwdPreferenceHasNoFallback covers the branch a supported provider cannot
// reach today. go-providers owns the constants; a third one arriving would
// otherwise be reported as one of the two cairn knows, and a launcher would
// invoke the harness in the wrong directory while the document read as though
// it knew.
func TestCwdPreferenceHasNoFallback(t *testing.T) {
	for _, p := range []goprovider.CwdPreference{goprovider.CwdBootDir, goprovider.CwdProjectDir} {
		name, err := cwdPreferenceName(p)
		if err != nil || name == "" {
			t.Errorf("cwdPreferenceName(%d) = %q, %v", p, name, err)
		}
	}
	if _, err := cwdPreferenceName(goprovider.CwdProjectDir + 1); !errors.Is(err, bootdir.ErrProviderLayout) {
		t.Errorf("an unnamed preference returned %v, want ErrProviderLayout", err)
	}
}

// deref renders a nullable report field for a failure message.
func deref(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}

// TestASlotThatProducedNothingLeavesNoTraceInTheBootFile is docs/plan.md §5
// read off disk rather than off a renderer.
//
// The seeded profile declares three slots: one that resolves, one that
// resolves empty, and one whose file is not there. Only the first reaches
// boot.md — no heading, no marker, and no blank section for the other two. An
// earlier revision wrote "**Unavailable.**" plus the error, which is cairn
// authoring prose into the agent's context; the operator still hears about it,
// on stderr, which is where an operator reads.
func TestASlotThatProducedNothingLeavesNoTraceInTheBootFile(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{
		"boot", "engineer",
		"--profile", bundle,
		"--boot-root", filepath.Join(home, "runtime", "boot"),
		"--session", "s1",
	}, &stdout, &stderr)
	// A slot that failed does not fail the boot. One unreachable endpoint
	// should not stop an operator working.
	if err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}

	boot := read(t, strings.TrimSpace(stdout.String()), "boot.md")
	if want := "## Note\nthe standing note\n"; boot != want {
		t.Errorf("boot.md is\n%q\nwant only the slot that resolved\n%q", boot, want)
	}
	for _, absent := range []string{"## Quiet", "## Memory", "never-written.md", "Unavailable"} {
		if strings.Contains(boot, absent) {
			t.Errorf("boot.md carries %q:\n%s", absent, boot)
		}
	}
	// One trailing newline, whichever slot happened to come last. The two that
	// produced nothing are gone before the file is written, so the last byte is
	// not theirs to decide.
	if !strings.HasSuffix(boot, "\n") || strings.HasSuffix(boot, "\n\n") {
		t.Errorf("boot.md does not end in exactly one newline: %q", boot)
	}

	// The failure is the operator's, and this is where the operator reads it.
	if !strings.Contains(stderr.String(), `slot "memory"`) {
		t.Errorf("stderr does not report the slot that failed:\n%s", stderr.String())
	}
	if strings.Contains(stdout.String(), "memory") {
		t.Errorf("the failure reached stdout, which carries only the path:\n%s", stdout.String())
	}
}

// TestTwoBootsOfOneProfileAreByteIdentical is the determinism contract over
// the whole directory rather than over one renderer.
//
// Slots resolve at materialization and may legitimately differ between two
// runs — that is a property of the resolver, and why agentcontext hashes the
// request rather than the result. These resolvers are fixed, so anything that
// differs here is cairn's own nondeterminism: a map iterated without sorting,
// a clock, an environment read from inside a renderer.
func TestTwoBootsOfOneProfileAreByteIdentical(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "runtime", "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	boot := func(session string) map[string]string {
		var stdout, stderr bytes.Buffer
		if err := run(ctx, []string{
			"boot", "engineer",
			"--profile", bundle,
			"--boot-root", bootRoot,
			"--session", session,
		}, &stdout, &stderr); err != nil {
			t.Fatalf("boot %s: %v\nstderr: %s", session, err, stderr.String())
		}
		return treeOf(t, strings.TrimSpace(stdout.String()))
	}

	first := boot("s1")
	if len(first) == 0 {
		t.Fatal("the first boot wrote nothing")
	}
	for i := range 3 {
		again := boot(fmt.Sprintf("s%d", i+2))
		if !maps.Equal(again, first) {
			for rel, want := range first {
				if got, ok := again[rel]; !ok {
					t.Errorf("boot %d did not write %s", i+2, rel)
				} else if got != want {
					t.Errorf("boot %d wrote a different %s:\n%q\nwant\n%q", i+2, rel, got, want)
				}
			}
			for rel := range again {
				if _, ok := first[rel]; !ok {
					t.Errorf("boot %d wrote %s, which the first did not", i+2, rel)
				}
			}
		}
	}
}

// TestBootRefusesAFileSourceThatDoesNotResolve is the deliberate opposite of
// the slot rule above, and the reason the two are not one mechanism.
//
// A slot that does not resolve leaves a section out of boot.md and the agent
// asks its tools instead. A file that does not resolve leaves a hole at a path
// the profile promised, and whatever reads that path cannot tell "never
// declared" from "the command that fills it fell over". So the boot is refused
// — and refused before anything is written, because half a boot directory is
// not something a caller can recover from.
func TestBootRefusesAFileSourceThatDoesNotResolve(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "runtime", "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{
		"boot", "brokenfiles",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", "s1",
	}, &stdout, &stderr)
	if !errors.Is(err, slots.ErrFileSource) {
		t.Fatalf("boot = %v, want ErrFileSource", err)
	}
	// The path is what makes the diagnostic actionable: the resolver's own
	// error knows the file it could not read and not the file it was filling.
	if !strings.Contains(err.Error(), "tasks/T-2/task.md") {
		t.Errorf("the refusal does not name the path it was going to write: %v", err)
	}
	if !strings.Contains(err.Error(), "brokenfiles") {
		t.Errorf("the refusal does not name the profile: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "" {
		t.Errorf("a refused boot printed %q, want nothing on stdout", got)
	}
	// Nothing was written. Sources resolve before rendering begins, so the
	// literal entry that would have resolved never reaches disk either.
	if _, err := os.Stat(filepath.Join(bootRoot, "brokenfiles", "s1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused boot left a directory behind: %v", err)
	}
}

// TestAFailedSlotNamesWhatTheOperatorWrote covers the diagnostic on the path
// that fails softest and is used most.
//
// Expansion runs before the request is built, so the resolver is handed the
// expanded value and reports that one — correct, and all it can say. A slot
// written "$NEVER_SET/process.md" with the variable unset therefore fails to
// open "/process.md", and without the declared form beside it the operator
// searches for a path nobody typed.
func TestAFailedSlotNamesWhatTheOperatorWrote(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	profiles := seed(t, bundle, skillsDir, scopeDir)

	engineer := profiles["engineer"]
	engineer.Spec["slots"] = `[
		{"name":"memory","section":"## Memory",
		 "source":{"kind":"static_file","static_file":{"path":"$CAIRN_TEST_NEVER_SET/process.md"}}}
	]`
	writeProfile(t, bundle, engineer)

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "engineer", "--profile", bundle,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}

	report := stderr.String()
	for _, want := range []string{
		`slot "memory" did not resolve`,    // unchanged, and still the library's message
		`$CAIRN_TEST_NEVER_SET/process.md`, // what the operator wrote
		`which expanded to "/process.md"`,  // and what was actually tried
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not carry %q:\n%s", want, report)
		}
	}
}

// TestAValueCairnCannotFillIsRenderedAwayAndReported is the end of the marker
// rule an operator meets: a cairn:value naming something outside the six is not
// a refusal any more. The boot directory is written, the marker's place is
// empty, and the operator hears about it on stderr.
//
// Refusing was the old behaviour and it does not survive one template serving
// every profile: a single word in a shared document decided whether anything
// was written at all. What replaces it has to hold three things at once, which
// is why they are asserted off the file rather than off the reporter — the
// unknown value leaves its line's other content alone, the known value that is
// empty for this instance renders the same way and is *not* reported, and the
// line naming the unknown one carries both the destination and the set it
// missed.
//
// The reviewer profile is booted rather than the engineer because it declares
// no scope, which is what makes "scope" a known value with nothing in it.
func TestAValueCairnCannotFillIsRenderedAwayAndReported(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	profiles := seed(t, bundle, skillsDir, scopeDir)

	reviewer := profiles["reviewer"]
	reviewer.Spec["templates"] = `{
		"AGENTS.md": "# <!-- cairn:value profile -->\n\n- tenant: <!-- cairn:value tenant -->\n- scope: <!-- cairn:value scope -->\n"
	}`
	writeProfile(t, bundle, reviewer)

	var stdout, stderr bytes.Buffer
	if err := run(ctx, []string{
		"boot", "reviewer", "--profile", bundle,
		"--boot-root", filepath.Join(home, "runtime"), "--session", "s1",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("boot: %v\nstderr: %s", err, stderr.String())
	}

	// Both markers rendered nothing and neither took its line: each shares one
	// with a label, and the trailing space after the colon is the template's.
	agents := read(t, strings.TrimSpace(stdout.String()), "AGENTS.md")
	if want := "# reviewer\n\n- tenant: \n- scope: \n"; agents != want {
		t.Errorf("AGENTS.md is\n%q\nwant\n%q", agents, want)
	}
	if strings.Contains(agents, "cairn:") {
		t.Errorf("AGENTS.md carries a marker's own text:\n%s", agents)
	}

	report := stderr.String()
	for _, want := range []string{
		`value "tenant"`, // the name that missed the set
		"AGENTS.md",      // the file it missed it in
		`the values cairn fills are "binding", "model", "profile", "provider", "scope", "session"`,
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not carry %q:\n%s", want, report)
		}
	}
	// The instance has no scope, and that is a fact about the instance rather
	// than a fault. Reporting it would put a line on stderr for every boot of
	// every unscoped profile.
	//
	// The test is on the diagnostic line and not on the word, because the line
	// naming the set carries every value name including this one. That line is
	// a listing; a line reading `value "scope"` would be a finding.
	if strings.Contains(report, `value "scope"`) {
		t.Errorf("the report names a value cairn knows and did not fill:\n%s", report)
	}
	if got := strings.Count(report, "cairn: AGENTS.md: value"); got != 1 {
		t.Errorf("the report carries %d value lines for one unfillable name, want 1:\n%s", got, report)
	}
}

// TestBootRefusals covers the three ways a boot is refused: an abstract
// profile, a target that names nothing, and a boot directory that would land
// inside the scope.
func TestBootRefusals(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	t.Run("an abstract profile", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := run(ctx, []string{"boot", "base", "--profile", bundle, "--boot-root", filepath.Join(home, "b1")}, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), "abstract") {
			t.Errorf("booting an abstract profile = %v, want a refusal naming it as abstract", err)
		}
	})

	t.Run("a target that names nothing", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := run(ctx, []string{"boot", "nobody", "--profile", bundle, "--boot-root", filepath.Join(home, "b2")}, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), "nobody") {
			t.Errorf("booting an unknown target = %v, want a refusal naming it", err)
		}
	})

	for name, target := range map[string]string{
		"a subagent with no profile":                "names-nosuchprofile",
		"a subagent that is abstract":               "names-template",
		"a subagent that declares no spec.subagent": "names-plain",
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := run(ctx, []string{
				"boot", target, "--profile", bundle,
				"--boot-root", filepath.Join(home, target), "--session", "s1",
			}, &out, &errOut)
			if err == nil {
				t.Fatalf("booting %s succeeded, want a refusal", target)
			}
			// Both ends of the reference: an operator reading it has to know
			// which profile named which id.
			for _, want := range []string{target, strings.TrimPrefix(target, "names-")} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal %q does not name %q", err, want)
				}
			}
			if _, err := os.Stat(filepath.Join(home, target, target, "s1")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused boot left a directory behind: %v", err)
			}
		})
	}

	t.Run("a boot directory inside the scope", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := run(ctx, []string{
			"boot", "engineer",
			"--profile", bundle,
			"--boot-root", filepath.Join(scopeDir, "runtime", "boot"),
			"--session", "s1",
		}, &out, &errOut)
		if !errors.Is(err, scope.ErrInsideScope) {
			t.Errorf("planting inside the scope = %v, want ErrInsideScope", err)
		}
	})

	// The scope here is not a checkout, which is the harder half of the
	// message to get right: the boot root is still inside a repository the
	// session's git would find, and the scope is still not it.
	t.Run("a boot root inside a foreign repository", func(t *testing.T) {
		foreign := filepath.Join(home, "elsewhere")
		mustMkdir(t, filepath.Join(foreign, ".git"))
		var out, errOut bytes.Buffer
		err := run(ctx, []string{
			"boot", "engineer",
			"--profile", bundle,
			"--boot-root", filepath.Join(foreign, "runtime", "boot"),
			"--session", "s1",
		}, &out, &errOut)
		if !errors.Is(err, bootdir.ErrForeignRepository) {
			t.Fatalf("planting inside a foreign repository = %v, want ErrForeignRepository", err)
		}
		for _, want := range []string{foreign, scopeDir} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal %q does not name %q", err, want)
			}
		}
		// Refused before the session segment is minted, so nothing was
		// created — including the root itself.
		if _, statErr := os.Stat(filepath.Join(foreign, "runtime")); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("a refused boot left a directory behind: %v", statErr)
		}
	})
}

// TestInstall covers the second command end to end, against a fixture root.
//
// Never against a real home directory: `cairn install` rewrites the live
// ~/.claude that every agent working on cairn is running under, which is why
// --root exists and why every test here passes one.
func TestInstall(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	// "base" is abstract. install renders it and boot refuses it — that
	// divergence is the reason the cascade carries the flag rather than acting
	// on it.
	if err := run(ctx, []string{"install", "base", "--profile", bundle, "--root", root}, &stdout, &stderr); err != nil {
		t.Fatalf("install: %v\nstderr: %s", err, stderr.String())
	}

	for _, rel := range []string{
		".claude/AGENTS.md",
		".claude/CLAUDE.md",
		".claude/settings.json",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("the installed layer is missing %s: %v", rel, err)
		}
	}
	// Not the boot directory's contract: no boot file, no MCP config, and none
	// of spec.files.
	for _, rel := range []string{"boot.md", ".claude/boot.md", ".mcp.json", ".claude/.mcp.json", "notes/scratch.md"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the installed layer carries %s, which belongs to a boot directory", rel)
		}
	}

	agents := read(t, root, ".claude/AGENTS.md")
	if !strings.HasPrefix(agents, "<!-- Generated by `cairn install` from profile \"base\". -->") {
		t.Errorf(".claude/AGENTS.md does not open with the provenance line:\n%s", agents)
	}
	if got := read(t, root, ".claude/CLAUDE.md"); got != "@AGENTS.md\n" {
		t.Errorf(".claude/CLAUDE.md = %q, want the pointer with no marker", got)
	}

	t.Run("a clean check reports no drift", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if err := run(ctx, []string{"install", "base", "--profile", bundle, "--root", root, "--check"}, &out, &errOut); err != nil {
			t.Fatalf("install --check on a freshly installed root = %v\n%s", err, out.String())
		}
	})

	t.Run("an edited file is drift, and check writes nothing", func(t *testing.T) {
		edited := filepath.Join(root, ".claude", "AGENTS.md")
		before := read(t, root, ".claude/AGENTS.md")
		writeFile(t, edited, before+"\nthe operator edited this in place\n", 0o644)

		var out, errOut bytes.Buffer
		err := run(ctx, []string{"install", "base", "--profile", bundle, "--root", root, "--check"}, &out, &errOut)
		var code exitCode
		if !errors.As(err, &code) || int(code) == 0 {
			t.Fatalf("install --check over an edited file = %v, want a non-zero exit status", err)
		}
		if !strings.Contains(out.String(), ".claude/AGENTS.md") {
			t.Errorf("the report does not name the edited file:\n%s", out.String())
		}
		// --check reports and repairs nothing.
		if got := read(t, root, ".claude/AGENTS.md"); !strings.Contains(got, "the operator edited this in place") {
			t.Error("--check rewrote the file it was reporting on")
		}
	})

	t.Run("the harness's own files are not drift", func(t *testing.T) {
		// A fresh root, so the edit from the previous subtest is not in play.
		clean := t.TempDir()
		var out, errOut bytes.Buffer
		if err := run(ctx, []string{"install", "base", "--profile", bundle, "--root", clean}, &out, &errOut); err != nil {
			t.Fatalf("install: %v", err)
		}
		for _, rel := range []string{".claude/settings.local.json", ".claude/.credentials.json"} {
			writeFile(t, filepath.Join(clean, filepath.FromSlash(rel)), "{}\n", 0o644)
		}
		out.Reset()
		if err := run(ctx, []string{"install", "base", "--profile", bundle, "--root", clean, "--check"}, &out, &errOut); err != nil {
			t.Fatalf("the harness's own state files were reported as drift: %v\n%s", err, out.String())
		}
	})

	t.Run("install takes exactly one target", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if err := run(ctx, []string{"install", "--root", root, "--profile", bundle}, &out, &errOut); err == nil {
			t.Error("install with no target reported success")
		}
		if err := run(ctx, []string{"install", "base", "engineer", "--profile", bundle, "--root", root}, &out, &errOut); err == nil {
			t.Error("install with two targets reported success")
		}
	})
}

// TestInstallReportsAValueCairnCannotFill is the regression the value rule
// introduced and this closes: `cairn install` never reported an unfilled marker
// at all, because the parser's refusal used to cover it.
//
// The failure it leaves is the one docs/plan.md made the pointer a template to
// avoid. A template that is nothing but a marker cairn cannot fill renders no
// bytes, and a template rendering no bytes writes no file — so `.claude/CLAUDE.md`
// lands holding an include of a `.claude/AGENTS.md` that is not there, and the
// harness resolves the include to nothing without a word.
//
// The root here has never held the instruction file, which is the case a check
// cannot cover. A check claims the paths its renderers can produce, so a file
// that stopped rendering into a root that already had it is an orphan and the
// check exits non-zero — that path is covered by TestInstall's drift subtests.
// Into a fresh root there is nothing to orphan, and the check says "In sync"
// with the pointer already dangling. Hence the stderr line, on the check as
// well as the write.
//
// A one-character typo is the whole setup, which is the point: this is not an
// exotic input.
func TestInstallReportsAValueCairnCannotFill(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	profiles := seed(t, bundle, skillsDir, scopeDir)

	base := profiles["base"]
	base.Spec["templates"] = `{
		"AGENTS.md": "<!-- cairn:value sesion -->\n",
		"CLAUDE.md": "@AGENTS.md\n"
	}`
	writeProfile(t, bundle, base)

	root := t.TempDir()
	for _, args := range [][]string{
		{"install", "base", "--profile", bundle, "--root", root},
		{"install", "base", "--profile", bundle, "--root", root, "--check"},
	} {
		mode := "write"
		if args[len(args)-1] == "--check" {
			mode = "check"
		}
		t.Run(mode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(ctx, args, &stdout, &stderr); err != nil {
				t.Fatalf("install: %v\nstderr: %s", err, stderr.String())
			}
			report := stderr.String()
			// The path the operator will look for the file at, not the
			// manifest's key for it. ".claude/" is the whole difference and
			// the whole point.
			if !strings.Contains(report, `.claude/AGENTS.md: value "sesion"`) {
				t.Errorf("the report does not name the typo and the file it is in:\n%s", report)
			}
			if strings.Contains(report, `: AGENTS.md: value`) {
				t.Errorf("the report names the manifest's key, not the installed path:\n%s", report)
			}
		})
	}
	// The file really is missing — the report is not describing a hazard that
	// did not happen.
	if _, err := os.Stat(filepath.Join(root, ".claude", "AGENTS.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the instruction file was written after all, so this test proves nothing: %v", err)
	}
	if got := read(t, root, ".claude/CLAUDE.md"); got != "@AGENTS.md\n" {
		t.Errorf(".claude/CLAUDE.md = %q, want the include that now points at nothing", got)
	}
}

// TestAMarkerThatWillNotParseNamesWhatTheOperatorEdits is the other half of the
// two names a report carries, and it is a fence rather than a feature.
//
// The unfilled-value line names the path the file lands at, because that is the
// file an operator goes looking for and does not find. A refusal must not: the
// render never ran, so ".claude/AGENTS.md" is an output path that this run will
// never produce, and sending someone to edit a file that does not exist is
// worse than telling them nothing. What they edit is spec.templates.
//
// It is asserted for both commands because only one of them can regress. In a
// boot directory the manifest key and the written path are the same string, so
// boot cannot tell the two apart and would not notice if the reporter started
// using the wrong one.
func TestAMarkerThatWillNotParseNamesWhatTheOperatorEdits(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	profiles := seed(t, bundle, skillsDir, scopeDir)

	base := profiles["base"]
	base.Spec["templates"] = `{
		"AGENTS.md": "<!-- cairn:section repo -->\n",
		"CLAUDE.md": "@AGENTS.md\n"
	}`
	writeProfile(t, bundle, base)

	for name, args := range map[string][]string{
		"install": {"install", "base", "--profile", bundle, "--root", t.TempDir()},
		"boot":    {"boot", "base2", "--profile", bundle, "--boot-root", filepath.Join(home, "rt"), "--session", "s1"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(ctx, args, &stdout, &stderr)
			if err == nil {
				t.Fatalf("%s accepted a marker it cannot act on\nstderr: %s", name, stderr.String())
			}
			if !strings.Contains(err.Error(), `spec.templates "AGENTS.md"`) {
				t.Errorf("the refusal does not name the manifest key: %v", err)
			}
			if strings.Contains(err.Error(), ".claude/AGENTS.md") {
				t.Errorf("the refusal sends the operator to a path this run never wrote: %v", err)
			}
			// Still the diagnostic the marker earned, quoted.
			if !strings.Contains(err.Error(), "<!-- cairn:section repo -->") {
				t.Errorf("the refusal does not quote the marker: %v", err)
			}
		})
	}
}

// TestInstanceValuesCarriesOnlyTheNamesCairnFills pins the composition root's
// half of the two-mechanism defence.
//
// A value marker can only render what this map holds, and this map is built
// from [bootdir.ValueNames] rather than from what a caller passed. Wiring a new
// value is exactly the edit that would put a manifest key in here by mistake —
// "mcp" alongside "model", say, while someone is adding the seventh value — and
// spec.mcp is where an MCP server's API keys live.
//
// Substitution refuses to read a name outside the set as well, so this is the
// second lock and not the only one. It is asserted anyway: it is one function,
// it is where the mistake would be made, and a defence nobody tests is a
// defence that quietly stops being one.
func TestInstanceValuesCarriesOnlyTheNamesCairnFills(t *testing.T) {
	const secret = "sk-live-do-not-render-me"

	got := instanceValues(map[string]string{
		"profile":  "engineer",
		"mcp":      secret,
		"settings": secret,
	})

	for _, name := range []string{"mcp", "settings"} {
		if _, carried := got[name]; carried {
			t.Errorf("instanceValues() carries %q, which a template could then name", name)
		}
	}
	if got["profile"] != "engineer" {
		t.Errorf("instanceValues()[\"profile\"] = %q, want the value it was handed", got["profile"])
	}
	// A key for every name cairn fills, and nothing else at all. The count is
	// the assertion that catches a key added without a name to go with it.
	for _, name := range bootdir.ValueNames() {
		if _, ok := got[name]; !ok {
			t.Errorf("instanceValues() has no key for %q, which cairn declares it fills", name)
		}
	}
	if len(got) != len(bootdir.ValueNames()) {
		t.Errorf("instanceValues() returned %d keys, want the %d in bootdir.ValueNames()",
			len(got), len(bootdir.ValueNames()))
	}
}

// seed writes the two profiles and the binding the tests above boot.
func seed(t *testing.T, bundle, skillsDir, scopeDir string) map[string]bundleProfile {
	t.Helper()
	// A path nothing ever writes, for the slot and the file source that have
	// to fail. It sits under the test's own temp tree so it cannot collide
	// with anything real.
	missing := filepath.Join(filepath.Dir(bundle), "never-written.md")

	base := bundleProfile{
		ID:       "base",
		Abstract: true,
		Name:     "Base",
		Provider: "claude",
		Model:    "opus",
		Body:     "base persona",
		Spec: map[string]string{
			// Under the provider it is for. A profile serving two harnesses
			// declares two documents here; this bundle renders claude, and
			// what the other keys would hold is that harness's business.
			"settings": `{"claude":{"env":{"CAIRN":"whateverTheOperatorWrote"}}}`,
			// Every file of prose in the boot directory is a template a profile
			// declared. Cairn names none of them: the destinations, the order
			// of the blocks, and whether an instruction file exists at all are
			// all here.
			"templates": `{
				"AGENTS.md": "# <!-- cairn:value profile --> on <!-- cairn:value provider -->\n\nbase persona\n\nscope: <!-- cairn:value scope -->\n\n<!-- cairn:slot note -->\n\n<!-- cairn:slot quiet -->\n\n<!-- cairn:slot memory -->\n",
				"CLAUDE.md": "@AGENTS.md\n",
				"boot.md":   "<!-- cairn:slot note -->\n"
			}`,
			// An unknown key: carried through the cascade and ignored by every
			// renderer, never an error.
			"somethingCairnHasNeverHeardOf": `{"nested":[1,2,3]}`,
		},
	}
	// A profile that exists to be dispatched. Its spec.subagent is the whole
	// of what a definition is rendered from — the parent neither narrows nor
	// widens it, so a tool this profile needs is a change made here.
	reviewer := bundleProfile{
		ID:      "reviewer",
		Extends: "base",
		Name:    "Reviewer",
		Body:    "reviewer persona, which the definition does not carry",
		Spec: map[string]string{
			"subagent": `{
				"description": "Fresh review with no shared context.",
				"tools": ["Read", "Grep", "Glob"],
				"model": "sonnet",
				"body": "You review a diff and report what you found.\n"
			}`,
		},
	}
	// Three profiles a boot has to refuse to name as a subagent: one that is
	// abstract, one that declares no definition, and — by its absence — one
	// that does not exist.
	abstractSub := bundleProfile{ID: "template", Extends: "base", Abstract: true, Name: "Template",
		Spec: map[string]string{"subagent": `{"description":"d"}`}}
	undeclaredSub := bundleProfile{ID: "plain", Extends: "base", Name: "Plain"}

	namesSubagent := func(id string) bundleProfile {
		return bundleProfile{
			ID: "names-" + id, Extends: "base", Name: "Names " + id,
			Spec: map[string]string{"subagents": `["` + id + `"]`},
		}
	}

	engineer := bundleProfile{
		ID:      "engineer",
		Extends: "base",
		Name:    "Engineer",
		Body:    "engineer persona",
		Spec: map[string]string{
			"subagents": `["reviewer"]`,
			// The leaf restates the whole template, which is how closest-wins
			// works for every other key. Ordering is the operator's: the
			// ancestor's prose is here because this profile put it here, not
			// because a cascade concatenated it first.
			"templates": `{
				"AGENTS.md": "# <!-- cairn:value profile --> on <!-- cairn:value provider -->\n\nbase persona\n\nengineer persona\n\nscope: <!-- cairn:value scope -->\n\n<!-- cairn:slot note -->\n\n<!-- cairn:slot quiet -->\n\n<!-- cairn:slot memory -->\n",
				"CLAUDE.md": "@AGENTS.md\n",
				"boot.md":   "<!-- cairn:slot note -->\n"
			}`,
			// Three slots on purpose: one that resolves, one that resolves
			// empty, and one that fails. The last two are the pair docs/plan.md
			// §5 says must leave nothing behind.
			"slots": `[
				{"name":"note",  "section":"## Note",  "source":{"kind":"inline","inline":{"content":"the standing note"}}},
				{"name":"quiet", "section":"## Quiet", "source":{"kind":"cmd","cmd":{"run":"true"}}},
				{"name":"memory","section":"## Memory","source":{"kind":"static_file","static_file":{"path":"` + missing + `"}}}
			]`,
			"mcp":        `[{"name":"vanta","command":"vanta-mcp","args":["serve"]}]`,
			"skills":     `["code-review"]`,
			"skills_dir": `"` + skillsDir + `"`,
			// A literal and a source side by side: the source is the torque
			// case, a task bundle rendered from state that is only true now.
			"files": `{
				"notes/scratch.md":  "scratch\n",
				"tasks/T-1/task.md": {"kind":"cmd","cmd":{"run":"printf '# T-1\\nin progress\\n'"}}
			}`,
		},
	}
	// A profile whose file source cannot resolve. Nothing boots it except the
	// test that asserts a boot is refused rather than half-written.
	broken := bundleProfile{
		ID:      "brokenfiles",
		Extends: "base",
		Name:    "Broken",
		Spec: map[string]string{
			"files": `{
				"notes/fine.md":     "a literal, which resolves",
				"tasks/T-2/task.md": {"kind":"static_file","static_file":{"path":"` + missing + `"}}
			}`,
		},
	}
	// A concrete profile that adds nothing of its own. `cairn boot` refuses an
	// abstract profile before it reads a template, so a test about templates
	// needs one the cascade will actually boot.
	base2 := bundleProfile{ID: "base2", Extends: "base", Name: "Base, bootable"}

	// A profile with no settings document at all, and no binding to give it a
	// scope. An explicit null clears the ancestor's key, and with nothing
	// declared and no scope to grant there is nothing for RenderSettings to
	// write — which is the only way a boot directory legitimately holds no
	// settings file, and the case a launcher must not be handed a path for.
	nosettings := bundleProfile{ID: "nosettings", Extends: "base", Name: "No settings",
		Spec: map[string]string{"settings": `null`}}

	// The same, plus one directory to grant. It is what keeps "no scope" and
	// "no settings file" from being one fact tested twice: this profile has no
	// binding either, so its scope is null, and the grant alone still renders
	// the settings document. A report that derived one from the other would
	// pass every other test in this file and fail here.
	grantsonly := bundleProfile{ID: "grantsonly", Extends: "base", Name: "Grants only",
		Spec: map[string]string{
			"settings": `null`,
			"access":   `{"directories":["` + scopeDir + `"]}`,
		}}

	written := map[string]bundleProfile{}
	for _, p := range []bundleProfile{
		base, base2, engineer, broken, reviewer, abstractSub, undeclaredSub, nosettings, grantsonly,
		namesSubagent("template"), namesSubagent("plain"), namesSubagent("nosuchprofile"),
	} {
		writeProfile(t, bundle, p)
		written[p.ID] = p
	}
	writeBinding(t, bundle, "engineer", "engineer", scopeDir)
	// The fixtures are returned so a test can re-author one: a file is the
	// store now, so changing a profile is writing its file again rather than
	// updating a row.
	return written
}

// writeSkill lays down a multi-file skill: an entry file, a reference under a
// subdirectory, and an executable script. Flattening it is the defect the
// directory-tree copier exists to prevent.
func writeSkill(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "code-review")
	mustMkdir(t, filepath.Join(dir, "references"))
	writeFile(t, filepath.Join(dir, "SKILL.md"), "# code review\n", 0o644)
	writeFile(t, filepath.Join(dir, "references", "checklist.md"), "- read it\n", 0o644)
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\necho hi\n", 0o755)
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
}

func writeFile(t *testing.T, path, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// treeOf reads every file under dir into a map of slash-separated relative
// path to content, so two boot directories can be compared as a whole rather
// than one artifact at a time.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestUsageNamesTheBootRootDefault ties the usage text to the constant it
// describes.
//
// Nothing else does: no golden renders --help, and a default documented as a
// literal goes stale the moment the constant moves, silently and in the one
// place an operator who does not know the default goes looking. That is the
// same vacuous-assertion failure the boot root itself was, so it gets a pin
// rather than a promise to remember.
func TestUsageNamesTheBootRootDefault(t *testing.T) {
	if !strings.Contains(usage, bootdir.DefaultRootRel) {
		t.Errorf("usage does not name the boot root default %q:\n%s", bootdir.DefaultRootRel, usage)
	}
	if !strings.Contains(usage, bootdir.EnvBootRoot) {
		t.Errorf("usage does not name %s, which overrides that default", bootdir.EnvBootRoot)
	}
}
