package bootdir

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/chrispian/cairn/profile"
)

// contractInstance returns an instance whose profile declares every artifact
// of the output contract at once, so that a test can assert the whole boot
// directory rather than one renderer's share of it.
func contractInstance(t *testing.T) *Instance {
	t.Helper()
	source := t.TempDir()
	writeSkillTree(t, source, "code-review", map[string]string{
		SkillFileName:             "# code review\n",
		"references/checklist.md": "- read the diff\n",
	})

	manifest, err := json.Marshal(map[string]any{
		profile.SpecKeyMCP: []map[string]any{
			{"name": "notes", "command": "notes-server", "args": []string{"--stdio"}},
		},
		profile.SpecKeySkills:    []string{"code-review"},
		profile.SpecKeySkillsDir: source,
		profile.SpecKeySettings:  json.RawMessage(`{"claude": {"model": "opus"}}`),
		profile.SpecKeyFiles: map[string]any{
			"notes/decisions.md": "a planted note\n",
			// A source, not a literal — declared here so the fixture carries
			// the shape a real manifest does, and resolved onto the instance
			// below the way the composition root resolves it.
			"tasks/T-1/task.md": map[string]any{"kind": "cmd", "cmd": map[string]any{"run": "torque task get T-1"}},
		},
		profile.SpecKeySubagents: []string{"scribe"},
		profile.SpecKeyTemplates: map[string]any{
			"AGENTS.md": "declared elsewhere and resolved onto the instance",
			"CLAUDE.md": "so is this",
			"boot.md":   "and this",
		},
		"a key nothing renders": "carried and ignored",
	})
	if err != nil {
		t.Fatalf("encode the manifest: %v", err)
	}

	inst := testInstance(t, profile.Resolved{
		ID:          "reviewer",
		Name:        "Reviewer",
		Description: "Reviews changes before they land.",
		Provider:    profile.ProviderClaude,
		Model:       "claude-sonnet-4-5",
		Body:        "You read diffs.\n",
		Spec:        testSpec(t, string(manifest)),
	})
	inst.Scope = "/Users/chrispian/dev/projects/cairn"
	// Templates reach a renderer already resolved, for the same reason files
	// do, and carry the markers the renderer substitutes. Nothing here is a
	// file cairn named: every destination came out of the manifest.
	inst.Templates = map[string]string{
		"AGENTS.md": "# <!-- cairn:value profile -->\n\n<!-- cairn:slot repo -->\n",
		"CLAUDE.md": "@" + "AGENTS.md" + "\n",
		"boot.md":   "<!-- cairn:slot repo -->\n",
	}
	inst.Sections = map[string]string{"repo": "## repo\n\nthe assembled slot content"}
	inst.Values = map[string]string{"profile": "reviewer", "scope": "/Users/chrispian/dev/projects/cairn"}
	// Files reach a renderer already resolved: a manifest entry may name a
	// slot source, and resolving one runs commands and makes requests, which a
	// renderer may not do.
	inst.Files = map[string]string{
		"notes/decisions.md": "a planted note\n",
		"tasks/T-1/task.md":  "# T-1\n\nin progress\n",
	}
	// Subagent declarations reach a renderer already resolved, for the same
	// reason: reading the profile a manifest names is a trip to the catalog.
	inst.Subagents = []Subagent{{
		ID:          "scribe",
		Declaration: json.RawMessage(`{"description": "Writes the decision down.", "body": "You write it down.\n"}`),
	}}
	return inst
}

// TestRenderProducesTheOutputContract is the whole of docs/plan.md §5 as one
// assertion: every artifact, at the path its harness reads it from, in render
// order, from one profile that declares all of them.
func TestRenderProducesTheOutputContract(t *testing.T) {
	inst := contractInstance(t)

	files, err := Render(inst)
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}
	want := []string{
		"AGENTS.md",
		"CLAUDE.md",
		"boot.md",
		".mcp.json",
		".claude/settings.json",
		".claude/skills/code-review/SKILL.md",
		".claude/skills/code-review/references/checklist.md",
		".claude/agents/scribe.md",
		"notes/decisions.md",
		"tasks/T-1/task.md",
	}
	if got := filePaths(files); !slices.Equal(got, want) {
		t.Fatalf("Render() produced\n%v\nwant\n%v", got, want)
	}
	for _, f := range files {
		if len(f.Content) == 0 {
			t.Errorf("%s was rendered with no bytes", f.Path)
		}
	}
	if got, want := string(fileByPath(t, files, "CLAUDE.md").Content), "@"+"AGENTS.md"+"\n"; got != want {
		t.Errorf("the pointer holds %q, want the template's own text %q", got, want)
	}
	// The instruction file is a template like any other: a value marker became
	// the profile id and a slot marker became that slot's whole section.
	if got, want := string(fileByPath(t, files, "AGENTS.md").Content),
		"# reviewer\n\n## repo\n\nthe assembled slot content\n"; got != want {
		t.Errorf("the instruction file holds\n%q\nwant\n%q", got, want)
	}
	// The settings document is the operator's stored one plus the single thing
	// cairn contributes to it: this instance works in a scope, so the harness
	// is told it may reach it. The expectation is spelled out rather than
	// composed through the merge under test, so a change in what cairn grants
	// fails here instead of agreeing with itself.
	wantSettings := "{\n  \"model\": \"opus\",\n  \"permissions\": {\n    \"additionalDirectories\": [\n      \"" +
		inst.Scope + "\"\n    ]\n  }\n}\n"
	if got := string(fileByPath(t, files, ".claude/settings.json").Content); got != wantSettings {
		t.Errorf("the settings document holds\n%s\nwant\n%s", got, wantSettings)
	}
}

// TestRenderIsByteStable states the determinism contract over the whole
// rendering rather than over one artifact of it: same instance, same bytes,
// same order, however many times it is rendered.
func TestRenderIsByteStable(t *testing.T) {
	inst := contractInstance(t)

	first, err := Render(inst)
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}
	for i := range 16 {
		again, err := Render(inst)
		if err != nil {
			t.Fatalf("Render() on render %d: %v", i, err)
		}
		if len(again) != len(first) {
			t.Fatalf("render %d produced %v, want %v", i, filePaths(again), filePaths(first))
		}
		for j := range again {
			if again[j].Path != first[j].Path {
				t.Fatalf("render %d file %d is %q, want %q", i, j, again[j].Path, first[j].Path)
			}
			if string(again[j].Content) != string(first[j].Content) {
				t.Fatalf("render %d changed %s", i, again[j].Path)
			}
			if again[j].Mode != first[j].Mode {
				t.Fatalf("render %d changed the mode of %s", i, again[j].Path)
			}
		}
	}
}

// TestRenderRefusesTwoFilesAtOnePath covers the collision the manifest's
// escape hatch makes reachable: a declared file at the path an artifact
// already claims. Which one won would depend on renderer order, so neither
// does.
func TestRenderRefusesTwoFilesAtOnePath(t *testing.T) {
	taken := []string{
		"AGENTS.md", "CLAUDE.md", "boot.md", ".mcp.json",
		".claude/skills/code-review/SKILL.md",
		".claude/agents/scribe.md",
	}
	for _, taken := range taken {
		inst := contractInstance(t)
		inst.Files[taken] = "a second file at a path an artifact already claims"

		_, err := Render(inst)
		if !errors.Is(err, ErrDuplicatePath) {
			t.Errorf("Render() with a second file at %s returned error %v, want ErrDuplicatePath",
				taken, err)
		}
		if err != nil && !strings.Contains(err.Error(), taken) {
			t.Errorf("the error %q does not name %s", err, taken)
		}
	}
}

// TestRenderersAreRegisteredOnce guards the registration list itself: every
// entry renders something and names itself, and no two entries share a label,
// so a diagnostic naming an artifact names one renderer.
func TestRenderersAreRegisteredOnce(t *testing.T) {
	seen := make(map[string]struct{})
	renderers, err := Renderers(testLayout(t))
	if err != nil {
		t.Fatalf("Renderers(): %v", err)
	}
	for i, renderer := range renderers {
		if renderer.Render == nil {
			t.Errorf("the renderer at index %d has no render function", i)
		}
		if strings.TrimSpace(renderer.Artifact) == "" {
			t.Errorf("the renderer at index %d has no artifact label", i)
			continue
		}
		if _, duplicate := seen[renderer.Artifact]; duplicate {
			t.Errorf("two renderers are labelled %q", renderer.Artifact)
		}
		seen[renderer.Artifact] = struct{}{}
	}
	if len(seen) != 8 {
		t.Errorf("%d artifacts are registered, want the 8 of the output contract: %v",
			len(seen), seen)
	}
}

// TestRenderNeedsAResolvedProfile covers the one input every renderer derives
// from. Each guards it in its own right, so a renderer called directly reports
// the same thing [Render] does.
func TestRenderNeedsAResolvedProfile(t *testing.T) {
	if _, err := Render(&Instance{Layout: testLayout(t)}); !errors.Is(err, ErrNoProfile) {
		t.Errorf("Render() with no profile returned error %v, want ErrNoProfile", err)
	}
	renderers, err := Renderers(testLayout(t))
	if err != nil {
		t.Fatalf("Renderers(): %v", err)
	}
	for _, renderer := range renderers {
		if _, err := renderer.Render(&Instance{}); !errors.Is(err, ErrNoProfile) {
			t.Errorf("%s rendered from no profile and returned error %v, want ErrNoProfile",
				renderer.Artifact, err)
		}
	}
}

// TestRenderCarriesAnUnknownManifestKeyPastEveryRenderer states the rule from
// docs/plan.md §3 at the point it matters: a key nothing renders reaches the
// rendering and changes nothing about it.
func TestRenderCarriesAnUnknownManifestKeyPastEveryRenderer(t *testing.T) {
	inst := contractInstance(t)
	before, err := Render(inst)
	if err != nil {
		t.Fatalf("Render(): %v", err)
	}
	inst.Profile.Spec["another key nothing renders"] = json.RawMessage(`{"deeply": ["nested"]}`)

	after, err := Render(inst)
	if err != nil {
		t.Fatalf("Render() with an unknown manifest key: %v", err)
	}
	if !slices.Equal(filePaths(before), filePaths(after)) {
		t.Errorf("an unknown manifest key changed the rendering from %v to %v",
			filePaths(before), filePaths(after))
	}
}

func TestCodexRenderProducesNativeInstructionConfigAndSkills(t *testing.T) {
	inst := contractInstance(t)
	layout, err := LayoutFor(profile.ProviderCodex)
	if err != nil {
		t.Fatalf("LayoutFor(%q): %v", profile.ProviderCodex, err)
	}
	inst.Layout = layout
	inst.Subagents = nil
	inst.Profile.Spec[profile.SpecKeyPrompts] = json.RawMessage(`null`)
	inst.Profile.Spec[profile.SpecKeySettings] = json.RawMessage(`{"codex":{"model":"gpt-5"}}`)

	files, err := Render(inst)
	if err != nil {
		t.Fatalf("Render codex: %v", err)
	}
	want := []string{
		"AGENTS.md",
		"boot.md",
		"config.toml",
		".agents/skills/code-review/SKILL.md",
		".agents/skills/code-review/references/checklist.md",
		"notes/decisions.md",
		"tasks/T-1/task.md",
	}
	if got := filePaths(files); !slices.Equal(got, want) {
		t.Fatalf("Render codex produced\n%v\nwant\n%v", got, want)
	}
	config := string(fileByPath(t, files, "config.toml").Content)
	for _, want := range []string{`model = 'gpt-5'`, `[mcp_servers]`, `[sandbox_workspace_write]`} {
		if !strings.Contains(config, want) {
			t.Errorf("config.toml missing %q:\n%s", want, config)
		}
	}
	for _, absent := range []string{"CLAUDE.md", ".mcp.json", "auth.json", ".claude/settings.json", ".claude/agents/scribe.md"} {
		if slices.Contains(filePaths(files), absent) {
			t.Errorf("Codex render produced %s", absent)
		}
	}
}

// TestCodexRenderReportsUnsupportedClaudeOnlyFeatures pins the refusal that
// used to be a second code path. The Codex tree lists `prompts` and
// `subagents` under renders and declares no directory for either, so the same
// renderers Claude Code uses report that this tree has nowhere to put what the
// manifest declared — rather than a shim per provider per feature.
func TestCodexRenderReportsUnsupportedClaudeOnlyFeatures(t *testing.T) {
	t.Run("prompts", func(t *testing.T) {
		inst := contractInstance(t)
		layout, err := LayoutFor(profile.ProviderCodex)
		if err != nil {
			t.Fatalf("LayoutFor(%q): %v", profile.ProviderCodex, err)
		}
		inst.Layout = layout
		inst.Subagents = nil
		inst.Profile.Spec[profile.SpecKeyPrompts] = json.RawMessage(`["report"]`)

		_, err = Render(inst)
		if !errors.Is(err, ErrProviderLayout) {
			t.Fatalf("Render codex with prompts = %v, want ErrProviderLayout", err)
		}
		if !strings.Contains(err.Error(), profile.SpecKeyPrompts) {
			t.Errorf("the error does not name prompts: %v", err)
		}
	})

	t.Run("subagents", func(t *testing.T) {
		inst := contractInstance(t)
		layout, err := LayoutFor(profile.ProviderCodex)
		if err != nil {
			t.Fatalf("LayoutFor(%q): %v", profile.ProviderCodex, err)
		}
		inst.Layout = layout

		_, err = Render(inst)
		if !errors.Is(err, ErrProviderLayout) {
			t.Fatalf("Render codex with subagents = %v, want ErrProviderLayout", err)
		}
		if !strings.Contains(err.Error(), profile.SpecKeySubagents) {
			t.Errorf("the error does not name subagents: %v", err)
		}
	})
}
