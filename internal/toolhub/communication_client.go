package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func (g *Gateway) callCommunication(ctx context.Context, auth identity.Envelope, method, path string, payload, out any) int {
	if !identity.ValidID(auth.PrincipalID) {
		return 0
	}
	base, err := url.Parse(strings.TrimRight(g.CommunicationURL, "/"))
	if err != nil || base.Scheme != "http" && base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" {
		return 0
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, base.String()+path, body) // #nosec G704 -- configured communication address, fixed path
	if err != nil {
		return 0
	}
	correlation := api.CallCorrelationFrom(ctx)
	if delegated := correlation.Delegation; delegated != nil {
		if delegated.Communication == "" || delegated.RunID == "" || delegated.RunID != correlation.HermesRunID {
			return http.StatusUnauthorized
		}
		req.Header.Set(contract.RunTokenHeader, delegated.Communication)
		req.Header.Set(api.HeaderRunID, delegated.RunID)
	} else {
		if g.RunIdentity != nil {
			return http.StatusUnauthorized
		}
		if g.ControlToken == "" {
			return 0
		}
		req.Header.Set("Authorization", "Bearer "+g.ControlToken)
		req.Header.Set("X-Hub-Principal", auth.PrincipalID)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req) // #nosec G704 -- configured communication address
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if decoder.Decode(out) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return 0
	}
	return http.StatusOK
}
