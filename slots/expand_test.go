package slots

import (
	"context"
	"strings"
	"testing"

	"github.com/hollis-labs/cairn/profile"
	"github.com/hollis-labs/libs/harness/core/agentcontext"
)

// testEnv returns an [Expander] over a map, so a test states what the
// environment held rather than setting it.
func testEnv(vars map[string]string) Expander {
	return func(name string) string { return vars[name] }
}

// TestExpandRewritesWhereToReadFrom is what expansion exists for: a profile
// says where a service lives without hardcoding a host that differs between
// machines, and without cairn growing a second configuration file to hold one.
func TestExpandRewritesWhereToReadFrom(t *testing.T) {
	env := testEnv(map[string]string{
		"TESSERACT_URL": "https://memory.internal",
		"AGENT_HOME":    "/srv/agents",
	})
	specs := []agentcontext.SlotSpec{
		{Name: "memory", Source: agentcontext.SlotSource{
			Kind:     agentcontext.SlotSourceKindHTTPJSON,
			HTTPJSON: agentcontext.HTTPJSONSource{URL: "${TESSERACT_URL}/v1/recall?limit=20"},
		}},
		{Name: "process", Source: agentcontext.SlotSource{
			Kind:       agentcontext.SlotSourceKindStaticFile,
			StaticFile: agentcontext.StaticFileSource{Path: "$AGENT_HOME/process/implement.md"},
		}},
	}

	got := expandSources(specs, env)
	if want := "https://memory.internal/v1/recall?limit=20"; got[0].Source.HTTPJSON.URL != want {
		t.Errorf("the URL expanded to %q, want %q", got[0].Source.HTTPJSON.URL, want)
	}
	if want := "/srv/agents/process/implement.md"; got[1].Source.StaticFile.Path != want {
		t.Errorf("the path expanded to %q, want %q", got[1].Source.StaticFile.Path, want)
	}
	// The caller's manifest is what a diagnostic quotes, so it is copied
	// rather than rewritten: an error naming an expanded value would name
	// something the operator never wrote.
	if specs[0].Source.HTTPJSON.URL != "${TESSERACT_URL}/v1/recall?limit=20" {
		t.Errorf("expandSources rewrote the caller's spec: %q", specs[0].Source.HTTPJSON.URL)
	}
}

// TestExpandLeavesACommandAlone states the boundary. Expansion answers "where
// do I read from", and a command line is not that: letting the environment
// rewrite what runs is a larger promise, and a cmd slot already runs through a
// shell that does its own expansion, so nothing is lost.
func TestExpandLeavesACommandAlone(t *testing.T) {
	const run = "torque task get $TASK --format md"
	got := expandSources([]agentcontext.SlotSpec{{
		Name:   "task",
		Source: agentcontext.SlotSource{Kind: agentcontext.SlotSourceKindCmd, Cmd: agentcontext.CmdSource{Run: run}},
	}}, testEnv(map[string]string{"TASK": "T-1"}))

	if got[0].Source.Cmd.Run != run {
		t.Errorf("the command expanded to %q, want it untouched", got[0].Source.Cmd.Run)
	}
}

// TestExpandAnUnsetNameYieldsNothing pins the behaviour of a name nobody set,
// which is [os.Expand]'s and every shell's.
func TestExpandAnUnsetNameYieldsNothing(t *testing.T) {
	if got := profile.ExpandEnv("${NOT_SET_ANYWHERE}/tail", testEnv(nil)); got != "/tail" {
		t.Errorf("ExpandEnv() = %q, want the unset name to yield nothing", got)
	}
}

// TestNoEnvironmentExpandsNothing states what a caller that supplies none gets.
// Not the process environment: nothing below the composition root reads that,
// for the reason the operator's home is carried rather than looked up.
func TestNoEnvironmentExpandsNothing(t *testing.T) {
	const declared = "${HOME}/docs"
	if got := profile.ExpandEnv(declared, nil); got != declared {
		t.Errorf("ExpandEnv(%q, nil) = %q, want it untouched", declared, got)
	}
	got := expandSources([]agentcontext.SlotSpec{{
		Source: agentcontext.SlotSource{StaticFile: agentcontext.StaticFileSource{Path: declared}},
	}}, nil)
	if got[0].Source.StaticFile.Path != declared {
		t.Errorf("expandSources with no environment = %q, want it untouched", got[0].Source.StaticFile.Path)
	}
}

// TestAssembleExpandsBeforeResolving covers the wiring rather than the
// function: a manifest written with a variable reaches the resolver expanded.
func TestAssembleExpandsBeforeResolving(t *testing.T) {
	spec := profile.Spec{profile.SpecKeySlots: []byte(`[
		{"name":"memory","section":"## Memory",
		 "source":{"kind":"http_json","http_json":{"url":"${MEMORY_URL}/recall"}}}
	]`)}

	seen := &recordingProvider{}
	_, err := Assemble(context.Background(), spec, Options{
		Provider: seen,
		Env:      testEnv(map[string]string{"MEMORY_URL": "https://memory.internal"}),
	})
	if err != nil {
		t.Fatalf("Assemble(): %v", err)
	}
	if len(seen.request.Slots) != 1 {
		t.Fatalf("the provider saw %d slots, want 1", len(seen.request.Slots))
	}
	if got := seen.request.Slots[0].Source.HTTPJSON.URL; got != "https://memory.internal/recall" {
		t.Errorf("the provider saw %q, want it expanded", got)
	}
}

// TestResolveEntriesExpandsAFileSource covers the same wiring on the other key.
// The habit of writing a variable does not stop at the slots key.
func TestResolveEntriesExpandsAFileSource(t *testing.T) {
	spec := profile.Spec{profile.SpecKeyFiles: []byte(`{
		"process.md": {"kind":"static_file","static_file":{"path":"${AGENT_HOME}/process.md"}}
	}`)}

	seen := &recordingProvider{}
	_, err := ResolveEntries(context.Background(), spec, profile.SpecKeyFiles, Options{
		Provider: seen,
		Env:      testEnv(map[string]string{"AGENT_HOME": "/srv/agents"}),
	})
	if err != nil {
		t.Fatalf("ResolveEntries(): %v", err)
	}
	if got := seen.request.Slots[0].Source.StaticFile.Path; got != "/srv/agents/process.md" {
		t.Errorf("the provider saw %q, want it expanded", got)
	}
}

// TestNoSecretReachesTheRenderedContent is the question expansion raises. A URL
// may now carry a token, and a token that reached a planted file would outlive
// the session that used it.
//
// The answer is structural: what lands in a file is the resolver's content, and
// the request's provenance — where SlotProvenance records the source — is never
// read by cairn, never rendered, and never written. This test asserts the
// rendered section carries only what the resolver returned.
func TestNoSecretReachesTheRenderedContent(t *testing.T) {
	const secret = "sk-live-do-not-plant-me"
	spec := profile.Spec{profile.SpecKeySlots: []byte(`[
		{"name":"memory","section":"## Memory",
		 "source":{"kind":"http_json","http_json":{"url":"https://x/recall?token=${TOKEN}"}}}
	]`)}

	answering := &recordingProvider{content: "what the service said"}
	res, err := Assemble(context.Background(), spec, Options{
		Provider: answering,
		Env:      testEnv(map[string]string{"TOKEN": secret}),
	})
	if err != nil {
		t.Fatalf("Assemble(): %v", err)
	}
	sections, err := Sections(res)
	if err != nil {
		t.Fatalf("Sections(): %v", err)
	}
	// The expansion did happen, so this test is not passing by doing nothing.
	if !strings.Contains(answering.request.Slots[0].Source.HTTPJSON.URL, secret) {
		t.Fatal("the token never reached the request, so this test proves nothing")
	}
	if strings.Contains(sections["memory"], secret) {
		t.Errorf("the rendered section carries the token:\n%s", sections["memory"])
	}
}

// recordingProvider records the request it was handed and answers with content.
type recordingProvider struct {
	request agentcontext.ContextRequest
	content string
}

// Assemble implements [agentcontext.ContextProvider].
func (p *recordingProvider) Assemble(_ context.Context, req agentcontext.ContextRequest) (*agentcontext.ContextResult, error) {
	p.request = req
	res := &agentcontext.ContextResult{}
	for _, slot := range req.Slots {
		res.Slots = append(res.Slots, agentcontext.SlotResult{
			Name:    slot.Name,
			Section: slot.Section,
			Content: p.content,
		})
	}
	return res, nil
}

// TestExpansionsNamesWhatTheOperatorWrote is the information only cairn ever
// holds. Expansion runs before the request is built, so a resolver is handed
// "/process.md" and reports "/process.md" — correct, and all it can say. The
// declared form has to come from here or from nowhere.
func TestExpansionsNamesWhatTheOperatorWrote(t *testing.T) {
	spec := profile.Spec{profile.SpecKeySlots: []byte(`[
		{"name":"memory","source":{"kind":"static_file","static_file":{"path":"$AGENT_DOCS/process.md"}}},
		{"name":"recall","source":{"kind":"http_json","http_json":{"url":"${MEMORY_URL}/recall"}}},
		{"name":"role",  "source":{"kind":"role_summary","role_summary":{"path":"$ROLES/eng.md"}}}
	]`)}

	got, err := Expansions(spec, profile.SpecKeySlots, testEnv(nil))
	if err != nil {
		t.Fatalf("Expansions(): %v", err)
	}
	want := map[string]string{
		"memory": `the static_file path is "$AGENT_DOCS/process.md", which expanded to "/process.md"`,
		"recall": `the http_json url is "${MEMORY_URL}/recall", which expanded to "/recall"`,
		"role":   `the role_summary path is "$ROLES/eng.md", which expanded to "/eng.md"`,
	}
	for name, note := range want {
		if got[name] != note {
			t.Errorf("Expansions()[%q] =\n%q\nwant\n%q", name, got[name], note)
		}
	}
}

// TestExpansionsSayNothingAboutWhatDidNotExpand keeps the common case quiet. A
// slot with no variable in it reports exactly as it always did; adding "which
// expanded to" where nothing expanded is noise on every boot.
func TestExpansionsSayNothingAboutWhatDidNotExpand(t *testing.T) {
	spec := profile.Spec{profile.SpecKeySlots: []byte(`[
		{"name":"plain","source":{"kind":"static_file","static_file":{"path":"/srv/process.md"}}},
		{"name":"inline","source":{"kind":"inline","inline":{"content":"$NOT_A_PATH"}}},
		{"name":"cmd","source":{"kind":"cmd","cmd":{"run":"echo $TASK"}}}
	]`)}

	got, err := Expansions(spec, profile.SpecKeySlots, testEnv(map[string]string{"TASK": "T-1"}))
	if err != nil {
		t.Fatalf("Expansions(): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Expansions() = %v, want nothing for sources that name no path or did not change", got)
	}
}

// TestExpansionsCoverAFilesEntry states that the same loss is answered on the
// key where it fails the boot rather than warning. A files entry is addressed
// by its destination, which is the name its failure is reported against.
func TestExpansionsCoverAFilesEntry(t *testing.T) {
	spec := profile.Spec{profile.SpecKeyFiles: []byte(`{
		"process.md": {"kind":"static_file","static_file":{"path":"$AGENT_HOME/process.md"}},
		"literal.md": "no source at all"
	}`)}

	got, err := Expansions(spec, profile.SpecKeyFiles, testEnv(nil))
	if err != nil {
		t.Fatalf("Expansions(): %v", err)
	}
	if len(got) != 1 || got["process.md"] == "" {
		t.Fatalf("Expansions() = %v, want the sourced entry only", got)
	}
	if !strings.Contains(got["process.md"], "$AGENT_HOME/process.md") {
		t.Errorf("Expansions()[%q] = %q, does not name what was written", "process.md", got["process.md"])
	}
}

// TestRoleSummaryPathExpands covers the field that named somewhere to read from
// and was not expanded. It is the same rule as every other path; it was simply
// missed.
func TestRoleSummaryPathExpands(t *testing.T) {
	got := expandSources([]agentcontext.SlotSpec{{
		Name: "role",
		Source: agentcontext.SlotSource{
			Kind:        agentcontext.SlotSourceKindRoleSummary,
			RoleSummary: agentcontext.RoleSummarySource{Path: "$ROLES/eng.md"},
		},
	}}, testEnv(map[string]string{"ROLES": "/srv/roles"}))

	if want := "/srv/roles/eng.md"; got[0].Source.RoleSummary.Path != want {
		t.Errorf("the role_summary path expanded to %q, want %q", got[0].Source.RoleSummary.Path, want)
	}
}
