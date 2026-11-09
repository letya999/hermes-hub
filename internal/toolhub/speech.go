package toolhub

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func (g *Gateway) execSpeak(ctx context.Context, auth identity.Envelope, args map[string]any) (api.BackendResult, error) {
	corr := api.CallCorrelationFrom(ctx)
	conversation, err := g.liveRunConversation(ctx, auth, corr)
	if err != nil {
		return api.BackendResult{}, fmt.Errorf("%w: speech requires a live run", api.ErrBeforeEffect)
	}
	text := strings.TrimSpace(api.ArgString(args, "text"))
	if text == "" || len(text) > 16000 {
		return api.BackendResult{IsError: true, Text: "speech text must be 1..16000 bytes"}, nil
	}
	request := map[string]any{
		"job_id": corr.JobID, "conversation_id": conversation,
		"delivery_id": toolOperationID(auth.PrincipalID, auth.ContextID, auth.Environment, corr.JobID, corr.ToolCallID),
		"text":        text, "language": api.ArgString(args, "language"),
	}
	var out map[string]any
	status := g.callCommunication(ctx, auth, http.MethodPost, "/v1/media/speak", request, &out)
	if status == http.StatusOK {
		return api.BackendResult{Structured: out}, nil
	}
	if status == http.StatusBadRequest || status == http.StatusForbidden || status == http.StatusUnauthorized || status == http.StatusServiceUnavailable {
		return api.BackendResult{IsError: true, Text: fmt.Sprintf("speech delivery rejected (HTTP %d)", status)}, nil
	}
	// A lost acceptance response may already have queued this delivery.
	return api.BackendResult{}, fmt.Errorf("speech delivery acceptance is unknown")
}
