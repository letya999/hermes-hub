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

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// When the broker rejects CreateRequest (e.g. the pinned contract vanished
// from its catalog), the surfaced error must name the contract — a bare
// "invalid_request (400)" leaves owner and operator guessing what to restore.
func TestBrokerRequestErrorNamesContract(t *testing.T) {
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
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contracts":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(brokerv1.Error{Code: "invalid_request"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	control := &ControlPlane{Store: NewStore(), Broker: &credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	definition := remoteDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "NOTION_TOKEN", Required: true}}
	definition.CredentialContractID = "notion-token"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"NOTION_TOKEN": "NOTION_TOKEN"}
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-test", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingCreds, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version}

	err = control.ensureBrokerRequest(context.Background(), aliceAuth(), &onboarding, definition)
	if err == nil || !strings.Contains(err.Error(), "notion-token") {
		t.Fatalf("error hides the rejected contract: %v", err)
	}
}
