package install_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/install"
	"github.com/chrispian/cairn/profile"
	"github.com/chrispian/cairn/slots"
)

// Every fixture below is built under t.TempDir(). Nothing here names a real
// home directory, and nothing here runs an install: `cairn install` rewrites
// the configuration the session running these tests reads, and a check against
// a fixture root is what answers "does it work".

// checkFixture is one install root holding the render of one layer, ready for
// a test to break in one specific way.
type checkFixture struct {
	// dir is the install root, a temporary directory.
	dir string

	// lay is the layer the root was rendered from.
	lay *install.Layer

	// files is that render, in render order.
	files []install.File
}

// newCheckFixture renders a layer declaring the named skills and writes the
// render into a fresh temporary install root, so the root starts out matching.
func newCheckFixture(t *testing.T, skills ...string) checkFixture {
	t.Helper()
	return newCheckFixtureDeclaring(t, map[string]any{"model": "opus"}, skills...)
}

// newCheckFixtureDeclaring is [newCheckFixture] with the settings document the
// profile declares supplied, for a test that needs a shape the one-key default
// cannot express — a key nested inside a key cairn declares, above all.
func newCheckFixtureDeclaring(t *testing.T, settings map[string]any, skills ...string) checkFixture {
	t.Helper()
	rootDir := t.TempDir()
	root, err := install.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("NewRoot(t.TempDir()): %v", err)
	}
	manifest := map[string]any{
		// Under the provider the layer is rendered for. spec.settings holds a
		// document per harness, and every fixture here renders claude's.
		"settings":  map[string]any{profile.ProviderClaude.String(): settings},
		"templates": map[string]any{"AGENTS.md": "declared, and resolved onto the layer"},
	}
	if len(skills) > 0 {
		// install.skills, not skills: the installed layer plants the set every
		// session on the machine loads, and spec.skills is one boot
		// directory's.
		manifest["install"] = map[string]any{"skills": skills}
		manifest["skills_dir"] = writeCheckSkills(t, skills)
	}
	lay := &install.Layer{
		Root: root,
		Profile: &profile.Resolved{
			ID:       "base",
			Name:     "Base",
			Provider: profile.ProviderClaude,
			Model:    "opus",
			Bodies:   []profile.Body{{ID: "base", Text: "Read the profile."}},
			Spec:     checkSpec(t, manifest),
		},
		Home: t.TempDir(),
		// Resolved template text and instance values, as the composition root
		// supplies them.
		Templates: map[string]string{
			"AGENTS.md": "# <!-- cairn:value profile -->\n\nRead the profile.\n",
			"CLAUDE.md": "@" + "AGENTS.md" + "\n",
		},
		Values: map[string]string{"profile": "base"},
	}
	files, err := install.Render(lay)
	if err != nil {
		t.Fatalf("render the fixture layer: %v", err)
	}
	fixture := checkFixture{dir: rootDir, lay: lay, files: files}
	fixture.plant(t)
	return fixture
}

// plant writes the render into the install root, which is the state a check
// finds after an install nobody has touched since.
func (f checkFixture) plant(t *testing.T) {
	t.Helper()
	for _, file := range f.files {
		dest := f.path(file.Path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			t.Fatalf("create the directory for %s: %v", dest, err)
		}
		mode := file.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(dest, file.Content, mode); err != nil {
			t.Fatalf("write %s: %v", dest, err)
		}
	}
}

// path returns the absolute location of a slash-separated path inside the
// install root.
func (f checkFixture) path(rel string) string {
	return filepath.Join(f.dir, filepath.FromSlash(rel))
}

// check runs the check against the fixture's root and fails on an error, since
// every finding a test asserts on is in the report rather than in an error.
func (f checkFixture) check(t *testing.T) *install.Report {
	t.Helper()
	report, err := install.Check(f.lay)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return report
}

// writeCheckSkills writes one directory per skill under a fresh temporary
// directory and returns it, for the manifest's skills_dir.
func writeCheckSkills(t *testing.T, names []string) string {
	t.Helper()
	source := t.TempDir()
	for _, name := range names {
		dir := filepath.Join(source, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create the skill directory %s: %v", dir, err)
		}
		body := []byte("# " + name + "\n")
		if err := os.WriteFile(filepath.Join(dir, bootdir.SkillFileName), body, 0o644); err != nil {
			t.Fatalf("write the skill file for %q: %v", name, err)
		}
	}
	return source
}

// checkSpec encodes a manifest written inline in a test into a [profile.Spec],
// so a test declares the JSON an operator would have stored.
func checkSpec(t *testing.T, manifest map[string]any) profile.Spec {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode the manifest: %v", err)
	}
	var spec profile.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("decode the manifest: %v", err)
	}
	return spec
}

// writeInRoot writes content at a slash-separated path inside the install
// root, creating the directories above it.
func writeInRoot(t *testing.T, rootDir, rel, content string) string {
	t.Helper()
	dest := filepath.Join(rootDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("create the directory for %s: %v", dest, err)
	}
	if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", dest, err)
	}
	return dest
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

// entryAt returns the report's entry for a path, and fails when the report
// carries none or more than one.
func entryAt(t *testing.T, report *install.Report, rel string) install.Entry {
	t.Helper()
	var found []install.Entry
	for _, entry := range report.Entries {
		if entry.Path == rel {
			found = append(found, entry)
		}
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		t.Fatalf("no entry for %q; the report holds %v", rel, entryPaths(report.Entries))
	default:
		t.Fatalf("%d entries for %q; a path is classified once", len(found), rel)
	}
	return install.Entry{}
}

// pathsWithStatus returns the paths carrying status, in path order.
func pathsWithStatus(report *install.Report, status install.Status) []string {
	entries := report.ByStatus(status)
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Path)
	}
	return out
}

// entryPaths returns every path in a report, for a failure message.
func entryPaths(entries []install.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Path+" ("+string(entry.Status)+")")
	}
	return out
}

// symlinkOrSkip links target at name, skipping the test when the platform will
// not make a symbolic link. The link cases below are the reason the check
// reads through [fs.ReadLinkFS], so a platform that cannot make one has
// nothing to say about them.
func symlinkOrSkip(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("this platform cannot create a symbolic link: %v", err)
	}
}

func TestNewSweepPlanClaimsTheFilesAndTheNamedSkillsAndNothingElse(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha", "beta")
	plan, err := install.NewSweepPlan(fixture.lay)
	if err != nil {
		t.Fatalf("NewSweepPlan: %v", err)
	}
	wantClaims := []string{".claude/AGENTS.md", ".claude/CLAUDE.md", ".claude/settings.json"}
	if !slices.Equal(plan.Claims, wantClaims) {
		t.Errorf("Claims = %v, want %v", plan.Claims, wantClaims)
	}
	// One tree per declared skill, and the skills directory itself is not one
	// of them: cairn writes into it and does not own it.
	wantTrees := []string{".claude/skills/alpha", ".claude/skills/beta"}
	if !slices.Equal(plan.Trees, wantTrees) {
		t.Errorf("Trees = %v, want %v", plan.Trees, wantTrees)
	}
	if !slices.Equal(plan.Shared, []string{".claude/skills"}) {
		t.Errorf("Shared = %v, want [.claude/skills]", plan.Shared)
	}
	// The provider directory itself is claimed by no list. ~/.claude is a live
	// harness's home, and a sweep that read it one level deep would call
	// settings.local.json and .credentials.json orphans on every run of every
	// real installation.
	for _, claimed := range append(slices.Clone(plan.Claims), plan.Trees...) {
		if claimed == ".claude" || claimed == ".claude/skills" {
			t.Errorf("the plan claims %q, which cairn shares", claimed)
		}
	}
}

// TestNewSweepPlanClaimsNoSkillDirectoryForAProfileDeclaringNone is the half
// of the rule that used to be the opposite. A profile declaring no skills
// claims no skill directory at all — the skills directory is still read, so a
// check can say what is in it, and nothing in it is cairn's to report as
// drift.
func TestNewSweepPlanClaimsNoSkillDirectoryForAProfileDeclaringNone(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t)
	if skills, err := fixture.lay.Profile.Spec.InstallSkills(); err != nil || len(skills) != 0 {
		t.Fatalf("the fixture should declare no skills; got %v, %v", skills, err)
	}
	plan, err := install.NewSweepPlan(fixture.lay)
	if err != nil {
		t.Fatalf("NewSweepPlan: %v", err)
	}
	if len(plan.Trees) != 0 {
		t.Errorf("Trees = %v, want none: the profile named no skill directory", plan.Trees)
	}
	// The directory is still in the plan, because a check still reads it. What
	// changed is that reading it is not the same as claiming it.
	if !slices.Equal(plan.Shared, []string{".claude/skills"}) {
		t.Errorf("Shared = %v, want [.claude/skills]", plan.Shared)
	}
}

// TestEveryClaimIsAPathTheRenderProduces pins the sweep's claims against the
// render, so the two cannot drift.
//
// A claim is built from a renderer's Artifact label joined to the provider
// directory, while the file itself is written at the path the Layout names.
// Those are two different derivations of one path, and if they disagree the
// sweep would report a file cairn writes as an orphan on every check while
// missing the leftover it exists to find.
func TestEveryClaimIsAPathTheRenderProduces(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	// A profile declaring everything, so every non-tree renderer produces its
	// file and the two derivations can actually be compared.
	plan, err := install.NewSweepPlan(fixture.lay)
	if err != nil {
		t.Fatalf("NewSweepPlan: %v", err)
	}
	files, err := install.Render(fixture.lay)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	produced := make(map[string]struct{}, len(files))
	for _, f := range files {
		produced[f.Path] = struct{}{}
	}
	for _, claim := range plan.Claims {
		if _, ok := produced[claim]; !ok {
			t.Errorf("the sweep claims %q and the render does not produce it; the Artifact label "+
				"and the Layout path have drifted apart. Rendered: %v", claim, produced)
		}
	}
}

func TestNewSweepPlanRefusesALayerItCannotPlan(t *testing.T) {
	t.Parallel()
	if _, err := install.NewSweepPlan(nil); !errors.Is(err, install.ErrNoProfile) {
		t.Errorf("NewSweepPlan(nil) = %v, want ErrNoProfile", err)
	}
	if _, err := install.NewSweepPlan(&install.Layer{}); !errors.Is(err, install.ErrNoProfile) {
		t.Errorf("NewSweepPlan(empty layer) = %v, want ErrNoProfile", err)
	}
	lay := &install.Layer{Profile: &profile.Resolved{ID: "base", Provider: profile.ProviderOpenCode}}
	if _, err := install.NewSweepPlan(lay); !errors.Is(err, bootdir.ErrUnsupportedProvider) {
		t.Errorf("NewSweepPlan(opencode) = %v, want ErrUnsupportedProvider", err)
	}
}

func TestCheckRootMatchingTheRender(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	report := fixture.check(t)

	if !report.Clean() {
		t.Errorf("Clean() = false, want true; report:\n%s", report)
	}
	if got := report.ExitCode(); got != 0 {
		t.Errorf("ExitCode() = %d, want 0", got)
	}
	if report.Root != fixture.dir {
		t.Errorf("Root = %q, want %q", report.Root, fixture.dir)
	}
	if len(report.Entries) != len(fixture.files) {
		t.Errorf("the report holds %d entries and the render holds %d files: %v",
			len(report.Entries), len(fixture.files), entryPaths(report.Entries))
	}
	for _, entry := range report.Entries {
		if entry.Status != install.StatusMatch {
			t.Errorf("%s is %q, want %q", entry.Path, entry.Status, install.StatusMatch)
		}
	}
	// The render is the whole installed layer, so the fixture is only a
	// fixture if it produced the artifacts an operator would recognise.
	for _, want := range []string{".claude/AGENTS.md", ".claude/CLAUDE.md", ".claude/settings.json", ".claude/skills/alpha/SKILL.md"} {
		if entry := entryAt(t, report, want); entry.Status != install.StatusMatch {
			t.Errorf("%s is %q", want, entry.Status)
		}
	}
}

func TestCodexInstallPreservesUnrelatedConfigAuthPluginsAndSkills(t *testing.T) {
	t.Parallel()
	rootDir := t.TempDir()
	root, err := install.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	source := writeCheckSkills(t, []string{"capture"})
	shared := filepath.ToSlash(t.TempDir())
	lay := &install.Layer{
		Root: root,
		Profile: &profile.Resolved{
			ID:       "base",
			Provider: profile.ProviderCodex,
			Spec: checkSpec(t, map[string]any{
				"settings":   map[string]any{"codex": map[string]any{"model": "gpt-5"}},
				"access":     map[string]any{"directories": []string{shared}},
				"install":    map[string]any{"skills": []string{"capture"}},
				"skills_dir": source,
			}),
		},
		Templates: map[string]string{"AGENTS.md": "# base\n"},
	}

	writeInRoot(t, rootDir, ".codex/config.toml", `service_tier = "priority"

[mcp_servers.mux]
command = "/Users/chrispian/go/bin/mux"

[mcp_servers.mux.tools.torque_health]
approval_mode = "auto"
`)
	writeInRoot(t, rootDir, ".codex/auth.json", `{"token":"synthetic"}`)
	writeInRoot(t, rootDir, ".codex/plugins/cache/plugin.json", `{"name":"keep"}`)
	writeInRoot(t, rootDir, ".agents/skills/operator/SKILL.md", "# Operator\n")

	if _, err := install.Install(lay); err != nil {
		t.Fatalf("Install codex layer: %v", err)
	}
	config := readFile(t, filepath.Join(rootDir, ".codex", "config.toml"))
	for _, want := range []string{
		`service_tier = 'priority'`,
		`model = 'gpt-5'`,
		`[mcp_servers.mux]`,
		`[mcp_servers.mux.tools.torque_health]`,
		`[sandbox_workspace_write]`,
	} {
		if !strings.Contains(config, want) {
			t.Errorf("config.toml missing %q:\n%s", want, config)
		}
	}
	for _, rel := range []string{
		".codex/auth.json",
		".codex/plugins/cache/plugin.json",
		".agents/skills/operator/SKILL.md",
	} {
		if _, err := os.Stat(filepath.Join(rootDir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("unrelated %s was not preserved: %v", rel, err)
		}
	}
	report, err := install.Check(lay)
	if err != nil {
		t.Fatalf("Check codex layer: %v", err)
	}
	if !report.Clean() {
		t.Errorf("Check after Codex install is not clean:\n%s", report)
	}
	if got := entryAt(t, report, ".agents/skills/operator").Status; got != install.StatusUnclaimed {
		t.Errorf("operator skill status = %s, want unclaimed", got)
	}
}

func TestCodexInstallRefusesMalformedExistingConfig(t *testing.T) {
	t.Parallel()
	rootDir := t.TempDir()
	root, err := install.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	lay := &install.Layer{
		Root: root,
		Profile: &profile.Resolved{
			ID:       "base",
			Provider: profile.ProviderCodex,
			Spec: checkSpec(t, map[string]any{
				"settings": map[string]any{"codex": map[string]any{"model": "gpt-5"}},
			}),
		},
		Templates: map[string]string{"AGENTS.md": "# base\n"},
	}

	configPath := writeInRoot(t, rootDir, ".codex/config.toml", `model = "user-owned"
broken = [
`)

	_, err = install.Install(lay)
	if !errors.Is(err, install.ErrInvalidExistingTOML) {
		t.Fatalf("Install with malformed existing Codex config = %v, want ErrInvalidExistingTOML", err)
	}
	if got := readFile(t, configPath); got != "model = \"user-owned\"\nbroken = [\n" {
		t.Fatalf("Install rewrote the malformed existing config:\n%s", got)
	}
}

func TestCheckReportsMalformedExistingCodexConfig(t *testing.T) {
	t.Parallel()
	rootDir := t.TempDir()
	root, err := install.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	lay := &install.Layer{
		Root: root,
		Profile: &profile.Resolved{
			ID:       "base",
			Provider: profile.ProviderCodex,
			Spec: checkSpec(t, map[string]any{
				"settings": map[string]any{"codex": map[string]any{"model": "gpt-5"}},
			}),
		},
		Templates: map[string]string{"AGENTS.md": "# base\n"},
	}
	writeInRoot(t, rootDir, ".codex/config.toml", "model = [\n")

	report, err := install.Check(lay)
	if err != nil {
		t.Fatalf("Check codex layer: %v", err)
	}
	entry := entryAt(t, report, ".codex/config.toml")
	if entry.Status != install.StatusModified {
		t.Fatalf("config status = %s, want modified\n%s", entry.Status, report)
	}
	if !strings.Contains(entry.Detail, install.ErrInvalidExistingTOML.Error()) {
		t.Errorf("config detail = %q, want malformed TOML detail", entry.Detail)
	}
}

func TestCheckLeavesExistingCodexConfigUnclaimedWhenTheProfileRendersNone(t *testing.T) {
	t.Parallel()
	rootDir := t.TempDir()
	root, err := install.NewRoot(rootDir)
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	lay := &install.Layer{
		Root: root,
		Profile: &profile.Resolved{
			ID:       "base",
			Provider: profile.ProviderCodex,
			Spec: checkSpec(t, map[string]any{
				"templates": map[string]any{"AGENTS.md": "codex base"},
			}),
		},
		Templates: map[string]string{"AGENTS.md": "# base\n"},
	}

	if _, err := install.Install(lay); err != nil {
		t.Fatalf("Install codex layer: %v", err)
	}
	writeInRoot(t, rootDir, ".codex/config.toml", `model = "user-owned"
`)

	report, err := install.Check(lay)
	if err != nil {
		t.Fatalf("Check codex layer: %v", err)
	}
	if !report.Clean() {
		t.Errorf("Check should not treat an undeclared Codex config as drift:\n%s", report)
	}
	for _, entry := range report.Entries {
		if entry.Path == ".codex/config.toml" {
			t.Errorf("Codex config was reported as %s; want it unreported when the profile renders none", entry.Status)
		}
	}
}

func TestCheckReportsADeletedFileAsMissing(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	const gone = ".claude/settings.json"
	if err := os.Remove(fixture.path(gone)); err != nil {
		t.Fatalf("remove %s: %v", gone, err)
	}

	report := fixture.check(t)
	if got := pathsWithStatus(report, install.StatusMissing); !slices.Equal(got, []string{gone}) {
		t.Errorf("missing = %v, want [%s]", got, gone)
	}
	if report.Clean() {
		t.Error("Clean() = true with a file missing")
	}
	if got := report.ExitCode(); got == 0 {
		t.Error("ExitCode() = 0 with a file missing")
	}
	if detail := entryAt(t, report, gone).Detail; detail != "" {
		t.Errorf("Detail = %q, want empty: the status says everything", detail)
	}
	// The check repairs nothing.
	if _, err := os.Lstat(fixture.path(gone)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the check recreated %s", gone)
	}
}

func TestCheckReportsAnEditedFileAsModified(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	const edited = ".claude/AGENTS.md"
	const content = "# Base\n\nedited in place by the operator\n"
	if err := os.WriteFile(fixture.path(edited), []byte(content), 0o644); err != nil {
		t.Fatalf("edit %s: %v", edited, err)
	}

	report := fixture.check(t)
	if got := pathsWithStatus(report, install.StatusModified); !slices.Equal(got, []string{edited}) {
		t.Errorf("modified = %v, want [%s]", got, edited)
	}
	if detail := entryAt(t, report, edited).Detail; !strings.Contains(detail, "bytes on disk") {
		t.Errorf("Detail = %q, want it to name the bytes", detail)
	}
	if report.Count(install.StatusMatch) != len(fixture.files)-1 {
		t.Errorf("%d matched, want %d: one edit is one finding",
			report.Count(install.StatusMatch), len(fixture.files)-1)
	}
	// The check repairs nothing.
	got, err := os.ReadFile(fixture.path(edited))
	if err != nil {
		t.Fatalf("read %s back: %v", edited, err)
	}
	if string(got) != content {
		t.Error("the check rewrote the edited file")
	}
}

func TestCheckReportsASymlinkWhereItRendersAFile(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	const linked = ".claude/CLAUDE.md"
	target := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(target, []byte("@AGENTS.md\n"), 0o644); err != nil {
		t.Fatalf("write the link target: %v", err)
	}
	if err := os.Remove(fixture.path(linked)); err != nil {
		t.Fatalf("remove %s: %v", linked, err)
	}
	symlinkOrSkip(t, target, fixture.path(linked))

	report := fixture.check(t)
	if got := pathsWithStatus(report, install.StatusNotAFile); !slices.Equal(got, []string{linked}) {
		t.Errorf("not a file = %v, want [%s]", got, linked)
	}
	detail := entryAt(t, report, linked).Detail
	if !strings.Contains(detail, "symbolic link") {
		t.Errorf("Detail = %q, want it to say the path is a symbolic link", detail)
	}
	if !strings.Contains(detail, target) {
		t.Errorf("Detail = %q, want it to name the target %q", detail, target)
	}
}

func TestCheckReportsADirectoryWhereItRendersAFile(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t)
	const occupied = ".claude/settings.json"
	if err := os.Remove(fixture.path(occupied)); err != nil {
		t.Fatalf("remove %s: %v", occupied, err)
	}
	if err := os.Mkdir(fixture.path(occupied), 0o755); err != nil {
		t.Fatalf("put a directory at %s: %v", occupied, err)
	}

	report := fixture.check(t)
	entry := entryAt(t, report, occupied)
	if entry.Status != install.StatusNotAFile {
		t.Errorf("%s is %q, want %q", occupied, entry.Status, install.StatusNotAFile)
	}
	if entry.Detail != "a directory" {
		t.Errorf("Detail = %q, want it to name the directory", entry.Detail)
	}
}

// TestCheckReportsAStrayFileInASkillDirectoryItDeclares is the case claiming
// by name exists to keep, and the reason the sweep was not simply deleted.
//
// A declared skill's directory is cairn's whole: cairn wrote every file in it,
// so a file in it the render does not produce is a leftover — the skill's
// source dropped a reference document, say, and the copy is still on disk.
// That is a finding and the report says so.
func TestCheckReportsAStrayFileInASkillDirectoryItDeclares(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	stray := []string{
		".claude/skills/alpha/references/notes.md",
		".claude/skills/alpha/run.sh",
	}
	for _, rel := range stray {
		writeInRoot(t, fixture.dir, rel, "left behind by a source that no longer holds it\n")
	}

	report := fixture.check(t)
	if got := pathsWithStatus(report, install.StatusOrphan); !slices.Equal(got, stray) {
		t.Errorf("orphan = %v, want %v", got, stray)
	}
	if report.Clean() {
		t.Error("Clean() = true with an orphan inside a skill cairn fills")
	}
	if report.ExitCode() == 0 {
		t.Error("ExitCode() = 0 with an orphan inside a skill cairn fills")
	}
	// The check deletes nothing.
	for _, rel := range stray {
		if _, err := os.Lstat(fixture.path(rel)); err != nil {
			t.Errorf("the check removed %s: %v", rel, err)
		}
	}
}

// TestCheckLeavesAHandWrittenSkillAloneAndStillReportsIt is the rule this
// replaces the whole-directory sweep with.
//
// ~/.claude/skills is where the operator keeps their own skills, beside the
// ones their profile declares. Claiming the directory whole reported every one
// of them as drift on every run — the settings.local.json disease, one level
// down — so cairn claims the directories it was told to plant and nothing
// else.
//
// It is still named. Cairn says what it found in a directory it shares; only
// what cairn claims can fail the check.
func TestCheckLeavesAHandWrittenSkillAloneAndStillReportsIt(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	theirs := []string{
		".claude/skills/handwritten/SKILL.md",
		".claude/skills/handwritten/references/notes.md",
		".claude/skills/another/SKILL.md",
	}
	for _, rel := range theirs {
		writeInRoot(t, fixture.dir, rel, "the operator wrote this\n")
	}

	report := fixture.check(t)
	if !report.Clean() {
		t.Errorf("Clean() = false; a skill the operator wrote is not drift. Report:\n%s", report)
	}
	if got := report.ExitCode(); got != 0 {
		t.Errorf("ExitCode() = %d, want 0", got)
	}
	// Named once each, at the directory: the sweep says what it found and does
	// not walk into what is not cairn's.
	want := []string{".claude/skills/another", ".claude/skills/handwritten"}
	if got := pathsWithStatus(report, install.StatusUnclaimed); !slices.Equal(got, want) {
		t.Errorf("unclaimed = %v, want %v", got, want)
	}
	for _, entry := range report.Entries {
		if slices.Contains(theirs, entry.Path) {
			t.Errorf("%s was reported as %q; the sweep walked into a directory that is not cairn's",
				entry.Path, entry.Status)
		}
	}
	// The declared skill is still checked against the bytes.
	if entry := entryAt(t, report, ".claude/skills/alpha/"+bootdir.SkillFileName); entry.Status != install.StatusMatch {
		t.Errorf("the declared skill is %q, want %q", entry.Status, install.StatusMatch)
	}
}

// TestCheckOfAProfileDeclaringNoSkillsIsClean is the other half: a profile may
// declare no skills at all, and then nothing under the skills directory is
// cairn's.
//
// The directory is still read — every skill in it is named — and the report is
// still clean, because naming and claiming are now two different things.
func TestCheckOfAProfileDeclaringNoSkillsIsClean(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t)
	for _, file := range fixture.files {
		if strings.HasPrefix(file.Path, ".claude/skills/") {
			t.Fatalf("the fixture rendered %s; it must declare no skills", file.Path)
		}
	}
	for _, rel := range []string{
		".claude/skills/handwritten/SKILL.md",
		".claude/skills/handwritten/references/notes.md",
	} {
		writeInRoot(t, fixture.dir, rel, "the operator wrote this\n")
	}

	report := fixture.check(t)
	if !report.Clean() {
		t.Errorf("Clean() = false for a profile that declares no skills. Report:\n%s", report)
	}
	if got := report.ExitCode(); got != 0 {
		t.Errorf("ExitCode() = %d, want 0", got)
	}
	if got := pathsWithStatus(report, install.StatusUnclaimed); !slices.Equal(got, []string{".claude/skills/handwritten"}) {
		t.Errorf("unclaimed = %v, want [.claude/skills/handwritten]", got)
	}
	if n := report.Count(install.StatusOrphan); n != 0 {
		t.Errorf("%d orphans; a profile declaring no skills claims nothing under the skills directory", n)
	}
}

// TestInstallLeavesAHandWrittenSkillWhereItIs is the same rule through the
// write rather than the check: an install plants what the profile declares and
// touches nothing else in the directory it shares, and the check that follows
// is clean.
//
// It installs into a temporary root, never a home directory — see the note at
// the top of this file.
func TestInstallLeavesAHandWrittenSkillWhereItIs(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	const theirs = ".claude/skills/handwritten/SKILL.md"
	const body = "# A skill cairn never heard of\n"
	writeInRoot(t, fixture.dir, theirs, body)

	if _, err := install.Install(fixture.lay); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(fixture.path(theirs))
	if err != nil {
		t.Fatalf("the install removed %s: %v", theirs, err)
	}
	if string(got) != body {
		t.Errorf("%s holds %q, want %q: the install rewrote a skill it did not plant", theirs, got, body)
	}

	report := fixture.check(t)
	if !report.Clean() || report.ExitCode() != 0 {
		t.Errorf("--check after the install is unclean (exit %d):\n%s", report.ExitCode(), report)
	}
}

// TestCheckReportsAClaimedFileTheProfileStoppedDeclaring covers the orphan the
// claim list exists to find: a profile that used to declare settings and no
// longer does leaves a settings.json behind, and cairn wrote that file, so
// cairn reports it.
func TestCheckReportsAClaimedFileTheProfileStoppedDeclaring(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t)
	const left = ".claude/settings.json"
	if _, declared, err := fixture.lay.Profile.Spec.Settings(profile.ProviderClaude); err != nil || !declared {
		t.Fatalf("the fixture should declare settings for claude, so that dropping the key leaves one behind: %v", err)
	}

	// The layer was installed while the profile declared settings. Now it does
	// not, and the file it wrote is still there.
	delete(fixture.lay.Profile.Spec, "settings")
	if _, declared, err := fixture.lay.Profile.Spec.Settings(profile.ProviderClaude); err != nil || declared {
		t.Fatalf("the settings key was not dropped: %v, %v", declared, err)
	}

	report := fixture.check(t)
	if got := pathsWithStatus(report, install.StatusOrphan); !slices.Equal(got, []string{left}) {
		t.Errorf("orphan = %v, want [%s] — a file cairn wrote and no longer renders", got, left)
	}
}

// TestCheckIgnoresTheHarnessOwnStateFiles is the defect that would have made
// `--check` useless on a real installation.
//
// ~/.claude is a live harness's home. If the sweep read it one level deep and
// called every unrendered file an orphan, settings.local.json and
// .credentials.json would be reported on every run of every real installation,
// the exit code would be non-zero forever, and a gate that always fails is not
// a gate.
func TestCheckIgnoresTheHarnessOwnStateFiles(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	theirs := []string{
		".claude/settings.local.json",
		".claude/.credentials.json",
		".claude/history.jsonl",
	}
	for _, rel := range theirs {
		writeInRoot(t, fixture.dir, rel, "not cairn's\n")
	}

	report := fixture.check(t)
	if !report.Clean() {
		t.Errorf("Clean() = false; the harness's own files in its own directory are not cairn's. Report:\n%s", report)
	}
	for _, entry := range report.Entries {
		if slices.Contains(theirs, entry.Path) {
			t.Errorf("%s was reported as %q and belongs to the harness", entry.Path, entry.Status)
		}
	}
}

func TestCheckIgnoresWhatIsOutsideTheDirectoriesCairnOwns(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	outside := []string{
		// Cairn does not own the home directory.
		"notes.md",
		".config/other/config.json",
		// Nor anything in the provider directory that cairn does not itself
		// write: it is the harness's own home — session state, caches, one
		// directory per project.
		".claude/projects/some-project/session.jsonl",
		".claude/todos/list.json",
	}
	for _, rel := range outside {
		writeInRoot(t, fixture.dir, rel, "not cairn's\n")
	}

	report := fixture.check(t)
	if !report.Clean() {
		t.Errorf("Clean() = false; nothing outside cairn's directories is a finding. Report:\n%s", report)
	}
	for _, entry := range report.Entries {
		if slices.Contains(outside, entry.Path) {
			t.Errorf("%s was reported as %q and is not cairn's", entry.Path, entry.Status)
		}
	}
}

func TestCheckReportsAnUnreadablePathAndKeepsGoing(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode, so there is no unreadable path to make")
	}
	fixture := newCheckFixture(t, "alpha")
	const locked = ".claude/settings.json"
	dest := fixture.path(locked)
	if err := os.Chmod(dest, 0o000); err != nil {
		t.Fatalf("make %s unreadable: %v", locked, err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dest, 0o644); err != nil {
			t.Logf("restore the mode on %s: %v", locked, err)
		}
	})

	report := fixture.check(t)
	entry := entryAt(t, report, locked)
	if entry.Status != install.StatusUnreadable {
		t.Fatalf("%s is %q, want %q", locked, entry.Status, install.StatusUnreadable)
	}
	if entry.Detail == "" {
		t.Error("Detail is empty, want the read error")
	}
	// One unreadable path does not hide the rest of the layer.
	if got, want := len(report.Entries), len(fixture.files); got != want {
		t.Errorf("the report holds %d entries, want %d: %v", got, want, entryPaths(report.Entries))
	}
	if got, want := report.Count(install.StatusMatch), len(fixture.files)-1; got != want {
		t.Errorf("%d matched, want %d", got, want)
	}
	if report.Clean() {
		t.Error("Clean() = true with an unreadable path")
	}
}

func TestCheckFSReadsAFilesystemItIsNotStandingIn(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	// A synthetic filesystem, holding the render plus one leftover inside the
	// skill the profile declares. Nothing here is on disk anywhere.
	fsys := fstest.MapFS{}
	for _, file := range fixture.files {
		fsys[file.Path] = &fstest.MapFile{Data: file.Content, Mode: 0o644}
	}
	const stray = ".claude/skills/alpha/references/notes.md"
	fsys[stray] = &fstest.MapFile{Data: []byte("left behind\n"), Mode: 0o644}
	delete(fsys, ".claude/CLAUDE.md")

	report, err := install.CheckFS(fsys, fixture.lay)
	if err != nil {
		t.Fatalf("CheckFS: %v", err)
	}
	if report.Root != "" {
		t.Errorf("Root = %q, want empty: a filesystem view names no location", report.Root)
	}
	if got := pathsWithStatus(report, install.StatusMissing); !slices.Equal(got, []string{".claude/CLAUDE.md"}) {
		t.Errorf("missing = %v, want [.claude/CLAUDE.md]", got)
	}
	if got := pathsWithStatus(report, install.StatusOrphan); !slices.Equal(got, []string{stray}) {
		t.Errorf("orphan = %v, want [%s]", got, stray)
	}
}

func TestCheckFSRefusesWhatItCannotCheck(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t)
	if _, err := install.CheckFS(nil, fixture.lay); err == nil {
		t.Error("CheckFS(nil, layer) returned no error")
	}
	fsys, err := fixture.lay.Root.FS()
	if err != nil {
		t.Fatalf("Root.FS(): %v", err)
	}
	if _, err := install.CheckFS(fsys, nil); !errors.Is(err, install.ErrNoProfile) {
		t.Errorf("CheckFS(fs, nil) = %v, want ErrNoProfile", err)
	}
	if _, err := install.CheckFS(fsys, &install.Layer{}); !errors.Is(err, install.ErrNoProfile) {
		t.Errorf("CheckFS(fs, empty layer) = %v, want ErrNoProfile", err)
	}
}

func TestCheckRefusesARootItCannotRead(t *testing.T) {
	t.Parallel()
	if _, err := install.Check(nil); !errors.Is(err, install.ErrNoProfile) {
		t.Errorf("Check(nil) = %v, want ErrNoProfile", err)
	}
	fixture := newCheckFixture(t)
	root, err := install.NewRoot(filepath.Join(fixture.dir, "not-a-directory-that-exists"))
	if err != nil {
		t.Fatalf("NewRoot: %v", err)
	}
	lay := &install.Layer{Root: root, Profile: fixture.lay.Profile, Home: fixture.lay.Home}
	if _, err := install.Check(lay); !errors.Is(err, install.ErrRootNotFound) {
		t.Errorf("Check(missing root) = %v, want ErrRootNotFound", err)
	}
}

func TestCheckReportsALinkTheSweepFindsWithoutFollowingIt(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	// Two directories of someone else's, linked into the skills tree: one
	// inside a skill cairn fills, one beside it. Cairn renders bytes and has
	// no way to emit a link, so neither is ever cairn's output — and neither
	// is descended into, whatever is on the far end.
	target := t.TempDir()
	writeInRoot(t, target, "SKILL.md", "# not cairn's\n")
	writeInRoot(t, target, "references/deep.md", "# also not cairn's\n")

	const inside = ".claude/skills/alpha/linked"
	symlinkOrSkip(t, target, fixture.path(inside))
	const beside = ".claude/skills/linked"
	symlinkOrSkip(t, target, fixture.path(beside))

	report := fixture.check(t)
	// Inside a directory cairn fills, a link is a leftover: cairn claims that
	// path and did not write this.
	if got := pathsWithStatus(report, install.StatusOrphan); !slices.Equal(got, []string{inside}) {
		t.Errorf("orphan = %v, want [%s]: the link is reported and not walked", got, inside)
	}
	// Beside it, in the directory cairn shares, it is the operator's.
	if got := pathsWithStatus(report, install.StatusUnclaimed); !slices.Equal(got, []string{beside}) {
		t.Errorf("unclaimed = %v, want [%s]", got, beside)
	}
	for _, linked := range []string{inside, beside} {
		if detail := entryAt(t, report, linked).Detail; !strings.Contains(detail, target) {
			t.Errorf("Detail for %s = %q, want it to name the target %q", linked, detail, target)
		}
	}
	// Neither link was followed: nothing on the far end is in the report.
	for _, entry := range report.Entries {
		if strings.Contains(entry.Path, "references/deep.md") {
			t.Errorf("the sweep walked a symbolic link: %s", entry.Path)
		}
	}
}

func TestCheckClassifiesEachPathOnce(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	writeInRoot(t, fixture.dir, ".claude/skills/stale/SKILL.md", "left behind\n")

	report := fixture.check(t)
	seen := make(map[string]int, len(report.Entries))
	for _, entry := range report.Entries {
		seen[entry.Path]++
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("%s appears %d times; the manifest half and the sweep must not both claim it", p, n)
		}
	}
	// The rendered skill file lives inside the swept tree and is classified by
	// the first half, against the bytes.
	if entry := entryAt(t, report, ".claude/skills/alpha/"+bootdir.SkillFileName); entry.Status != install.StatusMatch {
		t.Errorf("the rendered skill file is %q, want %q", entry.Status, install.StatusMatch)
	}
}

func TestCheckLeavesTheRootExactlyAsItFoundIt(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t, "alpha")
	if err := os.Remove(fixture.path(".claude/settings.json")); err != nil {
		t.Fatalf("remove the settings document: %v", err)
	}
	if err := os.WriteFile(fixture.path(".claude/AGENTS.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatalf("edit the instruction file: %v", err)
	}
	writeInRoot(t, fixture.dir, ".claude/skills/stale/SKILL.md", "left behind\n")
	before := treeOf(t, fixture.dir)

	if report := fixture.check(t); report.Clean() {
		t.Fatal("the fixture is broken: the report is clean")
	}
	if after := treeOf(t, fixture.dir); !slices.Equal(before, after) {
		t.Errorf("the check changed the root.\nbefore: %v\nafter:  %v", before, after)
	}
}

// treeOf returns every path beneath dir with its size, so a test can assert
// that a check wrote, deleted and rewrote nothing.
func treeOf(t *testing.T, dir string) []string {
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
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, path.Join(filepath.ToSlash(rel), info.Mode().String())+":"+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	slices.Sort(out)
	return out
}

// TestCheckIsStableForAProfileWhoseSlotsReadLiveState is the property that
// decided which slots the installed layer resolves.
//
// A check re-renders and diffs the result against disk. A cmd slot reading
// `git status` answers differently between two renders of one profile, so
// resolving it here would report drift on every invocation forever — a gate
// configured not to gate, which is the disease plan §5 names for the orphan
// sweep. The static half still composes, which is what makes an installed
// template worth writing at all.
func TestCheckIsStableForAProfileWhoseSlotsReadLiveState(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t)

	// A slot whose answer changes every time it is asked, beside one that does
	// not, both referenced from the installed instruction file.
	counter := filepath.Join(t.TempDir(), "counter.sh")
	if err := os.WriteFile(counter, []byte("#!/bin/sh\ndate +%s%N\n"), 0o755); err != nil {
		t.Fatalf("write the script: %v", err)
	}
	fixture.lay.Profile.Spec["slots"] = json.RawMessage(`[
		{"name":"prose","source":{"kind":"inline","inline":{"content":"shared prose"}}},
		{"name":"now","source":{"kind":"cmd","cmd":{"run":"` + counter + `"}}}
	]`)
	fixture.lay.Templates["AGENTS.md"] =
		"<!-- cairn:slot prose -->\n\n<!-- cairn:slot now -->\n"

	assembled, err := slots.Assemble(context.Background(), fixture.lay.Profile.Spec,
		slots.Options{Deterministic: true})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	sections, err := slots.Sections(assembled)
	if err != nil {
		t.Fatalf("Sections: %v", err)
	}
	fixture.lay.Sections = sections

	files, err := install.Render(fixture.lay)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	fixture.files = files
	fixture.plant(t)

	// The static half composed.
	agents := string(renderedContent(t, files, ".claude/AGENTS.md"))
	if !strings.Contains(agents, "shared prose") {
		t.Errorf("the installed instruction file did not compose its static slot:\n%s", agents)
	}

	// And the check is stable, however many times it is run.
	for i := range 3 {
		report, err := install.Check(fixture.lay)
		if err != nil {
			t.Fatalf("Check %d: %v", i, err)
		}
		if code := report.ExitCode(); code != 0 {
			t.Fatalf("check %d reported drift on a profile nobody changed:\n%s", i, report.String())
		}
	}
}

// renderedContent returns the bytes rendered at rel.
func renderedContent(t *testing.T, files []install.File, rel string) []byte {
	t.Helper()
	for _, f := range files {
		if f.Path == rel {
			return f.Content
		}
	}
	t.Fatalf("no file was rendered at %q", rel)
	return nil
}

// TestCheckForgivesTheHarnessRelayingOutTheSettingsDocument is the reason
// [install.Renderer].Normalize exists.
//
// Claude Code rewrites the settings document it was handed. Before the two
// layouts agreed, a byte comparison reported ~/.claude/settings.json as
// modified on every run of every real installation — drift over whitespace,
// which is the same disease as a lint gate configured not to fail. What the
// harness moved is forgiven; what it changed is not, and the test after this
// one is that half.
func TestCheckForgivesTheHarnessRelayingOutTheSettingsDocument(t *testing.T) {
	t.Parallel()
	fixture := newCheckFixture(t)
	const settings = ".claude/settings.json"
	// The same document, spelled the way nothing renders it: compact, on one
	// line, with no trailing newline.
	if err := os.WriteFile(fixture.path(settings), []byte(`{"model":"opus"}`), 0o644); err != nil {
		t.Fatalf("rewrite %s: %v", settings, err)
	}

	report := fixture.check(t)
	if got := entryAt(t, report, settings).Status; got != install.StatusMatch {
		t.Errorf("%s = %q, want %q: the document is the same one, laid out differently",
			settings, got, install.StatusMatch)
	}
	if !report.Clean() {
		t.Errorf("the report is not clean: %v", entryPaths(report.Entries))
	}
	// The check repairs nothing, so the operator's own layout survives it.
	got, err := os.ReadFile(fixture.path(settings))
	if err != nil {
		t.Fatalf("read %s back: %v", settings, err)
	}
	if string(got) != `{"model":"opus"}` {
		t.Errorf("the check rewrote %s to %q", settings, got)
	}
}

// TestCheckStillReportsAChangedSettingsDocument is the other half, and it is
// what keeps the two forgivenesses above and below from being a hole.
//
// Normalizing moves whitespace and nothing else; the merge covers the keys
// cairn declares and nothing else. Between them, every edit to a key cairn
// declared is still a finding — and so is a document the merge cannot read
// member by member, because a file cairn cannot parse is not a file it can
// claim part of.
func TestCheckStillReportsAChangedSettingsDocument(t *testing.T) {
	t.Parallel()
	const settings = ".claude/settings.json"
	for _, edit := range []struct {
		name     string
		document string
	}{
		{"a changed value", "{\n  \"model\": \"haiku\"\n}\n"},
		{"a removed key", "{}\n"},
		{"a document that is not JSON at all", "not a settings document\n"},
		{"a document that is not an object", "[\"model\"]\n"},
		{"content after the object", "{\n  \"model\": \"opus\"\n}\nand then some\n"},
		// The declared key twice. Go's decoder resolves that silently and the
		// merge refuses to, so the file is reported rather than collapsed —
		// which matters here more than anywhere, since permissions live in it.
		{"a key declared twice", "{\n  \"model\": \"opus\",\n  \"model\": \"haiku\"\n}\n"},
	} {
		t.Run(edit.name, func(t *testing.T) {
			t.Parallel()
			fixture := newCheckFixture(t)
			if err := os.WriteFile(fixture.path(settings), []byte(edit.document), 0o644); err != nil {
				t.Fatalf("rewrite %s: %v", settings, err)
			}

			report := fixture.check(t)
			if got := entryAt(t, report, settings).Status; got != install.StatusModified {
				t.Errorf("%s = %q, want %q", settings, got, install.StatusModified)
			}
		})
	}
}

// TestCheckIsQuietAboutASettingsKeyCairnNeverDeclared is the check half of
// T19, and it is the case the `--check` before this change could not express.
//
// ~/.claude/settings.json is a document the operator and the harness write
// too. Cairn declares nine keys of the live one and rendered every one of
// them; the tenth, `model`, was the operator's, and the render-and-overwrite
// install deleted it rather than reporting it. Reporting it would have been no
// better — it is not cairn's, an install now leaves it alone, and a check that
// named it would be crying wolf about a file cairn shares. So: not written,
// and not reported.
//
// The nested case is the one that matters most. Cairn declares
// permissions.defaultMode, and the operator's own rules sit in permissions
// beside it.
func TestCheckIsQuietAboutASettingsKeyCairnNeverDeclared(t *testing.T) {
	t.Parallel()
	const settings = ".claude/settings.json"
	for _, kept := range []struct {
		name string
		// declared is the settings document the profile declares; nil is the
		// fixture's own one-key default.
		declared map[string]any
		document string
	}{
		{name: "beside the keys cairn declares", document: `{"model":"opus","tui":"fullscreen"}`},
		{name: "before them", document: `{"tui":"fullscreen","model":"opus"}`},
		{
			name:     "inside a key cairn declares",
			declared: map[string]any{"permissions": map[string]any{"defaultMode": "auto"}},
			document: `{"permissions":{"allow":["Bash(ls:*)"],"defaultMode":"auto"}}`,
		},
	} {
		t.Run(kept.name, func(t *testing.T) {
			t.Parallel()
			declared := kept.declared
			if declared == nil {
				declared = map[string]any{"model": "opus"}
			}
			fixture := newCheckFixtureDeclaring(t, declared)
			if err := os.WriteFile(fixture.path(settings), []byte(kept.document), 0o644); err != nil {
				t.Fatalf("rewrite %s: %v", settings, err)
			}

			report := fixture.check(t)
			if got := entryAt(t, report, settings).Status; got != install.StatusMatch {
				t.Errorf("%s = %q, want %q: the added key is not cairn's",
					settings, got, install.StatusMatch)
			}
			if !report.Clean() {
				t.Errorf("the report is not clean: %v", entryPaths(report.Entries))
			}
			// The check repairs nothing, here least of all.
			got, err := os.ReadFile(fixture.path(settings))
			if err != nil {
				t.Fatalf("read %s back: %v", settings, err)
			}
			if string(got) != kept.document {
				t.Errorf("the check rewrote %s to %q", settings, got)
			}
		})
	}
}
