package commops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identity"
)

// ControlClient is communication's service-only route to the ToolHub owner.
type ControlClient struct {
	URL         string
	Token       string
	Environment string
	Signer      contract.AutomationServiceSigner
}

type ControlError struct{ StatusCode int }

func (e ControlError) Error() string {
	return fmt.Sprintf("automation control status %d", e.StatusCode)
}

func (c ControlClient) Call(ctx context.Context, caller identity.Envelope, operation string, payload, out any) error {
	if c.URL == "" || (c.Signer == nil && c.Token == "") || !identity.ValidID(caller.PrincipalID) || operation == "" {
		return errors.New("automation control is not configured")
	}
	body, err := json.Marshal(map[string]any{"operation": operation, "caller": caller, "payload": payload})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/v1/automation/operations", bytes.NewReader(body))
	if err != nil {
		return err
	}
	token := c.Token
	if c.Signer != nil {
		token, err = c.Signer.IssueAutomationService(ctx, contract.ServiceRequest{
			Space: caller.ContextID, Environment: c.Environment, RequestDigest: contract.PolicyDigest(body),
		})
		if err != nil {
			return err
		}
	} else {
		req.Header.Set("X-Hub-Principal", caller.PrincipalID)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ControlError{StatusCode: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("automation control response exceeds limit")
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Result) == 0 {
		return errors.New("invalid automation control response")
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}
