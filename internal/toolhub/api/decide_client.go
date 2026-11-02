package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DecideClient is the out-of-process PEP half of the rights PDP
// (SPEC-0092 R2.2): services that face the agent directly — supervisor,
// model-relay, communication-hub — ask ToolHub's /v1/rights/decide before
// acting. The client authenticates with the control-plane bearer and
// asserts the space principal ToolHub pins for the deployment; the verdict
// is vocabulary ∩ published ceiling ∩ live ledger.
type DecideClient struct {
	// URL is the ToolHub control address (http://toolhub:8090).
	URL string
	// Token is the control-plane bearer the route trusts for asserted
	// principals (HUB_COMMUNICATION_AUTH on ToolHub's side).
	Token string
	// Principal is the space user this service asserts; ToolHub refuses
	// anything else under its SpacePrincipal pin.
	Principal string
	// Environment narrows the ceiling profile when the space runs dev+prod.
	Environment string
	// HTTP allows tests to stub the transport; nil uses a short-timeout
	// default client.
	HTTP *http.Client
}

// Enabled reports whether the client is configured — an empty URL or token
// leaves the service in the legacy posture where the PEP is absent.
func (c DecideClient) Enabled() bool {
	return c.URL != "" && c.Token != "" && c.Principal != ""
}

// GrantsRight is the PEP gate: true only when the PDP explicitly allows.
// Errors are returned so callers can log — the caller's contract is deny on
// !allow, never fail-open on a PDP outage.
func (c DecideClient) GrantsRight(ctx context.Context, capability, operation, scope string) (bool, error) {
	if !c.Enabled() {
		return false, fmt.Errorf("rights decider unconfigured")
	}
	body, err := json.Marshal(map[string]string{
		"principal_id": c.Principal, "environment": c.Environment,
		"capability": capability, "operation": operation, "scope": scope,
	})
	if err != nil {
		return false, err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/rights/decide", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("rights decide: status %d", resp.StatusCode)
	}
	var out RightDecision
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Allow, nil
}
