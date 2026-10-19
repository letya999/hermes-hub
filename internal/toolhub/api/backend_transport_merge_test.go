package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func stubResponse() *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}
}

func TestBackendRoundTrippers(t *testing.T) {
	var seen *http.Request
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r
		return stubResponse(), nil
	})
	req, _ := http.NewRequest("POST", "https://mcp.example.io/x", nil)

	// legacy transport pins the protocol version on a cloned request
	if _, err := (LegacyMCPRoundTripper{Base: base}).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if seen.Header.Get("MCP-Protocol-Version") != LegacyMCPVersion {
		t.Fatalf("protocol header: %v", seen.Header)
	}
	if req.Header.Get("MCP-Protocol-Version") != "" {
		t.Fatal("original request mutated")
	}
	// bearer token rides only the clone; empty token adds no header
	if _, err := (bearerRoundTripper{Base: base, token: "tok"}).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if seen.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("auth: %v", seen.Header)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("original request mutated")
	}
	seen = nil
	if _, err := (bearerRoundTripper{Base: base}).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if seen.Header.Get("Authorization") != "" {
		t.Fatal("empty token added a header")
	}
}

func TestMCPBackendAdmitGuards(t *testing.T) {
	b := MCPBackend{}
	eff := EffectiveBinding{Definition: ToolDefinition{Transport: AgentTools}}
	// no verifier and no admission function: isolation failure
	if _, err := b.Admit(t.Context(), eff, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("no verifier: %v", err)
	}
	// legacy admission without a verifier returns no proof — still an isolation failure
	b.Admission = func(context.Context, EffectiveBinding) error { return nil }
	if _, err := b.Admit(t.Context(), eff, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("proofless admission: %v", err)
	}
	// an invalid container definition is rejected before any controller call
	bad := eff
	bad.Definition.Transport = ContainerMCP
	if _, err := b.Admit(t.Context(), bad, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid definition: %v", err)
	}
	// a rejected verifier receipt releases the workload before returning;
	// the release rides the binding's workload id, not the receipt's
	released := false
	b2 := MCPBackend{
		AdmissionVerifier: func(context.Context, EffectiveBinding) (AdmissionReceipt, error) {
			return AdmissionReceipt{WorkloadID: "wl-1"}, nil
		},
		AdmissionRelease: func(_ context.Context, _ string) error { released = true; return nil },
	}
	if _, err := b2.Admit(t.Context(), eff, nil); err == nil || !released {
		t.Fatalf("receipt rejection: %v released=%v", err, released)
	}
}
