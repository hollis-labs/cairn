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

// ErrPromptsSource reports that prompts were declared but the directory to
// read them from is unusable: the manifest names none, the one it names is not
// absolute once a leading "~/" is expanded, or it is not a directory. Cairn
// ships no prompts, so a profile that declares one has to say where it lives.
var ErrPromptsSource = errors.New("prompts directory is unusable")

// ErrPromptName reports a declared name that cannot name a file beneath the
// prompts source: it is empty, it is "." or "..", it holds a path separator,
// it carries the extension cairn appends, or the same name is declared twice.
var ErrPromptName = errors.New("invalid prompt name")

// ErrPromptNotFound reports that a declared prompt has no file in the prompts
// source. It stops the render rather than omitting the prompt, because the
// command it would have planted answers "Unknown command" and nothing says
// why.
var ErrPromptNotFound = errors.New("prompt not found")

// ErrPromptContent reports a prompt that cannot be planted: it is not a
// regular file, or it substitutes away to nothing.
var ErrPromptContent = errors.New("unusable prompt content")

// PromptFileExt is the extension a prompt file carries, in the bundle and in
// the boot directory both. A name declares neither of them — spec.prompts
// holds "handoff", and the file is handoff.md at each end.
const PromptFileExt = ".md"

// promptKind describes the prompt collection for the shared content helpers.
var promptKind = contentKind{
	key:       profile.SpecKeyPrompts,
	dirKey:    profile.SpecKeyPromptsDir,
	plural:    "prompts",
	entry:     "prompt file",
	nameErr:   ErrPromptName,
	sourceErr: ErrPromptsSource,
}

// PromptSource is one declared prompt as it was read: the name spec.prompts
// declared, the file its text came from, the path it plants at, and that text
// before any substitution.
//
// The text is the source and not the rendering, and that is the only reason
// this type exists. Substitution replaces every marker it acts on, so a
// rendered prompt cannot afterwards be asked which of its markers stood for
// nothing — and that question has a caller. `cairn boot` asks it of
// spec.templates and reports the answer on stderr; a template's text reaches
// that report on the [Instance], and a prompt's is on disk. This is how a
// prompt's gets there.
type PromptSource struct {
	// Name is what spec.prompts declared, without the extension.
	Name string

	// From is the absolute path the text was read from — what a diagnostic
	// about the prompt's content names, because that is the file to edit.
	From string

	// Path is where the prompt plants, relative to the boot directory root.
	// It is what /Name resolves to under the layout's prompt namespace and
	// what an operator looks for, so it is what a diagnostic about the planted
	// command names.
	Path string

	// Text is the file's bytes, unsubstituted.
	Text string
}

// PromptSources returns every prompt spec.prompts declares — resolved,
// checked, and read, and not substituted.
//
// It is the first half of [renderPrompts]: the declaration resolved against
// the directory the manifest names, each name checked for naming one file
// directly beneath it, and each file read. The substitution and the planting
// are the other half, and stay unexported because nothing outside this package
// plants a prompt.
//
// Exporting the read rather than leaving a caller to repeat it is the point.
// A caller wanting a prompt's source text would otherwise resolve the prompts
// directory a second time and decide a second time which file each name is,
// and two answers to one question is how a report comes to describe a file the
// render never read. That is the same failure in a different collection as the
// one the report exists to catch, so the read is shared instead.
//
// A profile declaring no prompts returns nothing and no error. The errors are
// [renderPrompts]'s because they are raised here: [ErrNoProfile],
// [ErrProviderLayout], [ErrPromptsSource], [ErrPromptName],
// [ErrPromptNotFound] and [ErrPromptContent].
//
// The output is deterministic: the prompts in the order the resolved manifest
// carries them, which for a key the cascade composed is sorted — see
// [profile.Resolve]. Nothing here depends on which, because the planted paths
// are one per name.
func PromptSources(inst *Instance) ([]PromptSource, error) {
	if inst == nil || inst.Profile == nil {
		return nil, ErrNoProfile
	}
	declared, err := inst.Profile.Spec.Prompts()
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return nil, nil
	}
	target := strings.TrimSpace(inst.Layout.PromptsDir)
	if target == "" {
		return nil, fmt.Errorf(
			"%w: spec.%s declares %s, and the %s layout declares no prompts directory",
			ErrProviderLayout, profile.SpecKeyPrompts, quotedNames(declared), inst.Layout.Provider)
	}
	dir, err := inst.Profile.Spec.PromptsDir()
	if err != nil {
		return nil, err
	}
	source, err := contentSource(dir, promptKind, declared, inst.Home, inst.Env)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(declared))
	out := make([]PromptSource, 0, len(declared))
	for _, raw := range declared {
		name := strings.TrimSpace(raw)
		if err := checkContentName(name, promptKind); err != nil {
			return nil, err
		}
		// The one mistake the shared check has no opinion about, and the one
		// an operator is most likely to make: a prompt is a file with an
		// extension, and writing the extension into the name would be read
		// from handoff.md.md. The not-found diagnostic would name that path
		// and be perfectly accurate about a path nobody wrote.
		// EqualFold, because the filesystems this runs on mostly are. On APFS
		// "handoff.MD" names the same file as "handoff.md", so a
		// case-sensitive guard would let it through to be read from
		// "handoff.MD.md" — which is the diagnostic about a path nobody wrote
		// that this guard exists to prevent, arriving in a different spelling.
		if strings.EqualFold(filepath.Ext(name), PromptFileExt) {
			return nil, fmt.Errorf(
				"%w: spec.%s declares %q; a prompt is named without its %s, so that would be read from %s",
				ErrPromptName, profile.SpecKeyPrompts, name, PromptFileExt,
				filepath.Join(source, name+PromptFileExt))
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("%w: spec.%s declares %q twice",
				ErrPromptName, profile.SpecKeyPrompts, name)
		}
		seen[name] = struct{}{}

		from := filepath.Join(source, name+PromptFileExt)
		text, err := readPrompt(name, source, from)
		if err != nil {
			return nil, err
		}
		out = append(out, PromptSource{
			Name: name,
			From: from,
			Path: path.Join(target, name+PromptFileExt),
			Text: text,
		})
	}
	return out, nil
}

// renderPrompts returns one file per prompt spec.prompts declares, substituted
// like any other template and planted under the layout's prompts directory.
//
// A prompt is [RenderSkills] for content a person invokes. The declaration
// cascades the same way, composes the same way, and resolves against a
// directory the manifest names for the same reason — so what is here is only
// the part that is about prompts: one flat file per name, substituted, at a
// path the harness reads as a namespaced command.
//
// It is a template and not a copy, which is the whole difference from a skill.
// The text goes through [Substitute], so a prompt carries `cairn:slot` and
// `cairn:value` markers and reaches the boot directory already knowing its
// scope, its session and its profile. Nothing new renders it: this is the same
// substitution spec.templates gets, over text read from a file instead of out
// of the manifest.
//
// A profile declaring no prompts renders nothing and reports no error. One
// declaring a prompt that cannot be planted reports [ErrPromptsSource],
// [ErrPromptName], [ErrPromptNotFound] or [ErrPromptContent], each naming the
// prompt and the path it was read from.
//
// It is unexported, where [RenderSkills] is not. The difference is who calls
// them: the installed layer renders skills through [RenderInstallSkills] and
// so needs the boot one on the same boundary, while nothing outside this
// package renders prompts — `cairn install` deliberately plants none. That is
// [renderSubagents]'s situation exactly, and this follows it. What is exported
// is [PromptSources], which reads and does not render.
func renderPrompts(inst *Instance) ([]File, error) {
	sources, err := PromptSources(inst)
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(sources))
	for _, src := range sources {
		file, err := renderPrompt(inst, src)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

// emptiedBy says why a prompt rendered nothing: it held nothing to begin with,
// or every marker in it stood for nothing, named.
//
// The distinction is the operator's next move. An empty file is a file to
// write; a file emptied by substitution is a slot that did not resolve, and
// that failure is already on stderr under a name this can be matched to.
func emptiedBy(text string) string {
	markers, err := Markers(text)
	if err != nil || len(markers) == 0 {
		return "holds nothing"
	}
	named := make([]string, 0, len(markers))
	for _, m := range markers {
		one := fmt.Sprintf("%s %q", m.Verb, m.Name)
		if !slices.Contains(named, one) {
			named = append(named, one)
		}
	}
	return "substitutes away to nothing — " + strings.Join(named, ", ") + " stood for nothing"
}

// readPrompt returns the bytes of the prompt named name, read from the file at
// from beneath source.
//
// It is the read and none of the rendering, so that [PromptSources] can hand a
// caller the source text and [renderPrompt] can substitute it without either
// of them owning the other's half.
func readPrompt(name, source, from string) (string, error) {
	// Stat and not Lstat, so a symlink to a regular file is read by value. That
	// is the skills copier's behaviour and this follows it rather than
	// narrowing it here: a bundle that keeps one prompt somewhere else and
	// links to it is a bundle, and refusing the link would be a rule invented
	// for the newer collection alone.
	info, err := os.Stat(from)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A dangling symlink lands here too, and "nothing at <path>" would be
		// wrong about it in the way that costs the most: there IS something
		// there, and an operator told to create the file finds one already in
		// the way. So the link is named, as the skills copier names it.
		if link, readErr := os.Readlink(from); readErr == nil {
			return "", fmt.Errorf("%w: prompt %q at %s is a symlink to %s, which does not exist",
				ErrPromptContent, name, from, link)
		}
		return "", fmt.Errorf("%w: spec.%s declares %q, which is not in %s: nothing at %s",
			ErrPromptNotFound, profile.SpecKeyPrompts, name, source, from)
	case err != nil:
		return "", fmt.Errorf("stat prompt %q at %s: %w", name, from, err)
	case !info.Mode().IsRegular():
		return "", fmt.Errorf("%w: prompt %q at %s is not a regular file",
			ErrPromptContent, name, from)
	}
	text, err := os.ReadFile(from)
	if err != nil {
		return "", fmt.Errorf("read prompt %q at %s: %w", name, from, err)
	}
	return string(text), nil
}

// renderPrompt substitutes one already-read prompt and returns it as the file
// it plants as.
func renderPrompt(inst *Instance, src PromptSource) (File, error) {
	rendered, err := Substitute(src.Text, inst.Sections, inst.Values)
	if err != nil {
		return File{}, fmt.Errorf("prompt %q at %s: %w", src.Name, src.From, err)
	}
	// A prompt that substitutes away to nothing is refused, where a template
	// that does renders no file. The difference is what each one is addressed
	// by. A template names its own destination and a profile that leaves one
	// empty has simply not written that document; a prompt is named in a list
	// and invoked by that name, so an empty one plants nothing and answers
	// "Unknown command" at the moment it is typed — which is the failure the
	// skills renderer refuses an empty skill to prevent, arriving later.
	//
	// It is worth being explicit about what this does to the slot rule. A slot
	// that fails to resolve is survivable everywhere else in cairn — the
	// section is dropped and the boot carries on — and a prompt made only of
	// such a slot is the one place that failure reaches the exit status. That
	// is the cost of refusing rather than planting nothing, and it is why the
	// message names the markers rather than only reporting the emptiness: the
	// slot failure is already on stderr, and this is what matches the two up.
	//
	// The markers named here are every marker the file held, which is a wider
	// set than the one `cairn boot` reports as unfilled — see [Unfilled]. A
	// prompt emptied by a slot no profile declared is emptied by a marker that
	// is nobody's mistake, and this has to say so; the report upstream names
	// only the faults. The two are a shortlist and an inventory, and a prompt
	// that empties out is the one case an operator reads both.
	if strings.TrimSpace(rendered) == "" {
		return File{}, fmt.Errorf("%w: prompt %q at %s %s, so /%s:%s would answer "+
			"\"Unknown command\"", ErrPromptContent, src.Name, src.From, emptiedBy(src.Text),
			inst.Layout.PromptNamespace, src.Name)
	}
	if !strings.HasSuffix(rendered, "\n") {
		rendered += "\n"
	}
	return File{Path: src.Path, Content: []byte(rendered)}, nil
}
