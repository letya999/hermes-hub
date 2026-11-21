package hermes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRuntimeStreamEmitsHeartbeat(t *testing.T) {
	old := streamHeartbeatInterval
	streamHeartbeatInterval = 10 * time.Millisecond
	defer func() { streamHeartbeatInterval = old }()
	s := &runtimeHTTP{}
	rec := httptest.NewRecorder()
	s.writeStream(rec, ExecuteRequest{JobID: "job-1"}, func(func(ExecuteResponse) error) (ExecuteResponse, error) {
		time.Sleep(55 * time.Millisecond)
		return ExecuteResponse{Status: "completed", LastEvent: "run.completed", Text: "done"}, nil
	})
	var events []ExecuteResponse
	dec := json.NewDecoder(rec.Body)
	for {
		var event ExecuteResponse
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) == 0 || events[len(events)-1].Status != "completed" {
		t.Fatalf("terminal event missing: %+v", events)
	}
	heartbeats := 0
	for i, event := range events {
		if event.LastEvent != "run.heartbeat" {
			continue
		}
		heartbeats++
		if event.JobID != "job-1" {
			t.Fatalf("heartbeat without job identity: %+v", event)
		}
		if i == len(events)-1 {
			t.Fatal("heartbeat emitted after the terminal event")
		}
	}
	if heartbeats == 0 {
		t.Fatalf("no heartbeat frames in %+v", events)
	}
}
