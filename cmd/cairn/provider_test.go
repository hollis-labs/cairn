package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/profile"
)

// TestProviderDefaultsToTheProfilesOwn is the property that makes this flag
// safe to have added: a command run without it renders exactly what it
// rendered before it existed.
//
// It is asserted as a byte comparison of two whole boot directories rather
// than as "the flag was read", because the claim is about output and the two
// paths through selectProvider are different code.
func TestProviderDefaultsToTheProfilesOwn(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	plain := bootInto(t, ctx, bundle, bootRoot, "plain")
	named := bootInto(t, ctx, bundle, bootRoot, "named", "--provider", "claude")

	for _, rel := range []string{"AGENTS.md", "CLAUDE.md", ".claude/settings.json", ".mcp.json"} {
		if a, b := read(t, plain, rel), read(t, named, rel); a != b {
			t.Errorf("%s differs between a default boot and --provider claude:\n%s\n---\n%s", rel, a, b)
		}
	}
}

// TestProviderSelectsTheSettingsDocument is the flag doing the one thing it
// exists to do, read off disk.
//
// The profile declares a document for a harness this boot is not for as well
// as for the one it is. What lands in .claude/settings.json is claude's alone:
// a provider is a materialization target, so the other document is carried
// through the cascade and written nowhere.
func TestProviderSelectsTheSettingsDocument(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "boot")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	profiles := seed(t, bundle, skillsDir, scopeDir)

	base := profiles["base"]
	base.Spec["settings"] = `{"claude":{"env":{"CAIRN":"claudes"}},"codex":{"env":{"CAIRN":"codexs"}}}`
	writeProfile(t, bundle, base)

	dir := bootInto(t, ctx, bundle, bootRoot, "s1")

	got := read(t, dir, ".claude/settings.json")
	if !strings.Contains(got, "claudes") {
		t.Errorf("the settings document is missing claude's own value:\n%s", got)
	}
	if strings.Contains(got, "codexs") {
		t.Errorf("the settings document carries another harness's document:\n%s", got)
	}
}

// TestProviderRefusesATargetWithNoLayout is the scope fence, stated as a test.
//
// Cairn knows the name "opencode" and renders nothing for it, and those are two
// different facts. The refusal has to name the flag rather than the profile —
// the profile says claude, and sending the reader to it would send them to a
// file that is not wrong — and it has to plant nothing, because a boot
// directory in another harness's shape is worse than no boot directory.
func TestProviderRefusesATargetWithNoLayout(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "boot")
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
		"--provider", "opencode",
	}, &stdout, &stderr)
	if !errors.Is(err, bootdir.ErrUnsupportedProvider) {
		t.Fatalf("boot --provider opencode = %v, want bootdir.ErrUnsupportedProvider", err)
	}
	for _, want := range []string{`--provider "opencode"`, `"claude"`, `"codex"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not carry %s", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(bootRoot, "engineer", "s1")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a refused target still planted a boot directory: %v", statErr)
	}
}

// TestProviderCodexBootRendersNativeArtifacts proves Codex is now a real
// materialization target while keeping the shared Claude-subagent fixture for
// unsupported-feature tests.
func TestProviderCodexBootRendersNativeArtifacts(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	bootRoot := filepath.Join(home, "boot")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	seedCodexable(t, bundle, scopeDir)

	var stdout, stderr bytes.Buffer
	err := run(ctx, []string{
		"boot", "coder",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", "s1",
		"--provider", "codex",
		"--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("boot --provider codex: %v\nstderr: %s", err, stderr.String())
	}
	var report bootReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode boot report: %v\n%s", err, stdout.String())
	}
	if report.Provider != profile.ProviderCodex.String() {
		t.Fatalf("provider = %q, want codex", report.Provider)
	}
	if report.CwdPreference != "boot_dir" {
		t.Errorf("cwd_preference = %q, want boot_dir", report.CwdPreference)
	}
	if !slices.Equal(report.ProjectDirArg, []string{"--add-dir", "{{.ProjectDir}}"}) {
		t.Errorf("project_dir_arg = %v, want --add-dir and the project placeholder", report.ProjectDirArg)
	}
	if !strings.Contains(strings.Join(report.ProjectDirArg, " "), "{{.ProjectDir}}") {
		t.Errorf("project_dir_arg = %v, want a project placeholder", report.ProjectDirArg)
	}
	if len(report.EnvAmendments) == 0 || !strings.Contains(report.EnvAmendments[0], "{{.BootDir}}") {
		t.Errorf("env_amendments = %v, want provider boot-dir placeholder", report.EnvAmendments)
	}
	if !slices.Equal(report.HomeResourcePaths, []string{"auth.json", "hooks.json", "hooks"}) {
		t.Errorf("home_resource_paths = %v, want Codex auth and hook resources", report.HomeResourcePaths)
	}
	if report.SettingsPath == nil || !strings.HasSuffix(*report.SettingsPath, "config.toml") {
		t.Fatalf("settings_path = %v, want config.toml", report.SettingsPath)
	}
	agents := read(t, report.BootDir, "AGENTS.md")
	if !strings.Contains(agents, "coder on codex") {
		t.Errorf("AGENTS.md does not carry the Codex materialization values:\n%s", agents)
	}
	config := read(t, report.BootDir, "config.toml")
	for _, want := range []string{`model = 'gpt-5'`, `[sandbox_workspace_write]`, `writable_roots`} {
		if !strings.Contains(config, want) {
			t.Errorf("config.toml missing %q:\n%s", want, config)
		}
	}
	for _, absent := range []string{"auth.json", ".mcp.json"} {
		if _, err := os.Stat(filepath.Join(report.BootDir, absent)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Codex boot planted %s: %v", absent, err)
		}
	}
}

// TestProviderRefusesAWordThatIsNoHarness is the other refusal, and it is a
// different one. "opencode" is a provider cairn cannot render; "cluade" is
// not a provider. One message for both would leave the operator who typo'd
// looking for a feature and the operator who asked for opencode looking for a
// typo.
func TestProviderRefusesAWordThatIsNoHarness(t *testing.T) {
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
		"--boot-root", filepath.Join(home, "boot"),
		"--provider", "cluade",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("boot --provider cluade returned no error")
	}
	if errors.Is(err, bootdir.ErrUnsupportedProvider) {
		t.Errorf("a word that is no harness was reported as an unsupported one: %v", err)
	}
	for _, want := range []string{"cluade", `"claude"`, `"codex"`, `"opencode"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not carry %s", err, want)
		}
	}
}

// TestShowReportsTheTargetProvider covers the flag on the command that renders
// nothing.
//
// show reports the target the way it reports the scope: what a boot would do,
// not what the file says. It looks up no layout, so a harness cairn cannot yet
// write is still a profile it can print — the same call it already makes about
// a scope that would not resolve, and the reason a profile declaring codex has
// always shown.
func TestShowReportsTheTargetProvider(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	bundle := filepath.Join(home, "bundle")
	skillsDir := filepath.Join(home, "skills")
	scopeDir := filepath.Join(home, "repo")
	mustMkdir(t, scopeDir)
	writeSkill(t, skillsDir)
	seed(t, bundle, skillsDir, scopeDir)

	if got := mustShow(t, ctx, bundle, "engineer"); !strings.Contains(got, "provider      claude") {
		t.Errorf("show reports no provider by default:\n%s", got)
	}
	out := mustShow(t, ctx, bundle, "engineer", "--provider", "codex")
	if !strings.Contains(out, "provider      codex") {
		t.Errorf("show --provider codex does not report the target:\n%s", out)
	}

	var report struct {
		Provider *string `json:"provider"`
	}
	raw := mustShow(t, ctx, bundle, "engineer", "--provider", "codex", "--json")
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("the show report does not decode: %v\n%s", err, raw)
	}
	if report.Provider == nil || *report.Provider != "codex" {
		t.Errorf("show --json reports provider %v, want the target", report.Provider)
	}
}

// TestInstallRefusesATargetWithNoLayout is the scope fence on the other
// materializing command. The installed layer lands in a harness's own
// directory under the root, so "which harness" is exactly as answerable here
// as it is for a boot — and exactly as refusable.
//
// The root is a fixture directory. `cairn install` against the live home
// rewrites the configuration of the session running it.
func TestInstallRefusesATargetWithNoLayout(t *testing.T) {
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
	err := run(ctx, []string{
		"install", "base",
		"--profile", bundle,
		"--root", root,
		"--provider", "opencode",
	}, &stdout, &stderr)
	if !errors.Is(err, bootdir.ErrUnsupportedProvider) {
		t.Fatalf("install --provider opencode = %v, want bootdir.ErrUnsupportedProvider", err)
	}
	if !strings.Contains(err.Error(), `--provider "opencode"`) {
		t.Errorf("the refusal %q does not name the flag that chose the target", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("read the install root: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a refused install left %d entries in the root", len(entries))
	}
}

func seedCodexable(t *testing.T, bundle, scopeDir string) {
	t.Helper()
	writeProfile(t, bundle, bundleProfile{
		ID:       "base",
		Abstract: true,
		Name:     "Base",
		Provider: "claude",
		Spec: map[string]string{
			"templates": `{
				"AGENTS.md": "# <!-- cairn:value profile --> on <!-- cairn:value provider -->\n\nscope: <!-- cairn:value scope -->\n"
			}`,
			"settings": `{"codex":{"model":"gpt-5"}}`,
			"access":   `{"directories":["` + scopeDir + `"]}`,
		},
	})
	writeProfile(t, bundle, bundleProfile{ID: "coder", Extends: "base", Name: "Coder"})
}

// bootInto runs one boot into a session of its own and returns the directory
// it printed.
func bootInto(t *testing.T, ctx context.Context, bundle, bootRoot, session string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := []string{
		"boot", "engineer",
		"--profile", bundle,
		"--boot-root", bootRoot,
		"--session", session,
	}
	if err := run(ctx, append(full, args...), &stdout, &stderr); err != nil {
		t.Fatalf("boot %v: %v\nstderr: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}
