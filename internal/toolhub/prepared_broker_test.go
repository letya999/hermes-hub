package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
)

func TestPreparedTelegramUsesBrokerBeforeAuthenticatedToolList(t *testing.T) {
	var telegram PreparedEntry
	entries, err := PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ID == "telegram" {
			telegram = entry
		}
	}
	definition := statefulContainerDefinition()
	definition.DefinitionID = "telegram"
	definition.Source.Repository = telegram.Source.Repository
	definition.Source.CommitSHA = telegram.Source.CommitSHA
	definition.Source.Command = telegram.Entrypoint[0]
	definition.Source.Args = telegram.Entrypoint[1:]
	definition.Credentials = telegram.Connection.CredentialInputs()
	definition.CredentialContractID = telegram.ContractID
	definition.CredentialContractRevision = telegram.ContractRevision
	definition.CredentialContractEnv = map[string]string{}
	for _, field := range telegram.Connection.Fields {
		definition.CredentialContractEnv[field.Name] = field.Name
	}
	definition.RuntimeEnvironment = cloneMap(telegram.RuntimeEnvironment)
	definition.ProxyEnvironment = append([]string(nil), telegram.ProxyEnvironment...)
	definition.Execution.Egress = append([]string(nil), telegram.Egress...)
	definition.Execution.Mounts = nil
	definition.Workload.Stateful = false
	definition.Tools = []ToolSpec{{Name: "list_accounts", Effect: ReadEffect}}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	var rotatedReady atomic.Bool
	var requests atomic.Int32
	var grants, releases, revocations atomic.Int32
	var activeGrant atomic.Value
	var activeCredential atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/requests":
			requestID := "request_1"
			if requests.Add(1) == 2 {
				requestID = "request_2"
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: requestID, ContractID: "telegram-session", ContractRevision: 1, Status: "pending", AuthorizationURL: "http://127.0.0.1/connect/" + requestID})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_1":
			status := "pending"
			credentialID := ""
			if ready.Load() {
				status, credentialID = "ready", "credential_1"
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_1", ContractID: "telegram-session", ContractRevision: 1, Status: status, CredentialID: credentialID, AuthorizationURL: "http://127.0.0.1/connect/request_1"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/requests/request_2":
			status, credentialID := "pending", ""
			if rotatedReady.Load() {
				status, credentialID = "ready", "credential_2"
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_2", ContractID: "telegram-session", ContractRevision: 1, Status: status, CredentialID: credentialID, AuthorizationURL: "http://127.0.0.1/connect/request_2"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/credential_1":
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{ID: "credential_1", Status: "active", ConnectionID: "connection_1", ConsumerID: "telegram"})
		case r.Method == http.MethodPost && (r.URL.Path == "/v1/credentials/credential_1/grants" || r.URL.Path == "/v1/credentials/credential_2/grants"):
			var input brokerv1.GrantRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.ContractID != "telegram-session" || input.PrincipalID != "alice" || input.Execution != "dedicated" {
				t.Error("temporary grant did not bind the Telegram owner and contract")
			}
			count := grants.Add(1)
			grantID := "grant_1"
			if count == 2 {
				grantID = "grant_2"
			} else if count == 3 {
				grantID = "grant_3"
			} else if count == 4 {
				grantID = "grant_4"
			} else if count == 5 {
				grantID = "grant_5"
			}
			credentialID := "credential_1"
			if r.URL.Path == "/v1/credentials/credential_2/grants" {
				credentialID = "credential_2"
			}
			activeGrant.Store(grantID)
			activeCredential.Store(credentialID)
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: grantID, CredentialID: credentialID, PrincipalID: input.PrincipalID, ContextID: input.ContextID, RuntimeID: input.RuntimeID, BindingID: input.BindingID, WorkloadID: input.WorkloadID, Execution: input.Execution, ContractID: input.ContractID, ContractRevision: input.ContractRevision, PolicyVersion: "policy-1", Active: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: activeGrant.Load().(string), CredentialID: activeCredential.Load().(string), Revision: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_1", Env: map[string]string{
				"TELEGRAM_API_ID": "12345", "TELEGRAM_API_HASH": "fake-hash", "TELEGRAM_SESSION_STRING": "fake-session", "TELEGRAM_EXPECTED_USER_ID": "42",
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/release":
			releases.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && (r.URL.Path == "/v1/grants/grant_1/revoke" || r.URL.Path == "/v1/grants/grant_2/revoke" || r.URL.Path == "/v1/grants/grant_3/revoke" || r.URL.Path == "/v1/grants/grant_4/revoke"):
			revocations.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	control := &ControlPlane{Store: NewStore(), Broker: &cfg, BrokerRuntime: &cfg, Now: time.Now}
	admitCalls := 0
	control.AdmitWithCredentials = func(_ context.Context, _ ToolDefinition, values map[string]string) (ToolDefinition, error) {
		admitCalls++
		if values["TELEGRAM_SESSION_STRING"] != "fake-session" || values["TELEGRAM_EXPECTED_USER_ID"] != "42" {
			t.Error("Broker did not deliver the owner session to the admission probe")
		}
		return definition, nil
	}
	auth := aliceAuth()
	preparing, err := control.newOnboarding(auth, OnboardingSelfInstall, "telegram-broker", definition, telegram.Source.Repository, telegram.Source.CommitSHA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.acceptCredentialGate(t.Context(), auth, preparing, SourceReview{Definition: definition, AdmissionPending: true, AdmissionDetail: "login required"}); err != nil {
		t.Fatal(err)
	}
	credentials, err := control.requiredCredentials(auth, map[string]any{"onboarding_id": preparing.OnboardingID})
	if err != nil || credentials["input"] != "credential-broker" || credentials["form_url"] != nil || credentials["authorization_url"] == nil || admitCalls != 0 {
		t.Fatalf("protected Broker form was bypassed: %+v %v", credentials, err)
	}
	ready.Store(true)
	status, err := control.status(auth, map[string]any{"onboarding_id": preparing.OnboardingID})
	if err != nil || status["phase"] != PhaseAwaitingConfirm || status["tools_confirmed"] == false || admitCalls != 1 || grants.Load() != 1 || releases.Load() != 1 || revocations.Load() != 1 {
		t.Fatalf("Broker admission did not complete and clean up: %+v %v (probe=%d grant=%d release=%d revoke=%d)", status, err, admitCalls, grants.Load(), releases.Load(), revocations.Load())
	}
	stored, err := control.Store.OnboardingFor(auth, preparing.OnboardingID)
	if err != nil || stored.AdmissionPending || stored.BrokerCredentialID != "credential_1" || stored.Definition == nil || len(stored.Definition.Tools) != 1 {
		t.Fatalf("confirmed tool contract missing: %+v %v", stored, err)
	}
	confirmed, err := control.confirm(t.Context(), auth, map[string]any{"onboarding_id": preparing.OnboardingID, "nonce": stored.ConfirmationNonce})
	if err != nil || confirmed["phase"] != PhaseConfirmed || grants.Load() != 2 || revocations.Load() != 1 {
		t.Fatalf("owner binding did not mint a permanent grant: %+v %v (grant=%d revoke=%d)", confirmed, err, grants.Load(), revocations.Load())
	}
	bound, err := control.Store.OnboardingFor(auth, preparing.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	_, credential, err := control.Store.OwnedConnection(auth, bound.ConnectionID)
	if err != nil || credential.Backend != "credential-broker" || credential.BrokerGrantID != "grant_2" {
		t.Fatalf("owner binding has no Broker credential: %+v %v", credential, err)
	}
	control.AdmitWithCredentials = func(_ context.Context, _ ToolDefinition, _ map[string]string) (ToolDefinition, error) {
		admitCalls++
		return ToolDefinition{}, errors.New("Telegram session rejected fake-session")
	}
	retry, err := control.newOnboarding(auth, OnboardingSelfInstall, "telegram-broker-retry", definition, telegram.Source.Repository, telegram.Source.CommitSHA)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := control.acceptCredentialGate(t.Context(), auth, retry, SourceReview{Definition: definition, AdmissionPending: true, AdmissionDetail: "login required"})
	if err != nil || failed["phase"] != PhaseAwaitingCreds || grants.Load() != 3 || releases.Load() != 2 || revocations.Load() != 2 {
		t.Fatalf("bad session did not stay pending and clean up: %+v %v", failed, err)
	}
	if failed["tools_confirmed"] != false || !strings.HasPrefix(failed["error"].(string), "Проверка не прошла") || strings.Contains(failed["error"].(string), "fake-session") {
		t.Fatalf("failed Broker admission was not reported: %+v", failed)
	}
	if _, err := control.status(auth, map[string]any{"onboarding_id": retry.OnboardingID}); err != nil || admitCalls != 2 {
		t.Fatalf("bad session was reprobed without rotation: %v (probes=%d)", err, admitCalls)
	}
	rotated, err := control.Rotate(auth, map[string]any{"onboarding_id": retry.OnboardingID})
	if err != nil || rotated["phase"] != PhaseAwaitingCreds || requests.Load() != 2 {
		t.Fatalf("reauth did not open a new Broker request: %+v %v", rotated, err)
	}
	control.AdmitWithCredentials = func(_ context.Context, _ ToolDefinition, _ map[string]string) (ToolDefinition, error) {
		admitCalls++
		return definition, nil
	}
	rotatedReady.Store(true)
	rotated, err = control.status(auth, map[string]any{"onboarding_id": retry.OnboardingID})
	if err != nil || rotated["phase"] != PhaseAwaitingConfirm || admitCalls != 3 || grants.Load() != 4 || revocations.Load() != 3 {
		t.Fatalf("rotated Broker credential was not admitted: %+v %v (probes=%d grants=%d revoked=%d)", rotated, err, admitCalls, grants.Load(), revocations.Load())
	}
	control.Release = func(context.Context, string) error { return nil }
	reauth, err := control.Store.OnboardingFor(auth, retry.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err = control.confirm(t.Context(), auth, map[string]any{"onboarding_id": retry.OnboardingID, "nonce": reauth.ConfirmationNonce})
	if err != nil || rotated["phase"] != PhaseConfirmed || grants.Load() != 5 || revocations.Load() != 4 {
		t.Fatalf("rotation did not replace the owner grant: %+v %v (grants=%d revoked=%d)", rotated, err, grants.Load(), revocations.Load())
	}
	_, renewed, err := control.Store.OwnedConnection(auth, bound.ConnectionID)
	if err != nil || renewed.Locator != "credential_2" || renewed.BrokerGrantID != "grant_5" {
		t.Fatalf("rotated owner binding retained an old credential: %+v %v", renewed, err)
	}
}
