package bootdir

import (
	"errors"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/chrispian/cairn/profile"
)

// TestTheLayoutDocumentsPinEveryPath is the regression net the constants used
// to be.
//
// Every path a harness reads used to be a Go constant, so a change to one was
// a change to the source and showed up in a diff of it. They are documents
// now, which is the point — a second harness is a second tree — and this is
// what keeps that from meaning a path can move unnoticed. It states each one
// as a literal rather than reading it back out of the document, because a test
// that asked the document what the document says would agree with any answer.
func TestTheLayoutDocumentsPinEveryPath(t *testing.T) {
	for _, tc := range []struct {
		provider profile.Provider
		want     Layout
	}{
		{
			provider: profile.ProviderClaude,
			want: Layout{
				Provider:        profile.ProviderClaude,
				Renders:         []string{"templates", "mcp", "settings", "skills", "prompts", "subagents", "trees", "files"},
				Agents:          Artifact{Dest: "AGENTS.md", RelPath: "AGENTS.md"},
				Pointer:         Artifact{Dest: "CLAUDE.md", RelPath: "CLAUDE.md"},
				MCP:             Artifact{RelPath: ".mcp.json"},
				Settings:        Artifact{RelPath: ".claude/settings.json"},
				SkillsDir:       ".claude/skills",
				SubagentsDir:    ".claude/agents",
				PromptsDir:      ".claude/commands/boot",
				PromptNamespace: "boot",
			},
		},
		{
			provider: profile.ProviderCodex,
			want: Layout{
				Provider:      profile.ProviderCodex,
				Renders:       []string{"templates", "settings", "skills", "prompts", "subagents", "trees", "files"},
				RenderImpls:   map[string]string{"settings": CodexConfigRenderer},
				Agents:        Artifact{Dest: "AGENTS.md", RelPath: "AGENTS.md"},
				Settings:      Artifact{RelPath: "config.toml", Mode: 0o600},
				SkillsDir:     ".agents/skills",
				DropTemplates: []string{"CLAUDE.md"},
			},
		},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			got, err := LayoutFor(tc.provider)
			if err != nil {
				t.Fatalf("LayoutFor(%q): %v", tc.provider, err)
			}
			if got.Agents != tc.want.Agents {
				t.Errorf("agents = %+v, want %+v", got.Agents, tc.want.Agents)
			}
			if got.Pointer != tc.want.Pointer {
				t.Errorf("pointer = %+v, want %+v", got.Pointer, tc.want.Pointer)
			}
			if got.MCP != tc.want.MCP {
				t.Errorf("mcp = %+v, want %+v", got.MCP, tc.want.MCP)
			}
			if got.Settings != tc.want.Settings {
				t.Errorf("settings = %+v, want %+v", got.Settings, tc.want.Settings)
			}
			if got.SkillsDir != tc.want.SkillsDir {
				t.Errorf("skills dir = %q, want %q", got.SkillsDir, tc.want.SkillsDir)
			}
			if got.SubagentsDir != tc.want.SubagentsDir {
				t.Errorf("subagents dir = %q, want %q", got.SubagentsDir, tc.want.SubagentsDir)
			}
			if got.PromptsDir != tc.want.PromptsDir {
				t.Errorf("prompts dir = %q, want %q", got.PromptsDir, tc.want.PromptsDir)
			}
			if got.PromptNamespace != tc.want.PromptNamespace {
				t.Errorf("prompt namespace = %q, want %q", got.PromptNamespace, tc.want.PromptNamespace)
			}
			if !slices.Equal(got.Renders, tc.want.Renders) {
				t.Errorf("renders = %v, want %v", got.Renders, tc.want.Renders)
			}
			if !slices.Equal(got.DropTemplates, tc.want.DropTemplates) {
				t.Errorf("dropped templates = %v, want %v", got.DropTemplates, tc.want.DropTemplates)
			}
			if !maps.Equal(got.RenderImpls, tc.want.RenderImpls) {
				t.Errorf("render implementations = %v, want %v", got.RenderImpls, tc.want.RenderImpls)
			}
		})
	}
}

// TestTheInstalledLayoutDocumentsPinEveryPath is the same net over the layer
// that is not disposable. Every path here is one inside the operator's home.
func TestTheInstalledLayoutDocumentsPinEveryPath(t *testing.T) {
	for _, tc := range []struct {
		provider  profile.Provider
		dir       string
		artifacts []InstalledArtifact
		layout    Layout
	}{
		{
			provider: profile.ProviderClaude,
			dir:      ".claude",
			artifacts: []InstalledArtifact{
				{Kind: KindAgents, Label: "AGENTS.md", Dest: "AGENTS.md"},
				{Kind: KindPointer, Label: "CLAUDE.md", Dest: "CLAUDE.md"},
				{Kind: KindSettings, Label: "settings.json", Merge: "json-settings", Normalize: "json-indent"},
				{Kind: KindSkills, Label: "skills", Fills: "install-skills"},
			},
			layout: Layout{
				Agents:    Artifact{Dest: "AGENTS.md", RelPath: ".claude/AGENTS.md"},
				Pointer:   Artifact{Dest: "CLAUDE.md", RelPath: ".claude/CLAUDE.md"},
				Settings:  Artifact{RelPath: ".claude/settings.json"},
				SkillsDir: ".claude/skills",
			},
		},
		{
			provider: profile.ProviderCodex,
			dir:      ".",
			artifacts: []InstalledArtifact{
				{Kind: KindAgents, Label: ".codex/AGENTS.md", Dest: "AGENTS.md"},
				{Kind: KindSettings, Label: ".codex/config.toml", Render: CodexConfigRenderer, Merge: "toml-document", Normalize: "toml-document", Claim: "codex-config"},
				{Kind: KindSkills, Label: ".agents/skills", Fills: "install-skills"},
			},
			layout: Layout{
				Agents:    Artifact{Dest: "AGENTS.md", RelPath: ".codex/AGENTS.md"},
				Settings:  Artifact{RelPath: ".codex/config.toml", Mode: 0o600},
				SkillsDir: ".agents/skills",
			},
		},
	} {
		t.Run(string(tc.provider), func(t *testing.T) {
			got, err := InstalledLayoutFor(tc.provider)
			if err != nil {
				t.Fatalf("InstalledLayoutFor(%q): %v", tc.provider, err)
			}
			if got.Dir != tc.dir {
				t.Errorf("dir = %q, want %q", got.Dir, tc.dir)
			}
			if !slices.Equal(got.Artifacts, tc.artifacts) {
				t.Errorf("artifacts =\n%+v\nwant\n%+v", got.Artifacts, tc.artifacts)
			}
			if got.Layout.Agents != tc.layout.Agents {
				t.Errorf("agents = %+v, want %+v", got.Layout.Agents, tc.layout.Agents)
			}
			if got.Layout.Pointer != tc.layout.Pointer {
				t.Errorf("pointer = %+v, want %+v", got.Layout.Pointer, tc.layout.Pointer)
			}
			if got.Layout.Settings != tc.layout.Settings {
				t.Errorf("settings = %+v, want %+v", got.Layout.Settings, tc.layout.Settings)
			}
			if got.Layout.SkillsDir != tc.layout.SkillsDir {
				t.Errorf("skills dir = %q, want %q", got.Layout.SkillsDir, tc.layout.SkillsDir)
			}
			// The installed layer resolves no MCP configuration. A renderer
			// handed an undeclared path for content the profile declared
			// reports it rather than dropping it, which is why no tree lists
			// it under `installed`.
			if got.Layout.MCP.Declared() {
				t.Errorf("the installed %s layer declares an MCP path: %+v", tc.provider, got.Layout.MCP)
			}
		})
	}
}

// TestEveryLayoutDocumentIsWellFormed asks of the shipped documents what
// parsing cannot: that each names renderers cairn has, and that a tree
// declaring a prompts directory also names the namespace an operator types.
func TestEveryLayoutDocumentIsWellFormed(t *testing.T) {
	providers := LayoutProviders()
	if len(providers) == 0 {
		t.Fatal("no layout documents are embedded")
	}
	known := RendererKeys()
	for _, p := range providers {
		layout, err := LayoutFor(p)
		if err != nil {
			t.Fatalf("LayoutFor(%q): %v", p, err)
		}
		if len(layout.Renders) == 0 {
			t.Errorf("the %s tree renders nothing", p)
		}
		for _, key := range layout.Renders {
			impl := key
			if named := layout.RenderImpls[key]; named != "" {
				impl = named
			}
			if !slices.Contains(known, impl) {
				t.Errorf("the %s tree renders %q with %q, which is not one of %v", p, key, impl, known)
			}
		}
		for key := range layout.RenderImpls {
			if !slices.Contains(layout.Renders, key) {
				t.Errorf("the %s tree names an implementation for %q, which it does not render", p, key)
			}
		}
		if deduped := slices.Compact(slices.Clone(layout.Renders)); len(deduped) != len(layout.Renders) {
			t.Errorf("the %s tree names a renderer twice: %v", p, layout.Renders)
		}
		if (layout.PromptsDir == "") != (layout.PromptNamespace == "") {
			t.Errorf("the %s tree declares prompts dir %q and namespace %q — a tree needs both or neither",
				p, layout.PromptsDir, layout.PromptNamespace)
		}
		if layout.PromptNamespace != "" && !strings.HasSuffix(layout.PromptsDir, "/"+layout.PromptNamespace) {
			t.Errorf("the %s tree plants prompts in %q under the namespace %q, so an operator would type a command that is not there",
				p, layout.PromptsDir, layout.PromptNamespace)
		}
	}
}

// TestAProviderWithNoTreeIsRefused covers the answer that must never become a
// fallback: cairn knows three provider names and holds trees for two of them,
// and a target silently redirected to another harness's tree would put that
// harness's files at its paths for one that reads neither.
func TestAProviderWithNoTreeIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider profile.Provider
		names    bool
	}{
		{name: "a provider with no document", provider: profile.ProviderOpenCode, names: true},
		{name: "a word that is not a provider", provider: "nope", names: true},
		{name: "no provider at all", provider: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LayoutFor(tc.provider)
			if !errors.Is(err, ErrUnsupportedProvider) {
				t.Fatalf("LayoutFor(%q) = %v, want ErrUnsupportedProvider", tc.provider, err)
			}
			if _, err := InstalledLayoutFor(tc.provider); !errors.Is(err, ErrUnsupportedProvider) {
				t.Fatalf("InstalledLayoutFor(%q) = %v, want ErrUnsupportedProvider", tc.provider, err)
			}
			if !tc.names {
				return
			}
			for _, p := range LayoutProviders() {
				if !strings.Contains(err.Error(), string(p)) {
					t.Errorf("the refusal %q does not name %q, which cairn does render", err, p)
				}
			}
		})
	}
}

// TestAMalformedLayoutDocumentIsReported covers what parsing refuses. The
// shipped documents are compiled in, so a fault in one is a build that should
// never have been produced — but the parser is what makes that true, and it
// only makes it true if it looks.
func TestAMalformedLayoutDocumentIsReported(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{
			name:  "not yaml",
			files: fstest.MapFS{"layouts/x.yaml": &fstest.MapFile{Data: []byte("provider: [\n")}},
			want:  "parse",
		},
		{
			name:  "no provider",
			files: fstest.MapFS{"layouts/x.yaml": &fstest.MapFile{Data: []byte("boot:\n  renders: [templates]\n")}},
			want:  "declares no provider",
		},
		{
			name:  "a provider no adapter answers for",
			files: fstest.MapFS{"layouts/x.yaml": &fstest.MapFile{Data: []byte("provider: emacs\n")}},
			want:  "no adapter answers for",
		},
		{
			name: "two documents for one provider",
			files: fstest.MapFS{
				"layouts/a.yaml": &fstest.MapFile{Data: []byte("provider: claude\n")},
				"layouts/b.yaml": &fstest.MapFile{Data: []byte("provider: claude\n")},
			},
			want: "another document already declares",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readLayouts(tc.files)
			if err == nil {
				t.Fatalf("readLayouts() accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestAnArtifactWithNowhereToLandIsReported covers the three ways a document
// can name an artifact and fail to place it. Each is caught while the document
// is read rather than part way through a render.
func TestAnArtifactWithNowhereToLandIsReported(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "no path at all",
			doc:  "provider: claude\nboot:\n  artifacts:\n    - kind: agents\n",
			want: "with no path",
		},
		{
			name: "both a path and a provider path",
			doc:  "provider: claude\nboot:\n  artifacts:\n    - kind: agents\n      path: AGENTS.md\n      provider_path: AGENTS.md\n",
			want: "both path and provider_path",
		},
		{
			name: "a provider path the spec does not declare",
			doc:  "provider: claude\nboot:\n  artifacts:\n    - kind: agents\n      provider_path: NOTES.md\n",
			want: "is not in its BootDirSpec",
		},
		{
			name: "a kind no layout has a member for",
			doc:  "provider: claude\nboot:\n  artifacts:\n    - kind: hooks\n      path: hooks.json\n",
			want: "no member for",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs, err := readLayouts(fstest.MapFS{"layouts/x.yaml": &fstest.MapFile{Data: []byte(tc.doc)}})
			if err != nil {
				t.Fatalf("readLayouts(): %v", err)
			}
			_, err = docs[profile.ProviderClaude].bootLayout()
			if !errors.Is(err, ErrProviderLayout) {
				t.Fatalf("bootLayout() = %v, want ErrProviderLayout", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error %q does not say %q", err, tc.want)
			}
		})
	}
}

// TestARendererNoTreeCanNameIsReported covers a `renders` entry cairn has no
// renderer for: a malformed document rather than anything an operator did, and
// an error rather than an omission, because a boot directory missing what a
// profile asked for looks exactly like one that was never asked.
func TestARendererNoTreeCanNameIsReported(t *testing.T) {
	_, err := Renderers(Layout{Provider: profile.ProviderClaude, Renders: []string{"templates", "hooks"}})
	if !errors.Is(err, ErrProviderLayout) {
		t.Fatalf("Renderers() = %v, want ErrProviderLayout", err)
	}
	if !strings.Contains(err.Error(), "hooks") {
		t.Errorf("the error %q does not name the renderer", err)
	}
}

// TestAModeIsReadAsOctal pins the one field of a document that is a number
// written as text. A mode read as decimal would be silently wrong in the
// direction that matters — 600 decimal is 0o1130, which is not a mode anybody
// meant.
func TestAModeIsReadAsOctal(t *testing.T) {
	got, err := parseMode("0600")
	if err != nil {
		t.Fatalf("parseMode(): %v", err)
	}
	if got != fs.FileMode(0o600) {
		t.Errorf("parseMode(%q) = %o, want %o", "0600", got, 0o600)
	}
	if got, err := parseMode(""); err != nil || got != 0 {
		t.Errorf("parseMode(\"\") = %o, %v, want the zero mode and no error", got, err)
	}
	if _, err := parseMode("rw-------"); err == nil {
		t.Error("parseMode() accepted a symbolic mode")
	}
}
