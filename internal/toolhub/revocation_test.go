package toolhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestRevokePrincipalAuthorityLeavesSiblingAuthority(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	bobGrant := api.OperatorGrant(api.GrantDefinition, "bob", "google-work", "1.0.0")
	if err := putGrant(t, store, bobGrant); err != nil {
		t.Fatal(err)
	}
	report, err := store.RevokePrincipalAuthority(auth.PrincipalID, auth.ContextID, time.Now().UTC())
	if err != nil || report.Profiles != 1 || report.Grants != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := store.capabilityProfiles[profile.ProfileID]; got.Status != api.RevokedStatus || got.IssuedBy != "platform" {
		t.Fatalf("alice profile=%+v", got)
	}
	for id, grant := range store.grants {
		if grant.PrincipalID == "alice" && grant.Status != api.RevokedStatus {
			t.Fatalf("alice grant %s still %s", id, grant.Status)
		}
		if grant.PrincipalID == "bob" && grant.Status != api.ActiveStatus {
			t.Fatalf("bob grant %s revoked", id)
		}
	}
	if report, err := store.RevokePrincipalAuthority(auth.PrincipalID, auth.ContextID, time.Now().UTC()); err != nil || report.Profiles != 0 || report.Grants != 0 {
		t.Fatalf("second report=%+v err=%v", report, err)
	}
}

func TestRevokePrincipalConnectorsPreservesSharedOwnership(t *testing.T) {
	store, _, aliceBinding := seededStore(t)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	shared := api.Connection{Schema: api.SchemaVersion, ConnectionID: "shared-conn", Owner: api.OwnerRef{Type: api.TeamOwner, ID: "team-1"}, DefinitionID: definition.DefinitionID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(shared); err != nil {
		t.Fatal(err)
	}
	binding := func(principal, contextID string) api.ToolBinding {
		b := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: principal, ContextID: contextID, RuntimeID: "runtime-" + principal, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: shared.ConnectionID, ConnectionRevision: shared.Revision, PolicyVersion: "policy-1", WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
		b.ToolBindingID = api.DeterministicBindingID(b.PrincipalID, b.ContextID, b.RuntimeID, b.DefinitionID, b.DefinitionVersion, b.ConnectionID, "")
		return b
	}
	if err := store.PutBinding(binding("alice", "alice")); err != nil {
		t.Fatal(err)
	}
	bobBinding := binding("bob", "bob")
	if err := store.PutBinding(bobBinding); err != nil {
		t.Fatal(err)
	}
	report, err := store.RevokePrincipalConnectors("alice", "alice")
	if err != nil || report.Bindings != 2 || report.Connections != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if got := store.bindings[aliceBinding.ToolBindingID]; got.Status != api.RevokedStatus {
		t.Fatalf("personal binding=%+v", got)
	}
	if got := store.bindings[api.DeterministicBindingID("alice", "alice", "runtime-alice", definition.DefinitionID, definition.Version, shared.ConnectionID, "")]; got.Status != api.RevokedStatus {
		t.Fatalf("alice shared-seat binding=%+v", got)
	}
	if got := store.connections[shared.ConnectionID]; got.Status != api.ActiveStatus {
		t.Fatalf("shared connection=%+v", got)
	}
	if got := store.bindings[bobBinding.ToolBindingID]; got.Status != api.ActiveStatus {
		t.Fatalf("bob shared-seat binding=%+v", got)
	}
}

func TestPrincipalRevocationHTTPContract(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	g := &Gateway{Store: store, ControlToken: "control", Tokens: map[string]identity.Envelope{"principal-token": auth}}
	valid := `{"principal_id":"alice","context_id":"alice","stage":"toolhub"}`
	for _, tc := range []struct {
		name             string
		token, principal string
		body, origin     string
		want             int
	}{
		{"valid", "control", "alice", valid, "", http.StatusOK},
		{"method", "control", "alice", valid, "", http.StatusOK},
		{"browser origin", "control", "alice", valid, "https://example.test", http.StatusForbidden},
		{"wrong bearer", "wrong", "alice", valid, "", http.StatusUnauthorized},
		{"foreign body", "control", "alice", `{"principal_id":"bob","context_id":"alice","stage":"toolhub"}`, "", http.StatusBadRequest},
		{"trailing json", "control", "alice", valid + ` {"x":1}`, "", http.StatusBadRequest},
		{"unknown stage", "control", "alice", `{"principal_id":"alice","context_id":"alice","stage":"delivery"}`, "", http.StatusBadRequest},
		{"unenrolled", "control", "bob", `{"principal_id":"bob","context_id":"bob","stage":"toolhub"}`, "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := http.MethodPost
			if tc.name == "method" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, "/v1/admin/revocations", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+tc.token)
			req.Header.Set("X-Hub-Principal", tc.principal)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			g.servePrincipalRevocation(w, req)
			want := tc.want
			if tc.name == "method" {
				want = http.StatusMethodNotAllowed
			}
			if w.Code != want {
				t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body)
			}
		})
	}
	if got := store.capabilityProfiles[profile.ProfileID]; got.Status != api.RevokedStatus {
		t.Fatalf("profile after HTTP revoke=%+v", got)
	}
}

func TestCronRevocationUsesEnrolledScopeAfterChannelRetirement(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	g := &Gateway{Store: store, ControlToken: "control", Tokens: map[string]identity.Envelope{"principal-token": auth}}
	calls := 0
	g.pauseSchedules = func(_ context.Context, got identity.Envelope) (int, error) {
		calls++
		if got.PrincipalID != auth.PrincipalID || got.ContextID != auth.ContextID {
			t.Error("cron revocation used an unenrolled scope")
		}
		return 1, nil
	}
	for _, tc := range []struct {
		contextID string
		want      int
	}{{"foreign", http.StatusUnauthorized}, {"alice", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/revocations", strings.NewReader(`{"principal_id":"alice","context_id":"`+tc.contextID+`","stage":"cron"}`))
		req.Header.Set("Authorization", "Bearer control")
		req.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.servePrincipalRevocation(w, req)
		if w.Code != tc.want {
			t.Fatalf("cron revocation of %s returned %d", tc.contextID, w.Code)
		}
	}
	if calls != 1 {
		t.Fatalf("cron revocation calls: %d", calls)
	}
}
