package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamReadersBoundAndDiscardPrivateFields(t *testing.T) {
	var event nativeRunEvent
	if err := readNativeEvents(strings.NewReader(": keepalive\n\ndata: {\"event\":\"tool.start\",\"run_id\":\"r\",\"args\":{\"token\":\"private\"}}\n\n"), func(e nativeRunEvent) error { event = e; return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(event)
	if event.RunID != "r" || strings.Contains(string(b), "private") {
		t.Fatalf("event=%s", b)
	}
	for _, input := range []string{"data: invalid\n\n", "data: " + strings.Repeat("x", 70*1024) + "\n\n"} {
		if err := readNativeEvents(strings.NewReader(input), func(nativeRunEvent) error { return nil }); err == nil {
			t.Fatal("invalid native stream accepted")
		}
	}
	for _, input := range []string{"invalid\n", strings.Repeat("x", 3*maxPromptBytes) + "\n"} {
		if err := ReadRunStream(strings.NewReader(input), func(ExecuteResponse) error { return nil }); err == nil {
			t.Fatal("invalid normalized stream accepted")
		}
	}
	want := errors.New("consumer stopped")
	if err := ReadRunStream(strings.NewReader("{}\n"), func(ExecuteResponse) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if err := readNativeEvents(strings.NewReader("data: {}\n\n"), func(nativeRunEvent) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestCompletedRunWithoutReadableFinalRemainsRecoverable(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&runtimeHTTP{}).writeStream(recorder, ExecuteRequest{JobID: "recover-output"}, func(func(ExecuteResponse) error) (ExecuteResponse, error) {
		return ExecuteResponse{RunID: "original", SessionID: "session", Status: "completed", LastEvent: "run.completed"}, errors.New("private session transport failure")
	})
	var event ExecuteResponse
	if err := ReadRunStream(recorder.Body, func(e ExecuteResponse) error { event = e; return nil }); err != nil {
		t.Fatal(err)
	}
	if event.Status != "uncertain" || event.RunID != "original" || event.SessionID != "session" || strings.Contains(recorder.Body.String(), "private") {
		t.Fatalf("lost recovery identity or leaked error: %+v", event)
	}
}

func TestStalledNativeReceiverIsBoundedAndCancellationClosesStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 40 {
			_, _ = w.Write([]byte("data: {\"event\":\"tool.start\",\"run_id\":\"r\"}\n\n"))
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := nativeRunEvents(ctx, server.URL, "test-auth", "r")
	deadline := time.After(5 * time.Second)
	for len(events) != cap(events) {
		select {
		case <-deadline:
			t.Fatal("stream did not fill bounded queue")
		case <-time.After(time.Millisecond):
		}
	}
	if cap(events) != 8 {
		t.Fatalf("unbounded native queue: %d", cap(events))
	}
	cancel()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled stream goroutine did not stop")
		}
	}
}

func TestNativeSSEApprovalDedupProgressCapAndPrivatePayloads(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events") {
			w.Header().Set("Content-Type", "text/event-stream")
			for range 2 {
				_, _ = w.Write([]byte("data: {\"event\":\"approval.request\",\"run_id\":\"original\",\"request_id\":\"request-1\",\"choices\":[\"once\",\"deny\"],\"args\":\"PRIVATE_CANARY\"}\n\n"))
			}
			for range 15 {
				_, _ = w.Write([]byte("data: {\"event\":\"tool.started\",\"run_id\":\"original\",\"tool\":\"mcp__toolhub__prepare_source\",\"args\":\"PRIVATE_CANARY\"}\n\n"))
			}
			_, _ = w.Write([]byte("data: {\"event\":\"trace\",\"run_id\":\"original\",\"prompt\":\"PRIVATE_CANARY\"}\n\n"))
			return
		}
		state := "running"
		if polls.Add(1) >= 22 {
			state = "completed"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": state, "output": "answer"})
	}))
	defer server.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	progress, approvals := 0, 0
	result, err := (&runtimeHTTP{}).observeHermesRun(ctx, ExecuteResponse{RunID: "original", SessionID: "session", Status: "running", LastEvent: "run.admitted"}, func(event ExecuteResponse) error {
		if event.LastEvent == "approval.request" {
			approvals++
		}
		if event.LastEvent == "tool.start" {
			progress++
		}
		payload, _ := json.Marshal(event)
		if strings.Contains(string(payload), "PRIVATE_CANARY") {
			t.Error("private SSE payload projected")
		}
		return nil
	})
	if err != nil || result.Status != "completed" || progress != 10 || approvals != 1 {
		t.Fatalf("result=%+v progress=%d approvals=%d error=%v", result, progress, approvals, err)
	}
}

func TestToolHubProgressMessages(t *testing.T) {
	if got := toolProgressText(nativeRunEvent{Event: "tool.started", Tool: "mcp__toolhub__prepare_source"}); !strings.Contains(got, "шаг 1/4") || !strings.Contains(got, "2–15 минут") {
		t.Fatalf("prepare progress=%q", got)
	}
	if got := toolProgressText(nativeRunEvent{Event: "tool.started", Tool: "mcp__toolhub__enable"}); !strings.Contains(got, "шаг 4/4") || !strings.Contains(got, "без рестарта") {
		t.Fatalf("enable progress=%q", got)
	}
	if got := toolProgressText(nativeRunEvent{Event: "tool.started", Tool: "mcp_toolhub_prepare_source"}); !strings.Contains(got, "шаг 1/4") {
		t.Fatalf("Hermes tool-name progress=%q", got)
	}
	if got := toolProgressText(nativeRunEvent{Event: "tool.completed", Error: true}); !strings.Contains(got, "ошибкой") {
		t.Fatalf("error progress=%q", got)
	}
	for tool, want := range map[string]string{
		"mcp__toolhub__required_credentials": "шаг 2/4",
		"mcp__toolhub__confirm":              "шаг 3/4",
		"mcp__toolhub__invoke":               "реальный вызов",
		"other":                              "Выполняю запрос",
	} {
		if got := toolProgressText(nativeRunEvent{Event: "tool.started", Tool: tool}); !strings.Contains(got, want) {
			t.Fatalf("%s progress=%q", tool, got)
		}
	}
	if got := toolProgressText(nativeRunEvent{Event: "tool.completed"}); !strings.Contains(got, "завершён") {
		t.Fatalf("completed progress=%q", got)
	}
}

func TestPersistentStreamAdmissionApprovalAndTerminal(t *testing.T) {
	var polls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			w.WriteHeader(http.StatusCreated)
		case "/v1/runs":
			_, _ = w.Write([]byte(`{"run_id":"run-stream"}`))
		case "/v1/runs/run-stream":
			if polls.Add(1) < 3 {
				_, _ = w.Write([]byte(`{"status":"waiting_for_approval","approval":{"request_id":"approval-1","choices":["once","deny"],"command":"secret"}}`))
			} else {
				_, _ = w.Write([]byte(`{"status":"completed","output":"answer"}`))
			}
		case "/v1/runs/run-stream/events":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"event\":\"tool.start\",\"run_id\":\"run-stream\",\"args\":\"secret\"}\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_RUNTIME_GENERATION", "gen-1")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_RUNTIME_ID", "alice")
	t.Setenv("HUB_POLICY_VERSION", "policy-1")
	t.Setenv("HUB_PERSISTENT_HERMES", "true")
	request := validExecuteRequest("alice", "personal", "stream-key", "hello")
	request.JobID = "job-stream"
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(mustJSON(t, request)))
	req.Header.Set("Authorization", "Bearer runtime-secret")
	req.Header.Set("Accept", RunStreamContentType)
	rec := httptest.NewRecorder()
	runtimeHandler().ServeHTTP(rec, req)
	var frames []ExecuteResponse
	if err := ReadRunStream(rec.Body, func(e ExecuteResponse) error { frames = append(frames, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(frames) < 3 || frames[0].LastEvent != "run.admitted" || frames[len(frames)-1].Text != "answer" {
		t.Fatalf("frames=%+v", frames)
	}
	approvals := 0
	for _, frame := range frames {
		if frame.JobID != "job-stream" || frame.RuntimeGeneration != "gen-1" || frame.RunID != "run-stream" {
			t.Fatalf("identity=%+v", frame)
		}
		if frame.ApprovalID != "" {
			approvals++
		}
	}
	if approvals != 1 {
		t.Fatalf("approvals=%d", approvals)
	}
	// Disconnect after admission must preserve the run rather than claim cancellation.
	result, err := (&runtimeHTTP{}).executePersistentEvents(context.Background(), request, func(ExecuteResponse) error { return errors.New("disconnect") })
	if err == nil || result.Status != "uncertain" || result.RunID != "run-stream" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestObserveValidatesBindingAndNeverAdmitsNewRun(t *testing.T) {
	t.Setenv("HUB_RUNTIME_AUTH", "secret")
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_RUNTIME_ID", "alice")
	t.Setenv("HUB_POLICY_VERSION", "policy-1")
	t.Setenv("HUB_RUNTIME_GENERATION", "gen-2")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runs/original" && r.URL.Path != "/v1/runs/original/events" {
			t.Errorf("unexpected admission: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "events") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"interrupted"}`))
	}))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	request := validExecuteRequest("alice", "personal", "observe", "unused")
	request.JobID = "observe-job"
	ref := RunReference{ExecuteRequest: request, RunID: "original", SessionID: sessionIDFor(request), RuntimeGeneration: "gen-2"}
	call := func(ref RunReference, auth string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(ref)
		req := httptest.NewRequest(http.MethodPost, "/v1/observe", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+auth)
		rec := httptest.NewRecorder()
		runtimeHandler().ServeHTTP(rec, req)
		return rec
	}
	if rec := call(ref, "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatal(rec.Code)
	}
	for _, field := range []string{"generation", "run", "session", "conversation"} {
		bad := ref
		switch field {
		case "generation":
			bad.RuntimeGeneration = "gen-1"
		case "run":
			bad.RunID = "../other"
		case "session":
			bad.SessionID = "another"
		case "conversation":
			bad.ConversationID = "another"
		}
		if rec := call(bad, "secret"); rec.Code != http.StatusConflict {
			t.Fatalf("%s=%d", field, rec.Code)
		}
	}
	rec := call(ref, "secret")
	var last ExecuteResponse
	if err := ReadRunStream(rec.Body, func(event ExecuteResponse) error { last = event; return nil }); err != nil {
		t.Fatal(err)
	}
	if last.Status != "interrupted" || last.RuntimeGeneration != "gen-2" || last.RunID != "original" {
		t.Fatalf("last=%+v", last)
	}
	if validHermesRunID("") || validHermesRunID(strings.Repeat("r", 257)) {
		t.Fatal("unbounded run ID accepted")
	}
}

func TestNativeEventFailureFallsBackAndTotalOutputIsBounded(t *testing.T) {
	for _, kind := range []string{"auth", "content"} {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if kind == "auth" {
				w.WriteHeader(http.StatusUnauthorized)
			} else {
				w.Header().Set("Content-Type", "application/json")
			}
		}))
		if _, ok := <-nativeRunEvents(context.Background(), api.URL, "secret", "original"); ok {
			t.Fatal("invalid upstream stream projected events")
		}
		api.Close()
	}
	event := ExecuteResponse{Text: strings.Repeat("x", maxPromptBytes-100)}
	b, _ := json.Marshal(event)
	input := strings.Repeat(string(b)+"\n", 5)
	if err := ReadRunStream(strings.NewReader(input), func(ExecuteResponse) error { return nil }); err == nil {
		t.Fatal("unbounded total stream accepted")
	}
}

func TestStopAcknowledgementIsNotTerminalCompletion(t *testing.T) {
	for _, state := range []string{"stopping", "cancelled", "completed"} {
		t.Run(state, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					_, _ = w.Write([]byte(`{"status":"stopping"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"status": state, "output": "result"})
			}))
			defer api.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			result, err := stopAndConfirmHermesRun(ctx, api.Client(), api.URL, "secret", ExecuteResponse{SessionID: "s", RunID: "original"})
			if state == "stopping" {
				if err == nil || result.Status != "uncertain" {
					t.Fatalf("false completion: %+v err=%v", result, err)
				}
			} else if err != nil || result.Status != state || result.RunID != "original" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestObservationRejectsWrongRunSessionAndFailedApprovalHandoff(t *testing.T) {
	for _, mode := range []string{"run", "session", "approval"} {
		t.Run(mode, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/events") {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"event\":\"tool.start\",\"run_id\":\"another-run\"}\n\n"))
					return
				}
				switch mode {
				case "session":
					_, _ = w.Write([]byte(`{"status":"running","session_id":"another-session"}`))
				case "approval":
					_, _ = w.Write([]byte(`{"status":"waiting_for_approval","approval":{"request_id":"a","choices":["once","deny"]}}`))
				default:
					_, _ = w.Write([]byte(`{"status":"running"}`))
				}
			}))
			defer api.Close()
			host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
			t.Setenv("HUB_HERMES_API_HOST", host)
			t.Setenv("HUB_HERMES_API_PORT", port)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls := 0
			result, err := (&runtimeHTTP{}).observeHermesRun(ctx, ExecuteResponse{RunID: "original", SessionID: "s", Status: "running"}, func(event ExecuteResponse) error {
				calls++
				if mode == "approval" && calls > 1 {
					return errors.New("approval delivery unavailable")
				}
				return nil
			})
			if err == nil || result.Status == "completed" || result.Status == "cancelled" {
				t.Fatalf("unsafe observation settled: %+v err=%v", result, err)
			}
			want := map[string]string{"run": "another run", "session": "session mismatch", "approval": "approval delivery unavailable"}[mode]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("expected boundary failure %q, got %v", want, err)
			}
		})
	}
}
