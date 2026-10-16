package install_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/install"
)

// mixedReport is one report carrying every status, with its entries in the
// order a check produces them — the render's order first, then the sweep's —
// so that a test can tell a method that sorts from one that happens to have
// been handed sorted input.
func mixedReport() *install.Report {
	return &install.Report{
		Root: "/home/operator",
		Entries: []install.Entry{
			{Path: ".claude/AGENTS.md", Status: install.StatusModified,
				Detail: "the bytes on disk are not the render's: 40 on disk, 96 rendered"},
			{Path: ".claude/CLAUDE.md", Status: install.StatusNotAFile,
				Detail: "a symbolic link to ../src/CLAUDE.md"},
			{Path: ".claude/settings.json", Status: install.StatusMissing},
			{Path: ".claude/skills/beta/SKILL.md", Status: install.StatusMatch},
			{Path: ".claude/skills/alpha/SKILL.md", Status: install.StatusUnreadable,
				Detail: "open .claude/skills/alpha/SKILL.md: permission denied"},
			{Path: ".claude/skills/beta/extra.md", Status: install.StatusOrphan},
			{Path: ".claude/skills/beta/references/notes.md", Status: install.StatusOrphan},
			{Path: ".claude/skills/handwritten", Status: install.StatusUnclaimed},
		},
	}
}

// cleanReport is one report in which every path holds what the render
// produces.
func cleanReport() *install.Report {
	return &install.Report{
		Root: "/home/operator",
		Entries: []install.Entry{
			{Path: ".claude/AGENTS.md", Status: install.StatusMatch},
			{Path: ".claude/CLAUDE.md", Status: install.StatusMatch},
		},
	}
}

func TestReportByStatusReturnsOnlyThatStatusInPathOrder(t *testing.T) {
	t.Parallel()
	report := mixedReport()

	orphans := report.ByStatus(install.StatusOrphan)
	got := make([]string, 0, len(orphans))
	for _, entry := range orphans {
		if entry.Status != install.StatusOrphan {
			t.Errorf("ByStatus returned %s, which is %q", entry.Path, entry.Status)
		}
		got = append(got, entry.Path)
	}
	want := []string{".claude/skills/beta/extra.md", ".claude/skills/beta/references/notes.md"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("ByStatus(orphan) = %v, want %v in path order", got, want)
	}
	if entries := report.ByStatus(install.StatusMatch); len(entries) != 1 {
		t.Errorf("ByStatus(match) returned %d entries, want 1", len(entries))
	}
}

func TestReportByStatusLeavesTheReportsOwnOrderAlone(t *testing.T) {
	t.Parallel()
	report := mixedReport()
	before := make([]string, 0, len(report.Entries))
	for _, entry := range report.Entries {
		before = append(before, entry.Path)
	}
	report.ByStatus(install.StatusOrphan)
	after := make([]string, 0, len(report.Entries))
	for _, entry := range report.Entries {
		after = append(after, entry.Path)
	}
	if !slices.Equal(before, after) {
		t.Errorf("Entries were reordered by ByStatus.\nbefore: %v\nafter:  %v", before, after)
	}
}

func TestReportCount(t *testing.T) {
	t.Parallel()
	report := mixedReport()
	for status, want := range map[install.Status]int{
		install.StatusMatch:      1,
		install.StatusMissing:    1,
		install.StatusModified:   1,
		install.StatusNotAFile:   1,
		install.StatusUnreadable: 1,
		install.StatusOrphan:     2,
		install.StatusUnclaimed:  1,
	} {
		if got := report.Count(status); got != want {
			t.Errorf("Count(%q) = %d, want %d", status, got, want)
		}
	}
	if got := report.Count("no such status"); got != 0 {
		t.Errorf("Count of an unknown status = %d, want 0", got)
	}
}

func TestReportCleanAndExitCode(t *testing.T) {
	t.Parallel()
	clean := cleanReport()
	if !clean.Clean() {
		t.Error("a report of nothing but matches is not clean")
	}
	if got := clean.ExitCode(); got != 0 {
		t.Errorf("ExitCode() = %d, want 0 for a clean report", got)
	}

	mixed := mixedReport()
	if mixed.Clean() {
		t.Error("a report carrying findings is clean")
	}
	if got := mixed.ExitCode(); got == 0 {
		t.Error("ExitCode() = 0 for a report carrying findings")
	}

	empty := &install.Report{}
	if !empty.Clean() {
		t.Error("a report with no entries is not clean")
	}
	if got := empty.ExitCode(); got != 0 {
		t.Errorf("ExitCode() = %d for an empty report, want 0", got)
	}
}

func TestReportEveryFindingMakesItUnclean(t *testing.T) {
	t.Parallel()
	for _, status := range []install.Status{
		install.StatusMissing,
		install.StatusModified,
		install.StatusNotAFile,
		install.StatusUnreadable,
		install.StatusOrphan,
		// A status this package has not defined. The verdict is by status, so
		// a new one has to default somewhere, and unclean is the safe
		// direction for a gate.
		install.Status("something nobody has named yet"),
	} {
		if !status.Finding() {
			t.Errorf("Status(%q).Finding() = false", status)
		}
		report := &install.Report{Entries: []install.Entry{
			{Path: ".claude/AGENTS.md", Status: install.StatusMatch},
			{Path: ".claude/settings.json", Status: status},
		}}
		if report.Clean() {
			t.Errorf("a report carrying %q is clean", status)
		}
		if report.ExitCode() == 0 {
			t.Errorf("a report carrying %q exits 0", status)
		}
	}
}

// TestReportUnclaimedIsReportedAndDoesNotFail is the verdict rule the whole
// change turns on. If Clean stayed "every status but match is unclean", the
// new status would fail a check on every hand-written skill in the operator's
// own directory — the opposite of what claiming by name is for.
func TestReportUnclaimedIsReportedAndDoesNotFail(t *testing.T) {
	t.Parallel()
	if install.StatusUnclaimed.Finding() {
		t.Error("StatusUnclaimed.Finding() = true; an unclaimed path is what the operator put there")
	}
	report := &install.Report{
		Root: "/home/operator",
		Entries: []install.Entry{
			{Path: ".claude/AGENTS.md", Status: install.StatusMatch},
			{Path: ".claude/skills/handwritten", Status: install.StatusUnclaimed},
		},
	}
	if !report.Clean() {
		t.Errorf("Clean() = false for a report whose only non-match is unclaimed:\n%s", report)
	}
	if got := report.ExitCode(); got != 0 {
		t.Errorf("ExitCode() = %d, want 0", got)
	}
	// Clean, and still said out loud: the check reports what it found in a
	// directory it shares.
	printed := report.String()
	if !strings.Contains(printed, "Unclaimed (1)") || !strings.Contains(printed, ".claude/skills/handwritten") {
		t.Errorf("String() does not name the unclaimed path:\n%s", printed)
	}
	if !strings.Contains(printed, "In sync") {
		t.Errorf("the verdict is not clean:\n%s", printed)
	}
}

func TestReportSummaryIsOneLine(t *testing.T) {
	t.Parallel()
	got := mixedReport().Summary()
	if strings.Contains(got, "\n") {
		t.Errorf("Summary() = %q, want one line", got)
	}
	for _, want := range []string{"/home/operator", "1 missing", "1 modified", "1 not a file",
		"1 unreadable", "2 orphan", "1 unclaimed", "1 match"} {
		if !strings.Contains(got, want) {
			t.Errorf("Summary() = %q, want it to carry %q", got, want)
		}
	}
	// A status nothing carries is left out rather than printed as a zero.
	if clean := cleanReport().Summary(); strings.Contains(clean, "0 ") {
		t.Errorf("Summary() = %q, want no zero counts", clean)
	}
	// A report with no location does not invent one.
	anonymous := &install.Report{Entries: []install.Entry{{Path: "x", Status: install.StatusMatch}}}
	if got := anonymous.Summary(); !strings.HasPrefix(got, "1 match") {
		t.Errorf("Summary() = %q, want it to open with the counts when no root is known", got)
	}
}

func TestReportStringNamesEveryFinding(t *testing.T) {
	t.Parallel()
	report := mixedReport()
	got := report.String()

	for _, entry := range report.Entries {
		if !strings.Contains(got, entry.Path) {
			t.Errorf("String() does not name %s:\n%s", entry.Path, got)
		}
		if entry.Detail != "" && !strings.Contains(got, entry.Detail) {
			t.Errorf("String() does not carry the detail for %s:\n%s", entry.Path, got)
		}
	}
	if !strings.Contains(got, report.Summary()) {
		t.Error("String() does not open with the summary")
	}
	if !strings.HasSuffix(got, "\n") {
		t.Error("String() does not end in a newline")
	}
}

func TestReportStringGroupsByStatusFindingsFirst(t *testing.T) {
	t.Parallel()
	got := mixedReport().String()
	headings := []string{"Missing (1)", "Modified (1)", "Not a file (1)", "Unreadable (1)",
		"Orphan (2)", "Unclaimed (1)", "Match (1)"}
	at := -1
	for _, heading := range headings {
		next := strings.Index(got, heading)
		if next < 0 {
			t.Fatalf("String() has no %q section:\n%s", heading, got)
		}
		if next < at {
			t.Errorf("%q is out of order; findings come before matches:\n%s", heading, got)
		}
		at = next
	}
	// Paths are sorted inside a section, so two runs over one tree read the
	// same however a directory happened to be listed.
	first := strings.Index(got, ".claude/skills/beta/extra.md")
	second := strings.Index(got, ".claude/skills/beta/references/notes.md")
	if first > second {
		t.Errorf("the orphan section is not in path order:\n%s", got)
	}
}

func TestReportStringSaysWhatTheCheckDidNotDo(t *testing.T) {
	t.Parallel()
	// The report is the only place `--check` speaks, so the promise it makes
	// about repairing nothing is stated where the operator reads it.
	got := mixedReport().String()
	if !strings.Contains(got, "repairs nothing") {
		t.Errorf("String() does not say the check repaired nothing:\n%s", got)
	}
	clean := cleanReport().String()
	if !strings.Contains(clean, "In sync") {
		t.Errorf("a clean report does not say so:\n%s", clean)
	}
	for _, heading := range []string{"Missing", "Modified", "Not a file", "Unreadable", "Orphan", "Unclaimed"} {
		if strings.Contains(clean, heading+" (") {
			t.Errorf("a clean report prints an empty %q section:\n%s", heading, clean)
		}
	}
}

func TestReportNilIsReadable(t *testing.T) {
	t.Parallel()
	// A caller printing whatever a check returned should not have to guard
	// against a nil report to do it.
	var report *install.Report
	if got := report.Count(install.StatusOrphan); got != 0 {
		t.Errorf("Count on a nil report = %d, want 0", got)
	}
	if got := report.ByStatus(install.StatusOrphan); got != nil {
		t.Errorf("ByStatus on a nil report = %v, want nil", got)
	}
	if !report.Clean() {
		t.Error("a nil report is not clean")
	}
	if got := report.ExitCode(); got != 0 {
		t.Errorf("ExitCode on a nil report = %d, want 0", got)
	}
	if got := report.Summary(); got == "" {
		t.Error("Summary on a nil report is empty")
	}
	if got := report.String(); got == "" {
		t.Error("String on a nil report is empty")
	}
}
