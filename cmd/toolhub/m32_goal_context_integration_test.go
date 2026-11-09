//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/compaction"
	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/runtime"
)

func TestM32ContextPreviewApplyAndContinue(t *testing.T) {
	image, script := os.Getenv("HUB_M32_IMAGE"), os.Getenv("HUB_M32_SCRIPT_IMAGE")
	if image == "" || script == "" {
		t.Skip("set HUB_M32_IMAGE and HUB_M32_SCRIPT_IMAGE for isolated container acceptance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	holdCompact, holdContinue := make(chan struct{}), make(chan struct{})
	enteredCompact, enteredContinue := make(chan struct{}, 1), make(chan struct{}, 1)
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	m32Require(t, err)
	provider := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream   bool            `json:"stream"`
			Messages json.RawMessage `json:"messages"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&request) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		messages := string(request.Messages)
		if strings.Contains(messages, "M32 context continue") {
			select {
			case enteredContinue <- struct{}{}:
			default:
			}
			select {
			case <-holdContinue:
			case <-r.Context().Done():
				return
			}
		} else if strings.Contains(messages, "M32 context compact") {
			select {
			case enteredCompact <- struct{}{}:
			default:
			}
			select {
			case <-holdCompact:
			case <-r.Context().Done():
				return
			}
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"m32\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"M32 acceptance complete\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"m32","object":"chat.completion","model":"m32-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"M32 acceptance complete"},"finish_reason":"stop"}]}`)
		}
	}))
	provider.Listener = listener
	provider.Start()
	t.Cleanup(func() {
		select {
		case <-holdCompact:
		default:
			close(holdCompact)
		}
		select {
		case <-holdContinue:
		default:
			close(holdContinue)
		}
		provider.CloseClientConnections()
		provider.Close()
	})
	port := provider.Listener.Addr().(*net.TCPAddr).Port
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	f, err := devcheck.NewM32Fixture(t.TempDir(), "dev", fmt.Sprintf("http://host.docker.internal:%d/v1", port), suffix)
	m32Require(t, err)
	h := newM32Lifecycle(t, ctx, image, "dev", f)
	stop := h.startToolHub(t, ctx)
	defer stop()
	user := f.Users[0]
	job := func(id, text string) runtime.ExecuteRequest {
		return runtime.ExecuteRequest{Envelope: f.Auth[user], JobID: id, IdempotencyKey: id, OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", Trigger: "message", Text: text}
	}
	seed, err := h.execute(ctx, job("m32-context-seed", strings.Repeat("M32 synthetic history. ", 120)))
	m32Require(t, err)
	if seed.Status != "completed" {
		t.Fatalf("seed run status=%s event=%s", seed.Status, seed.LastEvent)
	}
	outcome := make(chan runtime.ExecuteResponse, 1)
	failures := make(chan error, 1)
	go func() {
		result, err := h.execute(ctx, job("m32-context-compact", "M32 context compact"))
		outcome <- result
		failures <- err
	}()
	select {
	case <-enteredCompact:
	case <-ctx.Done():
		t.Fatal("compact run never entered provider")
	}
	run := h.waitRun(t, ctx, "m32-context-compact")
	if run.Status != "running" || run.SessionID == "" {
		t.Fatal("compact run has no active session")
	}
	before := m32ContextObject(t, h.call(t, ctx, user, run, "context_status", map[string]any{"id": run.SessionID}, true), "hub.context_status")
	baseline := before["compactions"]
	if before["tokens"] == nil {
		t.Fatal("isolated transcript has no token estimate")
	}
	preview := m32ContextObject(t, h.call(t, ctx, user, run, "context_edit", map[string]any{"action": "compact", "id": run.SessionID, "here": 1, "preview": true}, true), "hub.context_compact")
	if preview["applied"] != false {
		t.Fatal("preview mutated the session")
	}
	afterPreview := m32ContextObject(t, h.call(t, ctx, user, run, "context_status", map[string]any{"id": run.SessionID}, true), "hub.context_status")
	if afterPreview["compactions"] != baseline || afterPreview["tokens"] != before["tokens"] || !reflect.DeepEqual(afterPreview["last_event"], before["last_event"]) {
		t.Fatal("preview changed context status")
	}
	queued := m32ContextObject(t, h.call(t, ctx, user, run, "context_edit", map[string]any{"action": "compact", "id": run.SessionID, "here": 1, "queue": true}, true), "hub.context_compact")
	if queued["failure_class"] != "queued" {
		t.Fatal("active-turn compaction was not queued")
	}
	close(holdCompact)
	m32Require(t, <-failures)
	if result := <-outcome; result.Status != "completed" {
		t.Fatalf("compact run status=%s event=%s", result.Status, result.LastEvent)
	}
	status, ok, err := h.runtimeStatus(user)
	m32Require(t, err)
	if !ok {
		t.Fatal("managed runtime missing after compaction")
	}
	mounts := h.docker(t, ctx, "inspect", "--format", "{{json .Mounts}}", status.Container)
	var mounted []struct{ Source, Destination string }
	m32Require(t, json.Unmarshal(mounts, &mounted))
	stateDir := ""
	for _, mount := range mounted {
		if mount.Destination == "/state" {
			stateDir = mount.Source
		}
	}
	if stateDir == "" {
		t.Fatal("isolated state mount missing")
	}
	journal, err := compaction.Open(filepath.Clean(stateDir))
	m32Require(t, err)
	var events []compaction.Event
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		events, err = journal.Events(run.SessionID)
		m32Require(t, err)
		if len(events) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(events) != 1 || events[0].Trigger != compaction.TriggerManual || events[0].FailureClass != "" {
		t.Fatalf("compaction journal count=%d trigger_manual=%t", len(events), len(events) > 0 && events[0].Trigger == compaction.TriggerManual)
	}
	go func() {
		result, err := h.execute(ctx, job("m32-context-continue", "M32 context continue"))
		outcome <- result
		failures <- err
	}()
	select {
	case <-enteredContinue:
	case <-ctx.Done():
		t.Fatal("continuation never reached provider")
	}
	continued := h.waitRun(t, ctx, "m32-context-continue")
	if continued.Status != "running" || continued.SessionID != run.SessionID {
		t.Fatal("continuation switched session")
	}
	final := m32ContextObject(t, h.call(t, ctx, user, continued, "context_status", map[string]any{"id": run.SessionID}, true), "hub.context_status")
	if final["compactions"] != float64(1) || final["last_event"] == nil {
		t.Fatal("journaled compaction absent from ToolHub status")
	}
	close(holdContinue)
	m32Require(t, <-failures)
	if result := <-outcome; result.Status != "completed" {
		t.Fatalf("continuation status=%s event=%s", result.Status, result.LastEvent)
	}
	t.Logf("PASS context: preview applied=false and count unchanged; queued active turn; manual journal count=1; ToolHub count=1; continuation completed")
}

func m32ContextObject(t *testing.T, raw, object string) map[string]any {
	t.Helper()
	var tree any
	m32Require(t, json.Unmarshal([]byte(raw), &tree))
	var walk func(any) map[string]any
	walk = func(v any) map[string]any {
		switch x := v.(type) {
		case map[string]any:
			if x["object"] == object {
				return x
			}
			for _, child := range x {
				if found := walk(child); found != nil {
					return found
				}
			}
		case []any:
			for _, child := range x {
				if found := walk(child); found != nil {
					return found
				}
			}
		}
		return nil
	}
	result := walk(tree)
	if result == nil {
		t.Fatalf("ToolHub result omitted %s", object)
	}
	return result
}
