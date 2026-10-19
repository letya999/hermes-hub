package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A recorded request that expired at the broker still returns 200 with
// status "expired" — not a 404. refreshBrokerRequest must replace it with a
// fresh request too, otherwise the agent keeps resending a dead connect link.
func TestRefreshBrokerRequestHealsExpiredRequest(t *testing.T) {
	keyPath := staleBrokerKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_old":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_old", Status: "expired", AuthorizationURL: "http://127.0.0.1/connect/request_old"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/contracts":
			var c contract.Contract
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(c)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			var input brokerv1.CreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", ContractID: input.ContractID, ContractRevision: input.ContractRevision, ConnectionID: input.ConnectionID, Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_new"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_new":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_new", Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/request_new"})
		default:
			brokerNotFound(w)
		}
	}))
	defer server.Close()

	auth := aliceAuth()
	definition := remoteDefinition()
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GOOGLE_TOKEN"}
	store := NewStore()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: store, Broker: &cfg, Now: time.Now}
	onboarding, err := control.newOnboarding(auth, api.OnboardingCatalog, "expired-request", definition, "", "")
	if err != nil {
		t.Fatal(err)
	}
	onboarding.BrokerRequestID = "request_old"
	onboarding.BrokerAuthorizationURL = "http://127.0.0.1/connect/request_old"
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := control.refreshBrokerRequest(t.Context(), auth, &onboarding); err != nil {
		t.Fatalf("expired request aborted refresh: %v", err)
	}
	if onboarding.BrokerRequestID != "request_new" {
		t.Fatalf("expired request was not replaced: %+v", onboarding)
	}
	if onboarding.BrokerAuthorizationURL != "http://127.0.0.1/connect/request_new" {
		t.Fatalf("stale authorization URL survived: %+v", onboarding)
	}
}

// A stored definition whose reviewed artifact is gone from both the daemon
// and the quarantine store (image pruned, tar never persisted or lost) must
// not be reused: prepare_source has to re-review and rebuild, otherwise the
// onboarding wedges forever on "reviewed artifact image unavailable".
func TestPrepareSourceRebuildsWhenArtifactMissing(t *testing.T) {
	store := NewStore()
	repo := "https://github.com/example/mcp-missing-artifact"
	definition := remoteDefinition()
	definition.DefinitionID = "mcp-missing-artifact"
	definition.Version = "0.0.2"
	definition.Transport = api.ContainerMCP
	definition.Source = api.DefinitionSource{
		Image:         "hermes-artifact/mcp-missing-artifact",
		Digest:        "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		Repository:    repo,
		CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
		ArchiveDigest: "sha256:00000000000000000000000000000000000000000000000000000000deadbeef",
		RecipeDigest:  "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		ReviewDigest:  "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Command:       "/app/mcp-server",
		Args:          []string{"stdio"},
	}
	definition.Workload.ToolHiveVersion = "v0.48.0"
	definition.Workload.SidecarImages = []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	definition.Health = api.HealthProbe{Kind: "exec", Value: "/app/mcp-server --help", TimeoutSeconds: 5}
	if digest, err := api.ConfirmedToolContractDigest(api.ConfirmedToolContract{Source: api.ToolContractReviewManifest, Tools: definition.Tools}); err != nil {
		t.Fatal(err)
	} else {
		definition.Source.ToolContractSource = api.ToolContractReviewManifest
		definition.Source.ToolContractDigest = digest
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPublication(api.DefinitionPublication{DefinitionID: definition.DefinitionID, Version: definition.Version, Visibility: api.PublicationUser, OwnerPrincipalID: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HUB_ARTIFACT_DIR", t.TempDir())
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	reviewed := false
	control.SourceResolver = func(_ context.Context, raw string) (api.ArtifactSource, error) {
		return api.ArtifactSource{Repository: raw, CommitSHA: definition.Source.CommitSHA}, nil
	}
	control.Reviewer = func(_ context.Context, _ api.ArtifactSource, _ *api.RecipeCandidate) (api.SourceReview, error) {
		reviewed = true
		return api.SourceReview{}, errors.New("test stops at reviewer")
	}
	if _, err := control.prepareSource(t.Context(), aliceAuth(), map[string]any{"source": repo, "request_key": "rebuild-missing-artifact"}); err == nil || !reviewed {
		t.Fatalf("stale definition was reused without an artifact: err=%v reviewed=%v", err, reviewed)
	}
}
