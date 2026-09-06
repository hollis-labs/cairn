// Package bootdir renders a resolved profile into the files of one boot
// directory and writes them there.
//
// Rendering and writing are separate on purpose. [Render] produces every file
// in memory, so a render that fails writes nothing; [Plant] stages the whole
// tree beside the target and moves it into place with one rename, so a boot
// directory is either complete or absent. A half-built boot directory is one
// an agent might boot from.
package bootdir

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/chrispian/cairn/profile"
	goprovider "github.com/hollis-labs/go-providers/provider"
)

// AgentsFileName is the template destination the installed layer renders its
// instruction file from. Cairn declares the name rather than reading it from a
// provider's BootDirSpec: it is the one artifact whose name is the same for
// every harness.
//
// It is not a file cairn insists on. A boot directory renders whatever
// templates a profile declares, at whatever paths it declares them; this name
// matters only where a layout has to map a destination onto a path of its own.
const AgentsFileName = "AGENTS.md"

// PointerFileName is Claude Code's own instruction file. Cairn declares the
// name because Claude's installed layout has to know which template
// destination lands there; what the file holds is the profile's, like every
// other template.
const PointerFileName = "CLAUDE.md"

// SkillsDirName is the directory, relative to the boot directory root,
// declared skills are planted into. No provider's BootDirSpec declares it;
// it is Claude Code's on-disk convention, one directory per skill.
const SkillsDirName = ".claude/skills"

// CodexSkillsDirName is the user/repository skill directory Codex discovers.
// Unlike Claude Code, Codex does not read skills from its config directory;
// user-installed skills live under ~/.agents/skills and project skills under a
// repository's .agents/skills tree.
const CodexSkillsDirName = ".agents/skills"

// CodexConfigFileName is the configuration document Codex reads as TOML.
const CodexConfigFileName = "config.toml"

// SkillFileName is the file a skill directory must hold for a harness to load
// the skill at all.
const SkillFileName = "SKILL.md"

// PromptNamespace is the commands subdirectory cairn plants prompts into, and
// the prefix a planted prompt is invoked by: `/boot:<name>`.
//
// The namespace is required rather than incidental. A subdirectory of the
// commands directory genuinely namespaces the command rather than flattening
// it — verified against the harness, with the bare name answering "Unknown
// command" as the control — so a prompt cairn planted is addressed as cairn's
// and can never collide with a command the operator wrote by hand beside it.
const PromptNamespace = "boot"

// PromptsDirName is the directory, relative to the boot directory root,
// declared prompts are planted into, one file per prompt. No provider's
// BootDirSpec declares it; it is Claude Code's on-disk convention for custom
// commands, under [PromptNamespace].
const PromptsDirName = ".claude/commands/" + PromptNamespace

// JSONIndent is one level of indentation in a rendered JSON artifact.
//
// Every JSON file cairn writes is laid out rather than compacted, because the
// operator reads these files and diffs them. Two spaces is the harness's own
// convention: Claude Code rewrites the settings document it was given at this
// width, so a rendered document and the one the harness writes back differ by
// their content and never by their shape.
const JSONIndent = "  "

// DefaultFileMode is the mode a rendered file is written with when its [File]
// carries none.
const DefaultFileMode fs.FileMode = 0o644

// DefaultDirMode is the mode the boot directory and every directory inside it
// is created with.
const DefaultDirMode fs.FileMode = 0o755

// ErrUnsupportedProvider reports that no layout is implemented for a profile's
// provider, or that the profile declares none at all.
var ErrUnsupportedProvider = errors.New("unsupported provider")

// ErrProviderLayout reports that a provider's BootDirSpec no longer declares
// an artifact Cairn renders for it, so Cairn would be writing to a path the
// harness has stopped reading. It is an error rather than a fallback, because
// the failure it prevents is silent.
var ErrProviderLayout = errors.New("provider no longer declares an artifact cairn renders")

// Artifact is one file of a boot directory: where the harness reads it from,
// and the mode it is written with.
type Artifact struct {
	// RelPath is the path relative to the boot directory root,
	// slash-separated. Empty means the layout does not carry this artifact.
	RelPath string

	// Mode is the permission mode. Zero means [DefaultFileMode].
	Mode fs.FileMode
}

// Declared reports whether a carries a path at all.
func (a Artifact) Declared() bool { return a.RelPath != "" }

// Layout is where one provider's harness reads each boot-directory artifact
// from.
//
// Three of its artifacts — Pointer, MCP and Settings — are taken from
// go-providers' BootDirSpec, which is the library that owns each harness's
// on-disk convention. Cairn takes the path and the mode from there and
// supplies its own content: the spec's own render functions are never called,
// because some of them have side effects on the operator's real home
// directory.
//
// Agents, Skills, Subagents and Prompts are Cairn's, not the provider's. No
// BootDirSpec declares any of them.
type Layout struct {
	// Provider is the harness this layout describes.
	Provider profile.Provider

	// Agents is where a template declared for [AgentsFileName] is written.
	Agents Artifact

	// Pointer is where a template declared for [PointerFileName] is written.
	// Undeclared when the harness reads [AgentsFileName] directly.
	Pointer Artifact

	// MCP is the MCP server configuration.
	MCP Artifact

	// Settings is the harness settings document, written from the profile's
	// manifest, laid out and not otherwise touched.
	Settings Artifact

	// SkillsDir is the directory declared skills are planted under, one
	// directory per skill.
	SkillsDir string

	// SubagentsDir is the directory subagent definitions are planted under,
	// one file per named profile. Like SkillsDir it is cairn's, not the
	// provider's: no BootDirSpec declares it.
	SubagentsDir string

	// PromptsDir is the directory declared prompts are planted under, one file
	// per prompt. Like SkillsDir and SubagentsDir it is cairn's, not the
	// provider's: no BootDirSpec declares it.
	PromptsDir string

	// CwdPreference is where the harness expects to be invoked, and
	// ProjectDirArg is its flag pattern for granting access to the scope
	// directory. Both come from the provider's BootDirSpec. Cairn does not
	// launch anything, so nothing here reads them; they are carried so that
	// the caller printing a boot directory can also print how to open it.
	CwdPreference goprovider.CwdPreference
	ProjectDirArg string
	EnvAmendments []string

	// HomeResourcePaths are provider-home-relative resources Cairn does not
	// render, but a launcher must deliberately provide when EnvAmendments points
	// the harness at the boot directory as its home.
	HomeResourcePaths []string
}

// LayoutFor returns the [Layout] one provider's boot directory is rendered
// through.
//
// Claude Code and Codex are implemented. opencode reports
// [ErrUnsupportedProvider] rather than falling back to a layout that would
// write another harness's files, and so does a profile that declares no
// provider at all.
//
// The refusal names what is implemented, which it did not have to before
// `--provider` existed. A provider used to be something a profile declared, so
// the reader of this diagnostic was looking at the file that said it; now it
// is something an operator can ask for at the terminal, and the answer to
// "codex, then" is worth one clause rather than a lookup. What it must never
// be is a fallback: [profile.Providers] knows three names and cairn renders
// one of them, and a target silently redirected to claude's layout would put
// claude's files at claude's paths for a harness that reads neither.
func LayoutFor(p profile.Provider) (Layout, error) {
	switch p {
	case profile.ProviderClaude:
		return claudeLayout()
	case profile.ProviderCodex:
		return codexLayout()
	case "":
		return Layout{}, fmt.Errorf("%w: the resolved profile declares no provider", ErrUnsupportedProvider)
	default:
		return Layout{}, fmt.Errorf("%w: %q — cairn renders boot directories for %q and %q",
			ErrUnsupportedProvider, p, profile.ProviderClaude, profile.ProviderCodex)
	}
}

// claudeLayout derives the Claude Code layout from that adapter's BootDirSpec.
func claudeLayout() (Layout, error) {
	spec := goprovider.NewClaudeAdapter().BootDirSpec()
	declared, err := artifacts(spec, PointerFileName, ".mcp.json", ".claude/settings.json")
	if err != nil {
		return Layout{}, fmt.Errorf("claude: %w", err)
	}
	return Layout{
		Provider:      profile.ProviderClaude,
		Agents:        Artifact{RelPath: AgentsFileName},
		Pointer:       declared[PointerFileName],
		MCP:           declared[".mcp.json"],
		Settings:      declared[".claude/settings.json"],
		SkillsDir:     SkillsDirName,
		SubagentsDir:  SubagentsDirName,
		PromptsDir:    PromptsDirName,
		CwdPreference: spec.CwdPreference,
		ProjectDirArg: spec.ProjectDirArg,
	}, nil
}

// codexLayout derives the Codex layout from that adapter's BootDirSpec, but
// names only the artifacts Codex actually needs Cairn to render. The adapter
// also lists auth.json and a legacy .mcp.json sidecar; Cairn deliberately does
// not copy live auth into disposable boot directories or plant an inert MCP
// sidecar for Codex.
func codexLayout() (Layout, error) {
	spec := goprovider.NewCodexAdapter().BootDirSpec()
	declared, err := artifacts(spec, AgentsFileName, CodexConfigFileName)
	if err != nil {
		return Layout{}, fmt.Errorf("codex: %w", err)
	}
	return Layout{
		Provider:      profile.ProviderCodex,
		Agents:        declared[AgentsFileName],
		Settings:      declared[CodexConfigFileName],
		SkillsDir:     CodexSkillsDirName,
		CwdPreference: spec.CwdPreference,
		ProjectDirArg: "--add-dir {{.ProjectDir}}",
		EnvAmendments: append([]string(nil), spec.EnvAmendments...),
		HomeResourcePaths: []string{
			"auth.json",
			"hooks.json",
			"hooks",
		},
	}, nil
}

// artifacts looks each wanted path up in spec's planted files and returns them
// carrying the mode the spec declares. A path the spec no longer declares
// reports [ErrProviderLayout]: the harness has moved the file, and writing to
// the old path would leave a boot directory that looks complete and is not.
//
// The spec's PlantedFile.Render functions are deliberately never invoked. At
// least one of them writes to the operator's real home directory when handed a
// boot directory, and Cairn renders its own content for every path here
// anyway.
func artifacts(spec goprovider.BootDirSpec, want ...string) (map[string]Artifact, error) {
	byPath := make(map[string]goprovider.PlantedFile, len(spec.PlantedFiles))
	for _, pf := range spec.PlantedFiles {
		byPath[pf.RelPath] = pf
	}
	out := make(map[string]Artifact, len(want))
	for _, rel := range want {
		pf, ok := byPath[rel]
		if !ok {
			return nil, fmt.Errorf("%w: %q is not in its BootDirSpec", ErrProviderLayout, rel)
		}
		out[rel] = Artifact{RelPath: pf.RelPath, Mode: pf.Mode}
	}
	return out, nil
}
