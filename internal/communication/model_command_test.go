package communication

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// /model switches the owner's logical model through the space's own relay:
// the admin bearer proves the gateway — never the chat text — is the caller.
func TestModelCommandSwitchesThroughRelay(t *testing.T) {
	var gotAuth, gotModel string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"default"},{"id":"turbo"}]}`)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		decodeBody(r, &body)
		gotModel = body.Model
		if body.Model == "turbo" {
			_, _ = io.WriteString(w, `{"model":"turbo"}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer relay.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", relay.URL)
	t.Setenv("HUB_MODEL_RELAY_ADMIN", "admintoken")

	out := modelCommand(context.Background(), "/model")
	if !strings.Contains(out, "default") || !strings.Contains(out, "turbo") {
		t.Fatalf("model list not shown: %q", out)
	}
	out = modelCommand(context.Background(), "/model turbo")
	if gotAuth != "Bearer admintoken" || gotModel != "turbo" {
		t.Fatalf("switch call malformed: auth=%q model=%q", gotAuth, gotModel)
	}
	if !strings.Contains(out, "turbo") {
		t.Fatalf("switch not confirmed: %q", out)
	}
	out = modelCommand(context.Background(), "/model secret-model")
	if !strings.Contains(strings.ToLower(out), "нет права") && !strings.Contains(out, "недоступн") {
		t.Fatalf("denied switch misreported: %q", out)
	}
}

func TestModelCommandWithoutRelay(t *testing.T) {
	t.Setenv("HUB_MODEL_RELAY_URL", "")
	out := modelCommand(context.Background(), "/model turbo")
	if !strings.Contains(out, "недоступн") {
		t.Fatalf("missing relay not reported: %q", out)
	}
}

func decodeBody(r *http.Request, v any) {
	defer r.Body.Close()
	_ = json.NewDecoder(r.Body).Decode(v)
}
