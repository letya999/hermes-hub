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
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"testing"
)

// teamBrokerFixture returns a store holding a shareable:credential
// definition bound to a reviewed broker contract, plus a confirmed team.
func teamBrokerFixture(t *testing.T) (*Store, api.ToolDefinition, api.Team) {
	t.Helper()
	store := NewStore()
	definition := remoteDefinition()
	definition.Workload.Shareable = "credential"
	definition.CredentialContractID = "team-contract"
	definition.CredentialContractRevision = 2
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "BROKER_GOOGLE_TOKEN"}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	team := api.Team{
		Schema: api.SchemaVersion, TeamID: "platform", Members: []string{"alice", "bob"},
		Revision: 1, IssuedBy: "operator", IssuedAt: time.Now().UTC(), Status: api.ActiveStatus,
	}
	if err := api.Confirm(&team, "", team.IssuedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	return store, definition, team
}

// S7.5: a broker-backed team share pins the credential's owning broker
// context and copies the reviewed contract so member grants can mint later.
func TestShareTeamConnectionBrokerCarriesContract(t *testing.T) {
	store, definition, _ := teamBrokerFixture(t)
	if _, _, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "", []string{"GOOGLE_TOKEN"}); err == nil {
		t.Fatal("broker team share without owning context accepted")
	}
	conn, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatalf("broker team share rejected: %v", err)
	}
	if reference.BrokerContextID != "ctx-team-ops" || reference.BrokerContractID != "team-contract" || reference.BrokerContractRevision != 2 || reference.BrokerEnv["GOOGLE_TOKEN"] != "BROKER_GOOGLE_TOKEN" {
		t.Fatalf("broker fields not carried: %+v", reference)
	}
	if conn.Owner.Type != api.TeamOwner || conn.CredentialRefID != reference.CredentialRefID {
		t.Fatalf("connection=%+v", conn)
	}
}

// S7.5/S7.6: a member's first call on a broker-backed team connection mints
// its own grant — signed as the manager of the credential's owning context,
// attributed to the member's binding and workload — and records it in the
// binding grant ledger so lifecycle paths can retire exactly it.
func TestTeamBrokerInjectorMintsPerMemberGrant(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	mints := 0
	var grantRequest brokerv1.GrantRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/cred_team_1/grants":
			mints++
			if err := json.NewDecoder(r.Body).Decode(&grantRequest); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_member_1", CredentialID: "cred_team_1", Active: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_member_1", CredentialID: "cred_team_1", Revision: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_1", Env: map[string]string{"BROKER_GOOGLE_TOKEN": "team-secret"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}

	store, definition, _ := teamBrokerFixture(t)
	conn, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := store.Resolve(aliceAuth(), binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	injector := teamBrokerInjector(store, &cfg, &cfg)
	injection, err := injector(t.Context(), effective)
	if err != nil {
		t.Fatalf("member injection rejected: %v", err)
	}
	defer injection.Cleanup()
	if injection.Environment["GOOGLE_TOKEN"] != "team-secret" {
		t.Fatalf("env=%v", injection.Environment)
	}
	if mints != 1 {
		t.Fatalf("mints=%d", mints)
	}
	// The grant is attributed to the member's binding under the credential's
	// owning context — never the member's own context.
	if grantRequest.PrincipalID != "alice" || grantRequest.ContextID != "ctx-team-ops" || grantRequest.RuntimeID != "runtime" {
		t.Fatalf("grant identity=%+v", grantRequest)
	}
	if grantRequest.BindingID != binding.ToolBindingID || grantRequest.WorkloadID != effective.WorkloadID || grantRequest.Execution != "dedicated" {
		t.Fatalf("grant binding=%+v", grantRequest)
	}
	if grantRequest.ContractID != "team-contract" || grantRequest.ContractRevision != 2 {
		t.Fatalf("grant contract=%+v", grantRequest)
	}
	grant, ok := store.BrokerGrantFor(binding.ToolBindingID, reference.CredentialRefID)
	if !ok || grant.GrantID != "grant_member_1" || grant.ContextID != "ctx-team-ops" || grant.ConnectionID != conn.ConnectionID {
		t.Fatalf("ledger=%+v ok=%v", grant, ok)
	}
	// The shared reference stays grant-free; a second call reuses the ledger.
	if reference.BrokerGrantID != "" {
		t.Fatal("member grant leaked into the shared reference")
	}
	fresh, err := store.Resolve(aliceAuth(), binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Credential.BrokerGrantID != "" {
		t.Fatal("resolved reference carries a member grant")
	}
	if _, err := injector(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	if mints != 1 {
		t.Fatalf("second call re-minted: mints=%d", mints)
	}
	// Bob converges on the same connection but mints a distinct grant.
	bobBinding, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	bobEffective, err := store.Resolve(bobAuth(), bobBinding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := injector(t.Context(), bobEffective); err != nil {
		t.Fatalf("bob injection rejected: %v", err)
	}
	if mints != 2 || grantRequest.PrincipalID != "bob" || grantRequest.BindingID != bobBinding.ToolBindingID {
		t.Fatalf("bob grant: mints=%d req=%+v", mints, grantRequest)
	}
}

// S7.6: removing a member from the team record purges their seats — the
// binding and its workload drop, the minted broker grant retires through the
// revoke seam, and the remaining member is untouched.
func TestPutTeamPurgesRemovedMemberSeats(t *testing.T) {
	store, definition, team := teamBrokerFixture(t)
	conn, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	revoked := map[string]int{}
	store.BrokerRevoke = func(_ context.Context, grant api.BrokerBindingGrant) error {
		revoked[grant.GrantID]++
		return nil
	}
	if _, err := store.RecordBrokerGrant(api.BrokerBindingGrant{BindingID: alice.ToolBindingID, ConnectionID: conn.ConnectionID, CredentialRefID: reference.CredentialRefID, ContextID: "ctx-team-ops", GrantID: "grant_alice", PolicyVersion: "policy-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordBrokerGrant(api.BrokerBindingGrant{BindingID: bob.ToolBindingID, ConnectionID: conn.ConnectionID, CredentialRefID: reference.CredentialRefID, ContextID: "ctx-team-ops", GrantID: "grant_bob", PolicyVersion: "policy-1"}); err != nil {
		t.Fatal(err)
	}
	team.Members = []string{"bob"}
	team.Revision++
	if err := store.PutTeam(team); err != nil {
		t.Fatal(err)
	}
	// The binding row survives — per-call membership denies the resolve —
	// while the minted grant is gone for good.
	if _, err := store.Resolve(aliceAuth(), alice.ToolBindingID); err == nil {
		t.Fatal("removed member still resolves")
	}
	if _, err := store.Resolve(bobAuth(), bob.ToolBindingID); err != nil {
		t.Fatalf("remaining member broke: %v", err)
	}
	if revoked["grant_alice"] != 1 || revoked["grant_bob"] != 0 {
		t.Fatalf("grant retire=%v", revoked)
	}
	if _, ok := store.BrokerGrantFor(alice.ToolBindingID, reference.CredentialRefID); ok {
		t.Fatal("removed member grant row kept")
	}
	if _, ok := store.BrokerGrantFor(bob.ToolBindingID, reference.CredentialRefID); !ok {
		t.Fatal("remaining member grant row lost")
	}
}

// S7.6: disabling a member's binding retires exactly their minted grant —
// the row leaves the ledger and the revoke seam sees it once.
func TestTeamGrantRetireOnMemberDisable(t *testing.T) {
	store, definition, _ := teamBrokerFixture(t)
	conn, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	alice, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	revoked := map[string]int{}
	store.BrokerRevoke = func(_ context.Context, grant api.BrokerBindingGrant) error {
		revoked[grant.GrantID]++
		return nil
	}
	for _, seat := range []struct {
		binding api.ToolBinding
		grant   string
	}{{alice, "g_alice"}, {bob, "g_bob"}} {
		if _, err := store.RecordBrokerGrant(api.BrokerBindingGrant{BindingID: seat.binding.ToolBindingID, ConnectionID: conn.ConnectionID, CredentialRefID: reference.CredentialRefID, ContextID: "ctx-team-ops", GrantID: seat.grant, PolicyVersion: "policy-1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetBindingStatus(bob.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if revoked["g_bob"] != 1 || revoked["g_alice"] != 0 {
		t.Fatalf("disable retire=%v", revoked)
	}
	if _, ok := store.BrokerGrantFor(bob.ToolBindingID, reference.CredentialRefID); ok {
		t.Fatal("disabled member grant kept")
	}
	if _, ok := store.BrokerGrantFor(alice.ToolBindingID, reference.CredentialRefID); !ok {
		t.Fatal("active member grant lost")
	}
}

// S7.6: a member removing their own binding retires their minted grant with
// the row — the team-owned connection and other members' grants survive.
func TestTeamGrantRetireOnMemberRemove(t *testing.T) {
	store, definition, _ := teamBrokerFixture(t)
	conn, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	revoked := map[string]int{}
	store.BrokerRevoke = func(_ context.Context, grant api.BrokerBindingGrant) error {
		revoked[grant.GrantID]++
		return nil
	}
	if _, err := store.RecordBrokerGrant(api.BrokerBindingGrant{BindingID: bob.ToolBindingID, ConnectionID: conn.ConnectionID, CredentialRefID: reference.CredentialRefID, ContextID: "ctx-team-ops", GrantID: "g_bob", PolicyVersion: "policy-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveBindingRecords(bob.ToolBindingID); err != nil {
		t.Fatal(err)
	}
	if revoked["g_bob"] != 1 {
		t.Fatalf("remove retire=%v", revoked)
	}
	if _, ok := store.BrokerGrantFor(bob.ToolBindingID, reference.CredentialRefID); ok {
		t.Fatal("removed member grant kept")
	}
	if _, ok := store.credentialFor(reference.CredentialRefID); !ok {
		t.Fatal("team credential row lost to member remove")
	}
}

// S7.5: operator rotation on the team connection retires every grant pinned
// to the superseded reference — member calls re-mint lazily against the new
// credential instead of presenting a grant bound to dead material.
func TestTeamGrantRetireOnRotation(t *testing.T) {
	store, definition, _ := teamBrokerFixture(t)
	conn, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	revoked := map[string]int{}
	store.BrokerRevoke = func(_ context.Context, grant api.BrokerBindingGrant) error {
		revoked[grant.GrantID]++
		return nil
	}
	if _, err := store.RecordBrokerGrant(api.BrokerBindingGrant{BindingID: bob.ToolBindingID, ConnectionID: conn.ConnectionID, CredentialRefID: reference.CredentialRefID, ContextID: "ctx-team-ops", GrantID: "g_bob", PolicyVersion: "policy-1"}); err != nil {
		t.Fatal(err)
	}
	next := api.CredentialReference{CredentialRefID: api.CredentialReferenceID(conn.ConnectionID, 2), ConnectionID: conn.ConnectionID, Revision: 2, Backend: "credential-broker", Locator: "cred_team_2", Keys: []string{"GOOGLE_TOKEN"}, BrokerContractID: "team-contract", BrokerContractRevision: 2, BrokerEnv: map[string]string{"GOOGLE_TOKEN": "BROKER_GOOGLE_TOKEN"}, BrokerContextID: "ctx-team-ops"}
	if _, err := store.RotateCredentialRecord(next); err != nil {
		t.Fatal(err)
	}
	if revoked["g_bob"] != 1 {
		t.Fatalf("rotate retire=%v", revoked)
	}
	if _, ok := store.BrokerGrantFor(bob.ToolBindingID, reference.CredentialRefID); ok {
		t.Fatal("stale-revision grant kept")
	}
}

// S7.6: minted member grants are durable — a store reload keeps the ledger
// row so a restart does not re-mint or strand the grant.
func TestTeamGrantLedgerSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	store, definition, _ := teamBrokerFixture(t)
	conn, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	recorded := api.BrokerBindingGrant{BindingID: bob.ToolBindingID, ConnectionID: conn.ConnectionID, CredentialRefID: reference.CredentialRefID, ContextID: "ctx-team-ops", GrantID: "g_bob", PolicyVersion: "policy-1"}
	if _, err := store.RecordBrokerGrant(recorded); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	grant, ok := loaded.BrokerGrantFor(bob.ToolBindingID, reference.CredentialRefID)
	if !ok || grant != recorded {
		t.Fatalf("reloaded grant=%+v ok=%v", grant, ok)
	}
}

// S7.5: the deployed injector wiring mints the member grant inside the call
// path — an authorized member call on a broker team connection mints,
// records and uses the grant without the shared reference ever carrying it.
func TestTeamBrokerGrantMintsThroughEndpointWiring(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	mints := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials/cred_team_1/grants":
			mints++
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: "grant_member_1", CredentialID: "cred_team_1", Active: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_member_1", CredentialID: "cred_team_1", Revision: 1})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease_1", Env: map[string]string{"BROKER_GOOGLE_TOKEN": "team-secret"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/leases/lease_1/release":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	var seenEnv map[string]string
	backend := backendFunc(func(_ context.Context, effective api.EffectiveBinding, _ api.ToolSpec, _ map[string]any) (api.BackendResult, error) {
		return api.BackendResult{Text: "ok"}, nil
	})
	envBackend := &recordingEnvBackend{inner: backend, env: &seenEnv}
	store, definition, _ := teamBrokerFixture(t)
	if _, _, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enable(aliceAuth(), definition.DefinitionID, definition.Version); err != nil {
		t.Fatal(err)
	}
	handler, err := NewEndpointHandler(EndpointConfig{Token: aliceToken, Auth: aliceAuth(), Backend: envBackend, BrokerControl: &cfg, BrokerRuntime: &cfg}, store)
	if err != nil {
		t.Fatal(err)
	}
	gateway := handler.(*projectionEndpoint).gateway
	name := api.ProjectedToolName(definition.DefinitionID, definition.Version, "search")
	callDone := make(chan error, 1)
	go func() {
		_, err := gateway.CallAuthorized(t.Context(), aliceAuth(), name, map[string]any{})
		callDone <- err
	}()
	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("member call rejected: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("member call deadlocked: grant mint ran inside the store lock")
	}
	if mints != 1 || seenEnv["GOOGLE_TOKEN"] != "team-secret" {
		t.Fatalf("mints=%d env=%v", mints, seenEnv)
	}
}

type recordingEnvBackend struct {
	inner api.ToolBackend
	env   *map[string]string
}

func (r *recordingEnvBackend) Call(ctx context.Context, effective api.EffectiveBinding, spec api.ToolSpec, args map[string]any) (api.BackendResult, error) {
	return r.inner.Call(ctx, effective, spec, args)
}

// S7.6: the deployed wiring retires member grants at the broker — a member
// disable issues the grant revoke against the credential's owning context.
func TestTeamBrokerRevokeWiredThroughEndpoint(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	revoked := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/grants/") && strings.HasSuffix(r.URL.Path, "/revoke") {
			revoked[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/grants/"), "/revoke")]++
			_ = json.NewEncoder(w).Encode(map[string]any{})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	store, definition, _ := teamBrokerFixture(t)
	conn, reference, err := store.ShareTeamConnection("platform", definition.DefinitionID, definition.Version, "credential-broker", "cred_team_1", "ctx-team-ops", []string{"GOOGLE_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.Enable(bobAuth(), definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordBrokerGrant(api.BrokerBindingGrant{BindingID: bob.ToolBindingID, ConnectionID: conn.ConnectionID, CredentialRefID: reference.CredentialRefID, ContextID: "ctx-team-ops", GrantID: "grant_member_1", PolicyVersion: "policy-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEndpointHandler(EndpointConfig{Token: aliceToken, Auth: aliceAuth(), Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		return api.BackendResult{Text: "ok"}, nil
	}), BrokerControl: &cfg, BrokerRuntime: &cfg}, store); err != nil {
		t.Fatal(err)
	}
	if store.BrokerRevoke == nil {
		t.Fatal("revoke seam not wired")
	}
	if err := store.SetBindingStatus(bob.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if revoked["grant_member_1"] != 1 {
		t.Fatalf("broker revoke=%v", revoked)
	}
}

func (r *recordingEnvBackend) CallEnv(ctx context.Context, effective api.EffectiveBinding, spec api.ToolSpec, args map[string]any, env map[string]string) (api.BackendResult, error) {
	// The injector's Cleanup clears the delivery map after the call — clone.
	copied := make(map[string]string, len(env))
	for k, v := range env {
		copied[k] = v
	}
	*r.env = copied
	if envOK, ok := r.inner.(api.EnvBackend); ok {
		return envOK.CallEnv(ctx, effective, spec, args, env)
	}
	return r.inner.Call(ctx, effective, spec, args)
}
