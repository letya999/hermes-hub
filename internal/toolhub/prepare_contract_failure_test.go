package toolhub

import (
	"bytes"
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

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestPreparePostPersistBrokerFailureRevokesForm(t *testing.T) {
	reviewed, ok, err := api.PreparedContractFor("notion-token", 3)
	if err != nil || !ok {
		t.Fatalf("reviewed contract: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/contracts":
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode([]any{reviewed})
				return
			}
			_ = json.NewEncoder(w).Encode(reviewed)
		case "/v1/requests":
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(brokerv1.Error{Code: "conflict"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0o600); err != nil {
		t.Fatal(err)
	}
	definition := userMCPDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "NOTION_TOKEN", Required: true}}
	definition.CredentialContractID = reviewed.ID
	definition.CredentialContractRevision = reviewed.Revision
	definition.CredentialContractEnv = map[string]string{"NOTION_TOKEN": "NOTION_TOKEN"}
	control := asyncFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		return api.SourceReview{Definition: definition}, nil
	})
	control.PrepareSyncWindow = -1
	control.Broker = &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	_, err = control.Invoke(t.Context(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "post-persist-failure"})
	if err == nil {
		t.Fatal("broker rejection accepted")
	}
	onboarding, found := control.Store.FindOnboardingByKey(aliceAuth(), "post-persist-failure")
	if !found || onboarding.Phase != api.PhaseFailed || onboarding.FormNonce != "" || !onboarding.FormExpires.IsZero() || onboarding.Error == "" {
		t.Fatalf("failed onboarding retains live form: phase=%s nonce=%t expiry=%v error=%q", onboarding.Phase, onboarding.FormNonce != "", onboarding.FormExpires, onboarding.Error)
	}
	if _, err := control.Invoke(t.Context(), aliceAuth(), "status", map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "tokens.enc"), Key: bytes.Repeat([]byte("k"), 32)})
	if err != nil {
		t.Fatal(err)
	}
	control.Secrets = secrets
	if err := control.SubmitCredentials(onboarding.OnboardingID, "old-nonce", map[string]string{"NOTION_TOKEN": "value"}); err == nil {
		t.Fatal("failed onboarding accepted its stale form")
	}
}

func TestBindPreparedDigestConflictIsExplicit(t *testing.T) {
	reviewed, ok, err := api.PreparedContractFor("notion-token", 3)
	if err != nil || !ok {
		t.Fatal(err)
	}
	reviewed.Title += " drift"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/contracts" {
			_ = json.NewEncoder(w).Encode([]any{reviewed})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0o600); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	definition := userMCPDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "NOTION_TOKEN", Required: true}}
	definition.CredentialContractID = reviewed.ID
	definition.CredentialContractRevision = reviewed.Revision
	definition.CredentialContractEnv = map[string]string{"NOTION_TOKEN": "NOTION_TOKEN"}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err == nil || !strings.Contains(err.Error(), "contract revision conflict") {
		t.Fatalf("digest conflict hidden: %v", err)
	}
}

func TestBindPreparedRegistrationConflictIsExplicit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/contracts" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		if r.URL.Path == "/v1/contracts" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(brokerv1.Error{Code: "contract_conflict"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0o600); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	definition := userMCPDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "NOTION_TOKEN", Required: true}}
	definition.CredentialContractID = "notion-token"
	definition.CredentialContractRevision = 3
	definition.CredentialContractEnv = map[string]string{"NOTION_TOKEN": "NOTION_TOKEN"}
	if err := control.bindReviewedContract(t.Context(), aliceAuth(), &definition); err == nil || !strings.Contains(err.Error(), "contract revision conflict") {
		t.Fatalf("registration conflict hidden: %v", err)
	}
}
