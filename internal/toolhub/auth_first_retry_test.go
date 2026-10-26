package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// An interactive-auth declaration must surface through the connector journal
// as unsupported_auth, not collapse into the generic probe_failed bucket.
func TestConnectorInstallInteractiveIsUnsupportedAuth(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	profile.SelfInstall = true
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{
		Store:             store,
		PrepareSyncWindow: -1,
		SourceResolver: func(context.Context, string) (api.ArtifactSource, error) {
			return api.ArtifactSource{Repository: "https://github.com/example/interactive-mcp", CommitSHA: strings.Repeat("a", 40)}, nil
		},
		Reviewer: func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
			return api.SourceReview{}, fmt.Errorf("%w: server requires interactive authentication", api.ErrUnsupportedAuth)
		},
	}
	result, err := control.InvokeConnector(context.Background(), auth, "connector_install", map[string]any{
		"target":            "https://github.com/example/interactive-mcp",
		"connection_recipe": map[string]any{"interactive": true},
	})
	if err != nil {
		t.Fatalf("connector install returned transport error: %v", err)
	}
	if result.Code != api.OpCodeUnsupportedAuth {
		t.Fatalf("interactive classification lost in journal: code=%q state=%q summary=%q", result.Code, result.State, result.Summary)
	}
}

// A rejected credential must not force a rebuild of an unchanged artifact:
// the built evidence survives on the draft, so rotation re-runs only the
// credentialed tools/list against the stored artifact.
func TestAuthFirstRetryReusesBuiltArtifact(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	key := filepath.Join(root, "broker.private")
	if err := os.WriteFile(key, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/credentials/credential_1/grants":
			response = brokerv1.Grant{ID: "grant_a", Active: true}
		case "POST /v1/credentials/credential_2/grants":
			response = brokerv1.Grant{ID: "grant_b", Active: true}
		case "POST /v1/leases":
			response = brokerv1.Lease{ID: "lease_1"}
		case "POST /v1/runtime/leases/lease_1/materialize":
			response = brokerv1.Materialized{Env: map[string]string{"CUSTOM_TOKEN": "synthetic-token"}}
		case "POST /v1/runtime/leases/lease_1/release", "POST /v1/grants/grant_a/revoke", "POST /v1/grants/grant_b/revoke":
			response = struct{}{}
		case "GET /v1/credentials/credential_1":
			response = brokerv1.Credential{Status: "active", ConnectionID: "conn_1", ConsumerID: "auth-first-mcp"}
		case "POST /v1/requests":
			response = brokerv1.Request{ID: "request_2", Status: "pending", AuthorizationURL: "https://broker.test/connect/request_2", ContractID: "env-auth", ContractRevision: 1}
		case "GET /v1/requests/request_2":
			response = brokerv1.Request{ID: "request_2", Status: "ready", CredentialID: "credential_2", AuthorizationURL: "https://broker.test/connect/request_2", ContractID: "env-auth", ContractRevision: 1}
		default:
			t.Errorf("unexpected broker call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	auth := aliceAuth()
	draft := statefulContainerDefinition()
	draft.DefinitionID = "auth-first-mcp"
	draft.Source.Image = ""
	draft.Source.Digest = ""
	draft.Source.Repository = "https://github.com/example/auth-first-mcp"
	draft.Source.CommitSHA = strings.Repeat("a", 40)
	draft.Tools = nil
	draft.Credentials = []api.CredentialInput{{Name: "CUSTOM_TOKEN", Required: true, Delivery: "env"}}
	draft.CredentialContractID, draft.CredentialContractRevision = "env-auth", 1
	draft.CredentialContractEnv = map[string]string{"CUSTOM_TOKEN": "CUSTOM_TOKEN"}
	built := draft
	built.Source.Image = "hermes-artifact/auth-first-mcp"
	built.Source.Digest = "sha256:" + strings.Repeat("b", 64)
	built.Source.ArchiveDigest = "sha256:" + strings.Repeat("c", 64)
	artifacts := t.TempDir()
	if err := os.WriteFile(filepath.Join(artifacts, api.ArtifactObjectName(built.Source.ArchiveDigest)), []byte("synthetic-oci"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_ARTIFACT_DIR", artifacts)

	store := NewStore()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onboard-retry",
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingCreds, Revision: 1,
		AdmissionPending: true, Definition: &draft, BrokerRequestID: "request_1",
		Recipe: &api.RecipeResolution{
			Source:     api.ArtifactSource{Repository: draft.Source.Repository, CommitSHA: draft.Source.CommitSHA},
			Connection: api.ConnectionRecipe{AuthBeforeToolsList: true, Fields: []api.ConnectionField{{Name: "CUSTOM_TOKEN", Required: true, Delivery: "env"}}},
		},
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: key, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	reviews := 0
	control := &ControlPlane{
		Store: store, Broker: &cfg, BrokerRuntime: &cfg, Now: time.Now,
		Reviewer: func(context.Context, api.ArtifactSource, *api.RecipeCandidate) (api.SourceReview, error) {
			reviews++
			return api.SourceReview{Definition: built}, errors.New("server rejected the submitted token")
		},
		AdmitWithCredentials: func(_ context.Context, d api.ToolDefinition, env map[string]string) (api.ToolDefinition, error) {
			if d.Source.Digest == "" || d.Source.ArchiveDigest == "" {
				t.Error("stored-artifact retry lost the built evidence")
			}
			if env["CUSTOM_TOKEN"] != "synthetic-token" {
				t.Error("rotated credential did not reach the probe")
			}
			admitted := d
			admitted.Tools = []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}
			return admitted, nil
		},
	}
	if err := control.completeBrokerAdmission(context.Background(), auth, &onboarding, "credential_1"); err != nil {
		t.Fatalf("first admission: %v", err)
	}
	failed, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.ErrorCode != api.OpCodeProbeFailed || failed.Phase != api.PhaseAwaitingCreds {
		t.Fatalf("rejected credential did not reopen the form: phase=%q code=%q", failed.Phase, failed.ErrorCode)
	}
	if reviews != 1 {
		t.Fatalf("first admission reviewed %d times", reviews)
	}
	body, err := control.Rotate(auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if body["phase"] != api.PhaseAwaitingConfirm {
		t.Fatalf("retry did not reach confirmation: %v", body)
	}
	if reviews != 1 {
		t.Fatalf("retry rebuilt the unchanged artifact: %d reviews", reviews)
	}
}

// Two owners racing the serialized admission path must converge on their own
// durable records: no cross-owner reads, no lost updates, distinct results.
func TestConcurrentBrokerAdmissionStaysOwnerScoped(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	key := filepath.Join(root, "broker.private")
	if err := os.WriteFile(key, private, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var response any
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/credentials/credential_alice/grants":
			response = brokerv1.Grant{ID: "grant_alice", Active: true}
		case "POST /v1/credentials/credential_bob/grants":
			response = brokerv1.Grant{ID: "grant_bob", Active: true}
		case "POST /v1/leases":
			response = brokerv1.Lease{ID: "lease_1"}
		case "POST /v1/runtime/leases/lease_1/materialize":
			response = brokerv1.Materialized{Env: map[string]string{"CUSTOM_TOKEN": "synthetic-token"}}
		case "POST /v1/runtime/leases/lease_1/release", "POST /v1/grants/grant_alice/revoke", "POST /v1/grants/grant_bob/revoke":
			response = struct{}{}
		default:
			t.Errorf("unexpected broker call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	artifacts := t.TempDir()
	t.Setenv("HUB_ARTIFACT_DIR", artifacts)
	archiveDigest := "sha256:" + strings.Repeat("c", 64)
	if err := os.WriteFile(filepath.Join(artifacts, api.ArtifactObjectName(archiveDigest)), []byte("synthetic-oci"), 0600); err != nil {
		t.Fatal(err)
	}

	store := NewStore()
	owners := []struct {
		name         string
		onboardingID string
	}{
		{"alice", "onboard-alice"},
		{"bob", "onboard-bob"},
	}
	auths := map[string]identity.Envelope{"alice": aliceAuth(), "bob": bobAuth()}
	for _, owner := range owners {
		auth := auths[owner.name]
		draft := statefulContainerDefinition()
		draft.DefinitionID = "auth-first-" + owner.name
		draft.Source.Digest = "sha256:" + strings.Repeat("b", 64)
		draft.Source.ArchiveDigest = archiveDigest
		draft.Source.Repository = "https://github.com/example/auth-first-mcp"
		draft.Source.CommitSHA = strings.Repeat("a", 40)
		draft.Tools = nil
		draft.Credentials = []api.CredentialInput{{Name: "CUSTOM_TOKEN", Required: true, Delivery: "env"}}
		draft.CredentialContractID, draft.CredentialContractRevision = "env-auth", 1
		draft.CredentialContractEnv = map[string]string{"CUSTOM_TOKEN": "CUSTOM_TOKEN"}
		if err := store.PutOnboarding(api.Onboarding{
			Schema: api.SchemaVersion, OnboardingID: owner.onboardingID,
			PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
			Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingCreds, Revision: 1,
			AdmissionPending: true, Definition: &draft,
		}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: key, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{
		Store: store, Broker: &cfg, BrokerRuntime: &cfg, Now: time.Now,
		AdmitWithCredentials: func(_ context.Context, d api.ToolDefinition, env map[string]string) (api.ToolDefinition, error) {
			admitted := d
			admitted.Tools = []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}}
			return admitted, nil
		},
	}
	results := make(chan error, len(owners))
	for _, owner := range owners {
		owner := owner
		go func() {
			auth := auths[owner.name]
			onboarding, err := store.OnboardingFor(auth, owner.onboardingID)
			if err != nil {
				results <- err
				return
			}
			results <- control.completeBrokerAdmission(context.Background(), auth, &onboarding, "credential_"+owner.name)
		}()
	}
	for range owners {
		if err := <-results; err != nil {
			t.Fatalf("concurrent admission failed: %v", err)
		}
	}
	for _, owner := range owners {
		auth := auths[owner.name]
		record, err := store.OnboardingFor(auth, owner.onboardingID)
		if err != nil || record.Phase != api.PhaseAwaitingConfirm || record.AdmissionPending {
			t.Fatalf("%s admission did not converge: phase=%q err=%v", owner.name, record.Phase, err)
		}
		if record.BrokerCredentialID != "credential_"+owner.name {
			t.Fatalf("%s admission bound a foreign credential: %q", owner.name, record.BrokerCredentialID)
		}
	}
	if _, err := store.OnboardingFor(auths["alice"], "onboard-bob"); err == nil {
		t.Fatal("alice read bob's admission record")
	}
}
