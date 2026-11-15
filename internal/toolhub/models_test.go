package toolhub

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type modelTransport func(*http.Request) (*http.Response, error)

func (f modelTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestModelRelayOwnerRoutingAndSwitch(t *testing.T) {
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_MODEL_RELAY_URL", "http://model-relay-alice:8318")
	t.Setenv("HUB_MODEL_RELAY_ADMIN", "alice-admin")
	path := filepath.Join(t.TempDir(), "admins.json")
	if err := os.WriteFile(path, []byte(`{"bob":{"url":"http://model-relay-bob:8318","admin":"bob-admin"},"mallory":{"url":"http://model-relay-alice:8318","admin":"stolen"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MODEL_RELAY_ADMINS_FILE", path)
	var requests []string
	g := &Gateway{ModelHTTP: &http.Client{Transport: modelTransport(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.Method+" "+r.URL.Host+r.URL.Path)
		want := strings.TrimPrefix(r.URL.Hostname(), "model-relay-") + "-admin"
		if r.Header.Get("Authorization") != "Bearer "+want {
			t.Errorf("wrong owner bearer for %s", r.URL.Host)
		}
		body := `{"models":[{"name":"fast"}],"accounts":[],"switch":"catalog"}`
		if r.URL.Path == "/v1/model" {
			body = `{"model":"fast"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	for _, principal := range []string{"alice", "bob"} {
		out, err := g.execModel(context.Background(), identity.Envelope{PrincipalID: principal}, api.ToolSpec{Name: "model_list"}, nil)
		if err != nil || out.IsError || out.Structured == nil {
			t.Fatalf("%s list: %+v %v", principal, out, err)
		}
		out, err = g.execModel(context.Background(), identity.Envelope{PrincipalID: principal}, api.ToolSpec{Name: "model_switch"}, map[string]any{"name": "fast"})
		if err != nil || out.IsError {
			t.Fatalf("%s switch: %+v %v", principal, out, err)
		}
	}
	if len(requests) != 6 || requests[0] != "GET model-relay-alice:8318/v1/registry" || requests[3] != "GET model-relay-bob:8318/v1/registry" {
		t.Fatalf("unexpected routing: %v", requests)
	}
	out, err := g.execModel(context.Background(), identity.Envelope{PrincipalID: "mallory"}, api.ToolSpec{Name: "model_list"}, nil)
	if err != nil || !out.IsError || len(requests) != 6 {
		t.Fatalf("foreign relay routing must fail before HTTP: %+v %v", out, err)
	}
}
