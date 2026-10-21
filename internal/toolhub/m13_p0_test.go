package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CHG-0127 S0.1: /release purge must drop named state volumes even when the
// workload is absent from the controller map; non-purge keeps them.

func releaseRequest(t *testing.T, c *genericController, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/release", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	c.handler(token).ServeHTTP(w, r)
	return w
}

func newPurgeTestController(t *testing.T) (*genericController, *[][]string, string) {
	t.Helper()
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	budget, _ := api.NewWorkloadBudget(4, time.Minute)
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	c.command = func(_ context.Context, bin string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{bin}, args...))
		if bin == "docker" && len(args) > 1 && args[0] == "volume" && args[1] == "ls" {
			return []byte("orphan-state-0\nother-state-0\n"), nil
		}
		return nil, nil
	}
	return c, &calls, strings.Repeat("p", 32)
}

func dockerCallArgs(calls [][]string) [][]string {
	var out [][]string
	for _, call := range calls {
		if call[0] == "docker" {
			out = append(out, call[1:])
		}
	}
	return out
}

func hasDockerCall(calls [][]string, want ...string) bool {
	for _, call := range calls {
		if slices.Equal(call, want) {
			return true
		}
	}
	return false
}

func TestReleasePurgeAbsentWorkloadCleansByDeterministicName(t *testing.T) {
	c, calls, token := newPurgeTestController(t)
	// No workload registered: the map is empty, only deterministic names exist.
	if w := releaseRequest(t, c, token, `{"workload_id":"orphan","purge":true}`); w.Code != http.StatusNoContent {
		t.Fatalf("absent purge status: %d", w.Code)
	}
	docker := dockerCallArgs(*calls)
	if !hasDockerCall(docker, "rm", "--force", "orphan", "orphan-egress", "orphan-relay", "orphan-bridge-seed") {
		t.Fatalf("purge skipped deterministic container names: %v", docker)
	}
	if !hasDockerCall(docker, "volume", "rm", "orphan-state-0") {
		t.Fatalf("purge left orphan state volume: %v", docker)
	}
	if hasDockerCall(docker, "volume", "rm", "other-state-0") {
		t.Fatalf("purge removed a foreign state volume: %v", docker)
	}
}

func TestReleasePurgeDeletesStatefulVolumes(t *testing.T) {
	c, calls, token := newPurgeTestController(t)
	d := c.config.Definition
	d.Workload.Stateful = true
	plan := api.ControllerPlan{WorkloadID: "w-state", DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Definition: d}
	c.workloads["w-state"] = genericWorkload{plan: plan, proxyName: "w-state-egress", proxyVol: "w-state-egress-config", stateVols: []string{"w-state-state-0"}}

	if w := releaseRequest(t, c, token, `{"workload_id":"w-state","purge":true}`); w.Code != http.StatusNoContent {
		t.Fatalf("purge release status: %d", w.Code)
	}
	if !hasDockerCall(dockerCallArgs(*calls), "volume", "rm", "w-state-state-0") {
		t.Fatalf("purge left state volume behind: %v", dockerCallArgs(*calls))
	}
}

func TestReleasePurgeSurfacesStateVolumeFailure(t *testing.T) {
	c, calls, token := newPurgeTestController(t)
	d := c.config.Definition
	d.Workload.Stateful = true
	plan := api.ControllerPlan{WorkloadID: "w-volfail", DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Definition: d}
	c.workloads["w-volfail"] = genericWorkload{plan: plan, proxyName: "w-volfail-egress", proxyVol: "w-volfail-egress-config", stateVols: []string{"w-volfail-state-0"}}
	c.command = func(_ context.Context, bin string, args ...string) ([]byte, error) {
		*calls = append(*calls, append([]string{bin}, args...))
		if bin == "docker" && len(args) > 1 && args[0] == "volume" && args[1] == "rm" {
			return nil, errors.New("volume in use")
		}
		return nil, nil
	}
	if w := releaseRequest(t, c, token, `{"workload_id":"w-volfail","purge":true}`); w.Code == http.StatusNoContent {
		t.Fatal("purge reported success while a state volume removal failed")
	}
}

func TestReleaseKeepPreservesStatefulVolumes(t *testing.T) {
	c, calls, token := newPurgeTestController(t)
	d := c.config.Definition
	d.Workload.Stateful = true
	plan := api.ControllerPlan{WorkloadID: "w-keep", DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Definition: d}
	c.workloads["w-keep"] = genericWorkload{plan: plan, proxyName: "w-keep-egress", proxyVol: "w-keep-egress-config", stateVols: []string{"w-keep-state-0"}}

	if w := releaseRequest(t, c, token, `{"workload_id":"w-keep"}`); w.Code != http.StatusNoContent {
		t.Fatalf("keep release status: %d", w.Code)
	}
	if hasDockerCall(dockerCallArgs(*calls), "volume", "rm", "w-keep-state-0") {
		t.Fatalf("disable removed state volume: %v", dockerCallArgs(*calls))
	}
}

func TestRemoveReportsCleanupIncompleteOnReleaseFailure(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	failRelease := true
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now, Release: func(_ context.Context, release api.ControllerRelease) error {
		if failRelease {
			return errors.New("controller unreachable")
		}
		return nil
	}}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "incomplete-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": confirmNonceOf(t, store, prepared["onboarding_id"])}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	blocked, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"], "remove_nonce": removeNonceOf(t, store, prepared["onboarding_id"])})
	if err != nil {
		t.Fatal(err)
	}
	if blocked["phase"] != "cleanup_incomplete" {
		t.Fatalf("failed cleanup reported phase=%v", blocked["phase"])
	}
	if latest, lerr := store.onboarding(prepared["onboarding_id"].(string)); lerr != nil || latest.Phase == api.PhaseRemoved {
		t.Fatalf("tombstone before converged cleanup: %+v", latest.Phase)
	}
	failRelease = false
	resumed, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if resumed["phase"] != api.PhaseRemoved {
		t.Fatalf("retry did not converge: phase=%v", resumed["phase"])
	}
}

// failingSecrets wraps credstore.Backend so a test can break Delete while the
// remaining calls behave as a permissive no-op.
type failingSecrets struct {
	credstore.Backend
	failDelete bool
	failStatus bool
}

func (f failingSecrets) Delete(locator, owner string) error {
	if f.failDelete {
		return errors.New("ciphertext store unreachable")
	}
	return nil
}

func (f failingSecrets) SetStatus(locator, owner, status string) error {
	if f.failStatus {
		return errors.New("ciphertext store unreachable")
	}
	return nil
}

// missingSecrets answers ErrNotFound for every locator: a broker-backed
// onboarding owns no local ciphertext to revoke or delete.
type missingSecrets struct {
	credstore.Backend
}

func (missingSecrets) SetStatus(locator, owner, status string) error {
	return credstore.ErrNotFound
}

func (missingSecrets) Delete(locator, owner string) error {
	return credstore.ErrNotFound
}

func TestRemoveToleratesMissingLocalCiphertext(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now, Secrets: missingSecrets{}}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "missing-cred-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": confirmNonceOf(t, store, prepared["onboarding_id"])}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.onboarding(prepared["onboarding_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	onboarding.Locator = "broker-held-locator"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	finished, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"], "remove_nonce": removeNonceOf(t, store, prepared["onboarding_id"])})
	if err != nil {
		t.Fatal(err)
	}
	if finished["phase"] != api.PhaseRemoved {
		t.Fatalf("broker-held locator blocked purge: %+v", finished)
	}
}

func TestRemoveReportsCleanupIncompleteOnCredentialDeleteFailure(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	secrets := &failingSecrets{failDelete: true}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now, Secrets: secrets}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "incomplete-cred-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": confirmNonceOf(t, store, prepared["onboarding_id"])}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.onboarding(prepared["onboarding_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	onboarding.Locator = "loc-fail"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	blocked, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"], "remove_nonce": removeNonceOf(t, store, prepared["onboarding_id"])})
	if err != nil {
		t.Fatal(err)
	}
	if blocked["phase"] != "cleanup_incomplete" {
		t.Fatalf("failed credential delete reported phase=%v", blocked["phase"])
	}
	secrets.failDelete = false
	resumed, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if resumed["phase"] != api.PhaseRemoved {
		t.Fatalf("retry after credential delete did not converge: phase=%v", resumed["phase"])
	}
}

func TestRevokeSurfacesCredentialTombstoneFailure(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	secrets := &failingSecrets{}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now, Secrets: secrets}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "revoke-tomb-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": confirmNonceOf(t, store, prepared["onboarding_id"])}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.onboarding(prepared["onboarding_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	onboarding.Locator = "loc-revoke"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	secrets.failStatus = true
	if _, err := control.Invoke(context.Background(), auth, "revoke", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err == nil {
		t.Fatal("revoke reported success while the credential tombstone write failed")
	}
}

type errStopper struct{}

var errRuntimeUnreachable = errors.New("runtime unreachable")

func (errStopper) Stop(string) error { return errRuntimeUnreachable }

func TestSetBindingStatusSurfacesStopFailure(t *testing.T) {
	store := NewStore()
	store.Stopper = errStopper{}
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	binding := api.ToolBinding{
		Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1,
	}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	workload, err := api.NewWorkloadInstance(binding, &api.OwnerRef{Type: api.ContextOwner, ID: auth.ContextID}, "", 1, time.Now().UTC(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status = api.RunningStatus
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	err = store.SetBindingStatus(binding.ToolBindingID, api.DisabledStatus)
	if err == nil || !errors.Is(err, errRuntimeUnreachable) {
		t.Fatalf("stop failure was silently discarded or masked: %v", err)
	}
}

// CHG-0127 S0.3: nothing the model can read may carry a secret link
// parameter — form/confirmation/remove nonces and OAuth state travel only in
// the channel notice, never in an agent-facing tool result.
func TestAgentFacingResultsCarryNoSecrets(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	seed := func(id, phase string, mutate func(*api.Onboarding)) string {
		onboarding := api.Onboarding{
			Schema: api.SchemaVersion, OnboardingID: id, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
			RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
			Mode: api.OnboardingCatalog, Phase: phase,
			DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		}
		if mutate != nil {
			mutate(&onboarding)
		}
		if err := store.PutOnboarding(onboarding); err != nil {
			t.Fatal(err)
		}
		return id
	}
	secret := "NONCE-SECRET-VALUE"
	cases := []struct {
		op       string
		selector map[string]any
		leak     string
	}{
		{"status", map[string]any{"onboarding_id": seed("onb-creds", api.PhaseAwaitingCreds, func(o *api.Onboarding) {
			o.AdmissionPending = true
			o.FormNonce = secret
			o.FormExpires = time.Now().Add(time.Hour)
		})}, secret},
		{"required_credentials", map[string]any{"onboarding_id": seed("onb-creds2", api.PhaseAwaitingCreds, func(o *api.Onboarding) {
			o.AdmissionPending = true
			o.FormNonce = secret
			o.FormExpires = time.Now().Add(time.Hour)
		})}, secret},
		{"status", map[string]any{"onboarding_id": seed("onb-conf", api.PhaseAwaitingConfirm, func(o *api.Onboarding) {
			o.ConfirmationNonce = secret
			o.ConfirmationExpires = time.Now().Add(time.Hour)
		})}, secret},
		{"required_credentials", map[string]any{"onboarding_id": seed("onb-oauth", api.PhaseAwaitingOAuth, func(o *api.Onboarding) { o.ProviderAuthorizationURL = "https://as.example/authorize?state=" + secret })}, secret},
	}
	for _, tc := range cases {
		body, err := control.Invoke(context.Background(), auth, tc.op, tc.selector)
		if err != nil {
			t.Fatalf("%s failed: %v", tc.op, err)
		}
		encoded, _ := json.Marshal(body)
		if strings.Contains(string(encoded), tc.leak) {
			t.Fatalf("%s leaked %q: %s", tc.op, tc.leak, encoded)
		}
		for _, key := range []string{"nonce", "remove_nonce", "form_url", "form_path", "authorization_url"} {
			if _, present := body[key]; present {
				t.Fatalf("%s exposed key %q: %s", tc.op, key, encoded)
			}
		}
	}
	preview, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": "onb-conf"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(preview)
	if _, present := preview["remove_nonce"]; present {
		t.Fatalf("remove preview leaked the nonce: %s", encoded)
	}
	latest, _ := store.onboarding("onb-conf")
	if strings.Contains(string(encoded), latest.RemoveNonce) {
		t.Fatalf("remove preview carried the minted nonce: %s", encoded)
	}
}

func TestRemovePreviewSendsConfirmationLink(t *testing.T) {
	var noticed []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/prepare-outcome" {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			noticed = append(noticed, payload)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-remove-link", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		ConfirmationNonce: "c-nonce", ConfirmationExpires: time.Now().Add(time.Hour),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	preview, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": "onb-remove-link"})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := preview["remove_nonce"]; leaked {
		t.Fatal("preview leaked the remove nonce")
	}
	if len(noticed) == 0 {
		t.Fatal("remove preview did not notify the channel")
	}
	link, _ := noticed[len(noticed)-1]["form_url"].(string)
	if !strings.Contains(link, "/remove/onb-remove-link") {
		t.Fatalf("notice carried no remove link: %v", noticed[len(noticed)-1])
	}
	latest, _ := store.onboarding("onb-remove-link")
	if !strings.Contains(link, "t="+latest.RemoveNonce) {
		t.Fatalf("notice link does not carry the minted nonce: %q", link)
	}
}

func TestExpiredConfirmationRenewalSendsLink(t *testing.T) {
	var noticed []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/prepare-outcome" {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			noticed = append(noticed, payload)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-expired-conf", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		ConfirmationNonce: "stale", ConfirmationExpires: time.Now().Add(-time.Hour),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": "onb-expired-conf"}); err != nil {
		t.Fatal(err)
	}
	latest, err := store.onboarding("onb-expired-conf")
	if err != nil || latest.ConfirmationNonce == "stale" || latest.ConfirmationNonce == "" {
		t.Fatalf("expired nonce was not renewed: %+v", latest)
	}
	if len(noticed) == 0 {
		t.Fatal("renewed confirmation did not notify the channel")
	}
	link, _ := noticed[len(noticed)-1]["form_url"].(string)
	if !strings.Contains(link, "/connect/onb-expired-conf?k=") || !strings.Contains(link, "k="+latest.WizardNonce) {
		t.Fatalf("notice carried no wizard link: %v", noticed[len(noticed)-1])
	}
}

func TestExpiredCredentialFormRenewalSendsLink(t *testing.T) {
	var noticed []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/prepare-outcome" {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			noticed = append(noticed, payload)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-expired-form", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingCreds, AdmissionPending: true,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		FormNonce: "stale", FormExpires: time.Now().Add(-time.Hour),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "required_credentials", map[string]any{"onboarding_id": "onb-expired-form"}); err != nil {
		t.Fatal(err)
	}
	latest, err := store.onboarding("onb-expired-form")
	if err != nil || latest.FormNonce == "stale" || latest.FormNonce == "" {
		t.Fatalf("expired form nonce was not renewed: %+v", latest)
	}
	if len(noticed) == 0 {
		t.Fatal("renewed credential form did not notify the channel")
	}
	link, _ := noticed[len(noticed)-1]["form_url"].(string)
	if !strings.Contains(link, "/connect/onb-expired-form?k=") || !strings.Contains(link, "k="+latest.WizardNonce) {
		t.Fatalf("notice carried no wizard link: %v", noticed[len(noticed)-1])
	}
}

func TestConfirmLinkPageExecutesAuthorization(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	nonce := "confirm-link-nonce"
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-confirm-link", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		ConfirmationNonce: nonce, ConfirmationExpires: time.Now().Add(time.Hour),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/confirm/onb-confirm-link?nonce="+nonce, nil)
	get.Host = "127.0.0.1"
	gateway.serveConfirm(rec, get)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("confirm page status=%d", rec.Code)
	}
	bad := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/confirm/onb-confirm-link?nonce=wrong", nil)
	badReq.Host = "127.0.0.1"
	gateway.serveConfirm(bad, badReq)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong nonce status=%d", bad.Code)
	}
	post := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/confirm/onb-confirm-link?nonce="+nonce, nil)
	postReq.Host = "127.0.0.1"
	gateway.serveConfirm(post, postReq)
	if post.Code != http.StatusOK {
		t.Fatalf("confirm post status=%d body=%s", post.Code, post.Body.String())
	}
	latest, err := store.onboarding("onb-confirm-link")
	if err != nil || (latest.Phase != api.PhaseConfirmed && latest.Phase != api.PhaseEnabled) {
		t.Fatalf("confirm link did not finish authorization: phase=%v err=%v", latest.Phase, err)
	}
}

func TestRemoveLinkPageExecutesRemoval(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-remove-page", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseEnabled,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": "onb-remove-page"}); err != nil {
		t.Fatal(err)
	}
	armed, err := store.onboarding("onb-remove-page")
	if err != nil || armed.RemoveNonce == "" {
		t.Fatalf("preview did not arm the nonce: %+v", armed)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	get := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/remove/onb-remove-page?nonce="+armed.RemoveNonce, nil)
	get.Host = "127.0.0.1"
	gateway.serveRemove(rec, get)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Fatalf("remove page status=%d", rec.Code)
	}
	bad := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/remove/onb-remove-page?nonce=wrong", nil)
	badReq.Host = "127.0.0.1"
	gateway.serveRemove(bad, badReq)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong nonce status=%d", bad.Code)
	}
	post := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/remove/onb-remove-page?nonce="+armed.RemoveNonce, nil)
	postReq.Host = "127.0.0.1"
	gateway.serveRemove(post, postReq)
	if post.Code != http.StatusOK {
		t.Fatalf("remove post status=%d body=%s", post.Code, post.Body.String())
	}
	latest, err := store.onboarding("onb-remove-page")
	if err != nil || latest.Phase != api.PhaseRemoved {
		t.Fatalf("remove link did not finish removal: phase=%v err=%v", latest.Phase, err)
	}
}

func TestRemovePurgeReleasesWorkloadWithPurgeFlag(t *testing.T) {
	store := NewStore()
	grantTestControlOperations(t, store, "alice", api.ControlOperations...)
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	var purged, kept []string
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now, Release: func(_ context.Context, release api.ControllerRelease) error {
		if release.Purge {
			purged = append(purged, release.WorkloadID)
		} else {
			kept = append(kept, release.WorkloadID)
		}
		return nil
	}}
	auth := aliceAuth()
	prepared, err := control.Invoke(context.Background(), auth, "prepare_source", map[string]any{"definition_id": definition.DefinitionID, "version": definition.Version, "request_key": "purge-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": confirmNonceOf(t, store, prepared["onboarding_id"])}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"], "remove_nonce": removeNonceOf(t, store, prepared["onboarding_id"])}); err != nil {
		t.Fatal(err)
	}
	if len(purged) == 0 {
		t.Fatal("remove released the workload without the purge flag")
	}
	if len(kept) != 0 {
		t.Fatalf("remove used keep-state release: %v", kept)
	}
}

// The OAuth authorize URL and the Credential Broker connect URL are external —
// they reach the user through a loopback /connect/ link that validates the
// stored flow state (or broker request id) and only then redirects. Neither
// URL nor its state ever appears in a tool result or in the channel text.
func TestConnectLinkRedirectsToProviderURL(t *testing.T) {
	store := NewStore()
	control := &ControlPlane{Store: store, Now: time.Now}
	auth := aliceAuth()
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-oauth-link", PrincipalID: auth.PrincipalID, ContextID: auth.ContextID,
		RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion, Revision: 1,
		Mode: api.OnboardingRemote, Phase: api.PhaseAwaitingOAuth,
		ProviderAuthorizationURL: "https://accounts.example.com/authorize?state=flow-xyz&client_id=c&response_type=code",
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	brokerOn := onboarding
	brokerOn.OnboardingID = "onb-broker-link"
	brokerOn.Phase = api.PhaseAwaitingCreds
	brokerOn.ProviderAuthorizationURL = ""
	brokerOn.BrokerRequestID = "req-777"
	brokerOn.BrokerAuthorizationURL = "https://broker.example.com/connect/req-777"
	if err := store.PutOnboarding(brokerOn); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	good := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-oauth-link?state=flow-xyz", nil)
	good.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	gateway.serveConnect(rec, good)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), html.EscapeString(onboarding.ProviderAuthorizationURL)) {
		t.Fatalf("connect interstitial=%d", rec.Code)
	}
	bad := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-oauth-link?state=other", nil)
	bad.Host = "127.0.0.1"
	badRec := httptest.NewRecorder()
	gateway.serveConnect(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong state status=%d", badRec.Code)
	}
	br := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connect/onb-broker-link?request=req-777", nil)
	br.Host = "127.0.0.1"
	brRec := httptest.NewRecorder()
	gateway.serveConnect(brRec, br)
	if brRec.Code != http.StatusFound || brRec.Header().Get("Location") != brokerOn.BrokerAuthorizationURL {
		t.Fatalf("broker connect redirect=%d %q", brRec.Code, brRec.Header().Get("Location"))
	}
	_ = client
}

// Minting a provider authorization URL must deliver the /connect/ hop link to
// the channel — the model only sees the awaiting-oauth phase, never the URL.
func TestOAuthStartSendsConnectLink(t *testing.T) {
	var noticed []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/prepare-outcome" {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			noticed = append(noticed, payload)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")

	fixture := newRemoteOAuthFixture(t)
	fix := newRemoteControlFixture(t)
	if err := putGrant(t, fix.store, api.OperatorGrant(api.GrantSelfInstall, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	fix.control.OAuth = fixture.broker(fix.secrets, fix.control.origin()+"/oauth/callback")
	alice := fix.session(t, aliceToken)
	prepared, err := callControl(t, alice, "prepare_source", map[string]any{"remote_url": fixture.endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callControl(t, alice, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	var link string
	for _, payload := range noticed {
		if u, _ := payload["form_url"].(string); strings.Contains(u, "/connect/") {
			link = u
		}
	}
	if link == "" {
		t.Fatalf("no /connect/ link reached the channel: %v", noticed)
	}
	if !strings.HasPrefix(link, "http://127.0.0.1:9999/connect/") || !strings.Contains(link, "?k=") {
		t.Fatalf("connect link=%q", link)
	}
	if strings.Contains(link, "accounts.") {
		t.Fatalf("provider URL leaked into the channel notice: %q", link)
	}
}

// Instructions must never direct the model at fields that no longer exist in
// the body: no "send the nonce", no "send authorization_url", no "form URL
// from required_credentials" — links travel by channel notice only.
func TestInstructionsNeverReferenceRemovedFields(t *testing.T) {
	control := &ControlPlane{Store: NewStore(), Listen: "127.0.0.1:0"}
	cases := []api.Onboarding{
		{OnboardingID: "o1", Phase: api.PhaseAwaitingConfirm, ConfirmationNonce: "n", ConfirmationExpires: time.Now().Add(time.Hour)},
		{OnboardingID: "o2", Phase: api.PhaseAwaitingOAuth, ProviderAuthorizationURL: "https://accounts.example.com/a?state=s"},
		{OnboardingID: "o3", Phase: api.PhaseAwaitingCreds, FormNonce: "f", FormExpires: time.Now().Add(time.Hour), AdmissionPending: true, BrokerContractID: "c1"},
		{OnboardingID: "o4", Phase: api.PhaseAwaitingCreds, FormNonce: "f", FormExpires: time.Now().Add(time.Hour), Error: "Проверка не прошла: bad token", AdmissionPending: true},
		{OnboardingID: "o5", Phase: api.PhaseAwaitingCreds, FormNonce: "f", FormExpires: time.Now().Add(time.Hour), Error: "Не заполнено: TOKEN", AdmissionPending: true},
	}
	for _, on := range cases {
		body := control.statusBody(on, false)
		if text, ok := body["instructions"].(string); ok {
			lower := strings.ToLower(text)
			for _, bad := range []string{"nonce from this response", "authorization_url", "from required_credentials", "send the form url", "send authorization"} {
				if strings.Contains(lower, bad) {
					t.Fatalf("%s instructions reference removed field %q: %q", on.Phase, bad, text)
				}
			}
		}
	}
}

// CHG-0127 S0.6: env/header-bound credential values must never carry control
// characters — a submitted newline would inject extra env lines into the
// workload file. File delivery and valid JSON bodies stay multi-line.
func TestSubmitRejectsCredentialLineInjection(t *testing.T) {
	if _, err := filterSubmittedCredentials([]api.CredentialHint{{Name: "API_TOKEN", Delivery: "env"}},
		map[string]string{"API_TOKEN": "ok\nEVIL_VAR=1"}); err == nil {
		t.Fatal("newline in env credential accepted")
	}
	if _, err := filterSubmittedCredentials([]api.CredentialHint{{Name: "API_TOKEN", Delivery: "env"}},
		map[string]string{"API_TOKEN": "ok\r\nEVIL_VAR=1"}); err == nil {
		t.Fatal("CRLF in env credential accepted")
	}
	if _, err := filterSubmittedCredentials([]api.CredentialHint{{Name: "API_TOKEN", Delivery: "env"}},
		map[string]string{"API_TOKEN": "ok\x00byte"}); err == nil {
		t.Fatal("NUL in env credential accepted")
	}
	if _, err := filterSubmittedCredentials([]api.CredentialHint{{Name: "TLS_PEM", Delivery: "file"}},
		map[string]string{"TLS_PEM": "-----BEGIN-----\nabc\n-----END-----"}); err != nil {
		t.Fatalf("file-delivery newline rejected: %v", err)
	}
	if _, err := filterSubmittedCredentials([]api.CredentialHint{{Name: "GOOGLE_CREDENTIALS", Delivery: "json"}},
		map[string]string{"GOOGLE_CREDENTIALS": "{\n  \"a\": 1\n}"}); err != nil {
		t.Fatalf("valid JSON body rejected: %v", err)
	}
	if _, err := filterSubmittedCredentials([]api.CredentialHint{{Name: "GOOGLE_CREDENTIALS", Delivery: "json"}},
		map[string]string{"GOOGLE_CREDENTIALS": "not json\nEVIL=1"}); err == nil {
		t.Fatal("invalid JSON with newline accepted for env fallthrough")
	}
	// Alternative groups are covered too: a filled alternative with an
	// injected newline must not slip through the second pass.
	hints := []api.CredentialHint{
		{Name: "BOT_TOKEN", Delivery: "env", AlternativeGroup: 1},
		{Name: "OAUTH", Delivery: "env", AlternativeGroup: 1},
	}
	if _, err := filterSubmittedCredentials(hints, map[string]string{"BOT_TOKEN": "x\nINJECT=1", "OAUTH": "y"}); err == nil {
		t.Fatal("alternative-group value with newline accepted")
	}
}

// The optional-hints merge path in SubmitCredentials must apply the same
// line-safety rule — an optional field is stored verbatim into the env file.
func TestSubmitRejectsOptionalCredentialInjection(t *testing.T) {
	store := NewStore()
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "form.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now, ConfirmationTTL: time.Hour, FormOrigin: "http://127.0.0.1:8090"}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onboard-inj1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingCreds, SourceURL: "https://github.com/zereight/gitlab-mcp",
		DefinitionID: "gitlab", DefinitionVersion: "1.0.0", Revision: 1, CreatedAt: time.Now(),
		FormNonce:   api.RandomNonce(),
		FormExpires: time.Now().Add(time.Hour),
		Required:    []api.CredentialHint{{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Secret: true, Delivery: "env"}},
		Optional:    []api.CredentialHint{{Name: "GITLAB_PROJECT_ID", Delivery: "env"}},
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	err = control.SubmitCredentials(onboarding.OnboardingID, onboarding.FormNonce, map[string]string{
		"GITLAB_PERSONAL_ACCESS_TOKEN": "tok",
		"GITLAB_PROJECT_ID":            "42\nINJECTED=evil",
	})
	if !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("optional newline submit accepted: %v", err)
	}
}

// CHG-0127 S0.6: file mounting is keyed on the declared delivery, not on a
// CREDENTIALS-looking name — an undeclared input keeps its value inline and a
// json-declared input mounts regardless of its name.
func TestMaterializeMountsKeyOnDeliveryNotName(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "credentials.env"), []byte("SERVICE_ACCOUNT={\"k\":1}\nLOOKS_CREDENTIALS={\"x\":2}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	definition := api.ToolDefinition{Credentials: []api.CredentialInput{
		{Name: "SERVICE_ACCOUNT", Delivery: "json"},
		{Name: "LOOKS_CREDENTIALS"},
	}}
	env, mounts, err := materializeRuntimeCredentials(workspace, definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Target != "/run/credentials/SERVICE_ACCOUNT.json" {
		t.Fatalf("json delivery must mount by contract: %+v", mounts)
	}
	body, err := os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "LOOKS_CREDENTIALS={\"x\":2}") {
		t.Fatal("name-only input must stay inline")
	}
}

// A multi-line file/json body stored as a sibling .secret file is what the
// injector stages; materialize reads the content from the file, never an env
// line.
func TestMaterializeReadsSecretFiles(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "credentials.env"), []byte("API_TOKEN=tok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "TLS_PEM.secret"), []byte("-----BEGIN-----\nabc\n-----END-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	definition := api.ToolDefinition{Credentials: []api.CredentialInput{
		{Name: "TLS_PEM", Delivery: "file", Target: "/run/secrets/tls.pem"},
		{Name: "API_TOKEN", Delivery: "env"},
	}}
	env, mounts, err := materializeRuntimeCredentials(workspace, definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0].Target != "/run/secrets/tls.pem" {
		t.Fatalf("file delivery must mount the staged secret: %+v", mounts)
	}
	staged, err := os.ReadFile(mounts[0].Source)
	if err != nil || !strings.Contains(string(staged), "BEGIN") {
		t.Fatalf("staged content: %q %v", staged, err)
	}
	body, err := os.ReadFile(env)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "BEGIN") {
		t.Fatal("file content leaked into an env line")
	}
}

// CHG-0127 S0.7: remote effect classification fails closed — annotation-first,
// never name heuristics. readOnlyHint=true => read; absent/false => write.

func newAnnotatedRemoteFixture(t *testing.T, tools ...*mcp.Tool) *remoteMCPFixture {
	t.Helper()
	fixture := &remoteMCPFixture{}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "remote-annotation-fixture", Version: "1"}, nil)
	for _, tool := range tools {
		if tool.InputSchema == nil {
			tool.InputSchema = map[string]any{"type": "object"}
		}
		mcpServer.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	fixture.URL = server.URL
	pinRemoteFixtureClient(t, fixture, server.URL)
	return fixture
}

func probeAnnotatedEffects(t *testing.T, fixture *remoteMCPFixture) map[string]api.Effect {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	def, err := api.RemoteDefinitionBase(fixture.endpoint(), "remote-annotated", nil)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := api.ProbeRemoteMCP(ctx, def, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	effects := make(map[string]api.Effect, len(probe.Tools))
	for _, tool := range probe.Tools {
		effects[tool.Name] = tool.Effect
	}
	return effects
}

func TestRemoteToolEffectDefaultsToWrite(t *testing.T) {
	fixture := newAnnotatedRemoteFixture(t,
		&mcp.Tool{Name: "archive_item"},
		&mcp.Tool{Name: "unknown_verb"},
	)
	effects := probeAnnotatedEffects(t, fixture)
	for _, name := range []string{"archive_item", "unknown_verb"} {
		if effects[name] != api.WriteEffect {
			t.Fatalf("%s classified %v without readOnlyHint; must fail closed to write", name, effects[name])
		}
	}
}

func TestRemoteToolEffectHonorsReadOnlyHint(t *testing.T) {
	destructive := true
	fixture := newAnnotatedRemoteFixture(t,
		&mcp.Tool{Name: "archive_item", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		&mcp.Tool{Name: "delete_everything", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}},
		&mcp.Tool{Name: "reset_account", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &destructive}},
	)
	effects := probeAnnotatedEffects(t, fixture)
	if effects["archive_item"] != api.ReadEffect {
		t.Fatalf("readOnlyHint=true classified %v", effects["archive_item"])
	}
	if effects["delete_everything"] != api.WriteEffect {
		t.Fatalf("readOnlyHint=false classified %v", effects["delete_everything"])
	}
	if effects["reset_account"] != api.WriteEffect {
		t.Fatalf("readOnlyHint+destructiveHint classified %v", effects["reset_account"])
	}
}

// CHG-0127 S0.8: enable readiness runs outside s.mu — principal A's slow
// probe must not block principal B's Authorize.
func TestEnableReadyDoesNotBlockOtherPrincipals(t *testing.T) {
	store := NewStore()
	defA := catalogReadDefinition()
	defB := catalogReadDefinition()
	defB.DefinitionID = "bob-tool"
	for _, def := range []api.ToolDefinition{defA, defB} {
		if err := store.RegisterDefinition(def); err != nil {
			t.Fatal(err)
		}
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "alice", "", "")); err != nil {
		t.Fatal(err)
	}
	if err := putGrant(t, store, api.OperatorGrant(api.GrantCatalogDefault, "bob", "", "")); err != nil {
		t.Fatal(err)
	}
	bob := bobAuth()
	bindingB, err := store.Enable(bob, defB.DefinitionID, defB.Version)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.EnableReady(t.Context(), aliceAuth(), defA.DefinitionID, defA.Version, func(ctx context.Context, _ api.EffectiveBinding) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness probe never entered")
	}
	authDone := make(chan error, 1)
	go func() {
		authDone <- store.Authorize(bob, bindingB.ToolBindingID, func(api.EffectiveBinding) error { return nil })
	}()
	select {
	case err := <-authDone:
		if err != nil {
			t.Fatalf("authorize: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("authorize blocked behind another principal's readiness probe")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("enable: %v", err)
	}
}

// CHG-0127 S0.8: the same unlocked window covers the existing-binding
// re-enable path — a recheck probe must not hold s.mu either.
func TestEnableReadyRecheckDoesNotBlockOtherPrincipals(t *testing.T) {
	store := NewStore()
	defA := catalogReadDefinition()
	defB := catalogReadDefinition()
	defB.DefinitionID = "bob-tool"
	for _, def := range []api.ToolDefinition{defA, defB} {
		if err := store.RegisterDefinition(def); err != nil {
			t.Fatal(err)
		}
	}
	alice := aliceAuth()
	bob := bobAuth()
	if _, err := store.Enable(alice, defA.DefinitionID, defA.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Disable(alice, defA.DefinitionID, defA.Version); err != nil {
		t.Fatal(err)
	}
	bindingB, err := store.Enable(bob, defB.DefinitionID, defB.Version)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.EnableReady(t.Context(), alice, defA.DefinitionID, defA.Version, func(ctx context.Context, _ api.EffectiveBinding) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recheck probe never entered")
	}
	authDone := make(chan error, 1)
	go func() {
		authDone <- store.Authorize(bob, bindingB.ToolBindingID, func(api.EffectiveBinding) error { return nil })
	}()
	select {
	case err := <-authDone:
		if err != nil {
			t.Fatalf("authorize: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("authorize blocked behind an existing-binding recheck probe")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("re-enable: %v", err)
	}
}

// CHG-0127 S0.9: local recipe resolution is deterministic — map iteration
// never decides the recipe, and disagreeing same-rank declarations fail
// closed instead of first-wins.

func resolveLocalSignature(t *testing.T, files map[string][]byte) string {
	t.Helper()
	launch, connection, warnings, err := api.ResolveLocalRecipe(files, api.ArtifactSource{})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%#v|%#v|%v", launch, connection, warnings)
}

func TestResolveLocalRecipeDeterministicDigest(t *testing.T) {
	files := map[string][]byte{
		"Dockerfile":              []byte("FROM img:1\nENTRYPOINT [\"./server\"]\nENV API_KEY x\nVOLUME /state\n"),
		"docs/readme.md":          []byte("requires API_KEY and REGION set\n"),
		".env.example":            []byte("API_KEY=\nREGION=\n"),
		"tools/extra.env.example": []byte("MISC=\n"),
		"sub/Dockerfile.alt":      []byte("FROM img:1\nENTRYPOINT [\"./server\"]\n"),
	}
	first := resolveLocalSignature(t, files)
	for i := 0; i < 100; i++ {
		if got := resolveLocalSignature(t, files); got != first {
			t.Fatalf("iteration %d resolved differently: %s vs %s", i, got, first)
		}
	}
}

func TestResolveLocalRecipeAmbiguousEntrypoint(t *testing.T) {
	twoDockerfiles := map[string][]byte{
		"a/Dockerfile": []byte("FROM img:1\nENTRYPOINT [\"./one\"]\n"),
		"b/Dockerfile": []byte("FROM img:1\nENTRYPOINT [\"./two\"]\n"),
	}
	if _, _, _, err := api.ResolveLocalRecipe(twoDockerfiles, api.ArtifactSource{}); !errors.Is(err, api.ErrAmbiguousEntrypoint) {
		t.Fatalf("disagreeing Dockerfiles: %v", err)
	}
	dockerVsCompose := map[string][]byte{
		"Dockerfile":   []byte("FROM img:1\nENTRYPOINT [\"./one\"]\n"),
		"compose.yaml": []byte("services:\n  mcp:\n    image: img@sha256:1111111111111111111111111111111111111111111111111111111111111111\n    entrypoint: [\"./two\"]\n"),
	}
	if _, _, _, err := api.ResolveLocalRecipe(dockerVsCompose, api.ArtifactSource{}); !errors.Is(err, api.ErrAmbiguousEntrypoint) {
		t.Fatalf("Dockerfile vs compose disagreement: %v", err)
	}
}

func TestResolveLocalRecipeMultipleServerJSON(t *testing.T) {
	rootWins := map[string][]byte{
		"server.json":     []byte(`{"name":"io.acme/a","remotes":[{"type":"streamable-http","url":"https://root.example/mcp"}]}`),
		"sub/server.json": []byte(`{"name":"io.acme/b","remotes":[{"type":"streamable-http","url":"https://nested.example/mcp"}]}`),
	}
	launch, _, _, err := api.ResolveLocalRecipe(rootWins, api.ArtifactSource{})
	if err != nil {
		t.Fatalf("root-preferred server.json: %v", err)
	}
	if launch.Endpoint != "https://root.example/mcp" {
		t.Fatalf("endpoint=%q, want the scope-root server.json", launch.Endpoint)
	}
	noRoot := map[string][]byte{
		"a/server.json": []byte(`{"name":"io.acme/a","remotes":[{"type":"streamable-http","url":"https://a.example/mcp"}]}`),
		"b/server.json": []byte(`{"name":"io.acme/b","remotes":[{"type":"streamable-http","url":"https://b.example/mcp"}]}`),
	}
	if _, _, _, err := api.ResolveLocalRecipe(noRoot, api.ArtifactSource{}); !errors.Is(err, api.ErrAmbiguousEntrypoint) {
		t.Fatalf("two nested server.json without a root one: %v", err)
	}
}

// CHG-0127 follow-up: every stopWorkloads call site must surface the stop
// failure — a silently skipped stop leaves a revoked/disabled connector's
// runtime alive while reporting success (gosec G104 found the discards).

func storeWithRunningBindingWorkload(t *testing.T) (*Store, api.ToolBinding) {
	t.Helper()
	store, auth, binding := seededStore(t)
	store.Stopper = errStopper{}
	workload, err := api.NewWorkloadInstance(binding, &api.OwnerRef{Type: api.ContextOwner, ID: auth.ContextID}, "", 1, time.Now().UTC(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status = api.RunningStatus
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	return store, binding
}

func TestStopFailureSurfacesOnEveryCaller(t *testing.T) {
	t.Run("SetConnectionStatus", func(t *testing.T) {
		store, _ := storeWithRunningBindingWorkload(t)
		if err := store.SetConnectionStatus("google-work", api.DisabledStatus); err == nil || !errors.Is(err, errRuntimeUnreachable) {
			t.Fatalf("SetConnectionStatus discarded stop failure: %v", err)
		}
	})
	t.Run("MarkDegraded", func(t *testing.T) {
		store, _ := storeWithRunningBindingWorkload(t)
		if err := store.MarkDegraded("google-work"); err == nil || !errors.Is(err, errRuntimeUnreachable) {
			t.Fatalf("MarkDegraded discarded stop failure: %v", err)
		}
	})
	t.Run("RemoveBindingRecords", func(t *testing.T) {
		store, binding := storeWithRunningBindingWorkload(t)
		if err := store.RemoveBindingRecords(binding.ToolBindingID); err == nil || !errors.Is(err, errRuntimeUnreachable) {
			t.Fatalf("RemoveBindingRecords discarded stop failure: %v", err)
		}
	})
	t.Run("RotateCredentialRecord", func(t *testing.T) {
		store, _ := storeWithRunningBindingWorkload(t)
		reference := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 2), ConnectionID: "google-work", Revision: 2, Backend: "local", Locator: "local://alice/google/2", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
		if _, err := store.RotateCredentialRecord(reference); err == nil || !errors.Is(err, errRuntimeUnreachable) {
			t.Fatalf("RotateCredentialRecord discarded stop failure: %v", err)
		}
	})
	t.Run("Reload", func(t *testing.T) {
		src, binding := storeWithRunningBindingWorkload(t)
		path := filepath.Join(t.TempDir(), "registry.json")
		if err := src.Save(path); err != nil {
			t.Fatal(err)
		}
		live, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		live.Stopper = errStopper{}
		_ = binding
		other, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		for id, w := range other.workloads {
			w.Status = api.StoppedStatus
			other.workloads[id] = w
		}
		if err := other.Save(path); err != nil {
			t.Fatal(err)
		}
		if err := live.Reload(); err == nil || !errors.Is(err, errRuntimeUnreachable) {
			t.Fatalf("Reload discarded stop failure: %v", err)
		}
	})
}
