package skilleval

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeRunner struct {
	// activated maps run index → whether the receipt contains a skill call.
	activated map[int]bool
	calls     int
	err       error
}

func (f *fakeRunner) Run(_ context.Context, _ string, _ string) (Receipt, error) {
	if f.err != nil {
		return Receipt{}, f.err
	}
	idx := f.calls
	f.calls++
	if f.activated[idx] {
		return Receipt{ToolCalls: []ToolCall{{Name: "skill", Args: map[string]any{"name": "demo"}}}}, nil
	}
	return Receipt{}, nil
}

// R12.3: a trigger leg scores validation accuracy over N runs; activation
// is a `skill` tool call in the run receipt; an unreachable model reports
// unavailable — never a fabricated pass.
func TestRunTriggersScoresAndReportsUnavailable(t *testing.T) {
	suite := TriggerSuite{Split: 0.5, Runs: 2, Threshold: 0.5, Queries: []TriggerQuery{
		{Q: "train-pos", Expect: true},
		{Q: "val-pos", Expect: true},
		{Q: "val-neg", Expect: false},
	}}
	// Only validation queries run: val-pos takes calls 0,1; val-neg 2,3.
	runner := &fakeRunner{activated: map[int]bool{0: true, 1: true}}
	rep := RunTriggers(context.Background(), suite, "demo", runner, "/tmp/space")
	if rep.Status != "done" || rep.Accuracy != 1.0 || !rep.Pass {
		t.Fatalf("report %+v", rep)
	}
	// A failing model leg is reported, not scored zero.
	rep = RunTriggers(context.Background(), suite, "demo", &fakeRunner{err: errors.New("no model")}, "/tmp/space")
	if rep.Status != "model_unavailable" || rep.Pass {
		t.Fatalf("unavailable leg must be honest: %+v", rep)
	}
}

// The live leg is an operator-supplied endpoint: it receives the space dir
// and prompt, and its receipt drives activation detection.
func TestHTTPRunnerPostsPromptAndDecodesReceipt(t *testing.T) {
	var got struct {
		Space  string `json:"space_dir"`
		Prompt string `json:"prompt"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{
			"tool_calls": []map[string]any{{"name": "skill", "args": map[string]any{"name": "demo"}}},
		})
	}))
	defer srv.Close()
	rc, err := HTTPRunner{URL: srv.URL}.Run(context.Background(), "/tmp/sp", "do it")
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "do it" || got.Space != "/tmp/sp" {
		t.Fatalf("request %+v", got)
	}
	if !receiptActivated(rc, "demo") {
		t.Fatalf("receipt %+v", rc)
	}
}
