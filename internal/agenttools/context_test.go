package agenttools

import (
	"context"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/compaction"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// context_status is the agent-facing bounded view: threshold with provenance,
// journaled compaction count and last event, live token estimate. Values the
// substrate cannot measure stay null — never fabricated.
func TestContextStatusToolReportsBoundedView(t *testing.T) {
	dir := t.TempDir()
	tools := &Tools{StateDir: dir}
	j, err := compaction.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	if err := j.Append(compaction.Event{
		At: at, SessionID: "hub-abc", Trigger: compaction.TriggerAutoSweep,
		Engine: compaction.EngineHub, Method: compaction.MethodArchiveCompct}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MANAGED_CONTEXT", `{"compression":{"hub_sweep":{"threshold_tokens":120000,"enabled":true}}}`)
	t.Setenv("HUB_CONTEXT_ID", "alice")
	old := compaction.Runner
	compaction.Runner = func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"ok":true,"tokens":54000,"message_count":9}`), nil
	}
	t.Cleanup(func() { compaction.Runner = old })

	out, err := tools.ContextStatus(context.Background(), Input{ID: "hub-abc"})
	if err != nil {
		t.Fatal(err)
	}
	view, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("status shape: %T", out)
	}
	if view["session_id"] != "hub-abc" {
		t.Fatalf("session: %v", view["session_id"])
	}
	if view["threshold_provenance"] != "declared" {
		t.Fatalf("threshold provenance: %v", view["threshold_provenance"])
	}
	if threshold, _ := view["threshold_tokens"].(*int64); threshold == nil || *threshold != 120000 {
		t.Fatalf("threshold: %v", view["threshold_tokens"])
	}
	compactions, _ := view["compactions"].(*int64)
	if compactions == nil || *compactions != 1 {
		t.Fatalf("compactions: %v", view["compactions"])
	}
	event, _ := view["last_event"].(*compaction.Event)
	if event == nil || event.Trigger != "auto_sweep" {
		t.Fatalf("last event: %v", view["last_event"])
	}
	tokens, _ := view["tokens"].(*int64)
	if tokens == nil || *tokens != 54000 || view["tokens_provenance"] != "estimated" {
		t.Fatalf("tokens: %v %v", view["tokens"], view["tokens_provenance"])
	}
	// No session argument: the tool resolves the newest journaled session —
	// the agent cannot name its own upstream session id.
	out, err = tools.ContextStatus(context.Background(), Input{})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["session_id"] != "hub-abc" {
		t.Fatalf("default session: %v", out)
	}
}

// A status call that names a foreign context is refused before any read —
// the runtime only ever answers for its own bound context.
func TestNoCrossContextStatus(t *testing.T) {
	tools := &Tools{StateDir: t.TempDir()}
	t.Setenv("HUB_CONTEXT_ID", "alice")
	if _, err := tools.ContextStatus(context.Background(), Input{Context: "mallory"}); err == nil {
		t.Fatal("foreign context status answered")
	}
	// The bound context itself is always allowed.
	if _, err := tools.ContextStatus(context.Background(), Input{Context: "alice"}); err != nil {
		t.Fatalf("own context refused: %v", err)
	}
}

// The name reaches the handler through the same dispatch the MCP surface and
// the ToolHub executor share — an unregistered case is a silent dead tool.
func TestContextStatusDispatchAndCatalog(t *testing.T) {
	tools := &Tools{StateDir: t.TempDir()}
	out, err := tools.call(context.Background(), "context_status", Input{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if out.(map[string]any)["object"] != "hub.context_status" {
		t.Fatalf("dispatch result: %v", out)
	}
	found := false
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Name == "context_status" {
			found = true
			if spec.Effect != api.ReadEffect || spec.CapabilityID == "" {
				t.Fatalf("spec: %+v", spec)
			}
		}
	}
	if !found {
		t.Fatal("context_status absent from the tool catalog")
	}
	// The MCP surface the agent actually sees must carry the tool too.
	client, closeConn := connectHub(t, fixture(t))
	defer closeConn()
	list, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	exposed := false
	for _, tool := range list.Tools {
		if tool.Name == "context_status" {
			exposed = true
		}
	}
	if !exposed {
		t.Fatal("context_status absent from the MCP surface")
	}
}
