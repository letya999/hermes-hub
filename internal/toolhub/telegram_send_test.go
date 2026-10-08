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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

func TestTelegramSendRetainsOwnerSession(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed=%v", managed), func(t *testing.T) {
			testTelegramSendRetainsOwnerSession(t, managed)
		})
	}
}

func testTelegramSendRetainsOwnerSession(t *testing.T, managed bool) {
	entries, err := PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	var entry PreparedEntry
	for _, candidate := range entries {
		if candidate.ID == "telegram" {
			entry = candidate
		}
	}
	definition := statefulContainerDefinition()
	definition.DefinitionID = "telegram"
	definition.Source.Repository, definition.Source.CommitSHA = entry.Source.Repository, entry.Source.CommitSHA
	definition.Source.Command, definition.Source.Args = entry.Entrypoint[0], entry.Entrypoint[1:]
	definition.RuntimeEnvironment = cloneMap(entry.RuntimeEnvironment)
	definition.Workload.Stateful = entry.Stateful
	definition.Credentials = entry.Connection.CredentialInputs()
	definition.CredentialContractID, definition.CredentialContractRevision = entry.ContractID, entry.ContractRevision
	definition.CredentialContractEnv = map[string]string{}
	for _, input := range definition.Credentials {
		definition.CredentialContractEnv[input.Name] = input.Name
	}
	definition.Tools = []ToolSpec{{Name: "list_accounts", Effect: ReadEffect, InputSchema: json.RawMessage(`{"type":"object"}`)}}
	imported := ImportedArtifact{Definition: definition}
	if _, err := ApplyPreflightToolList(&imported, definition.Tools); err != nil {
		t.Fatal(err)
	}
	definition = imported.Definition
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	var grants, revoked, released atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/credentials/owner-session/grants":
			var input brokerv1.GrantRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.PrincipalID != "alice" || input.ContractID != entry.ContractID {
				t.Error("grant crossed owner or contract")
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Grant{ID: fmt.Sprintf("grant_%d", grants.Add(1))})
		case r.Method == "POST" && r.URL.Path == "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "probe-lease"})
		case r.Method == "POST" && r.URL.Path == "/v1/runtime/leases/probe-lease/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Env: map[string]string{"TELEGRAM_API_ID": "123", "TELEGRAM_API_HASH": "fake-hash", "TELEGRAM_SESSION_STRING": "owner-session-secret", "TELEGRAM_EXPECTED_USER_ID": "42"}})
		case r.Method == "POST" && r.URL.Path == "/v1/runtime/leases/probe-lease/release":
			released.Add(1)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/grants/") && strings.HasSuffix(r.URL.Path, "/revoke"):
			revoked.Add(1)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected Broker action: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	c := &ControlPlane{Store: NewStore(), Broker: &cfg, BrokerRuntime: &cfg, Release: func(context.Context, string) error { return nil }}
	// Require readiness so the normal Broker version-upgrade path mints a new
	// binding grant instead of the legacy no-controller shortcut.
	c.Ready = func(context.Context, EffectiveBinding) error { return nil }
	auth := aliceAuth()
	if managed {
		var profile CapabilityProfile
		c.Store, auth, _, profile = managedStore(t)
		profile.Revision++
		profile.SelfInstall = true
		profile.ControlOperations = []string{"prepare_source", "confirm", "enable"}
		if err := putProfile(t, c.Store, profile); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(owner identity.Envelope, bindingID string) (EffectiveBinding, error) {
		c.Store.mu.RLock()
		defer c.Store.mu.RUnlock()
		return c.Store.resolveLocked(owner, bindingID)
	}
	for _, principal := range []string{"alice", "bob"} {
		grantTestControlOperations(t, c.Store, principal, "prepare_source", "confirm", "enable")
		if err := putGrant(t, c.Store, OperatorGrant(GrantSelfInstall, principal, "", "")); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.registerAdmittedDefinition(&definition, auth.PrincipalID); err != nil {
		t.Fatal(err)
	}
	original, err := c.newOnboarding(auth, OnboardingSelfInstall, "read", definition, entry.Source.Repository, entry.Source.CommitSHA)
	if err != nil {
		t.Fatal(err)
	}
	original.BrokerCredentialID, original.Locator = "owner-session", "owner-session"
	if err := c.Store.PutOnboarding(original); err != nil {
		t.Fatal(err)
	}
	bind, err := c.materializeBinding(t.Context(), auth, original, definition)
	if err != nil {
		t.Fatal(err)
	}
	original.BindingID, original.ConnectionID, original.Phase = bind.ToolBindingID, bind.ConnectionID, PhaseEnabled
	if err := c.Store.PutOnboarding(original); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"telegram_send": true, "onboarding_id": original.OnboardingID}
	for _, invalid := range []map[string]any{{"telegram_send": false}, {"telegram_send": "true"}, {"telegram_send": true}, {"telegram_send": true, "onboarding_id": original.OnboardingID, "source": entry.Source.Repository}} {
		if _, err := c.Invoke(t.Context(), auth, "prepare_source", invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid selection accepted: %v", err)
		}
	}
	bob := identity.TelegramEnvelope("bob", 8, "runtime", "policy-1")
	if _, err := c.Invoke(t.Context(), bob, "prepare_source", args); err == nil || grants.Load() != 1 {
		t.Fatal("foreign session reached Broker")
	}
	c.AdmitWithCredentials = func(context.Context, ToolDefinition, map[string]string) (ToolDefinition, error) {
		return ToolDefinition{}, errors.New("provider echoed owner-session-secret")
	}
	if _, err := c.Invoke(t.Context(), auth, "prepare_source", args); err == nil || strings.Contains(err.Error(), "owner-session-secret") {
		t.Fatal("failed admission succeeded or exposed session")
	}
	if _, err := resolve(auth, original.BindingID); err != nil || released.Load() != 1 || revoked.Load() != 1 {
		t.Fatal("failed probe cut the working connection or leaked lease/grant")
	}
	var probes atomic.Int32
	c.AdmitWithCredentials = func(_ context.Context, draft ToolDefinition, values map[string]string) (ToolDefinition, error) {
		probes.Add(1)
		if draft.RuntimeEnvironment["TELEGRAM_EXPOSED_TOOLS"] != "read-only+send_message" || values["TELEGRAM_SESSION_STRING"] != "owner-session-secret" {
			t.Error("existing session or restricted send mode missing")
		}
		packet := ImportedArtifact{Definition: draft}
		_, err := ApplyPreflightToolList(&packet, append(append([]ToolSpec(nil), draft.Tools...), ToolSpec{Name: "send_message", Effect: WriteEffect, InputSchema: json.RawMessage(`{"type":"object"}`)}))
		return packet.Definition, err
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if body, err := c.Invoke(t.Context(), auth, "prepare_source", args); err != nil || body["phase"] != PhaseAwaitingConfirm {
				t.Errorf("send preparation: %v %v", body, err)
			}
		})
	}
	wg.Wait()
	upgrade, ok := c.Store.FindOnboardingByKey(auth, "telegram-send:"+original.OnboardingID)
	if !ok || probes.Load() != 1 || upgrade.BrokerCredentialID != original.BrokerCredentialID || upgrade.BrokerRequestID != "" || upgrade.DefinitionVersion == original.DefinitionVersion {
		t.Fatal("upgrade duplicated probe, login or session, or replaced immutable read version")
	}
	if _, err := c.Invoke(t.Context(), auth, "confirm", map[string]any{"onboarding_id": upgrade.OnboardingID, "nonce": upgrade.ConfirmationNonce}); err != nil {
		t.Fatal(err)
	}
	body, err := c.Invoke(t.Context(), auth, "enable", map[string]any{"onboarding_id": upgrade.OnboardingID})
	if err != nil || body["phase"] != PhaseEnabled {
		t.Fatalf("enable: %v %v", body, err)
	}
	upgrade, _ = c.Store.OnboardingFor(auth, upgrade.OnboardingID)
	if _, err := resolve(auth, original.BindingID); err == nil {
		t.Fatal("old binding still authorized")
	}
	effective, err := resolve(auth, upgrade.BindingID)
	if err != nil || effective.Credential.Locator != original.BrokerCredentialID || len(effective.Definition.Tools) != 2 || definition.RuntimeEnvironment["TELEGRAM_EXPOSED_TOOLS"] != "read-only" {
		t.Fatal("send upgrade lost session or mutated read definition")
	}
	if _, err := resolve(bob, upgrade.BindingID); err == nil {
		t.Fatal("foreign user can resolve write binding")
	}
	if managed {
		tools, err := c.Store.ListProjectedTools(auth)
		if err != nil || len(tools) != 3 {
			t.Fatalf("managed projection lost read or send tools: %v %v", tools, err)
		}
	}
}

func TestTelegramSendRejectsUnreadyOriginal(t *testing.T) {
	c := &ControlPlane{Store: NewStore()}
	auth := aliceAuth()
	grantTestControlOperations(t, c.Store, "alice", "prepare_source")
	if err := putGrant(t, c.Store, OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"telegram_send": true, "onboarding_id": "missing"}
	if _, err := c.Invoke(t.Context(), auth, "prepare_source", args); err == nil {
		t.Fatal("missing onboarding accepted")
	}
	original := Onboarding{Schema: SchemaVersion, OnboardingID: "telegram-read", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: OnboardingSelfInstall, Phase: PhaseAwaitingConfirm, SourceURL: "https://github.com/example/mcp", CommitSHA: "0123456789abcdef0123456789abcdef01234567", IdempotencyKey: "k1", Revision: 1, CreatedAt: time.Now()}
	if err := c.Store.PutOnboarding(original); err != nil {
		t.Fatal(err)
	}
	args["onboarding_id"] = original.OnboardingID
	if _, err := c.Invoke(t.Context(), auth, "prepare_source", args); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unconfirmed session accepted: %v", err)
	}
	original.Phase, original.BrokerCredentialID = PhaseEnabled, "credential"
	if err := c.Store.PutOnboarding(original); err != nil {
		t.Fatal(err)
	}
	upgrade := original
	upgrade.OnboardingID, upgrade.IdempotencyKey, upgrade.Phase = "telegram-send-upgrade", "telegram-send:"+original.OnboardingID, PhaseRemoved
	if err := c.Store.PutOnboarding(upgrade); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Invoke(t.Context(), auth, "prepare_source", args); !errors.Is(err, ErrRevoked) {
		t.Fatalf("removed upgrade accepted: %v", err)
	}
}

func TestTelegramSendRejectsContractDrift(t *testing.T) {
	base := statefulContainerDefinition()
	base.Tools = []ToolSpec{{Name: "read", Effect: ReadEffect}}
	for _, scenario := range []string{"valid", "whitespace", "extra-write", "missing-read", "read-effect", "image", "env", "missing-send", "unconfirmed"} {
		t.Run(scenario, func(t *testing.T) {
			packet := ImportedArtifact{Definition: base}
			expected := base
			tools := append(append([]ToolSpec(nil), base.Tools...), ToolSpec{Name: "send_message", Effect: WriteEffect, InputSchema: json.RawMessage(`{"type":"object"}`)})
			if scenario == "whitespace" {
				expected.Tools = []ToolSpec{{Name: "read", Effect: ReadEffect, InputSchema: json.RawMessage(`{"type":"object"}`)}}
				tools[0].InputSchema = json.RawMessage("{\n  \"type\": \"object\"\n}")
			}
			switch scenario {
			case "extra-write":
				tools = append(tools, ToolSpec{Name: "delete_message", Effect: WriteEffect})
			case "missing-read":
				tools = tools[1:]
			case "read-effect":
				tools[0].Effect = WriteEffect
			case "missing-send":
				tools = tools[:1]
			}
			if _, err := ApplyPreflightToolList(&packet, tools); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "image":
				packet.Definition.Source.Image = "unreviewed"
			case "env":
				packet.Definition.RuntimeEnvironment = map[string]string{"TELEGRAM_EXPOSED_TOOLS": "all"}
			case "unconfirmed":
				packet.Definition.Source.ToolContractDigest = ""
			}
			if err := validateTelegramSendUpgrade(expected, packet.Definition); (err == nil) != (scenario == "valid" || scenario == "whitespace") {
				t.Fatalf("unexpected admission: %v", err)
			}
		})
	}
}
