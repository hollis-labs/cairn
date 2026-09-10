// Command cairn assembles files and writes them into a directory.
//
// It reads a profile out of a bundle directory, resolves it through an extends
// cascade, and materializes a boot directory a CLI coding agent can be
// launched from. It prints the path — or, with --json, one object describing
// the boot — and exits; a human launches.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chrispian/cairn/bootdir"
	"github.com/chrispian/cairn/catalog"
	"github.com/chrispian/cairn/install"
	"github.com/chrispian/cairn/profile"
	"github.com/chrispian/cairn/scope"
	"github.com/chrispian/cairn/slots"
	"github.com/hollis-labs/agentkit/agentcontext"
)

const usage = `cairn assembles files and writes them into a directory.

usage:
  cairn boot <profile> [flags]              materialize a boot directory, print its path
  cairn install <profile> [flags]           render the installed layer
  cairn show <profile> [flags]              print what the profile resolves to
  cairn list [flags]                        enumerate the catalog

flags for boot and show:
  --with <part>          a profile merged after the extends chain resolves, closest-wins
                         and in the order given. Repeatable. A part is an ordinary
                         profile, so anything composable is also bootable and inspectable
                         on its own. A value holding a path separator, or beginning with
                         ".", "~" or "$", names a file; anything else is a catalog id, so
                         a part in the current directory is written ./part.md.
                         A profile the resolution has already reached — the target,
                         anything it extends, or a part named earlier — is folded once
                         where it first landed, so naming it again adds nothing and does
                         not move it: a part brings what it adds, and never reverts what
                         a profile closer to it already settled. Such a part is named on
                         stderr, so a flag that changed nothing is not silent about it
  --skill <a,b,c>        a skill the boot directory carries, added to the ones the profile
                         resolves to. Comma-separated and repeatable, the two forms
                         equivalent and composing. Additive only: nothing in cairn removes
                         a member of a collection keyed by its own id, so a session that
                         wants fewer skills boots a different profile
  --prompt <a,b,c>       a prompt the boot directory carries, added to the ones the profile
                         resolves to, planted as a command the operator invokes by name:
                         /boot:<name>. Comma-separated and repeatable, the two forms
                         equivalent and composing. Additive only, for the reason --skill is.
                         Cairn plants the file and prints the boot directory; nothing
                         fires a prompt, and a person types the command
  --set <slot>=<value>   an inline literal for a named slot, merged last. Repeatable. It
                         replaces a declared slot of that name whole, section included,
                         exactly as a part declaring that slot would

flags for boot:
  --scope <path>         the directory the instance works in
  --boot-root <path>     where boot directories are planted; defaults to $CAIRN_BOOT_ROOT,
                         else ~/.local/state/cairn/boot. Refused when it resolves inside a
                         git repository that is not the scope's: a boot directory is the
                         agent's working directory, so a boot root inside a foreign
                         checkout aims every git command the session runs at that
                         checkout, while the same boot.md's slots report the scope
  --session <name>       the session segment; defaults to a UTC timestamp and a random suffix
  --json                 print one JSON object describing the boot instead of the bare path

flags for install:
  --check                re-render, diff against disk, report drift, write nothing
  --root <path>          where the installed layer goes; defaults to the home directory

flags for show:
  --scope <path>         the scope to report, as boot would resolve it
  --json                 print one JSON object describing what the target resolves to, instead
                         of the document laid out for reading. It carries the merged manifest
                         and, per key, the profiles and flags that declared it — which is the
                         half a consumer cannot reconstruct. Per key: that spec.slots came
                         from two profiles, not which of them supplied the slot in front of you

flags for boot, install and show:
  --provider <name>      the harness this materializes into: it selects the layout the
                         files are written as, and the spec.settings document written
                         into them. Defaults to the provider the profile declares, so a
                         command that does not pass it renders what it always did. A
                         provider is a materialization target rather than a property of
                         the content — access, slots, templates and skills are neutral
                         and serve every target — which is why one profile can be asked
                         for another harness at all. claude and codex layouts are
                         implemented; opencode is refused by name rather than rendered
                         as another harness's files

flags for all four:
  --profile <dir>        the profile bundle — the directory the catalog is read from,
                         holding profiles/ and templates/. Defaults to $CAIRN_PROFILE_ROOT,
                         else $XDG_CONFIG_HOME/agents, else ~/.config/agents.
                         $CAIRN_PROFILE_ROOT expands to it in every manifest value that
                         names somewhere to read from, so a profile says
                         $CAIRN_PROFILE_ROOT/templates/agents.md and the bundle relocates
                         without edits

cairn install is human-executed. Every agent working under a provider home such
as ~/.claude or ~/.codex that runs it rewrites its own live configuration
mid-session.

cairn show and cairn install --check render nothing and write nothing — no boot
directory, no installed layer, and no part of the bundle they read. A read that
finds nothing says which bundle it was reading and where.
`

func main() {
	err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	if err == nil {
		return
	}
	// Drift is a finding, not a failure: `install --check` has already printed
	// its report to stdout, and writing it again to stderr as an error would
	// say the same thing twice in two voices.
	var code exitCode
	if errors.As(err, &code) {
		os.Exit(int(code))
	}
	fmt.Fprintf(os.Stderr, "cairn: %v\n", err)
	os.Exit(1)
}

// run is main's body with its inputs and outputs passed in, so that the
// command is testable without a process.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return errors.New("no command")
	}
	switch args[0] {
	case "boot":
		return runBoot(ctx, args[1:], stdout, stderr)
	case "install":
		return runInstall(ctx, args[1:], stdout, stderr)
	case "show":
		return runShow(ctx, args[1:], stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(stdout, usage)
		return nil
	default:
		_, _ = fmt.Fprint(stderr, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// exitCode is an error that carries only a process exit status. It is how
// `install --check` reports drift: drift is a finding, not a failure, so the
// report goes to stdout and nothing is written to stderr, but the status has
// to be non-zero for a shell to branch on it.
type exitCode int

// Error implements error.
func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

// runInstall renders the installed layer, or checks it against disk.
//
// cairn install is human-executed, permanently. An agent running under the
// provider home this writes rewrites its own live configuration mid-session.
// Nothing here enforces that — plan §1 rules out validation whose only job is
// to stop the operator doing what the operator meant — so the convention is
// documented and not policed.
func runInstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cairn install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		check        = fs.Bool("check", false, "re-render, diff against disk, report drift, write nothing")
		rootFlag     = fs.String("root", "", "the directory the installed layer is written beneath")
		providerFlag = fs.String("provider", "", providerFlagUsage)
		profileFlag  = fs.String("profile", "", profileFlagUsage)
	)
	target, rest := splitTarget(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if target == "" {
		target = fs.Arg(0)
	} else if fs.NArg() > 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return fmt.Errorf("install takes one profile, and was given %q as well", fs.Arg(0))
	}
	if target == "" || fs.NArg() > 1 {
		_, _ = fmt.Fprint(stderr, usage)
		return errors.New("install takes exactly one profile")
	}

	home, _ := os.UserHomeDir()

	// The bundle this command reads, and the environment every value in its
	// manifest expands against. They are one value: the catalog is the store,
	// so the directory the profile came out of is the directory
	// $CAIRN_PROFILE_ROOT names — see [bundleRoot] and [environment].
	bundle, err := bundleRoot(*profileFlag, home)
	if err != nil {
		return err
	}
	env := environment(bundle)

	cat, err := catalog.Open(bundle)
	if err != nil {
		return err
	}

	// install renders the machine-wide layer every session loads, so it takes
	// no composition at all: none of --with, --skill, --prompt or --set, and
	// nothing saved for it to replay. Bindings retired, and with them the
	// stderr line this command owed an operator who booted a composition and
	// installed none of it.
	profileID, err := profileTarget(ctx, cat, target)
	if err != nil {
		return err
	}
	// No abstract check. The installed layer is normally rendered from the
	// abstract root of the cascade, and refusing one here would refuse the
	// profile this command mostly exists to render. `cairn boot` is where a
	// direct boot of an abstract profile is refused — plan §7.
	resolved, err := profile.Resolve(ctx, cat, profileID)
	if err != nil {
		return err
	}

	// The harness this layer is written for. --provider is not a composition
	// flag and the paragraph above does not reach it: a composition says what
	// one launch carries, which the installed layer has no place for, while
	// this says which harness's layer is being written at all. The installed
	// layer is per provider by construction — it lands in that harness's own
	// directory under the root — so the question is exactly as meaningful here
	// as it is for a boot.
	provider, providerNamed, err := selectProvider(*providerFlag, resolved.Provider, resolved.ID)
	if err != nil {
		return err
	}

	dir := *rootFlag
	if strings.TrimSpace(dir) == "" {
		if strings.TrimSpace(home) == "" {
			return fmt.Errorf("%w: pass --root to say where the installed layer goes", install.ErrNoRoot)
		}
		dir = home
	}
	root, err := install.NewRoot(dir)
	if err != nil {
		return err
	}
	// Templates resolve here for the reason they do in a boot: a template may
	// name a source, and reading one is I/O. No slots are resolved — see
	// install.layerInstance — so a template's slot markers substitute nothing
	// in this layer.
	templates, err := slots.ResolveEntries(ctx, resolved.Spec, profile.SpecKeyTemplates,
		slots.Options{Env: env})
	if err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}
	// Slots resolve here too, restricted to the kinds whose answer changes only
	// when the operator changes something. Without them an installed template
	// renders a skeleton; with the rest of them a check would run the profile's
	// commands and report drift on every invocation.
	assembled, err := slots.Assemble(ctx, resolved.Spec, slots.Options{
		Deterministic: true,
		Env:           env,
		Provenance: agentcontext.ProvenanceInput{
			LineageAlias: target,
			ProfileID:    resolved.ID,
		},
	})
	if err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}
	if assembled != nil {
		expansions, err := slots.Expansions(resolved.Spec, profile.SpecKeySlots, env)
		if err != nil {
			return fmt.Errorf("profile %q: %w", resolved.ID, err)
		}
		reportSlotFailures(stderr, assembled, expansions)
	}
	sections, err := slots.Sections(assembled)
	if err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}
	// The slots this layer does not resolve at all, named once rather than as
	// one puzzling empty section per marker.
	skipped, err := slots.Nondeterministic(resolved.Spec)
	if err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}
	if len(skipped) > 0 {
		_, _ = fmt.Fprintf(stderr,
			"cairn: the installed layer renders no section for %s: it resolves only %s, "+
				"because a check re-renders and anything else would report drift on every run\n",
			strings.Join(skipped, ", "), kindList(slots.DeterministicKinds()))
	}

	values := instanceValues(map[string]string{
		"profile": resolved.ID,
		// The target rather than the declaration, for the reason the
		// layer is rendered for the target: a value marker names a fact
		// about this materialization, and "which harness is this" is one.
		"provider": provider.String(),
		"model":    resolved.Model,
	})

	// The profile's body, rendered. Deterministic for the reason the slots
	// above are: this layer is diffed against disk by `--check`, and a body
	// whose inline sources ran a command would report drift on every run.
	document, err := renderDocument(ctx, cat, resolved, values, slots.Options{
		Deterministic: true,
		Env:           env,
		Provenance: agentcontext.ProvenanceInput{
			LineageAlias: target,
			ProfileID:    resolved.ID,
		},
	}, stderr)
	if err != nil {
		return err
	}

	lay := &install.Layer{
		Root:      root,
		Profile:   resolved,
		Provider:  provider,
		Home:      home,
		Env:       env,
		Document:  document,
		Templates: templates,
		Sections:  sections,
		Values:    values,
	}

	// Reported before the render, and reported for a check as well as a write.
	// A marker that stood for nothing can empty a template to the point where
	// no file is written at all, and this layer's pointer document is a
	// declared include of the instruction file beside it: lose the instruction
	// file and the pointer resolves to nothing, silently, which is the exact
	// outcome making the pointer a template was meant to prevent.
	//
	// A check catches that on a root that already carries the file. It claims
	// the paths its renderers can produce rather than the ones one render did
	// — plan §7 — so an instruction file that stopped rendering is an orphan,
	// and the check exits non-zero naming it. What it cannot catch is the
	// first install into a root that never held the file: nothing on disk to
	// orphan, nothing rendered to diff, "In sync". That is the case this line
	// is for, and it is reported for a check as well as a write because a
	// check is where an operator goes to ask whether the layer is right.
	renderers, layout, err := install.PlanterFor(provider)
	if err != nil {
		return fmt.Errorf("%s: %w", providerNamed, err)
	}
	if err := reportUnfilledMarkers(stderr, installedTemplates(templates, renderers, layout), sections); err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}

	if *check {
		report, err := install.Check(lay)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprint(stdout, report.String()); err != nil {
			return err
		}
		if code := report.ExitCode(); code != 0 {
			return exitCode(code)
		}
		return nil
	}

	res, err := install.Install(lay)
	if err != nil {
		return err
	}
	for _, rel := range res.Files {
		if _, err := fmt.Fprintln(stdout, filepath.Join(res.Root, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	return nil
}

// kindList renders slot kinds for a diagnostic.
func kindList(kinds []agentcontext.SlotSourceKind) string {
	quoted := make([]string, 0, len(kinds))
	for _, k := range kinds {
		quoted = append(quoted, string(k))
	}
	return strings.Join(quoted, ", ")
}

// profileTarget checks that the bundle holds the profile a command was given,
// and returns its id.
//
// It is one line of work and it exists for the diagnostic. The argument used
// to be a binding OR a profile, tried in that order, and the failure had to
// say that neither was found; a target is now a profile and nothing else, so
// the refusal says so and names the bundle it looked in.
func profileTarget(ctx context.Context, cat *catalog.Catalog, name string) (string, error) {
	if _, err := cat.Profile(ctx, name); err != nil {
		if errors.Is(err, catalog.ErrProfileNotFound) {
			return "", fmt.Errorf("%s: no profile named %q", cat.Root(), name)
		}
		return "", err
	}
	return name, nil
}

// runBoot materializes one boot directory and prints its path, or --json and
// prints one object describing it.
//
// Without --json stdout is the bare path and nothing else. That is the seam
// this command is, and --json does not move it: a caller that wants a path
// keeps getting exactly a path, and one that wants the rest asks for it.
func runBoot(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cairn boot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		scopeFlag    = fs.String("scope", "", "the directory the instance works in")
		rootFlag     = fs.String("boot-root", "", "where boot directories are planted")
		sessFlag     = fs.String("session", "", "the session segment")
		jsonFlag     = fs.Bool("json", false, "print one JSON object describing the boot instead of the bare path")
		providerFlag = fs.String("provider", "", providerFlagUsage)
		profileFlag  = fs.String("profile", "", profileFlagUsage)
	)
	var compose composition
	compose.bind(fs)
	target, rest := splitTarget(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if target == "" {
		target = fs.Arg(0)
	} else if fs.NArg() > 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return fmt.Errorf("boot takes one profile, and was given %q as well", fs.Arg(0))
	}
	if target == "" || fs.NArg() > 1 {
		_, _ = fmt.Fprint(stderr, usage)
		return errors.New("boot takes exactly one profile")
	}

	home, _ := os.UserHomeDir()

	// The bundle this command reads, and the environment every value in its
	// manifest expands against. They are one value: the catalog is the store,
	// so the directory the profile came out of is the directory
	// $CAIRN_PROFILE_ROOT names — see [bundleRoot] and [environment].
	bundle, err := bundleRoot(*profileFlag, home)
	if err != nil {
		return err
	}
	env := environment(bundle)

	cat, err := catalog.Open(bundle)
	if err != nil {
		return err
	}

	name, err := profileTarget(ctx, cat, target)
	if err != nil {
		return err
	}

	// The composition resolves through the same call whether or not anything
	// was composed: --with, --skill, --prompt and --set contribute nothing
	// when they were not given, and a second code path for the plain case is a second
	// place for the two to disagree about what a boot resolves to.
	resolved, _, err := compose.resolve(ctx, cat, home, env, name)
	if err != nil {
		return err
	}
	// Reported before anything is written, beside the other things an operator
	// hears about a resolution rather than a render.
	compose.reportAbsorbedParts(stderr, resolved)
	// The target's own leaf decides this, never a part — a part is a fragment
	// and may well be abstract. See profile.ResolveComposition.
	if resolved.Abstract {
		return fmt.Errorf("profile %q is abstract: it exists to be extended, not booted", resolved.ID)
	}

	// The harness this boot directory is written for, and the one thing that
	// decides which spec.settings document reaches it. The layout carries it
	// down: bootdir.RenderSettings reads inst.Layout.Provider rather than the
	// profile's own declaration, which is what makes the flag select a target
	// rather than rewrite the profile.
	//
	// --provider is deliberately not one of the composition flags. Those add
	// content — a part, a skill, a prompt, an inline slot — and are refused on
	// install for exactly that reason. This one names where the content is
	// being written to, which is a question every command that materializes
	// anything has to answer.
	provider, providerNamed, err := selectProvider(*providerFlag, resolved.Provider, resolved.ID)
	if err != nil {
		return err
	}
	layout, err := bootdir.LayoutFor(provider)
	if err != nil {
		return fmt.Errorf("%s: %w", providerNamed, err)
	}

	// The scope is the flag's or nothing. It used to have a second source —
	// a binding's saved default — and dropping that is the point: a scope
	// belongs to the launch, so the launcher supplies it and cairn holds no
	// default of its own to fall back to.
	scopeDir, err := scope.Parse(strings.TrimSpace(*scopeFlag), home)
	if err != nil {
		return err
	}

	bootRoot := *rootFlag
	if strings.TrimSpace(bootRoot) == "" {
		bootRoot, err = bootdir.DefaultRoot(os.Getenv(bootdir.EnvBootRoot), home)
		if err != nil {
			return err
		}
	}

	// Refused here, before a session segment is even minted, because this is a
	// property of the configuration rather than of this boot: the same flag,
	// the same variable and the same default answer the same way every time,
	// so the earliest point the answer is knowable is the right place to say
	// it. The default satisfies this on its own; the two overrides above are
	// what it guards, and a launcher setting one is not a hypothetical.
	if err := bootdir.CheckRoot(bootRoot, scopeDir); err != nil {
		return err
	}

	session := *sessFlag
	if strings.TrimSpace(session) == "" {
		session, err = bootdir.NewSession(time.Now(), nil)
		if err != nil {
			return err
		}
	}
	dir, err := bootdir.Location{Root: bootRoot, Name: name, Session: session}.Dir()
	if err != nil {
		return err
	}

	// The one validation scope carries, and it guards this write.
	if err := scope.CheckBootDir(scopeDir, dir); err != nil {
		return err
	}

	// Slots resolve here rather than inside a renderer: resolving one runs
	// commands and makes requests, and a renderer may do neither.
	assembled, err := slots.Assemble(ctx, resolved.Spec, slots.Options{
		// The lookup, handed down rather than reached for, so a renderer and a
		// resolver expand the same manifest the same way and neither has a
		// hidden input. What an environment answers is decided in one place —
		// [environment] — and nowhere below; the reads themselves happen
		// wherever a value is expanded, through the closure it returned.
		Env: env,
		// Scope is the instance's working directory, and the workdir is what
		// workdir-relative slot paths resolve against. They are the same
		// directory, and joining them is this composition root's call to make
		// — package slots is handed the value and asks no questions about it.
		Workdir: scopeDir,
		// No budget. Read that as "cairn imposes none", not as "nothing here
		// can get large": http_text, http_json, role_summary and static_dir
		// each enforce a resolver-side cap and report Truncated, but cmd and
		// static_file do not — cmd clamps its duration and never its output
		// size. A cmd slot is therefore the one genuinely unbounded path into
		// boot.md, and bounding it is the operator's business for now.
		Provenance: agentcontext.ProvenanceInput{
			LineageAlias: name,
			ProfileID:    resolved.ID,
		},
	})
	if err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}
	if assembled != nil {
		// The declared form of an expanded path or URL, which only cairn ever
		// held: the resolver was handed the expansion and can name nothing
		// else. Without this a slot written "$AGENT_DOCS/process.md" with the
		// variable unset reports a failure to open "/process.md", and the
		// operator searches for a path nobody typed.
		expansions, err := slots.Expansions(resolved.Spec, profile.SpecKeySlots, env)
		if err != nil {
			return fmt.Errorf("profile %q: %w", resolved.ID, err)
		}
		reportSlotFailures(stderr, assembled, expansions)
	}
	// One rendered section per declared slot, addressed by name. The assembled
	// rendering the library returns is discarded: a template decides what order
	// sections appear in and whether they appear at all, so what is wanted is
	// each section on its own.
	sections, err := slots.Sections(assembled)
	if err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}

	// Files resolve here for the same reason slots do, and unlike a slot a
	// source that fails fails the boot: a missing section is degraded context,
	// a missing file is a hole at a path the profile promised.
	planted, err := slots.ResolveFiles(ctx, resolved.Spec,
		slots.Options{Workdir: scopeDir, Env: env})
	if err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}

	// A template's text resolves the same way a file's does, and for the same
	// reason: a profile keeps its prose in a file more often than in the
	// database, and reading one is I/O.
	templates, err := slots.ResolveEntries(ctx, resolved.Spec, profile.SpecKeyTemplates,
		slots.Options{Workdir: scopeDir, Env: env})
	if err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}

	// Subagent declarations resolve here for the same reason slots and files
	// do, and it is the third form the reason takes: naming a subagent means
	// reading another profile out of the catalog and walking its extends
	// chain, and a renderer does no I/O.
	subagents, err := resolveSubagents(ctx, cat, resolved)
	if err != nil {
		return err
	}

	values := instanceValues(map[string]string{
		"profile": resolved.ID,
		// The target rather than the declaration. A value marker names a
		// fact about this materialization, and which harness it was
		// written for is one of them — the same string bootReport carries,
		// which it already reads off the layout.
		"provider": provider.String(),
		"model":    resolved.Model,
		"scope":    scopeDir,
		"session":  session,
	})

	// The profile's body, rendered into the one instruction document this
	// boot directory plants. Resolved here rather than in a renderer for the
	// reason the slots and files above are: an inline source runs a command.
	document, err := renderDocument(ctx, cat, resolved, values, slots.Options{
		Workdir: scopeDir,
		Env:     env,
		Provenance: agentcontext.ProvenanceInput{
			LineageAlias: name,
			ProfileID:    resolved.ID,
		},
	}, stderr)
	if err != nil {
		return err
	}

	inst := &bootdir.Instance{
		Dir:       dir,
		Layout:    layout,
		Home:      home,
		Env:       env,
		Profile:   resolved,
		Scope:     scopeDir,
		Files:     planted,
		Subagents: subagents,
		Document:  document,
		Templates: templates,
		Sections:  sections,
		Values:    values,
	}
	// Reported before the write rather than after it, so that an operator
	// reading stderr sees the missing block named beside the slot failure that
	// explains it.
	//
	// Prompts are reported here beside the templates, and being here at all is
	// the point: a prompt is planted as a slash command, so a marker in one
	// that filled nothing is a command a person RUNS with a block of its
	// instructions missing and nothing anywhere saying so. It is a report and
	// not a refusal, the way the template one is — the boot still exits 0.
	//
	// One call and not two. The trailing line naming the values cairn fills is
	// printed once per report, so a second call would print it twice for a
	// profile that misspelled a value in each collection, and the sort that
	// makes two boots of one profile report in the same order only orders what
	// it is handed.
	//
	// It costs a second read of the prompt files, since the render reads them
	// again. That is a handful of small files against a report that is the
	// only thing standing between an operator and a half-empty command, and
	// the alternative — carrying the sources from here into the render — is a
	// second way to render a boot directory.
	if err := reportUnfilledMarkers(stderr,
		append(bootTemplates(templates), bootPrompts(inst)...), sections); err != nil {
		return fmt.Errorf("profile %q: %w", resolved.ID, err)
	}
	files, err := bootdir.Render(inst)
	if err != nil {
		return err
	}
	if _, err := bootdir.PlantFiles(ctx, dir, files); err != nil {
		return err
	}

	// Whichever form it takes, this is the whole output of the command, so a
	// write that fails is reported rather than dropped — and it names the
	// directory, which by now exists, so the failure does not also lose it.
	//
	// The bundle it quotes is the catalog's own root rather than the
	// value bundleRoot returned — the same string, read from the place `cairn
	// show` reads it, so one directory has one spelling in both documents.
	out := dir + "\n"
	if *jsonFlag {
		out, err = bootDocument(dir, layout, scopeDir, cat.Root(), files)
		if err != nil {
			return fmt.Errorf("the boot directory was written to %s but it could not be described: %w", dir, err)
		}
	}
	if _, err := fmt.Fprint(stdout, out); err != nil {
		return fmt.Errorf("the boot directory was written to %s but its path could not be printed: %w", dir, err)
	}
	return nil
}

// splitTarget lifts a leading positional argument out of args so that flags may
// be written on either side of it. Go's flag package stops parsing at the first
// non-flag argument, which would otherwise make `cairn boot eng --profile x`
// read --profile and its value as two more positionals.
//
// An empty target means the first argument was a flag, and the positional is
// whatever the flag set has left over.
func splitTarget(args []string) (target string, rest []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// A scope is a path, and both call sites reach [scope.Parse] directly. There
// used to be a resolveScope here in front of it, because a scope could also be
// a name in the bundle's alias registry and something had to decide which of
// the two a value was — a pathLike predicate, whose whole job was to keep a
// bare word from being tried as a path and a relative path from being tried as
// a name. The registry is gone, so both are gone with it. What remains is
// [scope.Parse]'s own contract: empty is the empty scope, and everything else
// must name a directory that exists.

// instanceValues returns the values a template may substitute: a key for each
// name in [bootdir.ValueNames] and no others, so that a value wired here under
// a name cairn does not fill is dropped at the composition root rather than
// carried into a render.
//
// It is the second of two mechanisms and not the only one. Substitution fills a
// value marker only from [bootdir.ValueNames] too, so a key that got past here
// would still render nothing — see [github.com/chrispian/cairn/bootdir.Substitute].
// This narrowing stays because it is the cheaper place to be right: the map
// this builds is handed to a library that renders whatever it is given for
// every artifact, not only templates, and the manifest values it would be
// carrying are the ones spec.mcp keeps API keys in.
func instanceValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for _, name := range bootdir.ValueNames() {
		out[name] = values[name]
	}
	return out
}

// reportUnfilledMarkers prints every marker that stood for nothing an operator
// would want to hear about: a slot that was declared and then filled nothing —
// it failed to resolve, or it resolved empty — and a value cairn cannot fill for
// any profile. Either leaves the document shorter than it reads, and nothing in
// the resulting file says so.
//
// It walks whatever it is handed, which is spec.templates and spec.prompts. A
// prompt is a template read from a file, substituted from the same sections
// and the same values, and a marker in one fails in exactly the way a marker
// in a template does — with one difference, and it runs the wrong way. A
// template is read; a prompt is planted as a slash command and is typed. A
// half-empty command is executed by a person expecting instructions that are
// not in it.
//
// A marker naming a slot no profile declared is not reported, and neither is a
// value cairn knows that is empty for this instance. See
// [github.com/chrispian/cairn/bootdir.Unfilled] for why each is told from the
// case beside it.
//
// It is a report and not a refusal, matching the slot rule it follows from: a
// section that is not there is degraded context and the agent asks its tools.
// The operator hears about it because they are the only one who can fix it.
//
// The set of values is named once at the end rather than on every line. A
// template that misspells one value usually carries the marker more than once,
// and a hundred and fifty characters of set repeated behind each occurrence is
// the noise this function stays quiet about undeclared slots to avoid.
//
// One line per name per destination, for the same reason. Neither line says
// where in the file the marker was, so a second line for the same name in the
// same file carries nothing the first did not.
//
// Each document carries two names because a diagnostic and a refusal are read
// for different reasons. A marker that stood for nothing names the path the
// file lands at, which is what an operator looks for and fails to find. A
// marker that would not parse names the manifest key and the name under it,
// which is what they open to fix it — the path is an output that this run will
// never produce, and naming a file an operator cannot find is worse than not
// naming one. For a boot directory's templates the two are the same string;
// for the installed layer, and for a prompt, they are not.
//
// The slice is sorted here, so a caller may hand one over in any order and two
// boots of one profile still report in the same one.
func reportUnfilledMarkers(stderr io.Writer, templates []reportedTemplate, sections map[string]string) error {
	sort.Slice(templates, func(i, j int) bool { return templates[i].path < templates[j].path })
	unfillable := false
	for _, tmpl := range templates {
		dest := tmpl.path
		unfilled, err := bootdir.Unfilled(tmpl.text, sections)
		if err != nil {
			return fmt.Errorf("spec.%s %q: %w", tmpl.spec, tmpl.key, err)
		}
		said := make(map[reportedMarker]bool, len(unfilled))
		for _, marker := range unfilled {
			key := reportedMarker{verb: marker.Verb, name: marker.Name}
			if said[key] {
				continue
			}
			said[key] = true
			switch marker.Verb {
			case bootdir.MarkerVerbSlot:
				_, _ = fmt.Fprintf(stderr, "cairn: %s: slot %q filled nothing, so %s renders no section\n",
					dest, marker.Name, dest)
			case bootdir.MarkerVerbValue:
				unfillable = true
				_, _ = fmt.Fprintf(stderr, "cairn: %s: value %q is not one cairn fills, so %s renders nothing where it stands\n",
					dest, marker.Name, dest)
			}
		}
	}
	if unfillable {
		_, _ = fmt.Fprintf(stderr, "cairn: the values cairn fills are %s\n", quotedValueNames())
	}
	return nil
}

// reportedMarker is one reported marker's identity, for telling a repeat from a
// new finding. It is the verb and the name and deliberately not the marker's
// text, so two spellings of one marker are one finding.
type reportedMarker struct {
	verb string
	name string
}

// quotedValueNames renders the value set for a diagnostic, quoted the way every
// other set-naming diagnostic cairn prints renders one.
//
// It builds its own slice rather than rewriting the one it was given.
// [bootdir.ValueNames] does return a fresh slice every call, but that is a
// promise made in another package and invisible here, and this function has no
// reason to need it.
func quotedValueNames() string {
	names := bootdir.ValueNames()
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return strings.Join(quoted, ", ")
}

// reportedTemplate is one substituted document a report walks: the manifest
// key that declares its collection, the name it was declared under, the path
// the file lands at, and its text.
//
// It is named for spec.templates because that is the collection it was written
// for, and it carries spec.prompts too — a prompt is a template, read from a
// file instead of out of the manifest, and there is nothing about a marker
// that stood for nothing that differs between the two. The spec field is what
// keeps a diagnostic from saying otherwise: without it a report about a prompt
// would name spec.templates, and send an operator to a key that does not hold
// it.
type reportedTemplate struct {
	spec string
	key  string
	path string
	text string
}

// bootTemplates lists a boot directory's templates for a report. A boot
// directory writes each template at the destination it was declared under, so
// the key and the path are one string and there is nothing to map.
func bootTemplates(templates map[string]string) []reportedTemplate {
	out := make([]reportedTemplate, 0, len(templates))
	for dest, text := range templates {
		out = append(out, reportedTemplate{
			spec: profile.SpecKeyTemplates, key: dest, path: dest, text: text})
	}
	return out
}

// bootPrompts lists a boot directory's prompts for a report, and nothing at
// all when they cannot be read.
//
// A prompt is substituted from the same sections and the same values a
// template is, so a marker in one that stands for nothing is the same fault
// with the same cause — and the only reason it was not reported until now is
// that a template's text arrives on the instance while a prompt's is on disk.
// [bootdir.PromptSources] is that read, and it is the read the render makes:
// asking for it here rather than resolving the prompts directory again is what
// keeps this report describing the files that are actually planted.
//
// The two names are the two a prompt answers to. The key is what spec.prompts
// declared, which is what an operator edits; the path is where the command
// lands, which is what they type. Both, because a prompt is the one artifact
// whose declaration and destination are different strings an operator holds in
// mind at once.
//
// An error is discarded rather than returned, and that is the whole of what
// this function decides. Every failure here — an unusable prompts directory, a
// name that cannot be a file, a prompt that is not there — is one
// [bootdir.Render] makes again a moment later, under the artifact name and the
// source path it belongs to. Returning it would put a worse-addressed copy of
// the same refusal one step in front of the good one, and the report has
// nothing of its own to add to it. What it must not do is suppress the render:
// nothing is reported, the render still runs, and the boot still fails on it.
func bootPrompts(inst *bootdir.Instance) []reportedTemplate {
	sources, err := bootdir.PromptSources(inst)
	if err != nil {
		return nil
	}
	out := make([]reportedTemplate, 0, len(sources))
	for _, src := range sources {
		out = append(out, reportedTemplate{
			spec: profile.SpecKeyPrompts, key: src.Name, path: src.Path, text: src.Text})
	}
	return out
}

// installedTemplates lists the installed layer's templates for a report, paired
// with the path this layer writes each at and dropping every destination it
// does not render.
//
// The installed layer renders some of the manifest's destinations and writes
// them beneath a provider directory of its own, so a report naming the
// manifest's own key would send an operator looking for "AGENTS.md" when the
// file is at ".claude/AGENTS.md", and one walking every destination would name
// files this layer never writes at all. The registration list decides which
// destinations are in play and the layout decides where each one lands, which
// is the same pair [github.com/chrispian/cairn/install.Render] renders from —
// and both now come from one layout document, so they cannot disagree.
//
// The destination a template renders from is the artifact's own, not this
// function's: cairn does not know that an instruction file is called
// AGENTS.md, only that the tree said which destination its agents artifact
// renders from.
func installedTemplates(templates map[string]string, renderers []install.Renderer, layout bootdir.Layout) []reportedTemplate {
	byKind := map[string]bootdir.Artifact{
		bootdir.KindAgents:  layout.Agents,
		bootdir.KindPointer: layout.Pointer,
	}
	out := make([]reportedTemplate, 0, len(byKind))
	for _, r := range renderers {
		artifact, rendered := byKind[r.Kind]
		if !rendered || !artifact.Declared() || artifact.Dest == "" {
			continue
		}
		if text, declared := templates[artifact.Dest]; declared {
			out = append(out, reportedTemplate{
				spec: profile.SpecKeyTemplates, key: artifact.Dest, path: artifact.RelPath, text: text})
		}
	}
	return out
}

// reportSlotFailures prints every non-required slot that failed to resolve,
// with the manifest value behind it when expansion changed one.
//
// The library records such a failure on the slot instead of blocking the
// assembly, which is the behaviour Cairn wants — one unreachable endpoint
// should not stop a boot. It is not a behaviour that should be silent: the
// boot directory is written either way, and the operator has no other way to
// learn that a section is missing.
//
// The library's message is passed through untouched and the declared form is
// added ahead of it, so the line reads cause first and consequence second: what
// the operator wrote, what was tried, then what went wrong with it. Expansion runs before the request is built, so a resolver
// only ever saw the expanded value: the two halves together are what the
// operator asked for and what was tried. A slot whose value expansion did not
// change reads exactly as it did before.
func reportSlotFailures(stderr io.Writer, res *agentcontext.ContextResult, expansions map[string]string) {
	for _, s := range res.Slots {
		if s.Err == nil {
			continue
		}
		note := ""
		if expanded := expansions[s.Name]; expanded != "" {
			note = expanded + ": "
		}
		_, _ = fmt.Fprintf(stderr, "cairn: slot %q did not resolve: %s%v\n", s.Name, note, s.Err)
	}
}
