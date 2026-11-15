package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// grantsRight consults the space's rights PDP for every models.* operation;
// unconfigured wiring keeps legacy auth-only behaviour.
func TestModelRelayGrantsRight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Operation string `json:"operation"`
			Scope     string `json:"scope"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{"allow": req.Operation == "use" && req.Scope == "gemini"})
	}))
	defer server.Close()
	r := &relayRegistry{owner: "alice", decider: api.DecideClient{URL: server.URL, Token: "tok", Principal: "alice"}}
	if !r.grantsRight(context.Background(), "use", "gemini") {
		t.Fatal("allowed models.use must pass")
	}
	if r.grantsRight(context.Background(), "switch", "gemini") {
		t.Fatal("denied models.switch must block")
	}
	r.decider.Token = ""
	if !r.grantsRight(context.Background(), "list", "") {
		t.Fatal("unconfigured decider keeps the legacy posture")
	}
}
