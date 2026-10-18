package communication

// Probe source spectrum tests (SPEC-0059 amendment, CHG-0117): the tool
// alias, hub-mediated needs, content/artifact delivery, render and
// registered service probes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScriptProbePrefetchsNeeds(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wn-exec", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Kind: "command", Script: "emit(needs()[0])", Needs: []ProbeNeed{
			{Tool: "web_search", Args: map[string]any{"q": "one"}},
			{Tool: "knowledge_search", Args: map[string]any{"query": "two"}},
		}}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	var gotNeeds []string
	var called []string
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, _, _ string, _ bool, needs []string) (string, error) {
		gotNeeds = needs
		return "combined", nil
	}
	toolCall := func(_ context.Context, _ string, probe *WatchProbe) ([]byte, error) {
		called = append(called, probe.Tool)
		return []byte("result-" + probe.Tool), nil
	}
	o.ScheduleID = "wn-exec"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	fetch := func(context.Context, string) ([]byte, error) { return nil, errors.New("unused") }
	if err := s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, g.authorizeOccurrence, fetch, toolCall, nil); err != nil {
		t.Fatal(err)
	}
	if len(called) != 2 || called[0] != "web_search" || called[1] != "knowledge_search" {
		t.Fatalf("needs not prefetched in order: %v", called)
	}
	if len(gotNeeds) != 2 || gotNeeds[0] != "result-web_search" {
		t.Fatalf("script did not receive need results: %v", gotNeeds)
	}
	st, _ := s.loadWatchStateLocked("wn-exec")
	if st.Value != "combined" {
		t.Fatalf("script value not stored: %+v", st)
	}
}

func TestScriptProbeNeedResultTruncated(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wn-trunc", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Kind: "command", Script: "emit(needs()[0])", Needs: []ProbeNeed{{Tool: "web_search"}}}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	var gotNeeds []string
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, _, _ string, _ bool, needs []string) (string, error) {
		gotNeeds = needs
		return "x", nil
	}
	toolCall := func(context.Context, string, *WatchProbe) ([]byte, error) {
		return []byte(strings.Repeat("y", 100*1024)), nil
	}
	o.ScheduleID = "wn-trunc"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	fetch := func(context.Context, string) ([]byte, error) { return nil, errors.New("unused") }
	if err := s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, g.authorizeOccurrence, fetch, toolCall, nil); err != nil {
		t.Fatal(err)
	}
	if len(gotNeeds) != 1 || len(gotNeeds[0]) != 64*1024 {
		t.Fatalf("need result must be bounded to the transport cap: %d", len(gotNeeds[0]))
	}
}

func TestScriptProbeNeedFailureSettlesAsError(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "wn-fail", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Kind: "command", Script: "emit('x')", Needs: []ProbeNeed{{Tool: "web_search"}}}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	ran := false
	s.probeScript = func(_ context.Context, _ RoutineOccurrence, _, _ string, _ bool, _ []string) (string, error) {
		ran = true
		return "x", nil
	}
	toolCall := func(context.Context, string, *WatchProbe) ([]byte, error) {
		return nil, errors.New("search backend down")
	}
	o.ScheduleID = "wn-fail"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	fetch := func(context.Context, string) ([]byte, error) { return nil, errors.New("unused") }
	if err := s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, g.authorizeOccurrence, fetch, toolCall, nil); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("script ran after a failed need")
	}
	st, _ := s.loadWatchStateLocked("wn-fail")
	if st.LastError == "" {
		t.Fatal("failed need must settle as probe error")
	}
}

func TestProbeNeedsValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := func() Schedule {
		return Schedule{ScheduleID: "wn-1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
			Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
			Probe: &WatchProbe{Kind: "command", Script: "emit(needs()[0])", Needs: []ProbeNeed{{Tool: "web_search", Args: map[string]any{"q": "x"}}}}}
	}
	if _, err := s.CreateSchedule(base(), caller, ""); err != nil {
		t.Fatalf("script probe with a bounded read need must create: %v", err)
	}
	bad := base()
	bad.ScheduleID = "wn-2"
	bad.Probe = &WatchProbe{Kind: "", URL: "https://example.com/x", Needs: []ProbeNeed{{Tool: "web_search"}}}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("needs on a non-script probe accepted")
	}
	bad = base()
	bad.ScheduleID = "wn-3"
	bad.Probe.Needs = []ProbeNeed{{Tool: "a"}, {Tool: "b"}, {Tool: "c"}, {Tool: "d"}, {Tool: "e"}}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("fifth need accepted")
	}
	bad = base()
	bad.ScheduleID = "wn-4"
	bad.Probe.Needs = []ProbeNeed{{Tool: "not a tool!!"}}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("invalid need tool name accepted")
	}
	bad = base()
	bad.ScheduleID = "wn-5"
	bad.Probe.Needs = []ProbeNeed{{Tool: "web_search", Args: map[string]any{"q": string(make([]byte, 0)) + "x", "pad": strings.Repeat("y", 5*1024)}}}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("oversized need args accepted")
	}
}

func TestDeliverModeValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := func() Schedule {
		return Schedule{ScheduleID: "dm-1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
			Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
			Probe: &WatchProbe{URL: "https://example.com/x"}, DeliverMode: "content"}
	}
	if _, err := s.CreateSchedule(base(), caller, ""); err != nil {
		t.Fatalf("deliver=content watcher must create: %v", err)
	}
	bad := base()
	bad.ScheduleID = "dm-2"
	bad.DeliverMode = "carrier-pigeon"
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("unknown deliver mode accepted")
	}
	bad = base()
	bad.ScheduleID = "dm-3"
	bad.JobKind = "agent"
	bad.Probe = nil
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("deliver mode on a non-watcher accepted")
	}
	bad = base()
	bad.ScheduleID = "dm-4"
	bad.DeliverMode = "artifact"
	bad.ArtifactName = "../escape"
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("artifact name escaping the blob path accepted")
	}
}

func TestDeliverContentAndArtifactTickEverySuccess(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "dc-1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{URL: "https://example.com/x"}, DeliverMode: "content"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "dc-1"
	o.JobKind = "watcher"
	run := func(due time.Duration, body string) {
		oc := o
		oc.DueAt = o.DueAt.Add(due)
		if err := s.PutOccurrence(oc, caller); err != nil {
			t.Fatal(err)
		}
		if err := s.DispatchDueOccurrences(oc.DueAt, g.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
		if err := s.ProbeDueOccurrences(o.DueAt.Add(due+time.Second), func(context.Context, string) ([]byte, error) { return []byte(body), nil }); err != nil {
			t.Fatal(err)
		}
	}
	run(0, "same-value")
	run(time.Hour, "same-value")
	var texts []string
	for i := 0; i < 2; i++ {
		name, err := firstJSON(filepath.Join(s.root, "outbox", "pending"))
		if err != nil || name == "" {
			t.Fatalf("content tick %d produced no delivery: %q %v", i, name, err)
		}
		b, _ := os.ReadFile(filepath.Join(s.root, "outbox", "pending", name))
		var d Delivery
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, d.Text)
		_ = os.Remove(filepath.Join(s.root, "outbox", "pending", name))
	}
	if len(texts) != 2 || !strings.Contains(texts[1], "same-value") {
		t.Fatalf("content mode must ship the probed value every tick: %v", texts)
	}

	if _, err := s.CreateSchedule(Schedule{ScheduleID: "da-1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{URL: "https://example.com/y"}, DeliverMode: "artifact", ArtifactName: "report.txt"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	oa := o
	oa.ScheduleID = "da-1"
	if err := s.PutOccurrence(oa, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(oa.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(oa.DueAt, func(context.Context, string) ([]byte, error) { return []byte("file-body"), nil }); err != nil {
		t.Fatal(err)
	}
	name, err := firstJSON(filepath.Join(s.root, "outbox", "pending"))
	if err != nil || name == "" {
		t.Fatalf("artifact tick produced no delivery: %q %v", name, err)
	}
	b, _ := os.ReadFile(filepath.Join(s.root, "outbox", "pending", name))
	var d Delivery
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Artifacts) != 1 || d.Artifacts[0].Name != "report.txt" {
		t.Fatalf("artifact delivery=%+v", d.Artifacts)
	}
	data, err := s.ReadDeliveryBlob(d.ID, d.Artifacts[0].Blob)
	if err != nil || string(data) != "file-body" {
		t.Fatalf("artifact blob=%q %v", data, err)
	}
}

func TestRenderAndServiceProbeValidation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := func() Schedule {
		return Schedule{ScheduleID: "rs-1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
			Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
			Probe: &WatchProbe{Kind: "render", URL: "https://spa.example.com", Service: "browserless", Steps: []RenderStep{{Action: "click", Selector: "#load"}, {Action: "wait_for", Selector: ".results"}}, WaitFor: "networkidle"}}
	}
	if _, err := s.CreateSchedule(base(), caller, ""); err != nil {
		t.Fatalf("render probe must create: %v", err)
	}
	bad := base()
	bad.ScheduleID = "rs-2"
	bad.Probe.Service = ""
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("render probe without a registered service accepted")
	}
	bad = base()
	bad.ScheduleID = "rs-3"
	bad.Probe.Service = "not a service!!"
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("invalid service name accepted")
	}
	bad = base()
	bad.ScheduleID = "rs-4"
	bad.Probe.Steps = []RenderStep{{Action: "detonate", Selector: "*"}}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("unknown browser step action accepted")
	}
	for i, action := range []string{"fill", "scroll", "wait", "evaluate"} {
		good := base()
		good.ScheduleID = fmt.Sprintf("rs-a%d", i)
		good.Probe.Steps = []RenderStep{{Action: action, Selector: "#x", Value: "1"}}
		if _, err := s.CreateSchedule(good, caller, ""); err != nil {
			t.Fatalf("browser step %s rejected: %v", action, err)
		}
	}
	bad = base()
	bad.ScheduleID = "rs-5"
	bad.Probe.URL = ""
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("render probe without a target url accepted")
	}
	good := base()
	good.ScheduleID = "rs-6"
	good.Probe = &WatchProbe{Kind: "service", Service: "jobftch", Args: map[string]any{"tenant": "alice"}}
	if _, err := s.CreateSchedule(good, caller, ""); err != nil {
		t.Fatalf("service probe must create: %v", err)
	}
	bad = base()
	bad.ScheduleID = "rs-7"
	bad.Probe = &WatchProbe{Kind: "service"}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("service probe without a service name accepted")
	}
	bad = base()
	bad.ScheduleID = "rs-8"
	bad.Probe.Steps = make([]RenderStep, 9)
	for i := range bad.Probe.Steps {
		bad.Probe.Steps[i] = RenderStep{Action: "wait", Value: "100"}
	}
	if _, err := s.CreateSchedule(bad, caller, ""); err == nil {
		t.Fatal("ninth browser step accepted")
	}
}

func TestRenderAndServiceProbesUseServiceCall(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "rts-1", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Kind: "render", URL: "https://spa.example.com", Service: "browserless", Extract: []ExtractStep{{Kind: "css", Selector: "div.price"}}}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "rts-1"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	var gotProbe *WatchProbe
	serviceCall := func(_ context.Context, _ string, probe *WatchProbe) ([]byte, error) {
		gotProbe = probe
		return []byte(`<html><div class="price">42</div></html>`), nil
	}
	fetch := func(context.Context, string) ([]byte, error) {
		return nil, errors.New("render must not fetch directly")
	}
	if err := s.ProbeDueOccurrencesContext(context.Background(), o.DueAt, g.authorizeOccurrence, fetch, nil, serviceCall); err != nil {
		t.Fatal(err)
	}
	if gotProbe == nil || gotProbe.Service != "browserless" {
		t.Fatalf("render probe did not reach the service path: %+v", gotProbe)
	}
	st, _ := s.loadWatchStateLocked("rts-1")
	if st.Value != "42" {
		t.Fatalf("rendered DOM did not feed the extract chain: %+v", st)
	}
}

func TestServiceProbePostsToRegisteredEndpoint(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`<div class="price">7</div>`))
	}))
	defer srv.Close()
	g := &Gateway{config: Config{ServiceEndpoints: map[string]ServiceEndpoint{
		"browserless": {URL: srv.URL, Auth: "svc-token"},
	}}}
	body, err := g.serviceProbe(context.Background(), "alice", &WatchProbe{
		Kind: "render", URL: "https://spa.example.com", Service: "browserless",
		Steps: []RenderStep{{Action: "click", Selector: "#go"}}, WaitFor: "networkidle",
	})
	if err != nil || string(body) != `<div class="price">7</div>` {
		t.Fatalf("service probe body=%q %v", body, err)
	}
	if gotAuth != "Bearer svc-token" {
		t.Fatalf("endpoint auth not sent: %q", gotAuth)
	}
	if gotBody["kind"] != "render" || gotBody["url"] != "https://spa.example.com" || gotBody["wait_for"] != "networkidle" {
		t.Fatalf("render request malformed: %v", gotBody)
	}
	steps, _ := gotBody["steps"].([]any)
	if len(steps) != 1 || steps[0].(map[string]any)["action"] != "click" {
		t.Fatalf("browser steps lost: %v", gotBody)
	}
	if _, err := g.serviceProbe(context.Background(), "alice", &WatchProbe{Kind: "render", Service: "unregistered"}); err == nil {
		t.Fatal("unregistered service name accepted")
	}
	g.config.ServiceEndpoints["broken"] = ServiceEndpoint{URL: srv.URL + "/fail"}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/huge" {
			_, _ = w.Write([]byte(strings.Repeat("x", 512*1024+1)))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	g.config.ServiceEndpoints["huge"] = ServiceEndpoint{URL: srv.URL + "/huge"}
	if _, err := g.serviceProbe(context.Background(), "alice", &WatchProbe{Kind: "service", Service: "broken"}); err == nil {
		t.Fatal("non-2xx service response accepted")
	}
	if _, err := g.serviceProbe(context.Background(), "alice", &WatchProbe{Kind: "service", Service: "huge"}); err == nil {
		t.Fatal("oversized service response accepted")
	}
}

// Check-kind creation probes once to seed the baseline; render/service probes
// must reach the registered service there too, not the raw HTTP fetch.
func TestCheckCreateSeedsBaselineThroughServiceProbe(t *testing.T) {
	s, _, g := occurrenceFixture(t)
	g.spool = s
	g.config.ControlAuth = "tok"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	var seeded string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seeded = "rendered-7"
		_, _ = w.Write([]byte(seeded))
	}))
	defer srv.Close()
	g.config.ServiceEndpoints = map[string]ServiceEndpoint{"browserless": {URL: srv.URL}}
	body := strings.NewReader(`{"schedule_id":"ck-render","timezone":"UTC","expression":"0 * * * *","input":"watch spa","job_kind":"check","expect":"changed","probe":{"kind":"render","url":"https://spa.example.com","service":"browserless"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/routines", body)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("render check create rejected: %d %s", rec.Code, rec.Body.String())
	}
	st, err := s.loadWatchStateLocked("ck-render")
	if err != nil || st.Value != seeded {
		t.Fatalf("baseline not seeded from render service: %q %v", st.Value, err)
	}
	seeded = ""
	svcBody := strings.NewReader(`{"schedule_id":"ck-svc","timezone":"UTC","expression":"0 * * * *","input":"jobs","job_kind":"check","expect":"changed","probe":{"kind":"service","service":"browserless"}}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/routines", svcBody)
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Hub-Principal", "alice")
	rec = httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || seeded == "" {
		t.Fatalf("service check seed did not reach endpoint: %d %s", rec.Code, rec.Body.String())
	}
}

func TestToolKindAliasNormalizesToMCPTool(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	created, err := s.CreateSchedule(Schedule{ScheduleID: "wt-tool", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID,
		Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "w", JobKind: "watcher",
		Probe: &WatchProbe{Kind: "tool", Tool: "file_read", Args: map[string]any{"path": "notes.md"}}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Probe.Kind != "mcp_tool" {
		t.Fatalf("kind=tool must normalize to mcp_tool: %+v", created.Probe)
	}
}
