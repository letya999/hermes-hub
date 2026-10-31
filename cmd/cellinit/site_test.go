package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/hosting"
)

type relayTransport func(*http.Request) (*http.Response, error)

func (f relayTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSiteRelayFixedDestinationAndHeaderBoundary(t *testing.T) {
	in := hosting.RelayRequest{Method: "POST", Path: "/api?x=1", Header: http.Header{"Authorization": []string{"forbidden"}, "X-Forwarded-Host": []string{"foreign"}, "Content-Type": []string{"application/json"}}, Body: []byte("request")}
	payload, _ := json.Marshal(in)
	var output bytes.Buffer
	transport := relayTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://127.0.0.1:8080/api?x=1" || r.Method != "POST" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Forwarded-Host") != "" {
			t.Fatal("relay destination/header boundary")
		}
		return &http.Response{StatusCode: 201, Header: http.Header{"Content-Type": []string{"text/plain"}, "X-Private": []string{"not-forwarded"}}, Body: io.NopCloser(strings.NewReader("reply"))}, nil
	})
	if err := siteRelayTransport(bytes.NewReader(payload), &output, transport); err != nil {
		t.Fatal(err)
	}
	var out hosting.RelayResponse
	if json.Unmarshal(output.Bytes(), &out) != nil || out.Status != 201 || string(out.Body) != "reply" || out.Header.Get("X-Private") != "" {
		t.Fatal("relay response contract")
	}
	for _, bad := range []string{`{}`, `{"method":"CONNECT","path":"/"}`, `{"method":"GET","path":"http://foreign.test/"}`, `{"method":"GET","path":"/","target":"foreign"}`, `{"method":"GET","path":"/"} {}`} {
		if err := siteRelayTransport(strings.NewReader(bad), io.Discard, relayTransport(func(*http.Request) (*http.Response, error) { t.Fatal("invalid relay reached network"); return nil, nil })); err == nil {
			t.Fatal("invalid relay accepted")
		}
	}
}
func TestSiteRelayBoundsAndSafeFailure(t *testing.T) {
	payload := `{"method":"GET","path":"/"}`
	err := siteRelayTransport(strings.NewReader(payload), io.Discard, relayTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("synthetic private detail") }))
	if err == nil || strings.Contains(err.Error(), "synthetic private") {
		t.Fatal("raw error exposed")
	}
	err = siteRelayTransport(strings.NewReader(payload), io.Discard, relayTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (8<<20)+1)))}, nil
	}))
	if err == nil {
		t.Fatal("unbounded response")
	}
	if err = siteRelay(strings.NewReader("invalid"), io.Discard); err == nil {
		t.Fatal("invalid relay input")
	}
}
func TestSiteGoRejectsUnboundedLifetimeBeforeSpawn(t *testing.T) {
	for _, args := range [][]string{nil, {"invalid"}, {"0"}, {strconv.FormatInt(time.Now().Add(31*24*time.Hour).Unix(), 10)}} {
		if err := siteGo(args); err == nil {
			t.Fatal("unbounded lifetime accepted")
		}
	}
	if code := run([]string{"cellinit", "site-go"}); code == 0 {
		t.Fatal("missing lifetime accepted through CLI")
	}
}
