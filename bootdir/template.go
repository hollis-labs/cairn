package bootdir

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// MarkerVerbSlot names a marker that stands for one of the manifest's declared
// slots, substituted with that slot's rendered section.
const MarkerVerbSlot = "slot"

// MarkerVerbValue names a marker that stands for one value of the instance
// being materialized — see [ValueNames].
const MarkerVerbValue = "value"

// ErrMarker reports a marker cairn cannot act on: it names no verb or no
// target, or it names a verb that is not [MarkerVerbSlot] or [MarkerVerbValue].
//
// A malformed marker is refused rather than left in place. The cairn: prefix is
// this package's namespace, so a marker inside it that cairn does not
// understand is a mistake in a template rather than someone else's syntax — and
// leaving it alone would plant the marker's own text in a file an agent reads.
var ErrMarker = errors.New("invalid template marker")

// markerPattern matches any marker in cairn's namespace, capturing everything
// between the prefix and the close so that a malformed one is caught here
// rather than silently left in the output.
//
// It is an HTML comment for four reasons. It is invisible in rendered markdown
// and unmistakable in source, so a template reads as a document rather than as
// code. It is already this repository's comment idiom — see
// [github.com/chrispian/cairn/install.GeneratedMarker]. A harness resolving
// "@name" imports strips HTML comments before it looks for them, so a marker
// can never be read as one. And there is nowhere in it to put a conditional,
// which is the property that keeps a template a substitution target rather than
// a program.
//
// Go's "." does not match a newline, so a marker is one line by construction.
var markerPattern = regexp.MustCompile(`<!--\s*cairn:(.*?)-->`)

// Marker is one occurrence of a marker in a template.
type Marker struct {
	// Verb is [MarkerVerbSlot] or [MarkerVerbValue].
	Verb string

	// Name is the slot or value the marker stands for.
	Name string

	// Text is the marker exactly as it was written, for a diagnostic that has
	// to quote it.
	Text string
}

// ValueNames returns the instance values cairn populates, sorted. It is both the
// set cairn can fill and the set [Substitute] will fill from — a value marker
// naming anything else reads the empty string whatever the caller's map holds.
//
// It is not a gate on what a template may name. A marker naming something
// outside it renders nothing and is reported, where it was once refused. The
// old argument was that cairn knows every member, so a name outside the set can
// only be a typo. That much is still true — the set is global, identical for
// every profile, and does not vary with what an instance carries. What does not
// follow is the refusal. Two things argue against it, and neither is about
// typos:
//
// A typo should degrade rather than fail. Refusing lets one word in one
// document decide whether a boot directory is written at all, and the document
// it is most likely to be in is the one every profile shares. Nothing else that
// stood for nothing behaves that way: a slot that failed, one that resolved
// empty, one no manifest declared — each leaves a gap in the file and the boot
// carries on. A misspelled value is the same kind of mistake and now gets the
// same treatment, with a line on stderr because unlike an undeclared slot it
// can never be deliberate.
//
// And a template outlives this list. One written against a cairn that fills a
// seventh value has to render under a cairn that does not, or the set can never
// grow without every older template becoming unbootable. Rendering nothing for
// a name this build has not got is what makes that possible.
//
// Sorted so that a diagnostic naming the set names it the same way twice.
//
// Deliberately absent: the boot directory's own path and the operator's home.
// Both are absolute paths into one machine rather than facts about a profile,
// and the first is the directory the file is being written into, which whatever
// reads it already knows.
//
// Also absent, and this one left: `binding`. It named the saved composition a
// boot was reached by, and bindings retired — launch-time assembly belongs to
// the launcher, so there is no such name for cairn to fill. A template still
// naming it renders nothing and is reported, which is exactly the degrade this
// function's own argument asks for: a template outlives the set of values any
// one build fills, in both directions.
func ValueNames() []string {
	return []string{"model", "profile", "provider", "scope", "session"}
}

// fills reports whether name is one cairn populates.
//
// It is one predicate with two callers on purpose. [substituteLine] asks it
// what to render and [Unfilled] asks it what to report, so the file and the
// diagnostic about the file cannot disagree about which names cairn fills. They
// used to be able to: substitution read the caller's map and the report read
// this list, and they agreed only for as long as a caller kept the two in step.
func fills(name string) bool {
	return slices.Contains(ValueNames(), name)
}

// Markers returns every marker in text, in the order they appear.
//
// It is the only scanner. Substitution runs through it and so does the caller
// that reports a marker which stood for nothing, because two scanners that
// disagreed would report one set of markers and substitute another.
func Markers(text string) ([]Marker, error) {
	found := markerPattern.FindAllStringSubmatch(text, -1)
	out := make([]Marker, 0, len(found))
	for _, match := range found {
		marker, err := parseMarker(match[0], match[1])
		if err != nil {
			return nil, err
		}
		out = append(out, marker)
	}
	return out, nil
}

// parseMarker reads one marker's body — the text between "cairn:" and the
// comment close — into a [Marker].
//
// The verb is checked and neither name is. A verb cairn has not got could mean
// anything, so there is nothing to render for it; a name cairn cannot fill
// means one thing, which the file says by leaving the marker's place empty and
// [Unfilled] says out loud.
func parseMarker(text, body string) (Marker, error) {
	fields := strings.Fields(body)
	if len(fields) != 2 {
		return Marker{}, fmt.Errorf(
			"%w: %s names %s, and a marker is a verb and one name: %s",
			ErrMarker, text, countedFields(fields), markerForms())
	}
	verb, name := fields[0], fields[1]
	switch verb {
	case MarkerVerbSlot, MarkerVerbValue:
		return Marker{Verb: verb, Name: name, Text: text}, nil
	default:
		return Marker{}, fmt.Errorf("%w: %s declares the verb %q; the verbs are %q and %q",
			ErrMarker, text, verb, MarkerVerbSlot, MarkerVerbValue)
	}
}

// Substitute returns text with every marker replaced.
//
// A slot marker becomes that slot's rendered section, which is the heading and
// the content together or nothing at all: a slot that failed to resolve and one
// that resolved empty were both dropped before this ran, so a marker standing
// for either leaves no heading behind. That is the same rule the boot file
// followed when it was the only place a slot could land, and moving the heading
// into the template is exactly where it would have been lost — see
// [github.com/chrispian/cairn/slots.Sections].
//
// A marker naming a slot the manifest never declared substitutes nothing too.
// It is not an error here because the file is the same file either way, and
// substitution has no reason to tell the two apart. The caller does have one,
// and does tell them apart — see [Unfilled].
//
// A value marker becomes that value, which may legitimately be empty — a boot
// with no declared scope substitutes nothing and the template reads as though
// the marker were not there. A name outside [ValueNames] renders the empty
// string too, so the two look identical in the file, and the caller is the one
// that tells them apart — see [Unfilled].
//
// The unknown name is read from [ValueNames] and not from values, which is what
// makes that a property rather than a coincidence. values is a map an arbitrary
// caller supplies, so a lookup that trusted it would render whatever key that
// caller put there — spec.mcp's env under the key "mcp", to name the one that
// matters. Substitution fills a value marker only from the names cairn declares
// it fills, and nothing a caller adds to the map can widen that.
//
// A line that held nothing but markers and whitespace, every one of which
// substituted the empty string, is removed entirely — its newline with it. The
// alternative leaves a blank line wherever a marker stood for nothing, and one
// shared template carrying every marker any profile might fill would open a
// role's instruction file with a run of them. A marker that shares its line
// with content is the other case: only the marker goes, and the line stays
// exactly as it was written, so "- scope: <!-- cairn:value scope -->" with no
// scope still renders "- scope: " and the trailing space is the template's.
//
// Consecutive blank lines are not collapsed. A blank line an operator wrote
// between two markers is their content, and a substitution pass that reflowed
// prose would be a worse promise than the one this makes. A template wanting
// no gap between two blocks writes their markers on adjacent lines.
//
// Substitution does not look at where in the document a marker sits. A marker
// inside a fenced code block is substituted like any other, so a template that
// documents this syntax has to avoid writing a live one.
func Substitute(text string, sections, values map[string]string) (string, error) {
	markers, err := Markers(text)
	if err != nil {
		return "", err
	}
	// The line walk uses markerPattern to find positions, and takes the
	// meaning of each match from the slice [Markers] already returned. Both
	// come from that one regex over this one text, so the nth match on the
	// walk is the nth marker in the slice — which is also what keeps two
	// markers spelled identically from being confused for each other, since
	// each is consumed at the position it was found rather than by its text.
	var out strings.Builder
	out.Grow(len(text))
	next := 0
	for rest := text; rest != ""; {
		line := rest
		if end := strings.IndexByte(rest, '\n'); end >= 0 {
			line, rest = rest[:end+1], rest[end+1:]
		} else {
			rest = ""
		}
		locs := markerPattern.FindAllStringIndex(line, -1)
		rendered, vanished := substituteLine(line, locs, markers[next:next+len(locs)], sections, values)
		next += len(locs)
		if vanished {
			continue
		}
		out.WriteString(rendered)
	}
	return out.String(), nil
}

// substituteLine returns line with the markers found at locs replaced, and
// reports whether the line vanished: it held nothing but markers and
// whitespace, and every one of them substituted the empty string.
//
// The emptiness test is on each replacement rather than on the finished line,
// so a slot whose section is itself only whitespace still holds its line. That
// section is content the slot produced, and this is not the place that decides
// it was worthless.
func substituteLine(line string, locs [][]int, markers []Marker, sections, values map[string]string) (string, bool) {
	if len(locs) == 0 {
		return line, false
	}
	var out strings.Builder
	out.Grow(len(line))
	filled := false
	prev := 0
	for i, loc := range locs {
		replacement := ""
		switch markers[i].Verb {
		case MarkerVerbSlot:
			replacement = sections[markers[i].Name]
		case MarkerVerbValue:
			if fills(markers[i].Name) {
				replacement = values[markers[i].Name]
			}
		}
		if replacement != "" {
			filled = true
		}
		out.WriteString(line[prev:loc[0]])
		out.WriteString(replacement)
		prev = loc[1]
	}
	out.WriteString(line[prev:])

	rendered := out.String()
	if !filled && strings.TrimSpace(rendered) == "" {
		return "", true
	}
	return rendered, false
}

// Unfilled returns the markers in text an operator hears about: a slot that was
// declared and then filled nothing — it failed to resolve, or resolved empty —
// and a value naming something outside [ValueNames]. The slot leaves a block
// missing from the file and the value leaves a gap where an inline fact should
// be, and in both cases nothing in the file says so.
//
// A marker naming a slot that is absent from sections is skipped in silence.
// The key set of sections is the declared set —
// [github.com/chrispian/cairn/slots.Sections] writes a key for every slot the
// manifest declared, the empty string included for the ones that produced
// nothing — so an absent key means nobody declared that slot, and that is not a
// fault to report. One template can carry every marker any profile might fill,
// and warning on each marker a profile does not use would bury the one case
// this exists to surface.
//
// The lookup is two-valued for exactly that reason. A single-value lookup
// returns "" for both cases and cannot tell them apart.
//
// The exception is an assembly run with [github.com/chrispian/cairn/slots.Options]
// Deterministic set, which drops the cmd and http slots before they resolve: their
// names are absent from sections though the manifest declared them. Nothing hands
// such a map here — the installed layer reports the slots it skipped itself — and
// anything that starts to would read "undeclared" off a slot that was merely not
// run.
//
// The value rule runs the other way round, and for the same reason the slot rule
// exists. The manifest owns the slot set, so a name absent from it is a block
// this profile does not use — ordinary, and silent. Cairn owns the value set and
// it is the same set for every profile, so a name outside it is one no profile
// will ever fill: worth the line. A value cairn does know and filled with
// nothing stays silent, because an empty value is a fact about the instance — a
// boot with no scope — rather than something that went wrong. That is why the
// test is on the name and never on the value.
func Unfilled(text string, sections map[string]string) ([]Marker, error) {
	markers, err := Markers(text)
	if err != nil {
		return nil, err
	}
	var out []Marker
	for _, marker := range markers {
		switch marker.Verb {
		case MarkerVerbSlot:
			if section, declared := sections[marker.Name]; declared && section == "" {
				out = append(out, marker)
			}
		case MarkerVerbValue:
			if !fills(marker.Name) {
				out = append(out, marker)
			}
		}
	}
	return out, nil
}

// countedFields describes what a malformed marker held, for a diagnostic that
// has to say what was wrong with it rather than only that something was.
func countedFields(fields []string) string {
	switch len(fields) {
	case 0:
		return "nothing"
	case 1:
		return fmt.Sprintf("only %q", fields[0])
	default:
		return fmt.Sprintf("%d words", len(fields))
	}
}

// markerForms renders the two legal marker spellings for a diagnostic.
func markerForms() string {
	return fmt.Sprintf("<!-- cairn:%s NAME --> or <!-- cairn:%s NAME -->",
		MarkerVerbSlot, MarkerVerbValue)
}
