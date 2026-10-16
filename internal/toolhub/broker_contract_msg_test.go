package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A self-install whose secrets no broker contract can deliver must fail with
// the uncovered credential names spelled out — "no reviewed contract covers
// <id>" tells neither the owner nor the agent what is actually missing.
func TestPrepareFailureNamesUncoveredCredentials(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/contracts" {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	control := asyncFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		definition := userMCPDefinition()
		definition.Credentials = []api.CredentialInput{
			{Name: "GOOGLE_OAUTH_CLIENT_ID", Required: true},
			{Name: "GOOGLE_OAUTH_CLIENT_SECRET", Required: true},
		}
		return api.SourceReview{Definition: definition, Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition), ReviewDigest: "sha256:review"}, nil
	})
	control.Broker = &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	notified := make(chan api.Onboarding, 1)
	control.PrepareDone = func(_ context.Context, _ identity.Envelope, onboarding api.Onboarding) { notified <- onboarding }

	body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "contract-miss"})
	if err != nil {
		// The reviewer answers inside the sync window, so the terminal
		// failure surfaces as the call error rather than a later wake.
		if !strings.Contains(err.Error(), "GOOGLE_OAUTH_CLIENT_ID") {
			t.Fatalf("failure hides the uncovered credentials: %v", err)
		}
		return
	}
	if body["phase"] != api.PhasePreparing {
		t.Fatalf("expected preparing, got %v", body)
	}
	var final api.Onboarding
	select {
	case final = <-notified:
	case <-time.After(5 * time.Second):
		t.Fatal("PrepareDone never fired")
	}
	if final.Phase != api.PhaseFailed {
		t.Fatalf("phase=%s", final.Phase)
	}
	if !strings.Contains(final.Error, "GOOGLE_OAUTH_CLIENT_ID") {
		t.Fatalf("failure hides the uncovered credentials: %q", final.Error)
	}
}
