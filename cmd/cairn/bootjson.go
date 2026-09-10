package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chrispian/cairn/bootdir"
	goprovider "github.com/hollis-labs/go-providers/provider"
)

// bootReport is what `cairn boot --json` prints: everything a launcher needs to
// open the directory cairn just wrote, and everything this run did that the
// directory itself does not record, so that nothing has to read the files
// inside it — or scrape a diagnostic — to find out.
//
// It exists because the alternative was scraping. The reference launcher used
// to pull the scope out of the rendered AGENTS.md with sed, which meant a
// launcher's access grant depended on a marker's rendered position in a
// document whose whole purpose is to be re-authored. The scrape worked; it
// survived by luck, and a template edit that moved the line would have returned
// an empty scope with no error — a launcher granting nothing, silently.
//
// The second clause of that first sentence is the same defect twice more, and
// both are answered below. A boot directory does not record which bundle it was
// composed out of, and a --save-as was announced on stderr and nowhere else; in
// each case the launcher was left inferring or parsing something it should have
// been told.
//
// # The contract
//
// Flat and snake_case. What it has to be cheapest for is a consumer reading
// one key at a time out of a shell — `jq -r '.settings_path'` — and every
// value here is a scalar or a list of them, so there is nothing for nesting to
// group.
//
// Every key is emitted on every boot. The key set is the contract: a key that
// came and went would make a consumer handle two shapes for one meaning, and it
// would get that wrong once.
//
// A value cairn does not have is null, never "" and never []. An empty string
// is the shape most likely to be interpolated straight into argv — `claude
// $FLAG "$VALUE"` passes an empty argument and the launch is wrong in a way
// nothing reports — where null forces the consumer to decide. Every key whose
// type is a pointer or a slice can be null, and each says something different:
// no scope was resolved, no file stands at the harness's settings path, this
// provider needs no flag to grant a directory, no --set
// was dropped from a save. Which those are is read off the struct below rather
// than counted here, so that adding another cannot make this paragraph wrong.
//
// ProfileRoot is a plain string and never null, and that is this rule applied
// rather than an exception to it: null is for a value cairn does not have, and
// a boot that resolved no bundle wrote no directory to describe.
//
// Scope and SettingsPath are not independent, and the dependency runs one way
// only: a
// non-null Scope guarantees a non-null SettingsPath, because the scope is
// itself a granted directory — see bootdir.grantedDirectories, which adds it
// first — and a directory to grant is enough on its own to render the settings
// document. The converse does not hold. A profile with no scope that declares
// spec.access.directories renders one anyway, so SettingsPath is non-null with
// Scope null, and nothing may read the pair as equivalent.
//
// There is no version field, and the reason it is absent is the rule that
// replaces it: **new keys are free; renaming or removing one is breaking and
// must update every consumer in the same change.** The rule does not rest on
// how many consumers there are to break, and it holds at none, because the
// cost of a rename lands on whoever adopts the contract next and the two
// directions do not cost the same. Keeping a key that has already been
// published costs a line in this struct. Renaming one hands a consumer still
// reading the old name a null where a path used to be, which is not a parse
// error but a session opened without --settings: no access grant, no trusted
// tier, and nothing reporting either. What prevents that is not a number a
// consumer would have to check but a sentence where someone about to rename a
// field will read it. A version integer would not have stopped the rename.
type bootReport struct {
	// BootDir is the directory that was written, absolute. It is what stdout
	// carries without --json, and it is repeated here so the object stands on
	// its own: a launcher that has this document has never needed another
	// output of the command.
	BootDir string `json:"boot_dir"`

	// Provider is the harness the directory was rendered for. A launcher
	// passing --settings does not need it today; it is the other half of what
	// makes the object self-describing, and adding it later is free where
	// needing it later is not.
	Provider string `json:"provider"`

	// ProfileRoot is the bundle this boot was composed out of, which is also
	// what $CAIRN_PROFILE_ROOT expanded to in every manifest value that names
	// somewhere to read from. It is [showReport.ProfileRoot] — same key, same
	// value, read off the same catalog — because a launcher holding both
	// documents must not get two answers for one directory.
	//
	// The boot directory does not record it, and the bundle resolves without
	// it: with no flag and no variable, [bundleRoot] falls to
	// $XDG_CONFIG_HOME/agents and then to ~/.config/agents. So a harness
	// launched with --profile that runs cairn again from inside its own boot
	// directory reads a DIFFERENT bundle — silently, and correctly by every
	// rule as written, which is what makes it hard to see. That is exactly the
	// class of failure this document exists to end.
	//
	// Never null, and that is the null rule applied rather than an exception
	// to it. Null is for a value cairn does not have, and there is no such
	// state: a boot that resolved no bundle read no profile and wrote no
	// directory, so there is no document for the null to appear in.
	//
	// Reported as the catalog holds it — the flag absolutized, the variable
	// and the default passed through as they were exported. That asymmetry is
	// [resolveProfileRoot]'s and is not re-decided here; a document that tidied
	// the value would hand back a spelling the operator cannot find in their
	// own shell, and would disagree with `cairn show` about one directory.
	//
	// Cairn does not export it into anything, and that is deliberate. Cairn
	// writes a directory and describes it; the process that has a child to put
	// a variable into is the one that spawns the harness, and passing this on
	// is a launcher's job exactly as passing --settings is. What was missing
	// was never the export — it was the value.
	ProfileRoot string `json:"profile_root"`

	// Scope is the directory the instance works in, absolute and symlink
	// resolved, or null when no --scope was
	// given.
	Scope *string `json:"scope"`

	// SettingsPath is the absolute path of the file standing at the harness's
	// settings path, or null when the render produced none — a profile that
	// declares no spec.settings and has no directory to grant produces no such
	// file.
	//
	// What it promises is that a file is there, not that cairn composed it. It
	// is read off what the render actually produced rather than off the layout,
	// because the layout says where such a file would go and not whether there
	// is one; a profile that declares a template at that same path is reported
	// here too, and correctly, because the key exists so that a launcher can
	// pass --settings to something that opens. Which renderer wrote it is a
	// question this key does not answer and a launcher does not ask.
	SettingsPath *string `json:"settings_path"`

	// CwdPreference is where the harness expects to be invoked: "boot_dir" or
	// "project_dir". It is the preference and not a resolved directory,
	// because cairn launches nothing and choosing between BootDir and Scope on
	// a launcher's behalf would be cairn deciding the invocation.
	CwdPreference string `json:"cwd_preference"`

	// ProjectDirArg is the provider's flag for granting access to the scope,
	// already split into argv tokens and with the placeholder left standing —
	// ["--add-dir", "{{.ProjectDir}}"] for Claude Code — or null when the
	// harness needs no flag at all.
	//
	// Split here, substituted there, and the order is the whole point. The
	// spec's own spelling is one string, "--add-dir {{.ProjectDir}}", and a
	// consumer handed that has to do both steps itself: substitute first and
	// then split, and a scope named ".../scope with space" becomes two
	// arguments instead of one, silently, on the one input nobody tests.
	// Nothing in go-providers states the order — both of that library's own
	// boot-dir examples replace on the whole pattern — so a consumer reading
	// this document and the upstream examples together lands on the unsafe
	// recipe. Split into tokens, no element can contain whitespace and the
	// shape is safe by construction rather than by discipline.
	//
	// The placeholder is deliberately left un-substituted. go-providers
	// documents substitution as the app's to perform at spawn time, and
	// substituting it here would tie this key to Scope: a boot with no scope
	// would have to report null, which is already how "this harness needs no
	// flag" is spelled. Null therefore means one thing only, and Scope — in
	// this same object, and what {{.ProjectDir}} stands for — says the other.
	ProjectDirArg []string `json:"project_dir_arg"`

	// EnvAmendments are provider-declared KEY=VALUE entries a launcher should
	// add to the spawned process environment, with provider placeholders left
	// for the launcher to substitute at spawn time. Null means the provider
	// declares none.
	EnvAmendments []string `json:"env_amendments"`

	// HomeResourcePaths are provider-home-relative resources Cairn does not
	// render into the boot directory, but a launcher that follows
	// EnvAmendments and points the provider home at the boot directory must
	// deliberately provide before launch. Null means Cairn knows of no such
	// resource for the selected provider.
	//
	// Codex reports auth.json, hooks.json and hooks. They are not rendered
	// because copying credentials or hook registrations into disposable boot
	// directories would make Cairn the owner of live operator state. Reporting
	// them gives a manual operator or launcher a checklist without changing
	// that ownership boundary.
	HomeResourcePaths []string `json:"home_resource_paths"`

	// There are no saved-composition keys. `--save-as` wrote a binding file
	// and this document reported its path and the --set values it dropped;
	// bindings retired, so a boot writes exactly one thing — the directory
	// this document describes — and there is nothing else for a launcher to
	// be told it created.
}

// projectDirPlaceholder is what a BootDirSpec's ProjectDirArg writes where the
// scope goes. It is not substituted here — see [bootReport.ProjectDirArg] —
// and is named only so the tests can assert it survives the split intact.
const projectDirPlaceholder = "{{.ProjectDir}}"

// bootDocument describes one written boot directory and renders the result the
// way every other JSON document cairn writes is rendered: laid out at
// [bootdir.JSONIndent], and with HTML escaping off.
//
// Escaping is off for the reason [bootdir.IndentJSON] and the settings fragment
// have it off. "&", "<" and ">" are legal in a directory name, every string in
// this document is a path or a flag, and an operator reads this one at a
// terminal long before any launcher parses it. A report spelling a path in
// & while the settings file one directory over spells it plainly would be
// cairn contradicting itself about the same directory.
//
// It is still one object and nothing else, so `$(cairn boot x --json)` is
// parseable. [json.Encoder] supplies the trailing newline that makes it a line.
func bootDocument(dir string, layout bootdir.Layout, scopeDir, profileRoot string,
	files []bootdir.File) (string, error) {

	report, err := newBootReport(dir, layout, scopeDir, profileRoot, files)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", bootdir.JSONIndent)
	if err := enc.Encode(report); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// newBootReport describes one written boot directory.
//
// files is what [bootdir.Render] produced, which is how SettingsPath can report
// a file that is there rather than one the layout has a path for.
func newBootReport(dir string, layout bootdir.Layout, scopeDir, profileRoot string,
	files []bootdir.File) (bootReport, error) {

	cwd, err := cwdPreferenceName(layout.CwdPreference)
	if err != nil {
		return bootReport{}, err
	}
	return bootReport{
		BootDir:           dir,
		Provider:          layout.Provider.String(),
		ProfileRoot:       profileRoot,
		Scope:             nullable(scopeDir),
		SettingsPath:      nullable(renderedPath(dir, layout.Settings, files)),
		CwdPreference:     cwd,
		ProjectDirArg:     argvTokens(layout.ProjectDirArg),
		EnvAmendments:     nonEmpty(layout.EnvAmendments),
		HomeResourcePaths: nonEmpty(layout.HomeResourcePaths),
	}, nil
}

// argvTokens splits a BootDirSpec's flag pattern into argv elements, or returns
// nil when the provider declares none.
//
// [strings.Fields] is the whole split, and whitespace is the whole separator a
// spec has ever used: all three of go-providers' adapters spell the pattern as
// a flag, a space, and the placeholder. A spec that put the placeholder inside
// a token — "--dir={{.ProjectDir}}" — comes through as one element and
// substitutes correctly there too, which is why this splits rather than
// assuming the pair.
//
// nil and not an empty slice, because [bootReport] spells an absent value null
// and [] would read as "pass these zero arguments" rather than "this harness
// takes no such flag".
func argvTokens(pattern string) []string {
	fields := strings.Fields(pattern)
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// renderedPath returns the absolute path a is written at, or "" when the layout
// declares no path for it or the render produced no file there.
func renderedPath(dir string, a bootdir.Artifact, files []bootdir.File) string {
	if !a.Declared() {
		return ""
	}
	for _, f := range files {
		if f.Path == a.RelPath {
			return filepath.Join(dir, filepath.FromSlash(a.RelPath))
		}
	}
	return ""
}

// cwdPreferenceName names a [goprovider.CwdPreference] for the report.
//
// The names are cairn's. go-providers declares the constants and gives them no
// String method, so something has to spell them, and spelling them as the
// integers they are would put a launcher in the business of tracking another
// library's iota order.
//
// A value neither constant covers is an error rather than a fallback, for the
// reason [bootdir.ErrProviderLayout] is one: the library would have grown a
// third preference cairn has not caught up with, and reporting it as one of the
// two would have a launcher invoke the harness in the wrong directory while the
// document reads as though it knew.
func cwdPreferenceName(p goprovider.CwdPreference) (string, error) {
	switch p {
	case goprovider.CwdBootDir:
		return "boot_dir", nil
	case goprovider.CwdProjectDir:
		return "project_dir", nil
	default:
		return "", fmt.Errorf("%w: its BootDirSpec declares a working-directory preference cairn has no name for (%d)",
			bootdir.ErrProviderLayout, int(p))
	}
}

// nullable returns a pointer to s, or nil when s is empty — the one place the
// report's "absent is null, never empty" rule is enforced for a string.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
