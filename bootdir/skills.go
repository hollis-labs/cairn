package bootdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/chrispian/cairn/profile"
)

// ExecFileMode is the mode a copied file is planted with when the file it was
// copied from is executable. A tree carries scripts, and a script that arrives
// without its executable bit fails halfway through instead of at boot.
const ExecFileMode fs.FileMode = 0o755

// ErrTreeContent reports a file in a copied directory that cannot be planted:
// it is not a regular file, or it is a symlink to a directory or to nothing.
var ErrTreeContent = errors.New("unusable tree content")

// ErrSkillsSource reports that skills were declared but the directory to copy
// them from is unusable: the manifest names none, the one it names is not
// absolute once a leading "~/" is expanded, or it is not a directory. Cairn
// ships no skills, so a profile that declares one has to say where it lives.
var ErrSkillsSource = errors.New("skills directory is unusable")

// ErrSkillName reports a declared name that cannot name a directory beneath
// the skills source: it is empty, it is "." or "..", it holds a path
// separator, or the same name is declared twice.
var ErrSkillName = errors.New("invalid skill name")

// ErrSkillNotFound reports that a declared skill has no directory in the
// skills source. It stops the render rather than omitting the skill, because
// an agent that boots without a skill its profile declared has no way to
// notice the difference.
var ErrSkillNotFound = errors.New("skill not found")

// ErrSkillContent reports a skill directory that cannot be planted: it holds
// no [SkillFileName], it holds no files at all, or something inside it is not
// a regular file.
var ErrSkillContent = errors.New("unusable skill content")

// skillsKey is the manifest key one skills render reads, spelled the way a
// diagnostic names it: "skills" for a boot directory, "install.skills" for the
// installed layer.
//
// The two entry points share every line of behaviour — the same source, the
// same target, the same name checking, the same order — and differ only in
// which key an error should send the operator to edit. So the key travels
// through the render rather than being restated in a second copy of it.
type skillsKey string

// installSkillsKey names spec.install.skills as a diagnostic spells it.
const installSkillsKey skillsKey = profile.SpecKeyInstall + "." + profile.SpecKeySkills

// RenderSkills returns every file of every skill spec.skills declares, planted
// under the layout's skills directory with one directory per skill.
//
// It is the boot directory's skill set. The installed layer reads a different
// key — see [RenderInstallSkills].
//
// Skills are copied, never linked. Each file's bytes are read here and carried
// in a [File], so a planted skill cannot reference the directory it came from:
// editing the source leaves every already-planted boot directory as it was,
// and the next boot picks the change up. The [File] contract is what makes
// that structural rather than a convention — a renderer has no way to emit a
// link.
//
// A profile declaring no skills renders nothing and reports no error. One
// declaring a skill that cannot be planted reports [ErrSkillsSource],
// [ErrSkillName], [ErrSkillNotFound] or [ErrSkillContent], each naming the
// skill and the path it was looked for at.
//
// The output is deterministic: the skills in the order the resolved manifest
// carries them, and the files inside each in the lexical order
// [filepath.WalkDir] walks. That order is the operator's declaration order for
// a key exactly one profile declares and key order for one the cascade
// composed — see [profile.Resolve]. Nothing here depends on which: the planted
// paths are one per skill name.
// Empty directories inside a skill are not reproduced, because a [File] names
// a file.
func RenderSkills(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	declared, err := inst.Profile.Spec.Skills()
	if err != nil {
		return nil, err
	}
	return renderSkills(inst, declared, profile.SpecKeySkills)
}

// RenderInstallSkills is [RenderSkills] over spec.install.skills: the skills
// the installed layer plants, rather than the ones a boot directory carries.
//
// Everything about the render is the same — the [profile.SpecKeySkillsDir]
// source, the layout's skills directory, the name checking, the duplicate
// detection, the order. Only the key differs, and a diagnostic names the one
// it read so that an operator is sent to the declaration they wrote.
//
// The two keys are separate because the two answers are. A profile holding the
// machine's skill set says so once, under install, and every profile extending
// it inherits that set for the installed layer without planting it beside its
// own boot file.
func RenderInstallSkills(inst *Instance) ([]File, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	declared, err := inst.Profile.Spec.InstallSkills()
	if err != nil {
		return nil, err
	}
	return renderSkills(inst, declared, installSkillsKey)
}

// renderSkills plants the named skills under the layout's skills directory,
// reporting key as the manifest key that declared them.
//
// It is the whole body of both entry points. What is above them is one spec
// accessor each; everything a skill render does is here, once.
func renderSkills(inst *Instance, declared []string, key skillsKey) ([]File, error) {
	if len(declared) == 0 {
		return nil, nil
	}
	target := strings.TrimSpace(inst.Layout.SkillsDir)
	if target == "" {
		return nil, fmt.Errorf(
			"%w: spec.%s declares %s, and the %s layout declares no skills directory",
			ErrProviderLayout, key, quotedNames(declared), inst.Layout.Provider)
	}
	source, err := skillsSource(inst.Profile.Spec, declared, key, inst.Home, inst.Env)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(declared))
	var files []File
	for _, raw := range declared {
		name := strings.TrimSpace(raw)
		if err := checkContentName(name, skillKind(key)); err != nil {
			return nil, err
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("%w: spec.%s declares %q twice",
				ErrSkillName, key, name)
		}
		seen[name] = struct{}{}

		planted, err := copySkill(source, target, name, key)
		if err != nil {
			return nil, err
		}
		files = append(files, planted...)
	}
	return files, nil
}

// skillKind describes a skill collection for the shared content helpers,
// under the manifest key that declared it.
//
// It is a function of the key rather than a constant because two keys declare
// skills — see [skillsKey] — and a diagnostic has to send the operator to the
// declaration they wrote.
func skillKind(key skillsKey) contentKind {
	return contentKind{
		key:       string(key),
		dirKey:    profile.SpecKeySkillsDir,
		plural:    "skills",
		entry:     "skill directory",
		nameErr:   ErrSkillName,
		sourceErr: ErrSkillsSource,
	}
}

// skillsSource resolves the directory declared skills are copied from: the
// value under [profile.SpecKeySkillsDir], put to [contentSource].
func skillsSource(spec profile.Spec, names []string, key skillsKey, home string, look profile.Expander) (string, error) {
	declared, err := spec.SkillsDir()
	if err != nil {
		return "", err
	}
	return contentSource(declared, skillKind(key), names, home, look)
}

// copySkill returns the skill named name under source as artifacts under
// target, refusing one a harness would not load. The copying itself is
// [CopyTree]'s; what is here is the part that is about skills.
func copySkill(source, target, name string, key skillsKey) ([]File, error) {
	dir := filepath.Join(source, name)
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w: spec.%s declares %q, which is not in %s: nothing at %s",
			ErrSkillNotFound, key, name, source, dir)
	case err != nil:
		return nil, fmt.Errorf("stat skill %q at %s: %w", name, dir, err)
	case !info.IsDir():
		return nil, fmt.Errorf("%w: skill %q at %s is not a directory", ErrSkillContent, name, dir)
	}

	files, err := CopyTree(dir, path.Join(target, name))
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", name, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: skill %q at %s holds no files", ErrSkillContent, name, dir)
	}
	entry := path.Join(target, name, SkillFileName)
	if !slices.ContainsFunc(files, func(f File) bool { return f.Path == entry }) {
		return nil, fmt.Errorf("%w: skill %q at %s has no %s, so a harness would not load it",
			ErrSkillContent, name, dir, SkillFileName)
	}
	return files, nil
}

// CopyTree reads every file under source and returns them as artifacts under
// target, with their bytes carried in memory.
//
// Files are copied, never linked. Each file's bytes are read here and carried
// in a [File], so a planted tree cannot reference the directory it came from:
// editing the source leaves every already-planted boot directory as it was,
// and the next boot picks the change up. The [File] contract is what makes that
// structural rather than a convention — a renderer has no way to emit a link.
//
// The output is deterministic: files in the lexical order [filepath.WalkDir]
// walks. Empty directories are not reproduced, because a [File] names a file.
//
// # Symlinks
//
// A symlink to a regular file is copied by value, including one whose target
// lies outside source. That is a property rather than a decision: it is what
// the skills copier has always done, and narrowing it here would be a change in
// behaviour smuggled into a new feature.
//
// A symlink to a directory, and a symlink that dangles, are both refused with
// [ErrTreeContent] naming the link. [filepath.WalkDir] does not descend a
// symlinked directory, so one arrives as a leaf that is not a file; following
// it instead would mean loop detection and a containment rule, and nothing
// needs either yet. Refusing by name is what keeps the answer diagnosable until
// something does.
func CopyTree(source, target string) ([]File, error) {
	var files []File
	err := filepath.WalkDir(source, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s under %s: %w", current, source, err)
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(source, current)
		if err != nil {
			return fmt.Errorf("locate %s inside %s: %w", current, source, err)
		}
		// Stat rather than the walk entry's own type: a symlink to a regular
		// file is copied by value, which is the point, while a symlink to a
		// directory or to nothing is refused rather than planted as a file
		// that is not one.
		info, err := os.Stat(current)
		if errors.Is(err, fs.ErrNotExist) && entry.Type()&fs.ModeSymlink != 0 {
			link, readErr := os.Readlink(current)
			if readErr != nil {
				link = "a path that cannot be read"
			}
			return fmt.Errorf("%w: %s is a symlink to %s, which does not exist",
				ErrTreeContent, current, link)
		}
		if err != nil {
			return fmt.Errorf("stat %s inside %s: %w", current, source, err)
		}
		if !info.Mode().IsRegular() {
			if entry.Type()&fs.ModeSymlink != 0 && info.IsDir() {
				link, readErr := os.Readlink(current)
				if readErr != nil {
					link = "a directory"
				}
				return fmt.Errorf("%w: %s is a symlink to the directory %s, which cairn does not follow",
					ErrTreeContent, current, link)
			}
			return fmt.Errorf("%w: %s is not a regular file", ErrTreeContent, current)
		}
		content, err := os.ReadFile(current)
		if err != nil {
			return fmt.Errorf("read %s inside %s: %w", current, source, err)
		}
		files = append(files, File{
			Path:    path.Join(target, filepath.ToSlash(rel)),
			Content: content,
			Mode:    treeFileMode(info.Mode()),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// treeFileMode maps a source file's mode onto the mode its copy is planted
// with: executable stays executable, everything else is [DefaultFileMode]. The
// source's exact bits are deliberately not carried through, so that a stray
// 0600 in the source cannot plant a file the harness cannot read.
func treeFileMode(mode fs.FileMode) fs.FileMode {
	if mode.Perm()&0o111 != 0 {
		return ExecFileMode
	}
	return DefaultFileMode
}
