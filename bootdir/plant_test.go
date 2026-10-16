package bootdir_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/cairn/bootdir"
	"github.com/hollis-labs/libs/harness/core/agentlaunch/planting"
)

// TestPlantFilesWritesModesPastTheUmask covers the three properties the write
// boundary exists for: the planted mode is the rendered mode and not whatever
// the launching shell's umask left of it, the directory is readable by a
// harness that is not this process, and the manifest reports what was written.
func TestPlantFilesWritesModesPastTheUmask(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "eng", "s1")
	files := []bootdir.File{
		{Path: "AGENTS.md", Content: []byte("hi\n")},
		{Path: ".claude/skills/cr/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0o755},
		{Path: ".claude/settings.json", Content: []byte("{}\n"), Mode: 0o600},
	}
	res, err := bootdir.PlantFiles(context.Background(), dir, files)
	if err != nil {
		t.Fatalf("plant: %v", err)
	}
	if len(res.PlantedFiles) != len(files) {
		t.Errorf("the manifest lists %d files, want %d: %v", len(res.PlantedFiles), len(files), res.PlantedFiles)
	}
	for rel, want := range map[string]os.FileMode{
		"AGENTS.md":                0o644,
		".claude/skills/cr/run.sh": 0o755,
		".claude/settings.json":    0o600,
	} {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s planted with mode %v, want %v — the umask reached it", rel, got, want)
		}
	}
	// os.MkdirTemp creates 0700, and the harness reading this directory is not
	// necessarily this process.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat the boot directory: %v", err)
	}
	if got := info.Mode().Perm(); got != bootdir.DefaultDirMode.Perm() {
		t.Errorf("the boot directory is mode %v, want %v", got, bootdir.DefaultDirMode.Perm())
	}
}

// TestPlantFilesRefusesAnExistingDirectory covers ErrExists. A boot directory
// is per-session and disposable; planting over one would leave an unknowable
// mixture of two materializations.
func TestPlantFilesRefusesAnExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "eng", "s1")
	files := []bootdir.File{{Path: "AGENTS.md", Content: []byte("hi\n")}}
	if _, err := bootdir.PlantFiles(context.Background(), dir, files); err != nil {
		t.Fatalf("first plant: %v", err)
	}
	if _, err := bootdir.PlantFiles(context.Background(), dir, files); !errors.Is(err, bootdir.ErrExists) {
		t.Errorf("planting over an existing directory = %v, want ErrExists", err)
	}
}

// TestPlantFilesIsAllOrNothing covers the reason the tree is staged beside the
// target rather than written into it: a plant that fails partway must leave no
// directory at the target and no half-built tree beside it, because a
// half-built boot directory is one an agent might boot from.
func TestPlantFilesIsAllOrNothing(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "s1")

	// The second file cannot be written: its parent path is the first file.
	_, err := bootdir.PlantFiles(context.Background(), dir, []bootdir.File{
		{Path: "a/b", Content: []byte("x")},
		{Path: "a/b/c", Content: []byte("y")},
	})
	if err == nil {
		t.Fatal("a plant that could not write every file reported success")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed plant left %s behind: %v", dir, err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read %s: %v", parent, err)
	}
	for _, e := range entries {
		t.Errorf("a failed plant left %s beside the target", e.Name())
	}
}

// TestPlanterSpeaksThePortfolioContract covers the planting.Planter adapter: the
// members of a planting.PlantSpec cairn can place, and the honest refusal of the ones
// it cannot.
func TestPlanterSpeaksThePortfolioContract(t *testing.T) {
	lay, err := bootdir.LayoutFor("claude")
	if err != nil {
		t.Fatalf("claude layout: %v", err)
	}
	p := bootdir.Planter{Layout: lay}
	dir := filepath.Join(t.TempDir(), "s1")

	res, err := p.Plant(context.Background(), dir, planting.PlantSpec{
		Files:            map[string][]byte{"AGENTS.md": []byte("hi\n")},
		MCPConfig:        []byte("{}\n"),
		ProviderSettings: map[string][]byte{"claude": []byte("{}\n")},
	})
	if err != nil {
		t.Fatalf("plant through the portfolio contract: %v", err)
	}
	if len(res.PlantedFiles) != 3 {
		t.Errorf("planted %v, want three files", res.PlantedFiles)
	}
	for _, rel := range []string{"AGENTS.md", lay.MCP.RelPath, lay.Settings.RelPath} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not planted: %v", rel, err)
		}
	}

	// Hooks and a recovery prompt are refused rather than dropped: a caller
	// handing cairn hooks and receiving a directory without them has no way to
	// tell.
	for name, spec := range map[string]planting.PlantSpec{
		"hooks":             {Hooks: []planting.PlantHook{{Provider: "claude", Name: "PreToolUse"}}},
		"a recovery prompt": {RecoveryPrompt: "resume"},
		"another provider's settings": {
			ProviderSettings: map[string][]byte{"codex": []byte("{}")},
		},
	} {
		if _, err := p.Plant(context.Background(), filepath.Join(t.TempDir(), "x"), spec); !errors.Is(err, bootdir.ErrUnsupportedPlantSpec) {
			t.Errorf("a spec carrying %s = %v, want ErrUnsupportedPlantSpec", name, err)
		}
	}
}
