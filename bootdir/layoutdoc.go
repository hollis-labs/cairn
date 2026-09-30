package bootdir

import (
	"embed"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/chrispian/cairn/profile"
	goprovider "github.com/hollis-labs/go-providers/provider"
	"gopkg.in/yaml.v3"
)

// LayoutDir is the directory inside this package holding one layout document
// per harness. It is named so that a diagnostic about a malformed document can
// say where the document is.
const LayoutDir = "layouts"

// layoutFS holds the layout documents, compiled into the binary.
//
// They are embedded rather than read from the bundle, and that is a staging
// decision rather than a final one: a bundle-supplied layout is the point of
// making the layout data, but it changes what a bundle is, and every profile
// in the portfolio renders through these trees today. Embedded first means the
// trees become content without the catalog format moving underneath them.
//
//go:embed layouts/*.yaml
var layoutFS embed.FS

// bootDirSpecs maps a harness onto the adapter in go-providers that owns its
// on-disk conventions.
//
// It is the one table a layout document cannot replace. A document can name a
// provider; only the linked library can answer what that provider's spec
// currently says, and taking the answer from there is what lets cairn notice
// that a harness moved a file rather than quietly writing to where it used to
// be.
//
// A provider here with no document is still refused: this table says what can
// be asked, and the documents say what cairn renders.
var bootDirSpecs = map[profile.Provider]func() goprovider.BootDirSpec{
	profile.ProviderClaude:   func() goprovider.BootDirSpec { return goprovider.NewClaudeAdapter().BootDirSpec() },
	profile.ProviderCodex:    func() goprovider.BootDirSpec { return goprovider.NewCodexAdapter().BootDirSpec() },
	profile.ProviderOpenCode: func() goprovider.BootDirSpec { return goprovider.NewOpencodeAdapter().BootDirSpec() },
}

// layoutDoc is one harness's tree as it is written in its document.
type layoutDoc struct {
	Provider profile.Provider `yaml:"provider"`
	// BootOnly declares a tree with no installed layer: `cairn install`
	// refuses it by name rather than reporting a malformed document.
	BootOnly  bool         `yaml:"boot_only"`
	Boot      bootDoc      `yaml:"boot"`
	Installed installedDoc `yaml:"installed"`
}

// bootDoc is the boot-directory half of a layout document.
type bootDoc struct {
	Renders         []string          `yaml:"renders"`
	RenderImpls     map[string]string `yaml:"renderers"`
	Artifacts       []artifactDoc     `yaml:"artifacts"`
	Directories     dirsDoc           `yaml:"directories"`
	Templates       templatesDoc      `yaml:"templates"`
	PromptNamespace string            `yaml:"prompt_namespace"`

	ProjectDirArg             string   `yaml:"project_dir_arg"`
	EnvAmendmentsFromProvider bool     `yaml:"env_amendments_from_provider"`
	HomeResources             []string `yaml:"home_resources"`

	Unrendered unrenderedDoc `yaml:"unrendered"`
}

// unrenderedDoc is what a tree says about the manifest keys it does not render.
type unrenderedDoc struct {
	Keys []string `yaml:"keys"`
	Note string   `yaml:"note"`
}

// unrenderableKeys are the keys a tree may declare as unrendered: the ones
// [Undeclared] knows how to read a profile's declaration of.
var unrenderableKeys = []string{profile.SpecKeyMCP, profile.SpecKeySettings}

// dirsDoc names the directories a boot directory plants collections into. An
// empty member is a collection this harness has nowhere to put, which the
// renderer for it reports when a profile declares one.
type dirsDoc struct {
	Skills    string `yaml:"skills"`
	Subagents string `yaml:"subagents"`
	Prompts   string `yaml:"prompts"`
}

// templatesDoc is what a layout says about the manifest's templates. Only
// Drop, so far: every other destination is planted where it was declared.
type templatesDoc struct {
	Drop []string `yaml:"drop"`
}

// installedDoc is the installed-layer half of a layout document.
type installedDoc struct {
	Dir       string        `yaml:"dir"`
	Artifacts []artifactDoc `yaml:"artifacts"`
}

// artifactDoc is one artifact as written in a document. Which fields are
// meaningful depends on the half it appears in — see [layoutDoc.bootLayout]
// and [layoutDoc.installedLayout].
type artifactDoc struct {
	Kind         string `yaml:"kind"`
	Dest         string `yaml:"dest"`
	Path         string `yaml:"path"`
	ProviderPath string `yaml:"provider_path"`
	Artifact     string `yaml:"artifact"`
	Mode         string `yaml:"mode"`
	Render       string `yaml:"render"`

	Merge     string `yaml:"merge"`
	Normalize string `yaml:"normalize"`
	Claim     string `yaml:"claim"`
	Fills     string `yaml:"fills"`
}

// The artifact kinds a layout document may declare. They are the names a
// document uses and the names the installed layer looks its renderers up by,
// so they are one set rather than two.
const (
	KindAgents   = "agents"
	KindPointer  = "pointer"
	KindMCP      = "mcp"
	KindSettings = "settings"
	KindSkills   = "skills"
)

// layoutDocs is every embedded layout document, keyed by the provider it
// declares.
//
// Parsed once at init and fatal on failure, for the reason
// [regexp.MustCompile] is used at package scope in template.go: these
// documents ship inside the binary, so a malformed one is a build that should
// never have been produced and there is no operator input to report it
// against. Everything an operator can get wrong — an unknown provider, a
// manifest that declares what a tree has nowhere to put — is reported as an
// error where it happens.
var layoutDocs = mustReadLayouts()

func mustReadLayouts() map[profile.Provider]*layoutDoc {
	docs, err := readLayouts(layoutFS)
	if err != nil {
		panic("bootdir: " + err.Error())
	}
	return docs
}

// readLayouts parses every layout document in fsys under [LayoutDir].
func readLayouts(fsys fs.FS) (map[profile.Provider]*layoutDoc, error) {
	names, err := fs.Glob(fsys, path.Join(LayoutDir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("list the layout documents: %w", err)
	}
	slices.Sort(names)
	out := make(map[profile.Provider]*layoutDoc, len(names))
	for _, name := range names {
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read the layout document %s: %w", name, err)
		}
		doc := new(layoutDoc)
		if err := yaml.Unmarshal(raw, doc); err != nil {
			return nil, fmt.Errorf("parse the layout document %s: %w", name, err)
		}
		if strings.TrimSpace(string(doc.Provider)) == "" {
			return nil, fmt.Errorf("the layout document %s declares no provider", name)
		}
		if _, taken := out[doc.Provider]; taken {
			return nil, fmt.Errorf("the layout document %s declares provider %q, which another document already declares",
				name, doc.Provider)
		}
		if _, known := bootDirSpecs[doc.Provider]; !known {
			return nil, fmt.Errorf("the layout document %s declares provider %q, which no adapter answers for",
				name, doc.Provider)
		}
		for _, key := range doc.Boot.Unrendered.Keys {
			if !slices.Contains(unrenderableKeys, key) {
				return nil, fmt.Errorf("the layout document %s declares %q unrendered, which cairn cannot report — only %v",
					name, key, unrenderableKeys)
			}
			if slices.Contains(doc.Boot.Renders, key) {
				return nil, fmt.Errorf("the layout document %s both renders and declares unrendered %q", name, key)
			}
		}
		out[doc.Provider] = doc
	}
	return out, nil
}

// LayoutProviders returns every harness cairn holds a layout document for,
// sorted. It is what a refusal names, so that "codex, then" costs one clause
// rather than a lookup.
func LayoutProviders() []profile.Provider {
	out := make([]profile.Provider, 0, len(layoutDocs))
	for p := range layoutDocs {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// quotedProviders renders [LayoutProviders] for a diagnostic.
func quotedProviders() string {
	providers := LayoutProviders()
	quoted := make([]string, 0, len(providers))
	for _, p := range providers {
		quoted = append(quoted, strconv.Quote(string(p)))
	}
	switch len(quoted) {
	case 0:
		return "no harness"
	case 1:
		return quoted[0]
	default:
		return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
	}
}

// layoutFor returns the parsed document for p, reporting
// [ErrUnsupportedProvider] for a harness cairn holds no tree for.
func layoutFor(p profile.Provider) (*layoutDoc, error) {
	if p == "" {
		return nil, fmt.Errorf("%w: the resolved profile declares no provider", ErrUnsupportedProvider)
	}
	doc, ok := layoutDocs[p]
	if !ok {
		return nil, fmt.Errorf("%w: %q — cairn renders %s", ErrUnsupportedProvider, p, quotedProviders())
	}
	return doc, nil
}

// bootLayout builds the boot-directory [Layout] from d, resolving every
// provider_path against that harness's BootDirSpec.
func (d *layoutDoc) bootLayout() (Layout, error) {
	spec := bootDirSpecs[d.Provider]()
	out := Layout{
		Provider:          d.Provider,
		Renders:           slices.Clone(d.Boot.Renders),
		RenderImpls:       maps.Clone(d.Boot.RenderImpls),
		SkillsDir:         d.Boot.Directories.Skills,
		SubagentsDir:      d.Boot.Directories.Subagents,
		PromptsDir:        d.Boot.Directories.Prompts,
		PromptNamespace:   d.Boot.PromptNamespace,
		DropTemplates:     slices.Clone(d.Boot.Templates.Drop),
		CwdPreference:     spec.CwdPreference,
		ProjectDirArg:     spec.ProjectDirArg,
		HomeResourcePaths: slices.Clone(d.Boot.HomeResources),
		Unrendered:        slices.Clone(d.Boot.Unrendered.Keys),
		UnrenderedNote:    d.Boot.Unrendered.Note,
	}
	if d.Boot.ProjectDirArg != "" {
		out.ProjectDirArg = d.Boot.ProjectDirArg
	}
	if d.Boot.EnvAmendmentsFromProvider {
		out.EnvAmendments = slices.Clone(spec.EnvAmendments)
	}
	for _, a := range d.Boot.Artifacts {
		artifact, err := d.bootArtifact(spec, a)
		if err != nil {
			return Layout{}, err
		}
		if err := out.set(a.Kind, artifact); err != nil {
			return Layout{}, err
		}
	}
	return out, nil
}

// bootArtifact resolves one boot artifact's path and mode.
//
// A provider_path is looked up in the spec's planted files and takes its mode
// from there. The spec's own PlantedFile.Render functions are deliberately
// never invoked: at least one of them writes to the operator's real home
// directory when handed a boot directory, and cairn renders its own content
// for every path here anyway.
func (d *layoutDoc) bootArtifact(spec goprovider.BootDirSpec, a artifactDoc) (Artifact, error) {
	switch {
	case a.Path != "" && a.ProviderPath != "":
		return Artifact{}, fmt.Errorf("%w: the %s layout declares both path and provider_path for %q",
			ErrProviderLayout, d.Provider, a.Kind)
	case a.Path != "":
		mode, err := parseMode(a.Mode)
		if err != nil {
			return Artifact{}, fmt.Errorf("the %s layout's %q artifact: %w", d.Provider, a.Kind, err)
		}
		return Artifact{Dest: a.Dest, RelPath: a.Path, Mode: mode}, nil
	case a.ProviderPath != "":
		for _, pf := range spec.PlantedFiles {
			if pf.RelPath == a.ProviderPath {
				return Artifact{Dest: a.Dest, RelPath: pf.RelPath, Mode: pf.Mode}, nil
			}
		}
		return Artifact{}, fmt.Errorf("%w: the %s layout takes %q from the provider, and %q is not in its BootDirSpec",
			ErrProviderLayout, d.Provider, a.Kind, a.ProviderPath)
	default:
		return Artifact{}, fmt.Errorf("%w: the %s layout declares %q with no path",
			ErrProviderLayout, d.Provider, a.Kind)
	}
}

// installedLayout builds the installed layer's placement from d: the provider
// directory, the artifacts in render order, and the [Layout] the shared
// renderers read their paths from.
func (d *layoutDoc) installedLayout() (InstalledLayout, error) {
	if d.BootOnly {
		return InstalledLayout{}, fmt.Errorf("%w: the %s layout covers the boot directory only — cairn has no installed layer for it",
			ErrUnsupportedProvider, d.Provider)
	}
	dir := d.Installed.Dir
	if strings.TrimSpace(dir) == "" {
		return InstalledLayout{}, fmt.Errorf("%w: the %s layout declares no installed directory",
			ErrProviderLayout, d.Provider)
	}
	out := InstalledLayout{Dir: dir, Layout: Layout{Provider: d.Provider}}
	for _, a := range d.Installed.Artifacts {
		if strings.TrimSpace(a.Artifact) == "" {
			return InstalledLayout{}, fmt.Errorf("%w: the %s layout declares the installed %q with no artifact label",
				ErrProviderLayout, d.Provider, a.Kind)
		}
		mode, err := parseMode(a.Mode)
		if err != nil {
			return InstalledLayout{}, fmt.Errorf("the installed %s layout's %q artifact: %w", d.Provider, a.Kind, err)
		}
		rel := path.Join(dir, a.Artifact)
		if err := out.Layout.set(a.Kind, Artifact{Dest: a.Dest, RelPath: rel, Mode: mode}); err != nil {
			return InstalledLayout{}, err
		}
		out.Artifacts = append(out.Artifacts, InstalledArtifact{
			Kind:      a.Kind,
			Label:     a.Artifact,
			Dest:      a.Dest,
			Render:    a.Render,
			Merge:     a.Merge,
			Normalize: a.Normalize,
			Claim:     a.Claim,
			Fills:     a.Fills,
		})
	}
	return out, nil
}

// set puts one artifact on the layout under the kind a document named it by,
// reporting a kind no layout has a member for.
//
// The skills directory is an artifact kind in a document and a directory on a
// [Layout], because in the installed layer a skills tree is one of the
// artifacts the sweep plans over. Subagents and prompts have no installed
// counterpart, so they arrive only through [dirsDoc].
func (l *Layout) set(kind string, a Artifact) error {
	switch kind {
	case KindAgents:
		l.Agents = a
	case KindPointer:
		l.Pointer = a
	case KindMCP:
		l.MCP = a
	case KindSettings:
		l.Settings = a
	case KindSkills:
		l.SkillsDir = a.RelPath
	default:
		return fmt.Errorf("%w: the %s layout declares an artifact of kind %q, which cairn has no member for",
			ErrProviderLayout, l.Provider, kind)
	}
	return nil
}

// parseMode reads an octal file mode from a document, where the empty string
// means [DefaultFileMode] and is written as the zero value.
func parseMode(raw string) (fs.FileMode, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("%q is not an octal file mode: %w", raw, err)
	}
	return fs.FileMode(n), nil
}
