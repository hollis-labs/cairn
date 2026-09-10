package bootdir

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestSubstituteReplacesWhatWasDeclared is the whole of the mechanism: a slot
// marker becomes that slot's section, a value marker becomes that value, and
// the text around them is untouched.
func TestSubstituteReplacesWhatWasDeclared(t *testing.T) {
	const template = "# <!-- cairn:value profile -->\n" +
		"\n" +
		"prose the operator wrote\n" +
		"\n" +
		"<!-- cairn:slot repo -->\n"
	sections := map[string]string{"repo": "## Repository\n\non branch main"}
	values := map[string]string{"profile": "engineer"}

	got, err := Substitute(template, sections, values)
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	want := "# engineer\n\nprose the operator wrote\n\n## Repository\n\non branch main\n"
	if got != want {
		t.Errorf("Substitute() =\n%q\nwant\n%q", got, want)
	}
}

// TestSubstituteOmitsASlotThatFilledNothing is docs/plan.md §5 carried into a
// world where the heading lives in a template.
//
// A slot that failed to resolve and one that resolved empty both arrive here as
// an empty section, and the marker takes the heading with it because the
// heading came back from the slot rather than being written around the marker.
// A template holding "## Memory" above the marker would keep that heading; that
// is why the heading belongs to the slot.
func TestSubstituteOmitsASlotThatFilledNothing(t *testing.T) {
	const template = "before\n\n<!-- cairn:slot memory -->\n\nafter\n"

	for name, sections := range map[string]map[string]string{
		"a slot that resolved empty": {"memory": ""},
		"a slot never declared":      {},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Substitute(template, sections, nil)
			if err != nil {
				t.Fatalf("Substitute(): %v", err)
			}
			// The marker's own line is gone, newline included. The two
			// blank lines around it are the author's and survive — see
			// TestSubstituteKeepsTheBlankLinesTheAuthorWrote.
			if want := "before\n\n\nafter\n"; got != want {
				t.Errorf("Substitute() = %q, want %q", got, want)
			}
			if strings.Contains(got, "cairn:") {
				t.Errorf("Substitute() left a marker behind: %q", got)
			}
		})
	}
}

// TestSubstituteTakesTheLineAMarkerHadToItself pins the rule this behaviour
// exists for: a marker with nothing but whitespace either side of it on its
// line, substituting the empty string, takes the line with it — the newline
// included.
//
// Without it every marker that stood for nothing left a blank line where it
// was. That was a curiosity while each profile had its own template; it stops
// being one the moment a single shared document carries every marker any
// profile might fill, because then a role's instruction file opens with a run
// of blanks for every block that role does not use.
//
// The indented and trailing-space spellings are here because whitespace around
// a marker is invisible in a template and nobody should have to think about
// which side of it they left a space on.
func TestSubstituteTakesTheLineAMarkerHadToItself(t *testing.T) {
	for name, template := range map[string]string{
		"flush left":                     "a\n<!-- cairn:slot memory -->\nb\n",
		"indented":                       "a\n    <!-- cairn:slot memory -->\nb\n",
		"trailing spaces":                "a\n<!-- cairn:slot memory -->   \nb\n",
		"a tab either side":              "a\n\t<!-- cairn:slot memory -->\t\nb\n",
		"last line, no trailing newline": "a\n<!-- cairn:slot memory -->",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Substitute(template, map[string]string{"memory": ""}, nil)
			if err != nil {
				t.Fatalf("Substitute(): %v", err)
			}
			want := "a\nb\n"
			if !strings.HasSuffix(template, "b\n") {
				want = "a\n"
			}
			if got != want {
				t.Errorf("Substitute() = %q, want %q", got, want)
			}
		})
	}
}

// TestSubstituteKeepsTheLineAMarkerFilled is the other half of that rule, and
// the reason it is stated as "substituted nothing" rather than "was a marker".
// A marker alone on its line that filled a section leaves the line exactly
// where the operator put it.
func TestSubstituteKeepsTheLineAMarkerFilled(t *testing.T) {
	got, err := Substitute("a\n<!-- cairn:slot memory -->\nb\n",
		map[string]string{"memory": "## Memory\n\nwhat is known"}, nil)
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	if want := "a\n## Memory\n\nwhat is known\nb\n"; got != want {
		t.Errorf("Substitute() = %q, want %q", got, want)
	}
}

// TestSubstituteKeepsALineAMarkerShared is the case that keeps this change
// byte-neutral, and the one easiest to get wrong.
//
// templates/agents.md ends "- scope: <!-- cairn:value scope -->", and a profile
// with no scope substitutes the empty string there. The marker goes and nothing
// else on the line does — not the label, and not the space between the colon
// and the marker. The want string below ends in that space deliberately: it is
// the template's byte, not cairn's, and tidying it away would move
// testdata/goldens/trees/install/base/.claude/AGENTS.md, whose last line is
// "- scope: " with exactly that trailing space.
func TestSubstituteKeepsALineAMarkerShared(t *testing.T) {
	const template = "## Profile\n\n" +
		"- profile: <!-- cairn:value profile -->\n" +
		"- provider: <!-- cairn:value provider -->\n" +
		"- scope: <!-- cairn:value scope -->\n"

	got, err := Substitute(template, nil, map[string]string{
		"profile":  "base",
		"provider": "claude",
		"scope":    "",
	})
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	want := "## Profile\n\n- profile: base\n- provider: claude\n- scope: \n"
	if got != want {
		t.Errorf("Substitute() =\n%q\nwant\n%q", got, want)
	}
}

// TestSubstituteKeepsTheBlankLinesTheAuthorWrote is the fence around the rule.
//
// Removing a marker's line is not licence to collapse the blank lines left
// around it. A blank line between two markers is the operator's content —
// cairn does not know it was only ever a separator — and a substitution pass
// that reflowed prose would break a larger promise than the one it kept. Two
// markers written a blank line apart, both filling nothing, leave that blank
// line behind. The way to close it is to write the markers on adjacent lines,
// which is the template's business and not this function's.
func TestSubstituteKeepsTheBlankLinesTheAuthorWrote(t *testing.T) {
	const template = "<!-- cairn:slot role -->\n\n<!-- cairn:slot context -->\n\nprose\n"

	got, err := Substitute(template, map[string]string{"role": "", "context": ""}, nil)
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	if want := "\n\nprose\n"; got != want {
		t.Errorf("Substitute() = %q, want %q — the author's blank lines are content", got, want)
	}
}

// TestSubstituteTakesALineOfNothingButMarkers settles the case the rule does
// not name: two markers sharing a line with nothing else, both substituting
// nothing.
//
// Strictly neither is "alone on its line", but the line is the same defect and
// takes the same remedy — a line that held only markers, all of which vanished,
// is not content anyone wrote. The alternative reading, that two markers keep
// each other's line alive, would mean an operator could reintroduce the blank
// line by putting two markers where one was, which is not a distinction worth
// having. A marker on that line that does fill holds it, by the same test every
// other line gets.
func TestSubstituteTakesALineOfNothingButMarkers(t *testing.T) {
	const template = "a\n<!-- cairn:slot one --> <!-- cairn:slot two -->\nb\n"

	t.Run("both fill nothing", func(t *testing.T) {
		got, err := Substitute(template, map[string]string{"one": "", "two": ""}, nil)
		if err != nil {
			t.Fatalf("Substitute(): %v", err)
		}
		if want := "a\nb\n"; got != want {
			t.Errorf("Substitute() = %q, want %q", got, want)
		}
	})
	t.Run("one of them fills", func(t *testing.T) {
		got, err := Substitute(template, map[string]string{"one": "", "two": "kept"}, nil)
		if err != nil {
			t.Fatalf("Substitute(): %v", err)
		}
		if want := "a\n kept\nb\n"; got != want {
			t.Errorf("Substitute() = %q, want %q", got, want)
		}
	})
}

// TestSubstituteRepeatsAMarkerByPosition covers a line carrying the same marker
// text twice.
//
// Substitution used to walk the marker list replacing the first occurrence of
// each one's text, which resolved duplicates by position for free. The line
// walk has to keep that: it takes each match's position from the same regex
// that produced the marker list, so the nth match on a line is the nth marker,
// and two markers spelled identically cannot be confused for each other.
func TestSubstituteRepeatsAMarkerByPosition(t *testing.T) {
	got, err := Substitute("<!-- cairn:slot one --> then <!-- cairn:slot one -->\n",
		map[string]string{"one": "x"}, nil)
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	if want := "x then x\n"; got != want {
		t.Errorf("Substitute() = %q, want %q", got, want)
	}
}

// TestSubstituteLandsAMultiLineSectionOnASingleMarkerLine covers the ordinary
// case for a slot, which the line walk has to not break: a section is a heading
// and a body and is almost always several lines, arriving on a line that held
// one marker. The replacement's own newlines are its, and the line's newline is
// still the line's.
func TestSubstituteLandsAMultiLineSectionOnASingleMarkerLine(t *testing.T) {
	const section = "## Repository\n\n## main...origin/main\n\n## Recent commits\nabc1234 a change\n"

	got, err := Substitute("before\n<!-- cairn:slot repo -->\nafter\n",
		map[string]string{"repo": section}, nil)
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	if want := "before\n" + section + "\nafter\n"; got != want {
		t.Errorf("Substitute() =\n%q\nwant\n%q", got, want)
	}
}

// TestSubstituteKeepsALineWhoseSectionIsOnlyWhitespace pins where the emptiness
// test sits: on each replacement, not on the finished line.
//
// A slot that produced nothing arrives here as the empty string —
// [github.com/chrispian/cairn/slots.Sections] is explicit about that — so a
// section made only of whitespace is a section that produced something. Testing
// the finished line instead would silently drop it, and this function is not
// the place that decides a slot's output was worthless.
func TestSubstituteKeepsALineWhoseSectionIsOnlyWhitespace(t *testing.T) {
	got, err := Substitute("a\n<!-- cairn:slot odd -->\nb\n",
		map[string]string{"odd": "   "}, nil)
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	if want := "a\n   \nb\n"; got != want {
		t.Errorf("Substitute() = %q, want %q", got, want)
	}
}

// TestSubstituteRepeatsAMarkerEveryTimeItAppears covers a name used twice. A
// template may want one section in two places, and substituting only the first
// would leave a marker in a file an agent reads.
func TestSubstituteRepeatsAMarkerEveryTimeItAppears(t *testing.T) {
	got, err := Substitute(
		"<!-- cairn:value profile -->/<!-- cairn:value profile -->",
		nil, map[string]string{"profile": "eng"})
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	if got != "eng/eng" {
		t.Errorf("Substitute() = %q, want both markers replaced", got)
	}
}

// TestSubstituteToleratesTheSpellingsAnOperatorWrites covers the whitespace a
// marker is written with, which nobody should have to think about.
func TestSubstituteToleratesTheSpellingsAnOperatorWrites(t *testing.T) {
	for _, marker := range []string{
		"<!-- cairn:value profile -->",
		"<!--cairn:value profile-->",
		"<!--   cairn:value    profile   -->",
	} {
		got, err := Substitute(marker, nil, map[string]string{"profile": "eng"})
		if err != nil {
			t.Fatalf("Substitute(%q): %v", marker, err)
		}
		if got != "eng" {
			t.Errorf("Substitute(%q) = %q, want %q", marker, got, "eng")
		}
	}
}

// TestSubstituteRendersNothingForAValueCairnCannotFill pins what replaced a
// refusal: a cairn:value naming something outside [ValueNames] substitutes the
// empty string rather than failing the whole document.
//
// The two spellings below are the pair most likely to be guessed wrong later,
// so both are pinned. Alone on its line, the marker takes the line with it,
// which is the rule every marker that substitutes nothing follows. Sharing its
// line with content, the marker goes and nothing else does — the label and the
// space after the colon are the template's bytes, not cairn's.
func TestSubstituteRendersNothingForAValueCairnCannotFill(t *testing.T) {
	for name, tc := range map[string]struct{ template, want string }{
		"alone on its line":       {"a\n<!-- cairn:value tenant -->\nb\n", "a\nb\n"},
		"sharing it with a label": {"a\n- tenant: <!-- cairn:value tenant -->\nb\n", "a\n- tenant: \nb\n"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Substitute(tc.template, nil, map[string]string{"profile": "eng"})
			if err != nil {
				t.Fatalf("Substitute(): %v", err)
			}
			if got != tc.want {
				t.Errorf("Substitute() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSubstituteFillsAValueOnlyFromValueNames is the guarantee that replaced
// the parser's refusal, and the one this package cannot afford to lose.
//
// Substitution reads a value marker's replacement from [ValueNames] and not
// from the map it was handed. Written the other way — replacement :=
// values[name] — the guarantee would rest entirely on every caller keeping its
// map narrow, and Substitute is exported to any caller at all. The refusal used
// to make that irrelevant: nothing outside the six parsed, whatever map came
// with it. This test is the refusal's replacement, so it hands over the most
// hostile map there is and asserts none of it renders.
//
// "mcp" and "settings" are the two that matter. They are manifest keys, one of
// them is where an MCP server's API keys live, and a template is a document
// written to be read into a model's context — see secrets_test.go.
func TestSubstituteFillsAValueOnlyFromValueNames(t *testing.T) {
	const secret = "sk-live-do-not-render-me"
	hostile := map[string]string{
		"mcp":      secret,
		"settings": secret,
		"tenant":   "a value nobody declared",
		"home":     "/Users/someone",
		"boot_dir": "/tmp/boot",
		"profile":  "engineer",
	}

	for name, replacement := range hostile {
		if fills(name) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			got, err := Substitute("x: <!-- cairn:value "+name+" -->\n", nil, hostile)
			if err != nil {
				t.Fatalf("Substitute(): %v", err)
			}
			if got != "x: \n" {
				t.Errorf("Substitute() = %q, want the marker to have read nothing", got)
			}
			if strings.Contains(got, replacement) {
				t.Errorf("Substitute() rendered a value cairn does not fill: %q", got)
			}
		})
	}
	// The same map, and the one name in it cairn does fill, so the assertions
	// above are not passing because nothing renders at all.
	got, err := Substitute("x: <!-- cairn:value profile -->\n", nil, hostile)
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	if got != "x: engineer\n" {
		t.Errorf("Substitute() = %q, want the declared value to have rendered", got)
	}
}

// TestSubstituteTreatsAKnownEmptyValueTheSameWay is the other half of that
// convergence, and the reason the case above needs no code of its own. A value
// cairn knows and did not fill renders exactly as one it has never heard of,
// line-taking included.
func TestSubstituteTreatsAKnownEmptyValueTheSameWay(t *testing.T) {
	const template = "a\n<!-- cairn:value scope -->\nb\n"

	for name, values := range map[string]map[string]string{
		"declared and empty": {"scope": ""},
		"not in the map":     {},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Substitute(template, nil, values)
			if err != nil {
				t.Fatalf("Substitute(): %v", err)
			}
			if want := "a\nb\n"; got != want {
				t.Errorf("Substitute() = %q, want %q", got, want)
			}
		})
	}
}

// TestSubstituteRefusesAMarkerItCannotActOn covers every malformed form.
//
// A marker in cairn's own namespace that cairn does not understand is refused
// rather than left in place. Leaving it would plant the marker's own text in a
// file an agent reads, which is the one outcome worse than failing.
//
// What is left here is a marker with no meaning at all: a verb cairn has not
// got, and a body that is not a verb and one name. A name cairn cannot fill is
// not on this list — it means one thing, and the file says it by rendering
// nothing. See TestSubstituteRendersNothingForAValueCairnCannotFill.
func TestSubstituteRefusesAMarkerItCannotActOn(t *testing.T) {
	for name, marker := range map[string]string{
		"no verb and no name":  "<!-- cairn: -->",
		"a verb and no name":   "<!-- cairn:slot -->",
		"a verb cairn has not": "<!-- cairn:section repo -->",
		"three words":          "<!-- cairn:slot repo extra -->",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Substitute(marker, nil, nil); !errors.Is(err, ErrMarker) {
				t.Fatalf("Substitute(%q) = %v, want ErrMarker", marker, err)
			}
		})
	}
}

// TestMarkersIgnoreWhatIsNotCairns states the other half of the namespace rule.
// A comment that is not cairn's, and an @-import the harness resolves for
// itself, both survive into the output untouched.
func TestMarkersIgnoreWhatIsNotCairns(t *testing.T) {
	const template = "<!-- an ordinary comment -->\n@AGENTS.md\n${NOT_EXPANDED_HERE}\n{{ notATemplate }}\n"

	markers, err := Markers(template)
	if err != nil {
		t.Fatalf("Markers(): %v", err)
	}
	if len(markers) != 0 {
		t.Errorf("Markers() found %v in text holding none of cairn's", markers)
	}
	got, err := Substitute(template, nil, nil)
	if err != nil {
		t.Fatalf("Substitute(): %v", err)
	}
	if got != template {
		t.Errorf("Substitute() rewrote text it does not own:\n%q", got)
	}
}

// TestMarkersAreOneLine pins the property that keeps a marker findable without
// a parser: Go's "." does not match a newline, so a comment spanning lines is
// not a marker and is left alone.
func TestMarkersAreOneLine(t *testing.T) {
	const template = "<!-- cairn:slot\nrepo -->\n"

	markers, err := Markers(template)
	if err != nil {
		t.Fatalf("Markers(): %v", err)
	}
	if len(markers) != 0 {
		t.Errorf("Markers() read a marker across a line break: %v", markers)
	}
}

// TestUnfilledIsSilentAboutAValueCairnKnows covers what the operator hears
// about. A declared slot that filled nothing left the document shorter than it
// reads and is worth saying; a value cairn knows and did not fill is a fact
// about the instance — a boot with no scope — and is not.
func TestUnfilledIsSilentAboutAValueCairnKnows(t *testing.T) {
	const template = "<!-- cairn:slot filled -->\n<!-- cairn:slot empty -->\n<!-- cairn:value scope -->\n"

	// "scope" is declared as a slot as well as being a value name. Without it
	// the value marker names nothing in sections, reads as undeclared under the
	// two-value lookup, and stays silent whether or not the verb switch above
	// sends it anywhere — which would leave this test's own claim pinned by
	// nothing.
	unfilled, err := Unfilled(template, map[string]string{
		"filled": "## Here\n\ncontent",
		"empty":  "",
		"scope":  "",
	})
	if err != nil {
		t.Fatalf("Unfilled(): %v", err)
	}
	if len(unfilled) != 1 || unfilled[0].Name != "empty" {
		t.Fatalf("Unfilled() = %v, want only the declared slot that filled nothing", unfilled)
	}
	if unfilled[0].Text != "<!-- cairn:slot empty -->" {
		t.Errorf("the report does not quote the marker: %q", unfilled[0].Text)
	}
}

// TestUnfilledIsSilentAboutASlotNobodyDeclared is the property that lets one
// shared template carry every marker any profile might fill.
//
// A name absent from sections names a slot this manifest never declared, which
// is not a fault: the profile simply does not use that block. Reporting it would
// print a line per unused marker and bury the case the warning exists for — a
// slot that was supposed to fill and did not. Distinguishing the two rests
// entirely on the two-value lookup, since a single-value one reads "" for both.
func TestUnfilledIsSilentAboutASlotNobodyDeclared(t *testing.T) {
	const template = "<!-- cairn:slot declared -->\n<!-- cairn:slot undeclared -->\n"

	unfilled, err := Unfilled(template, map[string]string{"declared": ""})
	if err != nil {
		t.Fatalf("Unfilled(): %v", err)
	}
	if len(unfilled) != 1 || unfilled[0].Name != "declared" {
		t.Fatalf("Unfilled() = %v, want only the declared slot", unfilled)
	}
}

// TestUnfilledReportsAValueCairnCannotFill is the value rule, which runs the
// opposite way to the slot rule beside it and for the same reason.
//
// A slot name absent from the manifest is a block this profile does not use, so
// it is silent. A value name outside [ValueNames] is one no profile will ever
// fill — cairn owns that set and it is the same for every profile — so it is
// reported. The marker renders nothing either way; only the line on stderr
// tells the two apart.
func TestUnfilledReportsAValueCairnCannotFill(t *testing.T) {
	const template = "<!-- cairn:value profile -->\n<!-- cairn:value tenant -->\n<!-- cairn:slot undeclared -->\n"

	unfilled, err := Unfilled(template, nil)
	if err != nil {
		t.Fatalf("Unfilled(): %v", err)
	}
	if len(unfilled) != 1 || unfilled[0].Name != "tenant" {
		t.Fatalf("Unfilled() = %v, want only the value cairn cannot fill", unfilled)
	}
	if unfilled[0].Verb != MarkerVerbValue {
		t.Errorf("the marker's verb is %q, and the caller words its line from that", unfilled[0].Verb)
	}
	if unfilled[0].Text != "<!-- cairn:value tenant -->" {
		t.Errorf("the report does not quote the marker: %q", unfilled[0].Text)
	}
}

// TestValueNamesIsClosedAndSorted pins the set cairn populates. It is closed
// because it is the set a report tells a fillable name from, and sorted so a
// diagnostic lists it the same way twice.
func TestValueNamesIsClosedAndSorted(t *testing.T) {
	names := ValueNames()
	if !slices.IsSorted(names) {
		t.Errorf("ValueNames() = %v, which is not sorted", names)
	}
	want := []string{"model", "profile", "provider", "scope", "session"}
	if !slices.Equal(names, want) {
		t.Errorf("ValueNames() = %v, want %v", names, want)
	}
	// A fresh slice, so a caller cannot shrink the set a name is reported
	// against.
	names[0] = "rewritten"
	if ValueNames()[0] == "rewritten" {
		t.Error("ValueNames() returns a slice a caller can rewrite")
	}
	// Deliberately absent, and each for its own reason. boot_dir and home are
	// absolute paths into one machine rather than facts about a profile.
	// binding named the saved composition a boot was reached by, and bindings
	// retired — launch-time assembly belongs to the launcher, so there is no
	// such name for cairn to fill.
	for _, absent := range []string{"boot_dir", "home", "binding"} {
		if slices.Contains(ValueNames(), absent) {
			t.Errorf("ValueNames() carries %q", absent)
		}
	}
}
