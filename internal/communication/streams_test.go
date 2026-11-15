package communication

// The stream/event machinery itself moved to the ToolHub automation owner
// (#523): acquisition, settlement, cursors, seen-sets, filters, sinks and
// pacing are asserted in internal/toolhub/automation/streams_ported_test.go
// against the real eventStore seams. What remains here are the surfaces
// communication still owns: the HTTP proxy routes, the chat command
// rendering and the hook intake endpoint.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// POST /v1/routines/{id}/sample is the owner-facing wire for watch_sample:
// a failed source surfaces its own error entry instead of pretending the
// source is quiet.
func TestWatchSampleHTTPRouteReportsSourceErrors(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, _, _ := automationGatewayFixture(t, c)
	g.api = &fakeAPI{}
	handler := g.Handler()
	post := func(path, body, principal string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer control-token")
		req.Header.Set("X-Hub-Principal", principal)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	create := `{"schedule_id":"samp-http","timezone":"UTC","expression":"* * * * *","input":"watch groups","job_kind":"event","delivery":"stream","probe":{"kind":"mcp_tool","tool":"hub-tg-wait-abc","sources":[{"name":"alpha","args":{"chat_id":1}}]}}`
	if rec := post("/v1/routines", create, "alice"); rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	rec := post("/v1/routines/samp-http/sample", `{}`, "alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("sample: %d %s", rec.Code, rec.Body.String())
	}
	var sample struct {
		ScheduleID string `json:"schedule_id"`
		Sources    []struct {
			Source string `json:"source"`
			Error  string `json:"error"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sample); err != nil {
		t.Fatal(err)
	}
	if sample.ScheduleID != "samp-http" || len(sample.Sources) != 1 || sample.Sources[0].Source != "alpha" {
		t.Fatalf("sample shape lost: %s", rec.Body.String())
	}
	// No live connector in tests: the source's failure must surface in its
	// own error field, not as a silent empty batch.
	if sample.Sources[0].Error == "" {
		t.Fatalf("dead source reported as quiet: %s", rec.Body.String())
	}
	// Foreign principal is denied — control auth rejects the unknown
	// caller before the routine-ownership check ever runs.
	if rec := post("/v1/routines/samp-http/sample", `{}`, "mallory"); rec.Code == http.StatusOK {
		t.Fatalf("foreign caller sampled: %s", rec.Body.String())
	}
}

// S6.4 — /watches marks a stream subscription's delivery mode so the owner
// can tell a reactive wait-loop from a cron poll at a glance.
func TestWatchesMarksStream(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, _, _ := automationGatewayFixture(t, c)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", `{"schedule_id":"wm","timezone":"UTC","expression":"* * * * *","input":"watch dms","job_kind":"event","delivery":"stream","probe":{"kind":"mcp_tool","tool":"hub-tg-wait-a"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("watch create: %d %s", w.Code, w.Body.String())
	}
	user := g.user("alice")
	out := g.watchesCommand(user, 11)
	if !strings.Contains(out, "wm") || !strings.Contains(out, ", stream") {
		t.Fatalf("stream delivery not rendered: %q", out)
	}
}

// TestHookIntakeEndpoint (SPEC-0081 R5.2): POST /v1/hooks/<id> stages the
// posted body in the schedule's durable inbox — only for the owning caller
// and only for probe.kind=hook schedules.
func TestHookIntakeEndpoint(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, _, root := automationGatewayFixture(t, c)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", `{"schedule_id":"hook-1","timezone":"UTC","expression":"* * * * *","job_kind":"event","input":"hook","probe":{"kind":"hook","url":"hook:github"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("hook create: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/hooks/hook-1", `{"events":[{"id":"h1","text":"push"}]}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("hook intake rejected: %d %s", w.Code, w.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(root, "users", "alice", "hooks", commodel.FileID("hook-1")))
	if err != nil || len(entries) != 1 {
		t.Fatalf("payload not staged under ToolHub owner: %d %v", len(entries), err)
	}
	// Non-hook schedules reject intake.
	w = destinationRequest(g, http.MethodPost, "/v1/routines", `{"schedule_id":"plain-1","timezone":"UTC","expression":"* * * * *","job_kind":"event","input":"plain","probe":{"kind":"http","url":"https://example.com"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("plain create: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/hooks/plain-1", `{"events":[]}`)
	if w.Code == http.StatusAccepted {
		t.Fatal("intake accepted a non-hook schedule")
	}
	// Unauthorized callers never reach intake.
	r := httptest.NewRequest(http.MethodPost, "/v1/hooks/hook-1", strings.NewReader(`{"events":[]}`))
	w = httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated intake must 401, got %d", w.Code)
	}
	// Empty payloads are rejected.
	if w = destinationRequest(g, http.MethodPost, "/v1/hooks/hook-1", ""); w.Code == http.StatusAccepted {
		t.Fatal("empty payload accepted")
	}
	// GET is not intake.
	if w = destinationRequest(g, http.MethodGet, "/v1/hooks/hook-1", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET must 405, got %d", w.Code)
	}
	// Missing and foreign IDs share a 404 response.
	if w = destinationRequest(g, http.MethodPost, "/v1/hooks/bob-hook", `{"events":[]}`); w.Code != http.StatusNotFound {
		t.Fatalf("foreign hook must 404, got %d", w.Code)
	}
}
