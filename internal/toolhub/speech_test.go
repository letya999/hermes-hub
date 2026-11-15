package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestSpeakUsesLiveRunAndPreservesUnknownAcceptance(t *testing.T) {
	auth := identity.TelegramEnvelope("alice", 11, "runtime", "policy-1")
	auth.Environment = "dev"
	record := privateRunRecord(auth, "speech-job", "run-1")
	record.Envelope.ConversationID = "telegram-11-task"
	corr := api.HeaderCorrelation("speech-job", "run-1")
	corr.ToolCallID = "call-speech"
	ctx := api.WithCallCorrelation(t.Context(), corr)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in map[string]string
		if json.NewDecoder(r.Body).Decode(&in) != nil || r.URL.Path != "/v1/media/speak" || r.Header.Get("X-Hub-Principal") != "alice" || in["conversation_id"] != "telegram-11-task" || in["delivery_id"] == "" || in["text"] != "hello" {
			t.Error("lost trusted routing or speech")
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	g := &Gateway{CommunicationURL: server.URL, ControlToken: "service", Runs: runAuthorityFunc(func(context.Context, string) (api.RunContext, error) { return record, nil })}
	if _, err := g.execSpeak(ctx, auth, map[string]any{"text": "hello"}); err == nil || errors.Is(err, api.ErrBeforeEffect) {
		t.Fatalf("unknown acceptance reported as known failure: %v", err)
	}
	record.Status = "done"
	if _, err := g.execSpeak(ctx, auth, map[string]any{"text": "hello"}); !errors.Is(err, api.ErrBeforeEffect) {
		t.Fatalf("finished run accepted: %v", err)
	}
	if calls != 1 {
		t.Fatalf("finished run reached communication: %d", calls)
	}
}
