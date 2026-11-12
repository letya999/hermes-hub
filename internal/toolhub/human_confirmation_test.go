package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func humanConsentContext(t *testing.T, control *ControlPlane, id string) context.Context {
	t.Helper()
	o := mustOnboarding(t, control.Store, id)
	proof := api.HumanConfirmation{OnboardingID: id, Actor: onboardingEnvelope(o), Nonce: o.ConfirmationNonce, Revision: api.ConfirmationRevision(o), ActionDigest: api.ConfirmationActionDigest(o), ExpiresAt: o.ConfirmationExpires}
	return context.WithValue(t.Context(), humanConfirmationKey{}, proof)
}

func finishAsHuman(t *testing.T, control *ControlPlane, id string) {
	t.Helper()
	o := mustOnboarding(t, control.Store, id)
	proof, err := json.Marshal(api.HumanConfirmation{OnboardingID: id, Actor: onboardingEnvelope(o), Nonce: o.ConfirmationNonce, Revision: api.ConfirmationRevision(o), ActionDigest: api.ConfirmationActionDigest(o), ExpiresAt: o.ConfirmationExpires})
	if err != nil {
		t.Fatal(err)
	}
	enrolled := onboardingEnvelope(o)
	for _, profile := range control.Store.capabilityProfiles {
		if profile.PrincipalID == o.PrincipalID && profile.ContextID == o.ContextID && profile.RuntimeID == o.RuntimeID && profile.PolicyVersion == o.PolicyVersion {
			enrolled.CapabilityProfile, enrolled.Environment, enrolled.Generation = profile.ProfileID, profile.Environment, profile.Generation
			break
		}
	}
	g := &Gateway{Store: control.Store, Control: control, ControlToken: testHumanControlToken, Tokens: map[string]identity.Envelope{aliceToken: enrolled}}
	req := httptest.NewRequest(http.MethodPost, "/v1/confirmations", bytes.NewReader(proof))
	req.Header.Set("Authorization", "Bearer "+testHumanControlToken)
	rec := httptest.NewRecorder()
	g.serveHumanConfirmation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trusted human confirmation failed: %d", rec.Code)
	}
}

func TestEnableResumesConsumedHumanConfirmationAfterAdmissionFailure(t *testing.T) {
	store := NewStore()
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	grantTestControlOperations(t, store, auth.PrincipalID, "confirm", "enable")
	on := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-resume-consent", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingCatalog, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, Phase: api.PhaseAwaitingConfirm,
		ConfirmationNonce: "synthetic-nonce", ConfirmationExpires: time.Now().Add(time.Hour), Revision: 1}
	if err := store.PutOnboarding(on); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Now: time.Now, Ready: func(context.Context, api.EffectiveBinding) error { return nil }}
	if _, err := control.Invoke(t.Context(), auth, "enable", map[string]any{"onboarding_id": on.OnboardingID}); err == nil {
		t.Fatal("unconfirmed onboarding enabled")
	}
	proof := api.HumanConfirmation{OnboardingID: on.OnboardingID, Actor: auth, Nonce: on.ConfirmationNonce, Revision: api.ConfirmationRevision(on), ActionDigest: api.ConfirmationActionDigest(on), ExpiresAt: on.ConfirmationExpires}
	if _, err := store.ConsumeHumanConfirmation(proof, time.Now()); err != nil {
		t.Fatal(err)
	}
	status, err := control.connectorStatus(t.Context(), auth, on.OnboardingID, "")
	if err != nil || status.NextAction != "enable" {
		t.Fatalf("consumed consent has no resumable action: %+v %v", status, err)
	}
	if _, err := control.Invoke(t.Context(), auth, "enable", map[string]any{"onboarding_id": on.OnboardingID}); err != nil {
		t.Fatalf("verified consent could not resume: %v", err)
	}
	after, err := store.onboarding(on.OnboardingID)
	if err != nil || after.Phase != api.PhaseEnabled || after.BindingID == "" {
		t.Fatalf("resumed state phase=%s bound=%t err=%v", after.Phase, after.BindingID != "", err)
	}
}

func TestManagedRevocationWinsHumanConfirmation(t *testing.T) {
	for _, name := range []string{"profile", "policy", "connectors", "self-install", "control-operations"} {
		t.Run(name, func(t *testing.T) {
			store, auth, policy, profile := managedStore(t)
			profile.Connectors, profile.SelfInstall = api.ConnectorsPersonal, true
			selection := profile.Selections[0]
			record := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-managed-human", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingConfirm, Revision: 1, DefinitionID: selection.DefinitionID, DefinitionVersion: selection.DefinitionVersion, PendingWrites: true, ConfirmationNonce: "managed-nonce", ConfirmationExpires: time.Now().Add(time.Minute)}
			if err := store.PutOnboarding(record); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "control-operations":
				profile.Connectors = ""
			case "profile":
				profile.Status = api.RevokedStatus
			case "policy":
				policy.Status = api.RevokedStatus
			case "connectors":
				profile.Connectors = api.ConnectorsNone
			case "self-install":
				profile.SelfInstall = false
			}
			store.capabilityProfiles[profile.ProfileID], store.capabilityPolicies[policy.PolicyID] = profile, policy
			g := &Gateway{Store: store, Control: &ControlPlane{Store: store}, ControlToken: testHumanControlToken, Tokens: map[string]identity.Envelope{aliceToken: auth}}
			body, _ := json.Marshal(api.HumanConfirmation{OnboardingID: record.OnboardingID, Actor: onboardingEnvelope(record), Nonce: record.ConfirmationNonce, Revision: api.ConfirmationRevision(record), ActionDigest: api.ConfirmationActionDigest(record), ExpiresAt: record.ConfirmationExpires})
			req := httptest.NewRequest(http.MethodPost, "/v1/confirmations", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+testHumanControlToken)
			rec := httptest.NewRecorder()
			g.serveHumanConfirmation(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("revoked authority accepted confirmation: %d", rec.Code)
			}
			if latest := mustOnboarding(t, store, record.OnboardingID); latest.WritesEnabled || latest.ConfirmationUsed {
				t.Error("revoked authority changed state")
			}
			if name == "control-operations" {
				profile.ControlOperations = []string{"confirm"}
				store.capabilityProfiles[profile.ProfileID] = profile
				req := httptest.NewRequest(http.MethodPost, "/v1/confirmations", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+testHumanControlToken)
				rec := httptest.NewRecorder()
				g.serveHumanConfirmation(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("explicit managed confirm authority denied: %d", rec.Code)
				}
			}
		})
	}
}

func TestHumanConsentDoesNotAliasStoreCallers(t *testing.T) {
	store := NewStore()
	record := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-copy", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingCatalog, Phase: api.PhaseConfirmed, Revision: 1, HumanConsent: &api.HumanConsent{ActorID: "telegram-7", ActionDigest: "sha256:" + strings.Repeat("a", 64), Revision: 1, ConfirmedAt: time.Now()}}
	if err := store.PutOnboarding(record); err != nil {
		t.Fatal(err)
	}
	record.HumanConsent.ActorID = "forged-input"
	visible, err := store.OnboardingFor(aliceAuth(), record.OnboardingID)
	if err != nil || visible.HumanConsent.ActorID != "telegram-7" {
		t.Errorf("input aliases consent: %+v %v", visible.HumanConsent, err)
	}
	visible.HumanConsent.ActorID = "forged-output"
	stored, err := store.OnboardingFor(aliceAuth(), record.OnboardingID)
	if err != nil || stored.HumanConsent.ActorID != "telegram-7" {
		t.Errorf("output aliases consent: %+v %v", stored.HumanConsent, err)
	}
}

func TestHumanActorConsentIsDurableBeforeMaterialization(t *testing.T) {
	fix := newControlFixture(t, fixtureReviewer(userMCPDefinition()))
	definition := catalogReadDefinition()
	if err := fix.store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	prepared, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "durable-human"})
	if err != nil {
		t.Fatal(err)
	}
	id := prepared["onboarding_id"].(string)
	record := mustOnboarding(t, fix.store, id)
	path := filepath.Join(t.TempDir(), "toolhub.json")
	if err := fix.store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fix.store, fix.control.Store, fix.gateway.Store = loaded, loaded, loaded
	ready := 0
	fix.control.Ready = func(context.Context, api.EffectiveBinding) error {
		ready++
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot struct {
			Onboardings []map[string]any `json:"onboardings"`
		}
		if err := json.Unmarshal(body, &snapshot); err != nil {
			t.Fatal(err)
		}
		for _, row := range snapshot.Onboardings {
			if row["onboarding_id"] != id {
				continue
			}
			consent, ok := row["human_consent"].(map[string]any)
			if !ok || consent["actor_id"] != "telegram-7" || consent["action_digest"] != api.ConfirmationActionDigest(record) || consent["revision"] != float64(record.Revision) || consent["confirmed_at"] == "" || consent["nonce"] != nil {
				t.Errorf("durable safe actor consent missing before effect: %v", consent)
			}
			return nil
		}
		t.Error("onboarding disappeared before effect")
		return nil
	}
	proof, err := json.Marshal(api.HumanConfirmation{OnboardingID: id, Actor: aliceAuth(), Nonce: record.ConfirmationNonce, Revision: api.ConfirmationRevision(record), ActionDigest: api.ConfirmationActionDigest(record), ExpiresAt: record.ConfirmationExpires})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/confirmations", bytes.NewReader(proof))
	req.Header.Set("Authorization", "Bearer "+testHumanControlToken)
	rec := httptest.NewRecorder()
	fix.gateway.serveHumanConfirmation(rec, req)
	if rec.Code != http.StatusOK || ready == 0 {
		t.Fatalf("confirmation did not exercise materialization: status=%d ready=%d", rec.Code, ready)
	}
}

func TestTrustedHumanConfirmationBindsActorActionRevisionAndExpiry(t *testing.T) {
	for _, name := range []string{"valid", "agent", "origin", "actor", "context", "runtime", "policy", "action", "revision", "expiry", "proof-expiry", "nonce", "unknown-field", "replay", "shared-agent", "missing-service", "missing-control", "method", "unknown", "empty-nonce", "phase", "revoked-grant", "unenrolled"} {
		t.Run(name, func(t *testing.T) {
			fix := newControlFixture(t, fixtureReviewer(userMCPDefinition()))
			fix.gateway.ControlToken = "synthetic-service-confirmation-bearer"
			definition := catalogReadDefinition()
			if err := fix.store.RegisterDefinition(definition); err != nil {
				t.Fatal(err)
			}
			if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
				t.Fatal(err)
			}
			prepared, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "human-proof"})
			if err != nil {
				t.Fatal(err)
			}
			record := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
			actor := aliceAuth()
			proof := map[string]any{"onboarding_id": record.OnboardingID, "nonce": record.ConfirmationNonce, "revision": record.Revision, "action_digest": api.ConfirmationActionDigest(record), "expires_at": record.ConfirmationExpires, "actor": actor}
			token, origin, method := fix.gateway.ControlToken, "", http.MethodPost
			switch name {
			case "unenrolled":
				delete(fix.gateway.Tokens, aliceToken)
			case "revoked-grant":
				for _, grant := range fix.store.grants {
					if grant.PrincipalID == "alice" && grant.Operation == "confirm" {
						grant.Status = api.RevokedStatus
						grant.Revision++
						if err := putGrant(t, fix.store, grant); err != nil {
							t.Fatal(err)
						}
						break
					}
				}
			case "agent":
				token = aliceToken
			case "phase":
				record.Phase = api.PhaseDisabled
				record.Revision++
				if err := fix.store.PutOnboarding(record); err != nil {
					t.Fatal(err)
				}
			case "empty-nonce":
				proof["nonce"] = ""
			case "unknown":
				proof["onboarding_id"] = "missing"
			case "method":
				method = http.MethodGet
			case "missing-control":
				fix.gateway.Control = nil
			case "missing-service":
				fix.gateway.ControlToken = ""
			case "shared-agent":
				token, fix.gateway.ControlToken = aliceToken, aliceToken
			case "origin":
				origin = "https://untrusted.example"
			case "actor":
				actor = bobAuth()
			case "context":
				actor.ContextID = "other"
			case "runtime":
				actor.RuntimeID = "other"
			case "policy":
				actor.PolicyVersion = "other"
			case "action":
				proof["action_digest"] = "sha256:wrong"
			case "revision":
				proof["revision"] = record.Revision + 1
			case "expiry":
				fix.control.Now = func() time.Time { return record.ConfirmationExpires.Add(time.Second) }
			case "proof-expiry":
				proof["expires_at"] = record.ConfirmationExpires.Add(time.Minute)
			case "nonce":
				proof["nonce"] = "wrong"
			case "unknown-field":
				proof["extra"] = "untrusted"
			}
			proof["actor"] = actor
			post := func() int {
				body, err := json.Marshal(proof)
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequest(method, fix.server.URL+"/v1/confirmations", bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Origin", origin)
				resp, err := fix.server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}
			want := http.StatusForbidden
			if name == "valid" || name == "replay" {
				want = http.StatusOK
			}
			if got := post(); got != want {
				t.Errorf("callback status=%d want=%d", got, want)
			}
			latest := mustOnboarding(t, fix.store, record.OnboardingID)
			if want == http.StatusOK {
				if latest.Phase != api.PhaseEnabled || !latest.ConfirmationUsed {
					t.Errorf("exact human confirmation did not enable once: phase=%s used=%v", latest.Phase, latest.ConfirmationUsed)
				}
				if name == "replay" && post() != http.StatusForbidden {
					t.Error("spent proof replay accepted")
				}
			} else if latest.ConfirmationUsed || latest.BindingID != "" {
				t.Error("rejected proof changed authority")
			}
		})
	}
}

func TestBearerConfirmationPagesCannotGrantHumanConsent(t *testing.T) {
	for _, wizard := range []bool{false, true} {
		fix := newControlFixture(t, fixtureReviewer(userMCPDefinition()))
		definition := catalogReadDefinition()
		if err := fix.store.RegisterDefinition(definition); err != nil {
			t.Fatal(err)
		}
		if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
			t.Fatal(err)
		}
		prepared, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "bearer-page"})
		if err != nil {
			t.Fatal(err)
		}
		record := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
		path := "http://127.0.0.1/confirm/" + record.OnboardingID + "?t=" + record.ConfirmationNonce
		if wizard {
			path = "http://127.0.0.1/connect/" + record.OnboardingID + "?k=" + record.WizardNonce
		}
		view, get := confirmLinkRequest(t, http.MethodGet, path)
		if wizard {
			fix.gateway.serveConnect(view, get)
		} else {
			fix.gateway.serveConfirm(view, get)
		}
		if view.Code != http.StatusOK || !strings.Contains(view.Body.String(), "в исходном чате") || strings.Contains(view.Body.String(), "<form") {
			t.Errorf("wizard=%v: page still offers unverified consent", wizard)
		}
		rec, req := confirmLinkRequest(t, http.MethodPost, path)
		if wizard {
			fix.gateway.serveConnect(rec, req)
		} else {
			fix.gateway.serveConfirm(rec, req)
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("wizard=%v: bearer POST granted consent: %d", wizard, rec.Code)
		}
		latest := mustOnboarding(t, fix.store, record.OnboardingID)
		if latest.ConfirmationUsed || latest.BindingID != "" {
			t.Errorf("wizard=%v: unverified actor changed authority", wizard)
		}
	}
}

// A proof rejected because the store cannot persist is a storage failure,
// not an authorization denial: answering 403 would tell the operator a
// forged or replayed confirmation was refused, hiding a real durability
// problem that needs a different response.
func TestHumanConfirmationStorageFailureIsNotAuthorizationDenial(t *testing.T) {
	fix := newControlFixture(t, fixtureReviewer(userMCPDefinition()))
	record := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-storage-fail", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingConfirm, Revision: 1, DefinitionID: "user-mcp", DefinitionVersion: "1.0.0", PendingWrites: true, ConfirmationNonce: "storage-nonce", ConfirmationExpires: time.Now().Add(time.Minute)}
	if err := fix.store.PutOnboarding(record); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "store.json")
	if err := fix.store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fix.store, fix.control.Store, fix.gateway.Store = loaded, loaded, loaded
	// Foreign write lands between load and consume: the next persist hits the
	// conflict fence — a storage failure, not a bad proof.
	if err := os.WriteFile(path, []byte(`{"schema":1,"definitions":[],"connections":[],"credential_references":[],"bindings":[],"workloads":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	record = mustOnboarding(t, fix.store, record.OnboardingID)
	proof, err := json.Marshal(api.HumanConfirmation{OnboardingID: record.OnboardingID, Actor: aliceAuth(), Nonce: record.ConfirmationNonce, Revision: api.ConfirmationRevision(record), ActionDigest: api.ConfirmationActionDigest(record), ExpiresAt: record.ConfirmationExpires})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/confirmations", bytes.NewReader(proof))
	req.Header.Set("Authorization", "Bearer "+fix.gateway.ControlToken)
	rec := httptest.NewRecorder()
	fix.gateway.serveHumanConfirmation(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("storage failure answered as authorization denial: %d", rec.Code)
	}
	if latest := mustOnboarding(t, fix.store, record.OnboardingID); latest.ConfirmationUsed {
		t.Fatal("failed storage consumed the confirmation")
	}
}

// The review surface must show what the button grants: the action digest
// binds permissions and effects, so a page that hides them has the human
// confirming blind.
func TestConfirmPageShowsGrantedPermissionsAndEffects(t *testing.T) {
	page := confirmPage(api.Onboarding{DefinitionID: "github-mcp", Permissions: []string{"repo.read", "issues.write"}, Effects: []string{"исходники", "тикеты"}}, locale.LangRU)
	for _, want := range []string{"github-mcp", "repo.read", "issues.write", "исходники", "тикеты"} {
		if !strings.Contains(page, want) {
			t.Errorf("confirm page hides %q from the consenting human", want)
		}
	}
}

// Consuming a human consent is a one-time authority grant; it must land in
// the durable control audit like every other admission/outcome event so a
// later review can prove who confirmed what.
func TestHumanConfirmationWritesControlAudit(t *testing.T) {
	fix := newControlFixture(t, fixtureReviewer(userMCPDefinition()))
	record := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-audit", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingConfirm, Revision: 1, DefinitionID: "user-mcp", DefinitionVersion: "1.0.0", PendingWrites: true, ConfirmationNonce: "audit-nonce", ConfirmationExpires: time.Now().Add(time.Minute)}
	if err := fix.store.PutOnboarding(record); err != nil {
		t.Fatal(err)
	}
	record = mustOnboarding(t, fix.store, record.OnboardingID)
	var events []string
	var fields map[string]string
	fix.gateway.AuditWrite = func(event string, f map[string]string) error {
		events = append(events, event)
		fields = f
		return nil
	}
	proof, err := json.Marshal(api.HumanConfirmation{OnboardingID: record.OnboardingID, Actor: aliceAuth(), Nonce: record.ConfirmationNonce, Revision: api.ConfirmationRevision(record), ActionDigest: api.ConfirmationActionDigest(record), ExpiresAt: record.ConfirmationExpires})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/confirmations", bytes.NewReader(proof))
	req.Header.Set("Authorization", "Bearer "+fix.gateway.ControlToken)
	rec := httptest.NewRecorder()
	fix.gateway.serveHumanConfirmation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid confirmation rejected: %d", rec.Code)
	}
	if !slices.Contains(events, "human_confirmation") || fields["onboarding_id"] != record.OnboardingID || fields["outcome"] != "allow" {
		t.Fatalf("confirmation missing durable audit: %v %v", events, fields)
	}
}

func TestHumanConfirmationRejectsOversizedAndTrailingJSON(t *testing.T) {
	for _, suffix := range []bool{false, true} {
		fix := newControlFixture(t, fixtureReviewer(userMCPDefinition()))
		fix.gateway.ControlToken = "synthetic-service-confirmation-bearer"
		definition := catalogReadDefinition()
		if err := fix.store.RegisterDefinition(definition); err != nil {
			t.Fatal(err)
		}
		if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
			t.Fatal(err)
		}
		prepared, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "bounded-human-proof"})
		if err != nil {
			t.Fatal(err)
		}
		record := mustOnboarding(t, fix.store, prepared["onboarding_id"].(string))
		body, err := json.Marshal(api.HumanConfirmation{OnboardingID: record.OnboardingID, Actor: aliceAuth(), Nonce: record.ConfirmationNonce, Revision: api.ConfirmationRevision(record), ActionDigest: api.ConfirmationActionDigest(record), ExpiresAt: record.ConfirmationExpires})
		if err != nil {
			t.Fatal(err)
		}
		if suffix {
			body = append(body, []byte("\n{}")...)
		} else {
			body = append([]byte(strings.Repeat(" ", 17<<10)), body...)
		}
		req, err := http.NewRequest(http.MethodPost, fix.server.URL+"/v1/confirmations", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+fix.gateway.ControlToken)
		resp, err := fix.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("suffix=%v: malformed proof accepted: %d", suffix, resp.StatusCode)
		}
		if latest := mustOnboarding(t, fix.store, record.OnboardingID); latest.ConfirmationUsed || latest.BindingID != "" {
			t.Errorf("suffix=%v: malformed proof changed authority", suffix)
		}
	}
}
