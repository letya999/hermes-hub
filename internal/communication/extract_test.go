package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestExtractCSSAndHTML2Text(t *testing.T) {
	page := `<html><body><nav>menu</nav><div class="price">  $42.00 </div><script>var x=1;</script></body></html>`
	p := &WatchProbe{Extract: []ExtractStep{{Kind: "css", Selector: "div.price"}}}
	got, err := extractValue(p, []byte(page))
	if err != nil || got != "$42.00" {
		t.Fatalf("css extract: %q %v", got, err)
	}
	p = &WatchProbe{Extract: []ExtractStep{{Kind: "html2text"}, {Kind: "strip"}}}
	got, err = extractValue(p, []byte(page))
	if err != nil || !strings.Contains(got, "$42.00") || strings.Contains(got, "var x=1") {
		t.Fatalf("html2text: %q %v", got, err)
	}
}

func TestExtractChainTransforms(t *testing.T) {
	// regex capture → grep keeps matching lines → sort_lines canonicalizes
	p := &WatchProbe{Extract: []ExtractStep{
		{Kind: "regex", Pattern: `(?s)<ul class="items">(.*?)</ul>`},
		{Kind: "drop_regex", Pattern: `<[^>]+>`},
		{Kind: "grep", Pattern: "price"},
		{Kind: "sort_lines"},
	}}
	doc := "<ul class=\"items\">\n<li>b price 2</li>\n<li>a price 1</li>\n<li>skip</li>\n</ul>"
	got, err := extractValue(p, []byte(doc))
	if err != nil || got != "a price 1\nb price 2" {
		t.Fatalf("chain: %q %v", got, err)
	}
	// drop_lines removes noisy lines
	p = &WatchProbe{Extract: []ExtractStep{{Kind: "drop_lines", Pattern: `updated|views`}}}
	got, err = extractValue(p, []byte("updated: today\nload: 5\nviews: 42"))
	if err != nil || got != "load: 5" {
		t.Fatalf("drop_lines: %q %v", got, err)
	}
}

func TestExtractJSONPath(t *testing.T) {
	body := []byte(`{"items":[{"status":"ok","name":"a"},{"status":"bad","name":"b"}],"n":2}`)
	p := &WatchProbe{Extract: []ExtractStep{{Kind: "json_path", Path: "$.items[1].status"}}}
	got, err := extractValue(p, body)
	if err != nil || got != "bad" {
		t.Fatalf("json_path index: %q %v", got, err)
	}
	p = &WatchProbe{Extract: []ExtractStep{{Kind: "json_path", Path: "$.items[*].name"}}}
	got, err = extractValue(p, body)
	if err != nil || got != "a\nb" {
		t.Fatalf("json_path wildcard: %q %v", got, err)
	}
}

func TestExtractLegacyAndEmpty(t *testing.T) {
	p := &WatchProbe{Regex: `version: ([0-9.]+)`}
	got, err := extractValue(p, []byte("release\nversion: 1.4.2\n"))
	if err != nil || got != "1.4.2" {
		t.Fatalf("legacy regex: %q %v", got, err)
	}
	got, err = extractValue(&WatchProbe{}, []byte("plain body"))
	if err != nil || got != "plain body" {
		t.Fatalf("whole body: %q %v", got, err)
	}
	got, err = extractValue(&WatchProbe{Kind: "github_issue"}, []byte(`{"state":"closed"}`))
	if err != nil || got != "closed" {
		t.Fatalf("github_issue: %q %v", got, err)
	}
	// Empty extraction is an error — never silent "no change".
	p = &WatchProbe{Extract: []ExtractStep{{Kind: "css", Selector: "div.missing"}}}
	if _, err = extractValue(p, []byte("<html><body>x</body></html>")); err == nil {
		t.Fatal("empty extraction must be an error")
	}
	// A legacy regex that stops matching is a probe error, not a change to "".
	if _, err = extractValue(&WatchProbe{Regex: `(v\d+)`}, []byte("no match here")); err == nil {
		t.Fatal("unmatched legacy regex must error")
	}
	// Malformed GitHub issue responses are probe errors too.
	if _, err = extractValue(&WatchProbe{Kind: "github_issue"}, []byte(`{"state":"weird"}`)); err == nil {
		t.Fatal("invalid issue state must error")
	}
	// A regex without a capture group is invalid (create-time validation
	// enforces it; the probe itself stays defensive).
	if _, err = extractValue(&WatchProbe{Regex: `v\d+`}, []byte("v42")); err == nil {
		t.Fatal("group-less regex must error")
	}
}

func TestExtractValidationAtCreate(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	mk := func(id string, extract []ExtractStep) Schedule {
		return Schedule{ScheduleID: id, Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
			Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
			Probe: &WatchProbe{URL: "https://example.com/x", Extract: extract}}
	}
	if _, err := s.CreateSchedule(mk("ok-css", []ExtractStep{{Kind: "css", Selector: "div.a"}}), caller, ""); err != nil {
		t.Fatalf("valid extract rejected: %v", err)
	}
	if _, err := s.CreateSchedule(mk("bad-kind", []ExtractStep{{Kind: "exec"}}), caller, ""); err == nil {
		t.Fatal("unknown extract kind accepted")
	}
	if _, err := s.CreateSchedule(mk("bad-css", []ExtractStep{{Kind: "css"}}), caller, ""); err == nil {
		t.Fatal("selector-less css accepted")
	}
	if _, err := s.CreateSchedule(mk("bad-path", []ExtractStep{{Kind: "json_path", Path: "a.b"}}), caller, ""); err == nil {
		t.Fatal("path without $ accepted")
	}
	if _, err := s.CreateSchedule(mk("bad-regex", []ExtractStep{{Kind: "regex", Pattern: "nogroup"}}), caller, ""); err == nil {
		t.Fatal("group-less regex step accepted")
	}
	big := make([]ExtractStep, 9)
	for i := range big {
		big[i] = ExtractStep{Kind: "strip"}
	}
	if _, err := s.CreateSchedule(mk("too-many", big), caller, ""); err == nil {
		t.Fatal("9-step extract accepted")
	}
}

func TestScriptProbeFeedsBodyToIsolatedRunner(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	var gotSrc, gotInput string
	var gotMount bool
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, src, input string, mountSpace bool, _ []string) (string, error) {
		gotSrc, gotInput, gotMount = src, input, mountSpace
		return "extracted-value", nil
	}
	sched := Schedule{ScheduleID: "ws-script", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "script watch", JobKind: "watcher",
		Probe: &WatchProbe{Kind: "script", URL: "https://example.com/p", Script: "emit(input().upper())"}}
	if _, err := s.CreateSchedule(sched, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "ws-script"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	fetch := func(context.Context, string) ([]byte, error) { return []byte("page-body"), nil }
	if err := s.ProbeDueOccurrences(o.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	if gotSrc != sched.Probe.Script || gotInput != "page-body" || gotMount {
		t.Fatalf("script runner contract: %q %q mount=%v", gotSrc, gotInput, gotMount)
	}
	st, _ := s.loadWatchStateLocked("ws-script")
	if st.Value != "extracted-value" {
		t.Fatalf("script result did not become watch value: %+v", st)
	}
}

func TestCommandProbeMountsSpaceAndNeverFetches(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	var gotMount bool
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, _, _ string, mountSpace bool, _ []string) (string, error) {
		gotMount = mountSpace
		return "dir-state-hash", nil
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wc-cmd", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "project state watch", JobKind: "watcher",
		Probe: &WatchProbe{Kind: "command", Script: "emit(hash_path('.'))"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "wc-cmd"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	fetch := func(context.Context, string) ([]byte, error) {
		t.Fatal("command probe must never fetch")
		return nil, nil
	}
	if err := s.ProbeDueOccurrences(o.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	if !gotMount {
		t.Fatal("command probe ran without the space mount")
	}
	st, _ := s.loadWatchStateLocked("wc-cmd")
	if st.Value != "dir-state-hash" {
		t.Fatalf("command value not stored: %+v", st)
	}
}

func TestCommandProbeWithoutRuntimeSettlesFailed(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wc-nort", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Kind: "command", Script: "emit('x')"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "wc-nort"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	fetch := func(context.Context, string) ([]byte, error) { return nil, errors.New("unused") }
	if err := s.ProbeDueOccurrences(o.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	st, _ := s.loadWatchStateLocked("wc-nort")
	if st.LastError == "" {
		t.Fatal("missing script runtime must settle as probe error")
	}
}

func TestScriptProbeAppliesExtractBeforeInput(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wc-sx", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Kind: "script", URL: "https://example.com/p", Script: "emit(input())", Extract: []ExtractStep{{Kind: "css", Selector: "div.price"}}}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	var gotInput string
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, _, input string, _ bool, _ []string) (string, error) {
		gotInput = input
		return input, nil
	}
	o.ScheduleID = "wc-sx"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	fetch := func(context.Context, string) ([]byte, error) {
		return []byte(`<html><body><div class="price">$9</div>` + strings.Repeat("<p>noise</p>", 8000) + `</body></html>`), nil
	}
	if err := s.ProbeDueOccurrences(o.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	if gotInput != "$9" {
		t.Fatalf("extract chain did not shrink script input: %q", gotInput)
	}
	st, _ := s.loadWatchStateLocked("wc-sx")
	if st.Value != "$9" {
		t.Fatalf("script probe value not stored: %+v", st)
	}
}

func TestExtractRSSAndAtom(t *testing.T) {
	rss := `<?xml version="1.0"?><rss version="2.0"><channel><title>Feed</title>
		<item><title>Post One</title><link>https://x/1</link><pubDate>Mon, 01 Jan 2024</pubDate></item>
		<item><title>Post Two</title><link>https://x/2</link><pubDate>Tue, 02 Jan 2024</pubDate></item></channel></rss>`
	got, err := extractValue(&WatchProbe{Extract: []ExtractStep{{Kind: "rss"}}}, []byte(rss))
	if err != nil || got != "Post One|https://x/1|Mon, 01 Jan 2024\nPost Two|https://x/2|Tue, 02 Jan 2024" {
		t.Fatalf("rss: %q %v", got, err)
	}
	atom := `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">
		<entry><title>Entry A</title><link href="https://x/a"/><updated>2024-01-01T00:00:00Z</updated></entry></feed>`
	got, err = extractValue(&WatchProbe{Extract: []ExtractStep{{Kind: "rss"}}}, []byte(atom))
	if err != nil || got != "Entry A|https://x/a|2024-01-01T00:00:00Z" {
		t.Fatalf("atom: %q %v", got, err)
	}
	if _, err = extractValue(&WatchProbe{Extract: []ExtractStep{{Kind: "rss"}}}, []byte("not xml")); err == nil {
		t.Fatal("malformed feed must be a probe error")
	}
}

func TestRecipeExpandsToSnapshottedPipeline(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	created, err := s.CreateSchedule(Schedule{ScheduleID: "w-rec", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Recipe: "rss_feed", URL: "https://example.com/feed"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Probe.Extract) == 0 || created.Probe.RecipeVersion == 0 {
		t.Fatalf("recipe not snapshotted: %+v", created.Probe)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w-bad", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Recipe: "no_such", URL: "https://example.com/x"}}, caller, ""); err == nil {
		t.Fatal("unknown recipe accepted")
	}
	override, err := s.CreateSchedule(Schedule{ScheduleID: "w-ovr", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Recipe: "rss_feed", URL: "https://example.com/feed", Extract: []ExtractStep{{Kind: "strip"}}}}, caller, "")
	if err != nil || len(override.Probe.Extract) != 1 || override.Probe.Extract[0].Kind != "strip" || override.Probe.RecipeVersion == 0 {
		t.Fatalf("explicit extract must win over recipe: %+v %v", override.Probe, err)
	}
}

func TestProbeScriptValidatedAtCreate(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	draft := func(id, kind, script string) Schedule {
		return Schedule{ScheduleID: id, Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
			Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
			Probe: &WatchProbe{Kind: kind, URL: "https://example.com/p", Script: script}}
	}
	if _, err := s.CreateSchedule(draft("w-badscript", "script", `emit(`), caller, ""); err == nil {
		t.Fatal("broken probe script accepted")
	}
	if _, err := s.CreateSchedule(draft("w-noscript", "script", ""), caller, ""); err == nil {
		t.Fatal("script probe without script accepted")
	}
	if _, err := s.CreateSchedule(draft("w-noscript2", "command", ""), caller, ""); err == nil {
		t.Fatal("command probe without script accepted")
	}
	if _, err := s.CreateSchedule(draft("w-cmd-ok", "command", `emit(hash_path("."))`), caller, ""); err != nil {
		t.Fatalf("valid command probe rejected: %v", err)
	}
	if _, err := s.CreateSchedule(draft("w-stray", "", ""), caller, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w-stray2", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{URL: "https://example.com/p", Script: `emit("x")`}}, caller, ""); err == nil {
		t.Fatal("script on a non-script probe accepted")
	}
}

func TestGatewayWiresProbeScriptRunner(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if g.spool.probeScript == nil {
		t.Fatal("script/command probes have no runtime in a constructed gateway")
	}
}

func TestProbeScriptRunnerContractsSingleEmit(t *testing.T) {
	_, o, g := occurrenceFixture(t)
	var gotInput string
	var gotMount bool
	response := `["value"]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request hubruntime.ScriptRequest
		if r.URL.Path != "/v1/script" || json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("bad script request")
		}
		gotInput, gotMount = request.Input, request.MountSpace
		_, _ = w.Write([]byte(response))
	}))
	defer srv.Close()
	g.config.Supervised = true
	g.config.RuntimeURL = srv.URL
	g.config.RuntimeAuth = "token"
	value, err := g.runProbeScript(context.Background(), o, "emit(input())", "fetched-body", true, nil)
	if err != nil || value != "value" {
		t.Fatalf("probe script=%q %v", value, err)
	}
	if gotInput != "fetched-body" || !gotMount {
		t.Fatalf("input/mount lost in transport: %q %v", gotInput, gotMount)
	}
	response = `["a","b"]`
	if _, err = g.runProbeScript(context.Background(), o, "src", "", false, nil); err == nil {
		t.Fatal("multi-emit probe accepted")
	}
}

func TestProbeScriptRunnerForwardsNeeds(t *testing.T) {
	_, o, g := occurrenceFixture(t)
	var gotNeeds []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request hubruntime.ScriptRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("bad script request")
		}
		gotNeeds = request.Needs
		_, _ = w.Write([]byte(`["value"]`))
	}))
	defer srv.Close()
	g.config.Supervised = true
	g.config.RuntimeURL = srv.URL
	g.config.RuntimeAuth = "token"
	if _, err := g.runProbeScript(context.Background(), o, "emit(needs()[0])", "", false, []string{"alpha", "beta"}); err != nil {
		t.Fatal(err)
	}
	if len(gotNeeds) != 2 || gotNeeds[0] != "alpha" || gotNeeds[1] != "beta" {
		t.Fatalf("needs lost in transport: %v", gotNeeds)
	}
}
