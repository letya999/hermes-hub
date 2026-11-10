package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDecideClientAllowsSlowAuthorizedDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(6 * time.Second)
		_ = json.NewEncoder(w).Encode(RightDecision{Allow: true})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	allowed, err := (DecideClient{URL: server.URL, Token: "test-token", Principal: "alice"}).GrantsRight(ctx, "models", "use", "default")
	if err != nil || !allowed {
		t.Fatalf("slow PDP answer was denied: allow=%t err=%v", allowed, err)
	}
}
