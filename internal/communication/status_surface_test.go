package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// SPEC-0053 R1.4-R1.6: /status names the missing tools, the surface reason and
// the model verdict in plain language — scoped to the caller's own runtime so
// one principal never sees another's surface.
func TestStatusShowsMissingTools(t *testing.T) {
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runtimes" || r.Header.Get("Authorization") != "Bearer supervisor-auth" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"principal_id": "alice", "context_id": "alice", "runtime_health": "running", "hermes_readiness": "degraded",
				"tool_surface": "degraded", "missing_tools": []string{"image_gen"},
				"tool_reasons": map[string]string{"hub": "child_env_unresolved:HUB_POLICY_VERSION"}, "model_status": "model_not_found",
			},
			{
				"principal_id": "bob", "context_id": "bob", "runtime_health": "running", "hermes_readiness": "degraded",
				"tool_surface": "degraded", "missing_tools": []string{"bob-secret-tool"},
			},
		})
	}))
	defer supervisor.Close()
	c := testConfig(t)
	c.Supervised = true
	c.RuntimeURL = supervisor.URL
	c.RuntimeAuth = "supervisor-auth"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	answer := g.taskStatus(user, Task{TaskID: "t1", ConversationID: "c1"})
	if !strings.Contains(answer, "image_gen") {
		t.Fatalf("missing tool not shown: %q", answer)
	}
	if !strings.Contains(answer, "child_env_unresolved") {
		t.Fatalf("reason not shown: %q", answer)
	}
	if !strings.Contains(answer, "model_not_found") && !strings.Contains(answer, "Модель") {
		t.Fatalf("model status not shown: %q", answer)
	}
	if strings.Contains(answer, "bob") || strings.Contains(answer, "bob-secret-tool") {
		t.Fatalf("cross-principal surface leaked: %q", answer)
	}
}

// Unsupervised mode reads the resident runtime's /v1/health directly — the
// same additive fields, no supervisor round-trip needed.
func TestStatusShowsMissingToolsUnsupervised(t *testing.T) {
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" || r.Header.Get("Authorization") != "Bearer runtime-auth" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hermes_readiness": "degraded", "connector_health": "healthy", "external_connections": "not_probed",
			"tool_surface": "degraded", "missing_tools": []string{"terminal"}, "model_status": "ok",
		})
	}))
	defer runtimeAPI.Close()
	c := testConfig(t)
	c.Supervised = false
	c.RuntimeURL = runtimeAPI.URL
	c.RuntimeAuth = "runtime-auth"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	answer := g.taskStatus(user, Task{TaskID: "t1", ConversationID: "c1"})
	if !strings.Contains(answer, "terminal") {
		t.Fatalf("missing tool not shown: %q", answer)
	}
}

// SPEC-0053 R1.8/S8: the per-run instructions field carries only per-run data
// (style, voice availability). The static channel contract lives in the
// rendered effective SOUL, so routine text turns stop re-paying its tokens.
func TestRunInstructionsArePerRunOnly(t *testing.T) {
	var gotInstructions any
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			gotInstructions = body["instructions"]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"job_id": "j1", "run_id": "r1", "session_id": "s1", "status": "completed", "text": "ok"})
	}))
	defer runtimeAPI.Close()

	runner := HTTPRunner{URL: runtimeAPI.URL, Auth: "auth", Voice: true}
	if _, err := runner.Run(context.Background(), Job{ID: "j1", Text: "hello"}, User{ID: "alice"}); err != nil {
		t.Fatal(err)
	}
	text, _ := gotInstructions.(string)
	for _, static := range []string{"artifacts/documents/", "MEDIA: ", "routine_create", "file_list"} {
		if strings.Contains(text, static) {
			t.Fatalf("per-run instructions still carry the static contract %q: %q", static, text)
		}
	}
	if !strings.Contains(text, "VOICE:") {
		t.Fatalf("voice availability must stay per-run: %q", text)
	}
}

// SPEC-0053 R3.6: /status carries a "Здоровье" block even when nothing has
// run — the empty case is explicit, not a missing section.
func TestStatusHealthWithNoData(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	answer := g.taskStatus(g.user("alice"), Task{TaskID: "t1", ConversationID: "c1"})
	if !strings.Contains(answer, "Здоровье") {
		t.Fatalf("health block missing: %q", answer)
	}
	if !strings.Contains(answer, "нет данных") && !strings.Contains(answer, "0") {
		t.Fatalf("empty health state not rendered: %q", answer)
	}
}

// The block shows last-24h latency percentiles, unconfirmed/failed counts,
// runtime restarts and average input tokens — all scoped to the caller.
func TestStatusHealthBlock(t *testing.T) {
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runtimes" || r.Header.Get("Authorization") != "Bearer supervisor-auth" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"principal_id": "alice", "context_id": "alice", "restarts": 3},
		})
	}))
	defer supervisor.Close()
	c := testConfig(t)
	c.Supervised = true
	c.RuntimeURL = supervisor.URL
	c.RuntimeAuth = "supervisor-auth"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	latencies := []time.Duration{2 * time.Second, 4 * time.Second, 10 * time.Second, 20 * time.Second}
	for i, d := range latencies {
		m := JobMapping{
			JobID: fmt.Sprintf("done-%d", i), IdempotencyKey: fmt.Sprintf("k-%d", i),
			UserID: "alice", PrincipalID: "alice", ContextID: "alice", Channel: "telegram",
			Status: "completed", CreatedAt: now.Add(-d - time.Second), UpdatedAt: now.Add(-d), TerminalAt: now.Add(-d),
		}
		m.CreatedAt = m.TerminalAt.Add(-d)
		if err := g.spool.writeMappingLocked(m); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []JobMapping{
		{JobID: "unc-1", IdempotencyKey: "ku-1", UserID: "alice", Status: "uncertain", CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)},
		{JobID: "fail-1", IdempotencyKey: "kf-1", UserID: "alice", Status: "failed", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), TerminalAt: now.Add(-time.Hour)},
	} {
		if err := g.spool.writeMappingLocked(m); err != nil {
			t.Fatal(err)
		}
	}
	// Usage journal: two measured calls, 1000 + 3000 input tokens → avg 2000.
	in1000, in3000 := int64(1000), int64(3000)
	entries := []usageEntry{
		{Version: 2, ScopeID: "user:alice", JobID: "done-0", MeasuredAt: now.Add(-time.Hour), InputTokens: &in1000},
		{Version: 2, ScopeID: "user:alice", JobID: "done-1", MeasuredAt: now.Add(-time.Hour), InputTokens: &in3000},
	}
	var journal bytes.Buffer
	for _, e := range entries {
		b, _ := json.Marshal(e)
		journal.Write(append(b, '\n'))
	}
	if err := os.WriteFile(usagePath(g.spool.root), journal.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	answer := g.taskStatus(g.user("alice"), Task{TaskID: "t1", ConversationID: "c1"})
	for _, want := range []string{"Здоровье", "p50", "p95", "1", "3"} {
		if !strings.Contains(answer, want) {
			t.Fatalf("health block missing %q: %q", want, answer)
		}
	}
	if !strings.Contains(answer, "2000") {
		t.Fatalf("average input tokens missing: %q", answer)
	}
}
