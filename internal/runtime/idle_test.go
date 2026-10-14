package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"time"
)

// ctxReader blocks until done closes, mirroring an HTTP body that fails once
// the request context is cancelled.
type ctxReader struct {
	done chan struct{}
}

func (c *ctxReader) Read(p []byte) (int, error) {
	<-c.done
	return 0, errors.New("cancelled")
}

func TestIdleReaderCancelsOnSilence(t *testing.T) {
	cancelled := make(chan struct{})
	reader := IdleReader(&ctxReader{done: cancelled}, 20*time.Millisecond, func() { close(cancelled) })
	errc := make(chan error, 1)
	go func() {
		_, err := reader.Read(make([]byte, 8))
		errc <- err
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("read returned before cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle deadline never cancelled the stream")
	}
}

func TestRunBudgetSingleSource(t *testing.T) {
	if got := RunBudget(); got != 30*time.Minute {
		t.Fatalf("default budget %v", got)
	}
	t.Setenv("HUB_RUN_BUDGET", "7m")
	if got := RunBudget(); got != 7*time.Minute {
		t.Fatalf("env budget %v", got)
	}
}

func TestStreamIdleDeadlineFromEnv(t *testing.T) {
	if got := StreamIdleDeadline(); got != time.Minute {
		t.Fatalf("default idle deadline %v", got)
	}
	t.Setenv("HUB_STREAM_IDLE", "5s")
	if got := StreamIdleDeadline(); got != 5*time.Second {
		t.Fatalf("env idle deadline %v", got)
	}
}

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
