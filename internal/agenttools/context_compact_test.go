package agenttools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/compaction"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// An ungranted engine is a named refusal with a journaled audit row — the
// tool never silently falls back to a permitted engine.
func TestContextCompactDeniesUngrantedEngine(t *testing.T) {
	dir := t.TempDir()
	tools := &Tools{StateDir: dir}
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"compact_engines":["hub"],"compact_engine":"hub"}`)
	old := compaction.Runner
	called := false
	compaction.Runner = func(context.Context, []byte) ([]byte, error) {
		called = true
		return []byte(`{"ok":true}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })

	out, err := tools.ContextCompact(context.Background(), Input{ID: "hub-abc", Engine: "native"})
	if err != nil {
		t.Fatal(err)
	}
	view := out.(map[string]any)
	if view["failure_class"] != "engine_denied" {
		t.Fatalf("denial: %v", view)
	}
	if called {
		t.Fatal("sidecar invoked for an ungranted engine")
	}
	j, err := compaction.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	events, err := j.Events("hub-abc")
	if err != nil || len(events) != 1 || events[0].FailureClass != "engine_denied" || events[0].Engine != "native" {
		t.Fatalf("audit row: %+v err=%v", events, err)
	}
}

// In-run the session's turn lease is held, so a bare call refuses
// deterministically; queue=true parks the request for the post-job boundary.
func TestContextCompactBusyQueue(t *testing.T) {
	dir := t.TempDir()
	tools := &Tools{StateDir: dir}
	t.Setenv("HUB_CONTEXT_ID", "alice")
	old := compaction.Runner
	compaction.Runner = func(_ context.Context, req []byte) ([]byte, error) {
		var m map[string]any
		_ = json.Unmarshal(req, &m)
		switch m["op"] {
		case "active_session":
			return []byte(`{"ok":true,"session_id":"hub-run"}`), nil
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":9000,"message_count":30}`), nil
		case "lease":
			return []byte(`{"ok":true,"acquired":false}`), nil
		}
		return []byte(`{"ok":false,"error":"internal"}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })

	out, err := tools.ContextCompact(context.Background(), Input{ID: "hub-run", Queue: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["failure_class"]; got != "queued" {
		t.Fatalf("busy+queue: %v", out)
	}
	if pending := compaction.TakePending(dir, "hub-run"); pending == nil {
		t.Fatal("pending not parked")
	}
}

// The tool is registered on the ToolHub catalog as a write effect and reaches
// the handler through the shared dispatch.
func TestContextCompactDispatchAndCatalog(t *testing.T) {
	dir := t.TempDir()
	tools := &Tools{StateDir: dir}
	t.Setenv("HUB_CONTEXT_ID", "alice")
	old := compaction.Runner
	compaction.Runner = func(_ context.Context, req []byte) ([]byte, error) {
		return []byte(`{"ok":true,"session_id":"hub-x","tokens":1,"message_count":1,"acquired":false}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	out, err := tools.call(context.Background(), "context_compact", Input{ID: "hub-x"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if out.(map[string]any)["object"] != "hub.context_compact" {
		t.Fatalf("dispatch result: %v", out)
	}
	found := false
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name == "context_compact" {
			found = true
			if spec.Effect != api.WriteEffect || spec.CapabilityID == "" {
				t.Fatalf("spec: %+v", spec)
			}
		}
	}
	if !found {
		t.Fatal("context_compact absent from the tool catalog")
	}
	client, closeConn := connectHub(t, fixture(t))
	defer closeConn()
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	exposed := false
	for _, tool := range list.Tools {
		if tool.Name == "context_compact" {
			exposed = true
		}
	}
	if !exposed {
		t.Fatal("context_compact absent from the MCP surface")
	}
}

// A context argument naming anything but this runtime's bound context is a
// cross-context probe — refused before any session work.
func TestContextCompactCrossContextDeny(t *testing.T) {
	tools := &Tools{StateDir: t.TempDir()}
	t.Setenv("HUB_CONTEXT_ID", "alice")
	old := compaction.Runner
	called := false
	compaction.Runner = func(context.Context, []byte) ([]byte, error) {
		called = true
		return []byte(`{"ok":true}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	err := func() error {
		_, err := tools.ContextCompact(context.Background(), Input{Context: "mallory", ID: "hub-x"})
		return err
	}()
	if err == nil || !strings.Contains(err.Error(), "bound to this runtime's context") {
		t.Fatalf("foreign context compact: %v", err)
	}
	if called {
		t.Fatal("sidecar invoked for a foreign context")
	}
}

// Without an explicit id the tool resolves the session holding the turn
// lease — in-run that is verifiably the caller's own conversation.
func TestContextCompactResolvesActiveSession(t *testing.T) {
	dir := t.TempDir()
	tools := &Tools{StateDir: dir}
	t.Setenv("HUB_CONTEXT_ID", "alice")
	old := compaction.Runner
	compaction.Runner = func(_ context.Context, req []byte) ([]byte, error) {
		var m map[string]any
		_ = json.Unmarshal(req, &m)
		switch m["op"] {
		case "active_session":
			return []byte(`{"ok":true,"session_id":"hub-leased"}`), nil
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":100,"message_count":4}`), nil
		case "lease":
			return []byte(`{"ok":true,"acquired":false}`), nil
		}
		return []byte(`{"ok":false,"error":"internal"}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })

	out, err := tools.ContextCompact(context.Background(), Input{Queue: true})
	if err != nil {
		t.Fatal(err)
	}
	view := out.(map[string]any)
	if view["session_id"] != "hub-leased" || view["failure_class"] != "queued" {
		t.Fatalf("resolution: %v", view)
	}
	if pending := compaction.TakePending(dir, "hub-leased"); pending == nil {
		t.Fatal("pending parked under the wrong session")
	}
}

// A committed compact reports the allowlisted projection: method, counts and
// token estimates — never summary or transcript text.
func TestContextCompactCommitProjection(t *testing.T) {
	dir := t.TempDir()
	tools := &Tools{StateDir: dir}
	t.Setenv("HUB_CONTEXT_ID", "alice")
	old := compaction.Runner
	var ops []string
	messages := []map[string]any{}
	for i := 0; i < 14; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": "m"})
	}
	messages[2] = map[string]any{"role": "tool", "content": strings.Repeat("x", 5000)}
	body, _ := json.Marshal(messages)
	compaction.Runner = func(_ context.Context, req []byte) ([]byte, error) {
		var m map[string]any
		_ = json.Unmarshal(req, &m)
		op, _ := m["op"].(string)
		ops = append(ops, op)
		switch op {
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":5000,"message_count":14}`), nil
		case "lease", "lock":
			return []byte(`{"ok":true,"acquired":true,"locked":true,"holder":"hub-compact"}`), nil
		case "read_transcript":
			return []byte(`{"ok":true,"session_id":"hub-x","watermark":14,"messages":` + string(body) + `}`), nil
		case "apply_transcript":
			return []byte(`{"ok":true,"applied":true,"active_count":9}`), nil
		case "unlease", "unlock":
			return []byte(`{"ok":true,"released":true}`), nil
		}
		return []byte(`{"ok":false,"error":"internal"}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })

	out, err := tools.ContextCompact(context.Background(), Input{ID: "hub-x", Here: 4, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	view := out.(map[string]any)
	if view["applied"] != true || view["session_id"] != "hub-x" {
		t.Fatalf("result: %v", view)
	}
	if view["method"] != "prune_only" || view["messages_before"] != 14 || view["messages_after"] != 14 {
		t.Fatalf("projection: %v", view)
	}
	if _, leaked := view["summary"]; leaked {
		t.Fatal("summary text leaked into the tool result")
	}
}

// A bare compact whose pruned head still exceeds the threshold reaches the
// summarizer — the tool wires the same managed relay the channel path uses.
func TestContextCompactInvokesSummarizer(t *testing.T) {
	dir := t.TempDir()
	tools := &Tools{StateDir: dir}
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_MANAGED_CONTEXT", `{"compression":{"hub_sweep":{"threshold_tokens":100}},"summary_model":"sum-model"}`)
	old := compaction.Runner
	messages := []map[string]any{}
	for i := 0; i < 12; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": strings.Repeat("y", 2000)})
	}
	body, _ := json.Marshal(messages)
	var applied int64
	compaction.Runner = func(_ context.Context, req []byte) ([]byte, error) {
		var m map[string]any
		_ = json.Unmarshal(req, &m)
		switch m["op"] {
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":6000,"message_count":12}`), nil
		case "lease", "lock":
			return []byte(`{"ok":true,"acquired":true,"locked":true}`), nil
		case "read_transcript":
			return []byte(`{"ok":true,"session_id":"hub-x","watermark":12,"messages":` + string(body) + `}`), nil
		case "apply_transcript":
			applied = int64(len(m["messages"].([]any)))
			return []byte(`{"ok":true,"applied":true,"active_count":5}`), nil
		case "unlease", "unlock":
			return []byte(`{"ok":true,"released":true}`), nil
		}
		return []byte(`{"ok":false,"error":"internal"}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })
	summarized := false
	gotModel := ""
	oldSum := contextCompactSummarize
	contextCompactSummarize = func(_ context.Context, model string, _ []map[string]any, _ string) (string, error) {
		summarized = true
		gotModel = model
		return "s", nil
	}
	t.Cleanup(func() { contextCompactSummarize = oldSum })

	out, err := tools.ContextCompact(context.Background(), Input{ID: "hub-x"})
	if err != nil {
		t.Fatal(err)
	}
	if !summarized || applied == 0 {
		t.Fatalf("summarizer not reached: %v", out)
	}
	if gotModel != "sum-model" {
		t.Fatalf("summary model not resolved: %q", gotModel)
	}

	// Undeclared spaces fall back to the managed main model id.
	t.Setenv("HUB_MANAGED_CONTEXT", `{"compression":{"hub_sweep":{"threshold_tokens":100}}}`)
	t.Setenv("HUB_MANAGED_MODEL_ID", "main-model")
	gotModel = ""
	if _, err := tools.ContextCompact(context.Background(), Input{ID: "hub-x"}); err != nil {
		t.Fatal(err)
	}
	if gotModel != "main-model" {
		t.Fatalf("fallback model: %q", gotModel)
	}
}
