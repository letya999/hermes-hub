package mediasvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpstreamReasonClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unauthorized", &upstreamStatusError{"upstream image", 401}, "provider_denied"},
		{"forbidden", &upstreamStatusError{"upstream image", 403}, "provider_denied"},
		{"rate limited", &upstreamStatusError{"fal image", 429}, "provider_rate_limited"},
		{"other 4xx", &upstreamStatusError{"ComfyUI", 404}, "provider_rejected"},
		{"5xx", &upstreamStatusError{"upstream image", 502}, "upstream_unavailable"},
		{"wrapped", fmt.Errorf("all media providers failed: %w", &upstreamStatusError{"upstream image", 403}), "provider_denied"},
		{"plain error", errors.New("dial tcp: connection refused"), "upstream_unavailable"},
		{"nil", nil, "upstream_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamReason(tc.err); got != tc.want {
				t.Fatalf("upstreamReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// A provider 403 must reach the client as a bounded reason token — the
// regression that hid a disabled-account denial behind a bare "image
// generation failed".
func TestImageGenerationErrorReasonContract(t *testing.T) {
	cases := []struct {
		status     int
		wantReason string
	}{
		{403, "provider_denied"},
		{429, "provider_rate_limited"},
		{400, "provider_rejected"},
		{503, "upstream_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.wantReason, func(t *testing.T) {
			s := newMediaService(t, &fakeGen{err: &upstreamStatusError{"upstream image", tc.status}})
			srv := httptest.NewServer(s.routes())
			defer srv.Close()
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"a cat"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			authReq(req)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", resp.StatusCode)
			}
			var body struct {
				Error struct {
					Message string `json:"message"`
					Reason  string `json:"reason"`
				} `json:"error"`
			}
			raw, _ := io.ReadAll(resp.Body)
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("error body must stay JSON: %v (%s)", err, raw)
			}
			if body.Error.Message != "image generation failed" || body.Error.Reason != tc.wantReason {
				t.Fatalf("error contract = %+v, want message+reason %q", body.Error, tc.wantReason)
			}
		})
	}
}

// Engine errors that are not typed upstream statuses — including text a
// provider embeds in its own body — must never reach the client. The
// response keeps the fixed message plus the unavailable token.
func TestImageGenerationErrorNeverLeaksProviderText(t *testing.T) {
	s := newMediaService(t, &fakeGen{err: errors.New("google upstream: account artemletyushev@gmail.com disabled for violation of Terms of Service")})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/images/generations", strings.NewReader(`{"prompt":"a cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	authReq(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(string(raw), "gmail") || strings.Contains(string(raw), "Terms of Service") {
		t.Fatalf("provider text leaked into the client response: %s", raw)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Reason  string `json:"reason"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("error body must stay JSON: %v", err)
	}
	if body.Error.Reason != "upstream_unavailable" {
		t.Fatalf("untyped provider error must classify as upstream_unavailable: %+v", body.Error)
	}
}

func TestSafeJobErrorBoundsProviderText(t *testing.T) {
	if got := safeJobError(&upstreamStatusError{"upstream image", 403}); got != "provider call failed: provider_denied" {
		t.Fatalf("upstream job error = %q", got)
	}
	if got := safeJobError(errSubmissionUncertain); got != errSubmissionUncertain.Error() {
		t.Fatalf("uncertain marker must keep its constant text: %q", got)
	}
	if got := safeJobError(errors.New("disk full")); got != "disk full" {
		t.Fatalf("internal errors keep their bounded text: %q", got)
	}
}
