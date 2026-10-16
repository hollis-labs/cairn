package bootdir

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/hollis-labs/libs/harness/core/agentlaunch/planting"
)

// ErrExists reports that the boot directory already exists. A boot directory
// is per-session and disposable; planting over one would leave an unknowable
// mixture of two materializations.
var ErrExists = errors.New("boot directory already exists")

// ErrUnsupportedPlantSpec reports that a [planting.PlantSpec] asked for something
// Cairn does not plant. It is an error rather than a silent omission: a caller
// handing Cairn hooks and receiving a directory without them has no way to
// tell.
var ErrUnsupportedPlantSpec = errors.New("plant spec asks for something cairn does not plant")

// Planter writes a boot directory through the same contract the rest of the
// portfolio plants through, so a caller holding a [planting.Planter] can hand
// work to Cairn without knowing it is Cairn.
//
// Cairn's own boot path does not go through it. [planting.PlantSpec] carries file
// contents but no file modes, and Cairn plants skills whose executable bit is
// load-bearing, so [PlantFiles] is the entry Cairn itself uses and this is the
// adapter onto it.
type Planter struct {
	// Layout resolves the provider-shaped members of a [planting.PlantSpec] —
	// MCPConfig and ProviderSettings — onto the paths that provider's harness
	// reads them from.
	Layout Layout
}

// Plant implements [planting.Planter]. It converts spec into rendered files and
// writes them into bootDir with the same all-or-nothing guarantee
// [PlantFiles] gives.
func (p Planter) Plant(ctx context.Context, bootDir string, spec planting.PlantSpec) (planting.PlantResult, error) {
	files, err := p.filesFromSpec(spec)
	if err != nil {
		return planting.PlantResult{}, err
	}
	return PlantFiles(ctx, bootDir, files)
}

// filesFromSpec converts a [planting.PlantSpec] into rendered files, resolving its
// provider-shaped members through p.Layout.
func (p Planter) filesFromSpec(spec planting.PlantSpec) ([]File, error) {
	if len(spec.Hooks) > 0 {
		return nil, fmt.Errorf("%w: %d hooks", ErrUnsupportedPlantSpec, len(spec.Hooks))
	}
	if spec.RecoveryPrompt != "" {
		return nil, fmt.Errorf("%w: a recovery prompt", ErrUnsupportedPlantSpec)
	}

	var files []File
	for rel := range spec.Files {
		files = append(files, File{Path: rel, Content: spec.Files[rel]})
	}
	if spec.MCPConfig != nil {
		if !p.Layout.MCP.Declared() {
			return nil, fmt.Errorf("%w: an MCP config, but %s declares no path for one",
				ErrUnsupportedPlantSpec, p.Layout.Provider)
		}
		files = append(files, File{
			Path:    p.Layout.MCP.RelPath,
			Content: spec.MCPConfig,
			Mode:    p.Layout.MCP.Mode,
		})
	}
	for name, content := range spec.ProviderSettings {
		if name != p.Layout.Provider.String() {
			return nil, fmt.Errorf("%w: settings for %q, but this planter's layout is %q",
				ErrUnsupportedPlantSpec, name, p.Layout.Provider)
		}
		if !p.Layout.Settings.Declared() {
			return nil, fmt.Errorf("%w: settings, but %s declares no path for them",
				ErrUnsupportedPlantSpec, p.Layout.Provider)
		}
		files = append(files, File{
			Path:    p.Layout.Settings.RelPath,
			Content: content,
			Mode:    p.Layout.Settings.Mode,
		})
	}

	// A planting.PlantSpec carries its files in a map, so render order has to be
	// recovered before the paths are checked for collisions.
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	seen := make(map[string]struct{}, len(files))
	for i := range files {
		clean, err := CleanArtifactPath(files[i].Path)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[clean]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicatePath, clean)
		}
		seen[clean] = struct{}{}
		files[i].Path = clean
	}
	return files, nil
}

// PlantFiles writes files into dir and reports what it wrote.
//
// The write is all-or-nothing: every file is written into a temporary
// directory beside the target and then moved into place with one rename. A
// failure at any point leaves no directory at the target and no half-built
// tree beside it.
//
// It refuses a target that already exists, reporting [ErrExists].
func PlantFiles(ctx context.Context, dir string, files []File) (planting.PlantResult, error) {
	if err := ctx.Err(); err != nil {
		return planting.PlantResult{}, err
	}
	if err := writeTree(dir, files); err != nil {
		return planting.PlantResult{}, err
	}
	planted := make([]string, 0, len(files))
	for _, f := range files {
		planted = append(planted, filepath.Join(dir, filepath.FromSlash(f.Path)))
	}
	sort.Strings(planted)
	return planting.PlantResult{PlantedFiles: planted}, nil
}

// writeTree writes files into a temporary directory beside dir and renames it
// into place. The temporary directory is a sibling so that the rename stays
// within one filesystem and is therefore atomic.
func writeTree(dir string, files []File) error {
	switch _, err := os.Lstat(dir); {
	case err == nil:
		return fmt.Errorf("plant into %q: %w", dir, ErrExists)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("plant into %q: %w", dir, err)
	}

	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, DefaultDirMode); err != nil {
		return fmt.Errorf("create boot-dir parent %q: %w", parent, err)
	}
	staging, err := os.MkdirTemp(parent, ".cairn-plant-*")
	if err != nil {
		return fmt.Errorf("create staging directory in %q: %w", parent, err)
	}
	// Removing the staging directory is a no-op once the rename has moved it,
	// so one deferred cleanup covers every failure path and costs nothing on
	// the success path.
	defer func() { _ = os.RemoveAll(staging) }()

	for _, f := range files {
		dest := filepath.Join(staging, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dest), DefaultDirMode); err != nil {
			return fmt.Errorf("create directory for %q: %w", f.Path, err)
		}
		if err := os.WriteFile(dest, f.Content, f.mode()); err != nil {
			return fmt.Errorf("write %q: %w", f.Path, err)
		}
		// os.WriteFile's mode is masked by the process umask, so the planted
		// mode would otherwise depend on the shell cairn was launched from.
		if err := os.Chmod(dest, f.mode()); err != nil {
			return fmt.Errorf("set mode on %q: %w", f.Path, err)
		}
	}

	// MkdirTemp creates 0o700. The planted directory is read by a harness that
	// is not necessarily this process, so it gets the same mode as every
	// directory inside it.
	if err := os.Chmod(staging, DefaultDirMode); err != nil {
		return fmt.Errorf("set mode on staging directory %q: %w", staging, err)
	}
	if err := os.Rename(staging, dir); err != nil {
		return fmt.Errorf("move staged boot dir into %q: %w", dir, err)
	}
	return nil
}

// Ensure Planter satisfies the portfolio's planting contract at compile time.
var _ planting.Planter = Planter{}
