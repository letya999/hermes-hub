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
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// dynamicContractBroker is a fake broker that accepts dynamic contract
// registration and mints enrollment requests.
func dynamicContractBroker(t *testing.T) (*httptest.Server, *[]contract.Contract) {
	t.Helper()
	registered := &[]contract.Contract{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode(*registered)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var in contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			in.Storage = "local"
			*registered = append(*registered, in)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(in)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			var input brokerv1.CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: input.ContractID, ContractRevision: input.ContractRevision, ConnectionID: input.ConnectionID, Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_new"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_new":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_new"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, registered
}

// A self-install whose secrets no catalog contract can deliver is healed by a
// synthesized contract: the prepared onboarding must reach the credential form
// with the declared names carried by the registered contract fields.
func TestPrepareRegistersContractForUncoveredCredentials(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server, registered := dynamicContractBroker(t)

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
		t.Fatalf("synthesized contract must not fail the prepare: %v", err)
	}
	phase, _ := body["phase"].(string)
	if phase == api.PhasePreparing {
		var final api.Onboarding
		select {
		case final = <-notified:
		case <-time.After(5 * time.Second):
			t.Fatal("PrepareDone never fired")
		}
		phase = final.Phase
		if final.BrokerAuthorizationURL == "" {
			t.Fatalf("no credential form minted: %+v", final)
		}
	} else if body["authorization_url"] == "" {
		t.Fatalf("no credential form minted: %v", body)
	}
	if phase != api.PhaseAwaitingCreds {
		t.Fatalf("expected awaiting-credentials, got %s", phase)
	}
	if len(*registered) != 1 {
		t.Fatalf("expected one dynamic contract, got %+v", *registered)
	}
	labels := map[string]bool{}
	for _, f := range (*registered)[0].Fields {
		labels[f.Label] = true
	}
	if !labels["GOOGLE_OAUTH_CLIENT_ID"] || !labels["GOOGLE_OAUTH_CLIENT_SECRET"] {
		t.Fatalf("registered contract misses the credential names: %+v", (*registered)[0].Fields)
	}
}

// Optional-only credential sets reach the same synthesized contract — the
// registered fields must stay optional so the form does not force them.
func TestPrepareRegistersContractForOptionalCredentials(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	server, registered := dynamicContractBroker(t)

	control := asyncFixture(t, func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
		definition := userMCPDefinition()
		definition.Credentials = []api.CredentialInput{
			{Name: "GOOGLE_OAUTH_CLIENT_ID"},
			{Name: "GOOGLE_OAUTH_CLIENT_SECRET"},
		}
		return api.SourceReview{Definition: definition, Permissions: api.ToolNames(definition), Effects: api.EffectNames(definition), ReviewDigest: "sha256:review"}, nil
	})
	control.Broker = &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	notified := make(chan api.Onboarding, 1)
	control.PrepareDone = func(_ context.Context, _ identity.Envelope, onboarding api.Onboarding) { notified <- onboarding }

	body, err := control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"source": githubCommitURL(), "request_key": "contract-miss-optional"})
	if err != nil {
		t.Fatalf("synthesized contract must not fail the prepare: %v", err)
	}
	phase, _ := body["phase"].(string)
	if phase == api.PhasePreparing {
		var final api.Onboarding
		select {
		case final = <-notified:
		case <-time.After(5 * time.Second):
			t.Fatal("PrepareDone never fired")
		}
		phase = final.Phase
	}
	// All-optional credentials mean no mandatory form: the onboarding may
	// confirm directly, while the registered contract keeps the creds
	// collectable through rotate/status.
	if phase != api.PhaseAwaitingCreds && phase != api.PhaseAwaitingConfirm {
		t.Fatalf("expected credential-capable phase, got %s", phase)
	}
	if len(*registered) != 1 {
		t.Fatalf("expected one dynamic contract, got %+v", *registered)
	}
	for _, f := range (*registered)[0].Fields {
		if f.Required {
			t.Fatalf("optional credential became a required field: %+v", f)
		}
	}
}
