package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/chrispian/cairn/catalog"
	"github.com/chrispian/cairn/profile"
	"github.com/hollis-labs/agentkit/agentcontext"
)

// The three composition flags, described once so that `cairn boot` and
// `cairn show` describe them identically. They are one set on purpose: show is
// the "what will this resolve to" preview, and a preview that could not be
// handed the same composition the boot will be handed would be blind to
// exactly the part that makes a composition differ from its base.
const (
	// The order clause carries its own exception, because the exception is
	// silent: a part already folded contributes nothing and says nothing, so an
	// operator who does not know the rule sees a flag they typed do nothing at
	// all. See profile.ResolveComposition — a part contributes what it adds,
	// and what it adds is what has not already been folded.
	withFlagUsage = "a profile merged after the extends chain resolves, closest-wins and in the " +
		"order given; a catalog id, or a path when the value holds a separator or begins " +
		`with ".", "~" or "$". Repeatable. A profile the resolution has already reached — ` +
		"the target, anything it extends, or a part named earlier — is folded once, where it " +
		"first landed, so naming it again adds nothing and does not move it, and cairn says " +
		"so on stderr rather than leaving the flag silently doing nothing"

	// The additive-only rule is in the help rather than left to be discovered.
	// Cairn has no spelling anywhere for removing one member of a collection
	// keyed by its own id — the null that clears a member of an object has
	// nowhere to be written in a list — so an operator who reads this flag as
	// "choose the skills" and passes one id would get that id added to the
	// profile's own, which is the moment to say so.
	skillFlagUsage = "a skill the boot directory carries, added to the ones the profile resolves " +
		"to. Comma-separated and repeatable, the two forms equivalent and composing. " +
		"Additive only: nothing in cairn removes a member of a collection keyed by its own " +
		"id, so a session that wants fewer skills boots a different profile"

	// The same sentence as --skill, because it is the same rule over the same
	// kind of collection. What differs is what a prompt is: content a person
	// invokes by name, planted as a command rather than loaded by the harness.
	//
	// The namespace is written out rather than taken from a layout: a flag's
	// help is one string and there are as many namespaces as there are trees.
	// It is Claude Code's, which is the only harness that plants prompts at
	// all — and TestPromptFlagUsageMatchesTheLayout holds the two together, so
	// a tree that renamed its prompts directory fails here rather than lying
	// to an operator.
	promptFlagUsage = "a prompt the boot directory carries, added to the ones the profile " +
		"resolves to and planted as /boot:<name>. Comma-separated and " +
		"repeatable, the two forms equivalent and composing. Additive only, for the reason " +
		"--skill is"

	setFlagUsage = "an inline literal for a named slot, merged last — it replaces a declared " +
		"slot of that name whole, section included, exactly as a part declaring that slot " +
		"would. Repeatable"
)

// composition is what a command is given beyond the profile it names: the
// parts merged after the extends chain, the skills and prompts added to the
// resolved sets, and the slots supplied inline. The fields below are the list;
// this sentence does not count them, because the last time it did it said
// three and stayed that way when prompts arrived.
//
// Every one of them is an instance concern rather than an authoring one, which
// is the test for what earns a flag here. spec.skills is what one boot
// directory carries — install.skills is what every session on the machine
// loads — and a --set value is a one-off direction for this materialization. A
// direction worth reusing is an ordinary profile and arrives through --with.
type composition struct {
	with    partList
	skills  idList
	prompts idList
	sets    slotList

	// named maps the id a part was loaded under back to how a diagnostic
	// should refer to it: the --with the operator typed. The id and the
	// written form differ for a path — the id is the expansion — and a
	// diagnostic quotes what was written, so the pair is kept here rather
	// than reconstructed by whoever needs to name one.
	//
	// Every entry is now a flag the operator typed. It used to be either that
	// or a part a replayed binding carried, and the whole of the bookkeeping
	// that told the two apart — how many leading entries came from where, and
	// which binding they came from — retired with bindings: a composition is
	// what the terminal said, and there is nothing else for it to have come
	// from.
	named map[string]string
}

// partAt names the part at index i of with the way a diagnostic should: the
// flag the operator typed.
//
// It is a function rather than a field read because it used to have two
// answers — the flag, or the binding that carried a part nobody typed — and
// the callers print whichever they are handed and know about neither. Keeping
// the seam is what makes a second source, if one ever returns, a change here
// and nowhere else.
func (c *composition) partAt(i int) string {
	return "--with " + c.with[i]
}

// sourceOf names where the part at index i came from, without the value: the
// diagnostic it serves goes on to name the value itself, and saying it twice
// in one sentence reads as two different values.
func (c *composition) sourceOf(int) string {
	return "--with"
}

// partAtQuoted is [composition.partAt] with the value quoted, for the
// diagnostics where it is a path: a value whose leading or trailing space, or
// whose emptiness after expansion, is the thing being reported reads as
// nothing at all unquoted.
func (c *composition) partAtQuoted(i int) string {
	return fmt.Sprintf("--with %q", c.with[i])
}

// bind registers the composition's flags on fs — one per field, and this list
// is the only place their number is written down.
func (c *composition) bind(fs *flag.FlagSet) {
	fs.Var(&c.with, "with", withFlagUsage)
	fs.Var(&c.skills, "skill", skillFlagUsage)
	fs.Var(&c.prompts, "prompt", promptFlagUsage)
	fs.Var(&c.sets, "set", setFlagUsage)
}

// reportAbsorbedParts prints a line for each --with that contributed nothing:
// every profile in the part's own chain had already been folded, so the flag
// was typed and changed nothing.
//
// It is a report and exit 0, not a refusal, and the wording says what happened
// rather than complaining about it. Naming a part the resolution already covers
// is a legitimate thing to do — it makes a composition explicit about what it
// rests on — and the operator may well have meant it.
//
// It exists because the alternative is the only silent no-op in the command. A
// declared slot that fills nothing earns a line; a cairn:value marker cairn
// cannot fill earns a line; an explicit flag that contributes nothing should
// not be the one thing that says nothing. Both shapes reach here — a part
// already in the target's chain, including the target itself, and a part naming
// an ancestor an earlier part already brought, which is the one an operator is
// least likely to predict.
//
// It must stay quiet for a part that contributed. A diagnostic that fires on
// the ordinary case is noise, and noise is read past, which is worse than the
// silence it replaced.
func (c *composition) reportAbsorbedParts(stderr io.Writer, resolved *profile.Resolved) {
	for _, id := range resolved.AlreadyFolded {
		named := "--with " + id
		if from, ok := c.named[id]; ok {
			named = from
		}
		_, _ = fmt.Fprintf(stderr, "cairn: %s: already in the chain, contributed nothing\n", named)
	}
}

// contributors names, per manifest key, the flag that contributed to it.
//
// It exists for `cairn show`, which reports which profiles declared each key
// because a value composed from three profiles reads exactly like a value one
// profile wrote. A flag is a contributor the chain cannot name — --with lands
// in the chain and shows there, but --skill and --set do not — so without this
// the one command whose job is attribution would quietly credit the profile
// with what was typed at the terminal.
//
// The names are the flags as they are spelled, because that is what the reader
// would have to change to change the value.
func (c *composition) contributors() map[string][]string {
	out := map[string][]string{}
	if len(c.skills) > 0 {
		out[profile.SpecKeySkills] = []string{"--skill"}
	}
	if len(c.prompts) > 0 {
		out[profile.SpecKeyPrompts] = []string{"--prompt"}
	}
	if len(c.sets) > 0 {
		out[profile.SpecKeySlots] = []string{"--set"}
	}
	return out
}

// resolve resolves profileID together with everything the composition adds,
// and returns the loader the parts were read through so that a caller which
// re-reads the chain — `cairn show`, attributing each manifest key — reads the
// same profiles the cascade folded.
//
// The order is the whole of the rule. The extends chain resolves, then each
// part in the order given, then the skills and the prompts, then the slots:
// every contributor is closer than the one ahead of it, and the last word
// belongs to the flag typed at the terminal.
func (c *composition) resolve(ctx context.Context, cat *catalog.Catalog, home string,
	env profile.Expander, profileID string) (*profile.Resolved, profile.Loader, error) {

	loader, parts, err := c.load(ctx, cat, home, env)
	if err != nil {
		return nil, nil, err
	}
	resolved, err := profile.ResolveComposition(ctx, loader, profileID, parts)
	if err != nil {
		return nil, nil, err
	}
	if err := c.skills.mergeInto(resolved.Spec, profile.SpecKeySkills, "--skill"); err != nil {
		return nil, nil, err
	}
	if err := c.prompts.mergeInto(resolved.Spec, profile.SpecKeyPrompts, "--prompt"); err != nil {
		return nil, nil, err
	}
	if err := c.sets.mergeInto(resolved.Spec); err != nil {
		return nil, nil, err
	}
	return resolved, loader, nil
}

// load turns each --with value into an id the cascade can walk, reading the
// ones that name a file and checking the ones that name a catalog profile.
//
// This is the one place the id-or-path question is decided. Everything below
// it sees ids: a part read from a file is keyed in [partLoader] under the path
// it was read from, so [profile.ResolveComposition] walks it exactly as it
// walks a catalog id, and its own extends resolves against the catalog because
// the loader falls through to the catalog for every id it was not handed.
//
// A catalog id is looked up here rather than left to fail during the walk. The
// walk's diagnostic is right for a broken extends and wrong for this: a bare
// "x.md" is the one ambiguous input the detection rule creates, and the whole
// mitigation for it is the sentence below, which the walk has no way to say.
func (c *composition) load(ctx context.Context, cat *catalog.Catalog, home string,
	env profile.Expander) (profile.Loader, []string, error) {

	loader := &partLoader{cat: cat, paths: map[string]*profile.Profile{}}
	if len(c.with) == 0 {
		return loader, nil, nil
	}

	c.named = make(map[string]string, len(c.with))
	parts := make([]string, 0, len(c.with))
	for i, raw := range c.with {
		named := c.partAt(i)
		if !partIsPath(raw) {
			// A name. It is a catalog id or it is nothing, and "or it is
			// nothing" is where the operator who meant a file ends up.
			if _, err := cat.Profile(ctx, raw); err != nil {
				if errors.Is(err, catalog.ErrProfileNotFound) {
					return nil, nil, fmt.Errorf("%s: %s: no profile named %q; if you meant a file, write %q",
						c.sourceOf(i), cat.Root(), raw, "./"+raw)
				}
				return nil, nil, err
			}
			nameOnce(c.named, raw, named)
			parts = append(parts, raw)
			continue
		}

		// A path. $VAR and ~/ expand the way they do in every manifest value
		// that names somewhere to read from — the expander is the one this
		// command already threads down from the composition root, so a part
		// may be written "$CAIRN_PROFILE_ROOT/parts/docs-only.md" and relocate
		// with the bundle.
		quoted := c.partAtQuoted(i)
		expanded, err := profile.ExpandPath(raw, home, env)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", quoted, err)
		}
		if strings.TrimSpace(expanded) == "" {
			return nil, nil, fmt.Errorf("%s: names no file — a variable in it is not set", quoted)
		}
		p, err := catalog.ReadProfile(expanded)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", quoted, hintBareID(ctx, cat, expanded, err))
		}
		// The path is what the part is keyed under, and keying by the id the
		// file declares is what would let a part read from ./engineer.md
		// shadow the catalog's engineer for every id the rest of the
		// composition resolves, extends included. The two spellings never
		// meet.
		//
		// Overwriting the profile's own ID is the smaller half and it does one
		// thing: package profile reports a merge that failed as `profile %q`,
		// reading it off the [profile.Profile] rather than off the chain, so
		// without this a part that will not compose is blamed on a bare
		// "docs-only" and the operator has no way to tell which of the two
		// files by that name it meant. The chain and the declaring-profile
		// column come from the ids passed to the walk and would name the path
		// either way.
		p.ID = expanded
		loader.paths[expanded] = p
		nameOnce(c.named, expanded, named)
		parts = append(parts, expanded)
	}
	return loader, parts, nil
}

// hintBareID adds the one hint that turns a part which could not be read into
// a fix, and returns err untouched when it has none to offer.
//
// The mistake it answers is the one catalog.PartsDir creates. A part whose file
// sits in profiles/parts/ is still named by its bare id — the directory is
// where the file lives, not part of what the profile is called — so
// `--with parts/docs-only` is the natural thing to type, is wrong, and without
// this earns "read the profile parts/docs-only: no such file or directory":
// true, and no help at all to someone who is looking at the file.
//
// It is the mirror of the diagnostic on the other branch of this function,
// where a bare "x.md" resolves as a name and the refusal says how to spell a
// file. Both exist because the id-or-path question is decided by spelling
// alone — see [partIsPath], which explains why it may not be decided by
// touching the filesystem — so each spelling has exactly one plausible mistake
// and cairn owes an answer to both.
//
// The hint is offered only when the stem names a profile the bundle actually
// holds, and only for a file that is not there. Guessing at a name nobody has
// declared would send the operator looking for the wrong problem, and a file
// that exists but will not parse has a diagnostic of its own that this must
// not bury.
func hintBareID(ctx context.Context, cat *catalog.Catalog, path string, err error) error {
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if _, missing := cat.Profile(ctx, stem); missing != nil {
		return err
	}
	return fmt.Errorf("%w; %s holds a profile named %q, and a profile is named by its id "+
		"wherever its file sits — write %q", err, cat.Root(), stem, stem)
}

// nameOnce records how to refer to a part, keeping the first spelling.
//
// One id can arrive twice — the operator names the same part twice — and the
// fold keeps it where it first landed, which is the earlier flag's
// position. So the first spelling is the one that describes what happened, and
// the later write would replace a correct answer with a plausible one.
func nameOnce(named map[string]string, id, as string) {
	if _, ok := named[id]; !ok {
		named[id] = as
	}
}

// partIsPath reports whether a --with value names a file rather than a profile
// in the catalog: it holds a path separator, or begins with ".", "~" or "$".
//
// The rule is spelled out rather than probed for, and nothing here touches the
// filesystem. Deciding by whether the file happens to exist would make one
// spelling mean two different things depending on the working directory, and
// would silently resolve a catalog profile when a generated part was missing —
// which is the failure a launcher would least like to have hidden from it.
//
// It is sound because a profile id holds no separator; catalog.parseProfile
// refuses one, so the two sets cannot overlap.
//
// One input is genuinely ambiguous and resolves as a name: a bare "x.md" has
// no separator and no leading marker. That is the right call — a name is what
// most --with values are — and the not-found diagnostic in [composition.load]
// is the whole mitigation for it.
//
// It has no counterpart on --scope, and used to. While a scope could be a name
// in the bundle's alias registry, something had to tell a name from a path
// there too — deliberately by a different rule, since a scope is a directory
// and a leading "." is far more likely to be a relative file here than a
// directory there. The registry retired and a scope is now unambiguously a
// path, so this is the only place in cairn that still asks the question.
func partIsPath(raw string) bool {
	return strings.ContainsRune(raw, '/') ||
		strings.ContainsRune(raw, filepath.Separator) ||
		strings.HasPrefix(raw, ".") ||
		strings.HasPrefix(raw, "~") ||
		strings.HasPrefix(raw, "$")
}

// partLoader reads a composition's profiles: the parts loaded from a file by
// the path they were read from, and everything else through the catalog.
//
// The fall-through is the whole point and is pinned by a test. A part loaded
// from a path is an ordinary profile, so its extends names a catalog profile
// and resolves there; a loader that answered only from its own map would make
// a path-loaded part the one profile in cairn that cannot inherit, which is a
// restriction nobody wrote and everybody would infer.
type partLoader struct {
	cat   *catalog.Catalog
	paths map[string]*profile.Profile
}

// Profile implements [profile.Loader].
func (l *partLoader) Profile(ctx context.Context, id string) (*profile.Profile, error) {
	if p, ok := l.paths[id]; ok {
		return p, nil
	}
	return l.cat.Profile(ctx, id)
}

// partList collects --with in the order the parts were given. Order is the
// merge order, so nothing here sorts or reorders: two parts touching one key
// are resolved by which was typed last, and that has to survive the flag.
//
// It does not deduplicate either, and that is a division of labour rather than
// an oversight. A part named twice is folded once, but the fold is what decides
// that — [profile.ResolveComposition] skips a profile it has already reached,
// which it must do anyway for a part that merely shares an ancestor with the
// target. Repeating the rule here would be a second copy of it, and a second
// copy is what drifts.
type partList []string

// String implements [flag.Value].
func (l *partList) String() string { return strings.Join(*l, " ") }

// Set implements [flag.Value].
func (l *partList) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("names no part")
	}
	*l = append(*l, strings.TrimSpace(v))
	return nil
}

// idList collects a flag naming members of a keyed collection by id, which is
// comma-separated and repeatable both. The two forms are equivalent and
// compose: `--skill a,b --skill c` and `--skill a --skill b,c` are the same
// three skills, so a launcher composing a list never has to decide which shape
// cairn wants.
//
// One type for --skill and --prompt, because they are one flag over two keys.
// A second copy would be a second chance for the comma form, the empty-value
// refusal or the merge to differ between two flags an operator reads as a
// pair.
type idList []string

// String implements [flag.Value].
func (l *idList) String() string { return strings.Join(*l, ",") }

// Set implements [flag.Value].
//
// A value that names nothing at all is refused rather than ignored. A flag
// that takes a value and does nothing with it is worse than one that says the
// value was empty.
func (l *idList) Set(v string) error {
	before := len(*l)
	for _, id := range strings.Split(v, ",") {
		if id = strings.TrimSpace(id); id != "" {
			*l = append(*l, id)
		}
	}
	if len(*l) == before {
		return errors.New("names no id")
	}
	return nil
}

// folded returns the ids once each, keeping the position the first of a repeat
// landed at.
//
// One id arrives twice the ordinary way, which is the way --skill exists for:
// "boot this profile, and make sure x is on" is a thing to type without first
// reading what the profile holds, and not having to know that is the whole
// point of the flag. Two --skill values naming one id, and one comma-separated
// value naming it twice, are the same collision.
//
// So this is [nameOnce]'s rule over the other collection the flags contribute
// to, and it keeps the first for the reason nameOnce does: the earlier entry
// is where the composition put the id, and moving it to the end because the
// operator restated it would report a position nothing chose.
//
// The fold is here rather than downstream because downstream is right as it
// is. [profile.Merge] already folds these keys, a member being its own key,
// but a merger only ever runs on two declared values: a target that declares
// no skills at all leaves the flag's list the whole value, carried unread so
// that a single declarer survives byte for byte. bootdir then refuses the
// repeat, and that refusal is correct for the manifest it guards — a profile
// declaring one skill twice IS an authoring error worth naming. What was wrong
// was handing the checker a list composition had never folded.
//
// Two ids fold only when they are the same bytes. A skill id is a directory
// name under spec.skills_dir and a prompt id a file name under
// spec.prompts_dir, so "Adr" and "adr" are two names a case-sensitive
// filesystem tells apart. Folding them would be an identity rule cairn has
// nowhere else — not in the cascade's own union, which keys by the member's
// bytes, and not in bootdir, which keys by the trimmed name — and both stand.
func (l idList) folded() []string {
	out := make([]string, 0, len(l))
	seen := make(map[string]struct{}, len(l))
	for _, id := range l {
		if _, repeat := seen[id]; repeat {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// mergeInto folds the collected ids into spec's value for key, last and by id
// — exactly what a part declaring the same ids would do, through the same
// table of keyed collections. There are no new merge semantics here and that
// is the point: a flag is one more contributor to a union that already had
// several. flag names the flag a diagnostic quotes.
//
// What the flag contributes is folded first, by [idList.folded], so that the
// value handed to the merge is one an operator could have written into a
// manifest. Otherwise the flags would be the one contributor able to put a
// repeat into a key whose whole rule is that a member is its own key.
//
// Additive, permanently as far as these flags are concerned. See
// [skillFlagUsage].
func (l idList) mergeInto(spec profile.Spec, key, flag string) error {
	if len(l) == 0 {
		return nil
	}
	raw, err := composedJSON(l.folded())
	if err != nil {
		return fmt.Errorf("%s: %w", flag, err)
	}
	merged, err := profile.Merge(key, spec[key], raw)
	if err != nil {
		return fmt.Errorf("%s: %w", flag, err)
	}
	spec[key] = merged
	return nil
}

// slotList collects --set. A slot named twice keeps the last value, because
// the members merge by name in the order they were given.
type slotList []slotValue

// slotValue is one --set: the slot's name and the literal it stands for.
type slotValue struct {
	name  string
	value string
}

// String implements [flag.Value].
func (l *slotList) String() string {
	out := make([]string, 0, len(*l))
	for _, s := range *l {
		out = append(out, s.name+"="+s.value)
	}
	return strings.Join(out, " ")
}

// Set implements [flag.Value].
//
// The value may be empty — `--set note=` is a slot that stands for nothing,
// which renders nothing, and is a legitimate way to silence a section for one
// materialization. The name may not be.
func (l *slotList) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("takes <slot>=<value>, and %q holds no \"=\"", v)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%q names no slot", v)
	}
	*l = append(*l, slotValue{name: strings.TrimSpace(name), value: value})
	return nil
}

// mergeInto folds each --set into spec's slots as an inline slot, last and by
// name, which is how spec.slots composes for every other contributor.
//
// Cairn gains no "direction" concept from this. What arrives is a slot like
// any other, named by the profile that declared the marker it fills, and cairn
// goes on owning shape rather than content.
//
// The member is built as a minimal object rather than by marshalling an
// [agentcontext.SlotSpec]: the library's source is a tagged union carrying a
// parameter block per kind, none of which JSON omits, so marshalling one would
// put seven empty blocks into a manifest an operator reads out of `cairn show`.
// The kind is the library's own constant, so the one field that could drift
// cannot.
func (l slotList) mergeInto(spec profile.Spec) error {
	if len(l) == 0 {
		return nil
	}
	members := make([]any, 0, len(l))
	for _, s := range l {
		members = append(members, map[string]any{
			"name": s.name,
			"source": map[string]any{
				"kind":   string(agentcontext.SlotSourceKindInline),
				"inline": map[string]any{"content": s.value},
			},
		})
	}
	raw, err := composedJSON(members)
	if err != nil {
		return fmt.Errorf("--set: %w", err)
	}
	merged, err := profile.Merge(profile.SpecKeySlots, spec[profile.SpecKeySlots], raw)
	if err != nil {
		return fmt.Errorf("--set: %w", err)
	}
	spec[profile.SpecKeySlots] = merged
	return nil
}

// composedJSON serializes a flag's contribution to a manifest key with HTML
// escaping off, for the reason every other encoder in cairn has it off: "&",
// "<" and ">" are ordinary characters in the text an operator types, and a
// manifest carries what they wrote rather than a re-spelling of it.
func composedJSON(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}
