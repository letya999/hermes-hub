package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Only enrolled identity and published scope select the runtime; tool arguments never do.
func (g *Gateway) acquireAgentRuntime(ctx context.Context, effective api.EffectiveBinding) (func(), error) {
	endpoint, err := url.Parse(strings.TrimRight(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"), "/"))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Scheme != "http" && endpoint.Scheme != "https" || g.ControlToken == "" || g.Spawn == nil {
		return nil, api.ErrIsolation
	}
	auth, err := (&projectionEndpoint{gateway: g}).EnrolledAuth(effective.Binding.PrincipalID)
	if err != nil || auth.ContextID != effective.Binding.ContextID || auth.RuntimeID != effective.Binding.RuntimeID || auth.PolicyVersion != effective.Binding.PolicyVersion {
		return nil, api.ErrUnauthorized
	}
	eff, err := effectiveFor(ctx, g.Spawn, auth.PrincipalID)
	if err != nil || eff.Header.Metadata.Space != auth.PrincipalID || eff.Header.Metadata.Environment != auth.Environment || eff.Stack.Identity.User != auth.PrincipalID || auth.ContextID != auth.PrincipalID {
		return nil, api.ErrUnauthorized
	}
	scope := "user:"
	switch eff.Stack.Identity.Kind {
	case "", "user":
	case "group":
		scope = "group:"
	default:
		return nil, api.ErrUnauthorized
	}
	organization := eff.Stack.Identity.Organization
	if organization == "" {
		organization = "personal"
	}
	body, err := json.Marshal(struct {
		identity.Envelope
		UserID         string `json:"user_id"`
		ActorID        string `json:"actor_id"`
		ScopeID        string `json:"scope_id"`
		OrganizationID string `json:"organization_id"`
		Kind           string `json:"kind"`
	}{auth, auth.PrincipalID, auth.PrincipalID, scope + auth.PrincipalID, organization, "lifecycle"})
	if err != nil {
		return nil, api.ErrInvalid
	}
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String()+"/v1/leases", bytes.NewReader(body)) // #nosec G704 -- endpoint is the configured control-plane URL, not request input.
	if err != nil {
		return nil, api.ErrIsolation
	}
	req.Header.Set("Authorization", "Bearer "+g.ControlToken)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req) // #nosec G704 -- same fixed control-plane endpoint.
	if err != nil {
		return nil, api.ErrIsolation
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return nil, api.ErrIsolation
	}
	var result struct {
		Lease struct {
			ID, Kind   string
			Principal  string `json:"principal_id"`
			Context    string `json:"context_id"`
			Mode       string `json:"runtime_mode"`
			Generation string
		} `json:"lease"`
		Runtime struct {
			Principal             string `json:"principal_id"`
			Context               string `json:"context_id"`
			RuntimeID             string `json:"runtime_id"`
			Container, Generation string
		} `json:"runtime"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, api.ErrIsolation
	}
	wantContainer, err := api.ManagedAgentContainer(effective)
	lease, runtime := result.Lease, result.Runtime
	if err != nil || !identity.ValidID(lease.ID) || lease.Kind != "lifecycle" || lease.Principal != auth.PrincipalID || lease.Context != auth.ContextID || lease.Mode != "gateway" || lease.Generation == "" || lease.Generation != runtime.Generation || runtime.Principal != auth.PrincipalID || runtime.Context != auth.ContextID || runtime.RuntimeID != auth.RuntimeID || runtime.Container != wantContainer {
		return nil, api.ErrIsolation
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(cleanup, http.MethodDelete, endpoint.String()+"/v1/leases/"+url.PathEscape(lease.ID), nil) // #nosec G704 -- endpoint is the configured control-plane URL, not request input.
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+g.ControlToken)
			var response *http.Response
			response, err = client.Do(req) // #nosec G704 -- same fixed control-plane endpoint.
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNotFound {
					return
				}
			}
		}
		// The durable supervisor lease expires even if cleanup loses its acknowledgement.
		slog.Warn("agent runtime lease release failed", "principal_id", auth.PrincipalID, "error_class", "lease-release")
	}, nil
}
