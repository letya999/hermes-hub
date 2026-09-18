package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

type toolHubReconnectMarker struct {
	Revision uint64 `json:"revision"`
}

// startToolHubReconnectWatcher keeps Hermes' MCP tool surface current without
// restarting the runtime process. The marker is the durable hand-off from
// ToolHub; Hermes' native /reload-mcp owns transport teardown, discovery and
// cached-agent refresh.
func startToolHubReconnectWatcher(ctx context.Context, stateDir string) {
	if toolHubEndpoint() == "" || strings.EqualFold(strings.TrimSpace(os.Getenv("HUB_TOOLHUB_RECONNECT")), "false") {
		return
	}
	go watchToolHubReconnect(ctx, stateDir)
}

func watchToolHubReconnect(ctx context.Context, stateDir string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var handled uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			change, err := readToolHubReconnectMarker(stateDir)
			if err != nil || change.Revision <= handled {
				continue
			}
			if err := requestHermesMCPReload(ctx, change.Revision); err != nil {
				// Keep the revision pending. A cold Hermes startup or a transient
				// API failure must not lose the reconnect request.
				fmt.Fprintf(os.Stderr, "ToolHub MCP reconnect revision %d pending: %v\n", change.Revision, err)
				continue
			}
			handled = change.Revision
		}
	}
}

func readToolHubReconnectMarker(stateDir string) (toolHubReconnectMarker, error) {
	body, err := os.ReadFile(filepath.Join(stateDir, "toolhub-reconnect.request"))
	if err != nil {
		return toolHubReconnectMarker{}, err
	}
	var marker toolHubReconnectMarker
	if err := json.Unmarshal(body, &marker); err != nil || marker.Revision == 0 {
		return toolHubReconnectMarker{}, errors.New("invalid ToolHub reconnect marker")
	}
	return marker, nil
}

func requestHermesMCPReload(ctx context.Context, revision uint64) error {
	if revision == 0 {
		return errors.New("invalid ToolHub reconnect revision")
	}
	principal := os.Getenv("HUB_PRINCIPAL_ID")
	if principal == "" {
		principal = os.Getenv("HUB_USER_ID")
	}
	if principal == "" {
		return errors.New("runtime principal is unavailable")
	}
	contextID := os.Getenv("HUB_CONTEXT_ID")
	if contextID == "" {
		contextID = principal
	}
	runtimeID := os.Getenv("HUB_RUNTIME_ID")
	if runtimeID == "" {
		runtimeID = principal
	}
	policy := os.Getenv("HUB_POLICY_VERSION")
	if policy == "" {
		policy = "policy-1"
	}
	conversation := os.Getenv("HUB_CONVERSATION_ID")
	if conversation == "" {
		conversation = "toolhub"
	}
	if err := (identity.Envelope{Schema: identity.Schema, PrincipalID: principal, ExternalIdentityID: principal, ContextID: contextID, RuntimeID: runtimeID, ConversationID: conversation, DeliveryTargetID: conversation, PolicyVersion: policy}).Validate(principal, contextID, runtimeID, policy); err != nil {
		return fmt.Errorf("runtime identity: %w", err)
	}
	reconnectSession := sessionIDFor(ExecuteRequest{Envelope: identity.Envelope{ContextID: contextID, ConversationID: conversation}})
	request := ExecuteRequest{
		Envelope:       identity.Envelope{Schema: identity.Schema, PrincipalID: principal, ExternalIdentityID: principal, ContextID: contextID, RuntimeID: runtimeID, ConversationID: conversation, DeliveryTargetID: conversation, PolicyVersion: policy},
		JobID:          "toolhub-reconnect-" + strconv.FormatUint(revision, 10),
		OrganizationID: os.Getenv("HUB_ORGANIZATION_ID"),
		UserID:         principal,
		ActorID:        principal,
		ScopeID:        "user:" + principal,
		Channel:        "toolhub",
		Trigger:        "projection",
		// The marker revision is durable across runtime generations. Bind the
		// idempotency key to the session too, otherwise a changed SOUL/config
		// reuses Hermes' old key with a new session and is rejected with 409.
		IdempotencyKey: "toolhub-reconnect-" + strconv.FormatUint(revision, 10) + "-" + strings.TrimPrefix(reconnectSession, "hub-"),
		Text:           "/reload-mcp",
	}
	base := "http://" + env("HUB_HERMES_API_HOST", "127.0.0.1") + ":" + env("HUB_HERMES_API_PORT", "8642")
	return requestHermesMCPReloadAt(ctx, base, env("API_SERVER_KEY", os.Getenv("HUB_RUNTIME_AUTH")), request)
}

func requestHermesMCPReloadAt(ctx context.Context, base, auth string, request ExecuteRequest) error {
	client := &http.Client{Timeout: 10 * time.Second}
	sessionID := sessionIDFor(request)
	if err := hermesRequest(ctx, client, http.MethodPost, base+"/api/sessions", auth, map[string]any{"id": sessionID}, nil); err != nil && !errors.Is(err, errSessionExists) {
		return fmt.Errorf("hermes session: %w", err)
	}
	var admission struct {
		RunID string `json:"run_id"`
	}
	if err := hermesRequest(ctx, client, http.MethodPost, base+"/v1/runs", auth, map[string]any{"input": request.Text, "session_id": sessionID}, &admission, request.IdempotencyKey); err != nil {
		return fmt.Errorf("hermes reload admission: %w", err)
	}
	if strings.TrimSpace(admission.RunID) == "" {
		return errors.New("hermes reload returned no run ID")
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var status struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := hermesRequest(ctx, client, http.MethodGet, base+"/v1/runs/"+admission.RunID, auth, nil, &status); err != nil {
			return fmt.Errorf("hermes reload status: %w", err)
		}
		switch status.Status {
		case "completed":
			return nil
		case "failed", "cancelled", "interrupted":
			if status.Error != "" {
				return errors.New("hermes MCP reload failed")
			}
			return fmt.Errorf("hermes MCP reload ended: %s", status.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.New("hermes MCP reload timed out")
}
