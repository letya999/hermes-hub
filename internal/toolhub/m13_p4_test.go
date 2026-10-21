package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// S4.1: every Docker object the controller spawns carries the ownership
// label set so finalizers and the reconciler can find it by label query.
func TestSpawnStampsOwnershipLabels(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	plan.WorkspacePath = t.TempDir()
	plan.Ownership = api.OwnershipLabels{
		Owner:      "principal-1",
		Connection: "conn_test_1",
		Binding:    "bind_test_1",
		Recipe:     d.Source.RecipeDigest,
	}
	var creates [][]string
	stop := errors.New("observed")
	c.commandEnv = func(context.Context, map[string]string, string, ...string) ([]byte, error) { return nil, nil }
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "docker" || len(args) == 0 {
			return nil, nil
		}
		switch args[0] {
		case "context":
			return []byte("unix:///var/run/docker.sock"), nil
		case "info":
			return []byte("linux"), nil
		case "inspect":
			return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
		case "create":
			creates = append(creates, append([]string(nil), args...))
			if api.Contains(args, "/hermes-bridge/hubctl") {
				return nil, stop
			}
		case "network", "volume":
			if len(args) > 1 && args[1] == "create" {
				creates = append(creates, append([]string(nil), args...))
			}
		}
		return nil, nil
	}
	_, _ = c.startDockerRemoteFallback(t.Context(), plan)
	if len(creates) == 0 {
		t.Fatal("no docker create calls observed")
	}
	for _, argv := range creates {
		for _, want := range []string{
			"hermes-hub.owner=principal-1",
			"hermes-hub.connection=conn_test_1",
			"hermes-hub.binding=bind_test_1",
			"hermes-hub.recipe=" + d.Source.RecipeDigest,
		} {
			if !api.Contains(argv, "--label") || !containsLabel(argv, want) {
				t.Errorf("%v missing label %q", argv[:1], want)
			}
		}
	}
}

// S4.1: the admission verifier posts the ownership block with the plan so
// the spawned footprint is label-findable by finalizers and the reconciler.
func TestAdmissionVerifierPostsOwnership(t *testing.T) {
	var sawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sawBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(api.AdmissionReceipt{WorkloadID: "w-own", State: "running", Enforced: true})
	}))
	defer srv.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", srv.URL)
	t.Setenv("HUB_STATE", t.TempDir())
	verifier, err := api.ControllerAdmissionVerifierFromEnv()
	if err != nil || verifier == nil {
		t.Fatalf("verifier: %v", err)
	}
	effective := api.EffectiveBinding{
		WorkloadID: "w-own",
		Binding:    api.ToolBinding{PrincipalID: "principal-1", ToolBindingID: "bind_9", WorkloadClass: api.PerUser, ContextID: "ctx_9"},
		Connection: &api.Connection{ConnectionID: "conn_9"},
	}
	effective.Definition = api.ToolDefinition{Transport: api.ContainerMCP}
	effective.Definition.Workload.Class = api.PerUser
	effective.Definition.Source.RecipeDigest = "sha256:" + strings.Repeat("9", 64)
	if _, err := verifier(t.Context(), effective); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(sawBody, `"connection":"conn_9"`) || !strings.Contains(sawBody, `"owner":"principal-1"`) {
		t.Fatalf("plan body lacks ownership: %s", sawBody)
	}
}

// S4.1: bounded-cli cells answer the same ownership label query as workload
// containers — the generic cell.* keys stay for compat.
func TestCellStampsOwnershipLabels(t *testing.T) {
	c, fake := cellFixture(t, nil)
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); err != nil {
		t.Fatalf("exec: %v", err)
	}
	var createArgv []string
	for _, call := range fake.calls {
		if len(call) > 1 && call[0] == "create" {
			createArgv = call
		}
	}
	if createArgv == nil {
		t.Fatal("no cell create call recorded")
	}
	if !containsLabel(createArgv, "hermes-hub.owner=alice") || !containsLabel(createArgv, "hermes-hub.binding=bind-1") {
		t.Fatalf("cell create lacks standard ownership labels: %v", createArgv)
	}
}

// S4.1: the plan builder must carry ownership from the effective binding so
// spawned resources can be found by label query later.
func TestPlanOwnershipFromEffective(t *testing.T) {
	effective := api.EffectiveBinding{
		Binding:    api.ToolBinding{PrincipalID: "principal-1", ToolBindingID: "bind_9"},
		Connection: &api.Connection{ConnectionID: "conn_9"},
	}
	effective.Definition.Source.RecipeDigest = "sha256:" + strings.Repeat("9", 64)
	got := api.PlanOwnership(effective)
	if got.Owner != "principal-1" || got.Connection != "conn_9" || got.Binding != "bind_9" || got.Recipe != effective.Definition.Source.RecipeDigest {
		t.Fatalf("ownership not propagated: %+v", got)
	}
}

// S4.1: the materialized workspace carries an ownership marker file so the
// workspace finalizer and reconciler can prove the dir belongs to the
// connection before deleting it.
func TestWorkspaceOwnershipMarker(t *testing.T) {
	effective := api.EffectiveBinding{
		Binding:    api.ToolBinding{PrincipalID: "principal-1", ToolBindingID: "bind_9", WorkloadClass: api.PerUser, ContextID: "ctx_9"},
		Connection: &api.Connection{ConnectionID: "conn_9"},
	}
	effective.Definition.Workload.Class = api.PerUser
	ws, err := api.OpenWorkloadWorkspace(t.TempDir(), effective, "")
	if err != nil || ws.Path == "" {
		t.Fatalf("open workspace: %v path=%q", err, ws.Path)
	}
	marker, err := api.ReadWorkspaceOwnership(ws.Path)
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	if marker.Owner != "principal-1" || marker.Connection != "conn_9" || marker.Binding != "bind_9" {
		t.Fatalf("marker=%+v", marker)
	}
}

// S4.2: remove runs the finalizer ladder, records each step and reports a
// verified-zero receipt once the label query comes back empty.
func TestRemoveEmitsVerifiedReceipt(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	enabled := m13P3EnabledOnboarding(t, store, auth)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	footprint := map[string][]string{"container": {"w-1"}, "volume": {"v-1"}, "network": {"n-1"}}
	queried := false
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute, WorkloadRoot: t.TempDir()}
	c.LabelQuery = func(_ context.Context, labels map[string]string) (map[string][]string, error) {
		if labels[api.LabelConnection] != enabled.ConnectionID {
			t.Errorf("label query without connection label: %v", labels)
		}
		if !queried {
			queried = true
			return footprint, nil
		}
		return map[string][]string{}, nil
	}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_remove", map[string]any{"target": "google-work"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != api.OpStateAwaitingConfirm {
		t.Fatalf("remove preview: %+v", result)
	}
	onboarding, err := store.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.remove(context.Background(), auth, map[string]any{"onboarding_id": enabled.OnboardingID, "remove_nonce": onboarding.RemoveNonce}); err != nil {
		t.Fatal(err)
	}
	onboarding, err = store.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil || onboarding.Phase != api.PhaseRemoved {
		t.Fatalf("onboarding after purge: %v %+v", err, onboarding)
	}
	if len(onboarding.Finalizers) == 0 {
		t.Fatal("no finalizer ladder recorded")
	}
	for _, step := range onboarding.Finalizers {
		if step.State != api.FinalizerDone {
			t.Fatalf("finalizer %s not done: %+v", step.Name, onboarding.Finalizers)
		}
	}
	if !strings.Contains(onboarding.RemoveReceipt, "verified 0") {
		t.Fatalf("receipt missing or unverified: %q", onboarding.RemoveReceipt)
	}
}

// S4.2: a failing finalizer (broker revoke 500) leaves cleanup_incomplete
// naming the stuck step; a retry resumes the ladder and converges.
func TestRemoveFinalizerRetryAfterFault(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	enabled := m13P3EnabledOnboarding(t, store, auth)
	enabled.BrokerCredentialID = "credential_1"
	enabled.Revision++
	if err := store.PutOnboarding(enabled); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	revokes := 0
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokes++
		if revokes <= 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer broker.Close()
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute, WorkloadRoot: t.TempDir(),
		Broker: &credentialbroker.Config{URL: broker.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"},
	}
	if _, err := c.InvokeConnector(context.Background(), auth, "connector_remove", map[string]any{"target": "google-work"}); err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := c.remove(context.Background(), auth, map[string]any{"onboarding_id": enabled.OnboardingID, "remove_nonce": onboarding.RemoveNonce})
	if err != nil {
		t.Fatal(err)
	}
	onboarding, err = store.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil || onboarding.Phase != api.PhaseCleanupIncomplete {
		t.Fatalf("expected cleanup_incomplete after broker 500: %v %+v", err, onboarding)
	}
	if body["finalizer"] != api.FinalizerBrokerCredential {
		t.Fatalf("stuck finalizer not reported: %v", body)
	}
	path := filepath.Join(t.TempDir(), "store.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	restarted, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Store = restarted
	endpoint := &projectionEndpoint{gateway: &Gateway{Control: c}}
	// Attempts 2 and 3 still fail; attempt 4 reaches the stubbed 204.
	for i := 0; i < 3; i++ {
		if err = endpoint.ResumeRemovals(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	onboarding, err = restarted.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil || onboarding.Phase != api.PhaseRemoved {
		t.Fatalf("onboarding after retries: %v %+v", err, onboarding)
	}
	if !strings.Contains(onboarding.RemoveReceipt, "verified 0") {
		t.Fatalf("receipt: %q", onboarding.RemoveReceipt)
	}
}

func TestResumeRemovalAfterRestartWithPendingFinalizers(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	onboarding.Finalizers = newRemoveLadder()
	onboarding.RemoveNonce = "spent-remove"
	onboarding.RemoveExpires = time.Now().Add(-time.Hour)
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConnectionStatus(onboarding.ConnectionID, api.DeletingStatus); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, WorkloadRoot: t.TempDir()}
	if err := c.ResumeRemovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil || got.Phase != api.PhaseRemoved {
		t.Fatalf("pending finalizers not resumed: %v %s", err, got.Phase)
	}
}

func TestResumeRemovalAfterCrashBeforeFinalizerRecord(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	onboarding := m13P3EnabledOnboarding(t, store, auth)
	onboarding.RemoveNonce = "spent-remove"
	onboarding.RemoveExpires = time.Now().Add(-time.Hour)
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConnectionStatus(onboarding.ConnectionID, api.DeletingStatus); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, WorkloadRoot: t.TempDir()}
	if err := c.ResumeRemovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil || got.Phase != api.PhaseRemoved {
		t.Fatalf("remove before finalizer record not resumed: %v %s", err, got.Phase)
	}
}

// S4.2: verify-zero is a real label query — a leftover volume keeps the
// remove in cleanup_incomplete instead of emitting a false receipt.
func TestRemoveVerifyZeroBlocksOnLeftover(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	enabled := m13P3EnabledOnboarding(t, store, auth)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute, WorkloadRoot: t.TempDir()}
	c.LabelQuery = func(context.Context, map[string]string) (map[string][]string, error) {
		return map[string][]string{"volume": {"stray-state-vol"}}, nil
	}
	if _, err := c.InvokeConnector(context.Background(), auth, "connector_remove", map[string]any{"target": "google-work"}); err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.remove(context.Background(), auth, map[string]any{"onboarding_id": enabled.OnboardingID, "remove_nonce": onboarding.RemoveNonce}); err != nil {
		t.Fatal(err)
	}
	onboarding, err = store.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil || onboarding.Phase != api.PhaseCleanupIncomplete {
		t.Fatalf("leftover volume not caught by verify-zero: %v %+v", err, onboarding)
	}
	var verify *api.Finalizer
	for i := range onboarding.Finalizers {
		if onboarding.Finalizers[i].Name == api.FinalizerVerifyZero {
			verify = &onboarding.Finalizers[i]
		}
	}
	if verify == nil || verify.State != api.FinalizerFailed {
		t.Fatalf("verify-zero finalizer state: %+v", onboarding.Finalizers)
	}
}

// S4.2: the volumes/network sweeps purge leftovers the release path missed;
// the receipt names what the ladder actually removed.
func TestRemoveSweepsLeftoversAndReceiptCounts(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	enabled := m13P3EnabledOnboarding(t, store, auth)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	live := map[string]map[string]bool{"volume": {"stray-1": true, "stray-2": true}, "container": {"w-1": true}, "network": {"net-1": true}, "image": {"shared-recipe:local": true}}
	var purged []string
	c := &ControlPlane{Store: store, Now: time.Now, ConfirmationTTL: time.Minute, WorkloadRoot: t.TempDir()}
	c.LabelQuery = func(_ context.Context, labels map[string]string) (map[string][]string, error) {
		out := map[string][]string{}
		for kind, names := range live {
			for name := range names {
				out[kind] = append(out[kind], name)
			}
		}
		return out, nil
	}
	c.LabelPurge = func(_ context.Context, kind, name string, selector map[string]string) error {
		if selector[api.LabelConnection] != enabled.ConnectionID {
			t.Fatalf("purge selector=%v", selector)
		}
		purged = append(purged, kind+"/"+name)
		delete(live[kind], name)
		return nil
	}
	result, err := c.InvokeConnector(context.Background(), auth, "connector_remove", map[string]any{"target": "google-work"})
	if err != nil {
		t.Fatal(err)
	}
	onboarding, err := store.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.remove(context.Background(), auth, map[string]any{"onboarding_id": enabled.OnboardingID, "remove_nonce": onboarding.RemoveNonce}); err != nil {
		t.Fatal(err)
	}
	onboarding, err = store.OnboardingFor(auth, enabled.OnboardingID)
	if err != nil || onboarding.Phase != api.PhaseRemoved {
		t.Fatalf("sweep did not converge: %v %+v", err, onboarding)
	}
	if len(purged) != 4 {
		t.Fatalf("purged=%v", purged)
	}
	if !live["image"]["shared-recipe:local"] || strings.Contains(onboarding.RemoveReceipt, "image") {
		t.Fatalf("shared image must be retained and excluded from remove receipt: %q", onboarding.RemoveReceipt)
	}
	if !strings.Contains(onboarding.RemoveReceipt, "2 volumes") || !strings.Contains(onboarding.RemoveReceipt, "1 container") || !strings.Contains(onboarding.RemoveReceipt, "1 network") {
		t.Fatalf("receipt lacks counts: %q", onboarding.RemoveReceipt)
	}
	// Resuming the journaled remove completes the operation; the durable
	// record and connector_status both surface the verified-zero receipt and
	// the finalizer ladder.
	resumed, err := c.InvokeConnector(context.Background(), auth, "connector_remove", map[string]any{"target": "google-work"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != api.OpStateDone || !strings.Contains(resumed.Summary, "verified 0") {
		t.Fatalf("resumed remove: %+v", resumed)
	}
	status, err := c.InvokeConnector(context.Background(), auth, "connector_status", map[string]any{"operation_id": result.OperationID})
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := json.Marshal(status)
	if !strings.Contains(string(summary), "verified 0") || !strings.Contains(string(summary), "verify-zero") {
		t.Fatalf("connector_status lacks receipt/finalizers: %s", summary)
	}
}

// S4.3: the reconciler scans hub-labelled resources, purges objects whose
// owner connection is absent from the store, and never touches foreign or
// unlabelled objects.
func TestReconcilerPurgesOrphans(t *testing.T) {
	store, _, _, _ := managedStore(t)
	universe := []api.LabeledResource{
		{Kind: "container", Name: "w-orphan", Labels: map[string]string{api.LabelConnection: "conn-gone"}},
		{Kind: "volume", Name: "v-orphan", Labels: map[string]string{api.LabelConnection: "conn-gone"}},
		{Kind: "container", Name: "w-live", Labels: map[string]string{api.LabelConnection: "google-work"}},
		{Kind: "volume", Name: "foreign", Labels: map[string]string{"app": "other"}},
	}
	var purged []string
	r := &Reconciler{
		Store:   store,
		MaxScan: 10,
		Scan: func(_ context.Context, selector map[string]string) ([]api.LabeledResource, error) {
			var out []api.LabeledResource
			for _, res := range universe {
				match := true
				for key, want := range selector {
					got, ok := res.Labels[key]
					if !ok || (want != "" && got != want) {
						match = false
						break
					}
				}
				if match {
					out = append(out, res)
				}
			}
			return out, nil
		},
		Purge: func(_ context.Context, kind, name string, _ map[string]string) error {
			purged = append(purged, kind+"/"+name)
			kept := universe[:0]
			for _, res := range universe {
				if res.Kind == kind && res.Name == name {
					continue
				}
				kept = append(kept, res)
			}
			universe = kept
			return nil
		},
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(purged) != 2 || !containsString(purged, "container/w-orphan") || !containsString(purged, "volume/v-orphan") {
		t.Fatalf("purged=%v", purged)
	}
	if containsString(purged, "volume/foreign") || containsString(purged, "container/w-live") {
		t.Fatalf("foreign/live object purged: %v", purged)
	}
}

// S4.3: a live connection whose broker credential is gone is degraded and
// the owner is notified for reauth; the reconciler never re-mints secrets.
func TestReconcilerDegradesMissingCredential(t *testing.T) {
	store, _, _, _ := managedStore(t)
	var notices []string
	r := &Reconciler{
		Store:     store,
		Scan:      func(context.Context, map[string]string) ([]api.LabeledResource, error) { return nil, nil },
		Purge:     func(context.Context, string, string, map[string]string) error { return nil },
		CredAlive: func(context.Context, string) (bool, error) { return false, nil },
		Notify: func(_ context.Context, principal, kind string, _ map[string]any) error {
			notices = append(notices, kind+":"+principal)
			return nil
		},
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	connection, ok := store.connectionFor("google-work")
	if !ok || connection.Status != api.DegradedStatus {
		t.Fatalf("connection=%+v ok=%v", connection, ok)
	}
	if !containsString(notices, "reauth:alice") {
		t.Fatalf("notices=%v", notices)
	}
	if _, ok := store.credentials[connection.CredentialRefID]; !ok {
		t.Fatal("credential record dropped — reconciler must not re-mint")
	}
}

// S4.3: a recipe-digest change under a live connection marks it degraded —
// writes hide until a human confirms the new surface.
func TestReconcilerRecipeDrift(t *testing.T) {
	store, _, _, _ := managedStore(t)
	connection, ok := store.connectionFor("google-work")
	if !ok {
		t.Fatal("seeded connection missing")
	}
	connection.Metadata[api.LabelRecipe] = "sha256:pinned"
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	var notices []string
	r := &Reconciler{
		Store:     store,
		Scan:      func(context.Context, map[string]string) ([]api.LabeledResource, error) { return nil, nil },
		Purge:     func(context.Context, string, string, map[string]string) error { return nil },
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Resolve:   func(context.Context, api.Connection) (string, error) { return "sha256:drifted", nil },
		Notify: func(_ context.Context, principal, kind string, _ map[string]any) error {
			notices = append(notices, kind+":"+principal)
			return nil
		},
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.connectionFor("google-work")
	if after.Status != api.DegradedStatus {
		t.Fatalf("drift not degraded: %s", after.Status)
	}
	if !containsString(notices, "drift:alice") {
		t.Fatalf("notices=%v", notices)
	}
}

// S6.4: a credential-relevant recipe drift (provider/endpoint/client
// claims) purges the stored tokens and moves the connection to reauth —
// no projected call can keep riding a credential minted for a provider
// the recipe no longer names. A second pass stays converged: the reauth
// connection is re-purged, never degraded back.
func TestReconcilerCredentialDriftReauths(t *testing.T) {
	store, _, _, _ := managedStore(t)
	connection, ok := store.connectionFor("google-work")
	if !ok {
		t.Fatal("seeded connection missing")
	}
	connection.Metadata[api.LabelCredentialRecipe] = "sha256:cred-pinned"
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	var purged, notices []string
	r := &Reconciler{
		Store:       store,
		Scan:        func(context.Context, map[string]string) ([]api.LabeledResource, error) { return nil, nil },
		Purge:       func(context.Context, string, string, map[string]string) error { return nil },
		CredAlive:   func(context.Context, string) (bool, error) { return true, nil },
		CredResolve: func(context.Context, api.Connection) (string, error) { return "sha256:cred-drifted", nil },
		CredPurge: func(_ context.Context, conn api.Connection, ref api.CredentialReference) error {
			purged = append(purged, ref.CredentialRefID)
			return nil
		},
		Notify: func(_ context.Context, principal, kind string, _ map[string]any) error {
			notices = append(notices, kind+":"+principal)
			return nil
		},
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := store.connectionFor("google-work")
	if after.Status != api.ReauthStatus {
		t.Fatalf("credential drift not moved to reauth: %s", after.Status)
	}
	if len(purged) != 1 || purged[0] != connection.CredentialRefID {
		t.Fatalf("purged=%v, want the connection's credential ref", purged)
	}
	if !containsString(notices, "reauth:alice") {
		t.Fatalf("notices=%v", notices)
	}
	if got := store.credentials[connection.CredentialRefID]; got.Status != api.RevokedStatus {
		t.Fatalf("credential reference status=%s, want revoked", got.Status)
	}
	// A follow-up pass with the pin still stale retries the purge without
	// resurrecting or demoting the connection.
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ = store.connectionFor("google-work")
	if after.Status != api.ReauthStatus {
		t.Fatalf("second pass demoted reauth connection: %s", after.Status)
	}
	if len(purged) != 2 {
		t.Fatalf("purge not retried on the converged pass: %v", purged)
	}
}

// S6.4: a recipe-carrying onboarding pins both digests on the connection at
// materialize time — the launch digest under hermes-hub.recipe and the
// credential-relevant digest (provider/endpoint/client claims) under
// hermes-hub.credential-recipe, which is what the reauth drift path reads.
func TestMaterializeBindingPinsCredentialRecipe(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	resolution := api.RecipeResolution{
		Source: api.ArtifactSource{Repository: "https://github.com/acme/work", CommitSHA: strings.Repeat("7", 40)},
		State:  "ready",
		Launch: api.LaunchRecipe{Transport: api.RemoteMCP, Endpoint: "https://mcp.acme.example/x"},
		Connection: api.ConnectionRecipe{Fields: []api.ConnectionField{
			{Name: "ACME_TOKEN", Type: "token", Required: true, Secret: true, Delivery: "env", Target: "ACME_TOKEN", Source: "server.json"},
		}},
	}
	digest, err := store.PutRecipe(resolution)
	if err != nil {
		t.Fatal(err)
	}
	definition := userMCPDefinition()
	definition.Credentials = []api.CredentialInput{{Name: "ACME_TOKEN", Required: true, Delivery: "env"}}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: api.DeterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, "pin-recipe"),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingCatalog, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		Phase: api.PhaseEnabled, Locator: "loc_pin", RecipeDigest: digest,
		Required: []api.CredentialHint{{Name: "ACME_TOKEN", Secret: true, Delivery: "env"}},
		Revision: 1, CreatedAt: time.Now(),
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	if _, err := c.materializeBinding(context.Background(), auth, onboarding, definition); err != nil {
		t.Fatal(err)
	}
	var connection api.Connection
	ok := false
	for _, candidate := range store.Connections() {
		if candidate.DefinitionID == definition.DefinitionID {
			connection, ok = candidate, true
		}
	}
	if !ok {
		t.Fatal("materialized connection missing")
	}
	if got := connection.Metadata[api.LabelRecipe]; got != digest {
		t.Fatalf("recipe pin=%q want %q", got, digest)
	}
	credDigest, err := api.CredentialRecipeDigest(resolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := connection.Metadata[api.LabelCredentialRecipe]; got != credDigest {
		t.Fatalf("credential recipe pin=%q want %q", got, credDigest)
	}
	// Endpoint/client/provider claims drift the credential digest; launch-only
	// noise never invalidates stored tokens.
	moved := resolution
	moved.Launch.Endpoint = "https://other.acme.example/x"
	if other, _ := api.CredentialRecipeDigest(moved); other == credDigest {
		t.Fatal("endpoint drift did not change the credential digest")
	}
	changed := resolution
	changed.Connection.Fields = append([]api.ConnectionField{}, resolution.Connection.Fields...)
	changed.Connection.Fields[0].Name = "ACME_KEY"
	if other, _ := api.CredentialRecipeDigest(changed); other == credDigest {
		t.Fatal("connection-field drift did not change the credential digest")
	}
	noisy := resolution
	noisy.Warnings = []string{"heuristic"}
	if other, _ := api.CredentialRecipeDigest(noisy); other != credDigest {
		t.Fatal("warnings noise changed the credential digest")
	}
}

// S4.3: MaxScan bounds each pass — leftover orphans converge on the next
// pass instead of starving the loop.
func TestReconcilerMaxScanBoundsPass(t *testing.T) {
	store, _, _, _ := managedStore(t)
	var purged int
	r := &Reconciler{
		Store:   store,
		MaxScan: 1,
		Scan: func(context.Context, map[string]string) ([]api.LabeledResource, error) {
			return []api.LabeledResource{
				{Kind: "container", Name: "a", Labels: map[string]string{api.LabelConnection: "gone"}},
				{Kind: "container", Name: "b", Labels: map[string]string{api.LabelConnection: "gone"}},
			}, nil
		},
		Purge: func(context.Context, string, string, map[string]string) error { purged++; return nil },
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Fatalf("MaxScan ignored: purged=%d", purged)
	}
}

// S4.4: a connection revoked while its readiness probe runs outside the
// store lock must win — the commit re-checks the reserved connection
// revision and refuses to activate a binding on a revoked connection.
func TestRevokeWinsDuringProbe(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	revoked := make(chan struct{})
	var once sync.Once
	_, err := store.EnableReady(context.Background(), auth, "google-work", "2.0.0", func(context.Context, api.EffectiveBinding) error {
		once.Do(func() {
			if err := store.SetConnectionStatus("google-work", api.RevokedStatus); err != nil {
				t.Errorf("revoke during probe: %v", err)
			}
			close(revoked)
		})
		return nil
	})
	<-revoked
	var committed api.ToolBinding
	for _, b := range store.bindings {
		if b.DefinitionID == "google-work" && b.DefinitionVersion == "2.0.0" && b.PrincipalID == auth.PrincipalID {
			committed = b
		}
	}
	if err == nil && committed.Status == api.ActiveStatus {
		t.Fatalf("revoked connection committed active binding: %+v", committed)
	}
	if committed.ToolBindingID != "" {
		if authErr := store.Authorize(auth, committed.ToolBindingID, func(api.EffectiveBinding) error { return nil }); authErr == nil {
			t.Fatal("Authorize allowed a revoked connection")
		}
	}
}

// S4.4: the readiness probe runs outside the store lock — unrelated
// principals keep reading projections while one connection's probe is
// still in flight.
func TestProbeDoesNotBlockStore(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.EnableReady(context.Background(), auth, "google-work", "2.0.0", func(context.Context, api.EffectiveBinding) error {
			<-release
			return nil
		})
		done <- err
	}()
	deadline := time.After(2 * time.Second)
	for i := 0; i < 20; i++ {
		if _, err := store.ProjectionRevision(auth); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-deadline:
		t.Fatal("enable did not converge")
	}
}

// S4.5: snapshot writes compact terminal records — aged operations and
// tombstone onboardings leave the file; fresh tombstones survive.
func TestCompactionBoundsSnapshot(t *testing.T) {
	store := NewStore()
	old := time.Now().Add(-40 * 24 * time.Hour)
	for i := 0; i < 10000; i++ {
		store.operations[fmt.Sprintf("op-%d", i)] = api.Operation{
			Schema: api.SchemaVersion, OperationID: fmt.Sprintf("op-%d", i), Intent: api.OpIntentInstall,
			Target: "x", PrincipalID: "alice", State: api.OpStateDone, CreatedAt: old, UpdatedAt: old,
		}
	}
	store.onboardings["dead-old"] = api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "dead-old", PrincipalID: "alice", Phase: api.PhaseRemoved, Revision: 1, CreatedAt: old}
	store.onboardings["dead-new"] = api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "dead-new", PrincipalID: "alice", Phase: api.PhaseRemoved, Revision: 1, CreatedAt: time.Now()}
	store.operations["op-fresh"] = api.Operation{Schema: api.SchemaVersion, OperationID: "op-fresh", Intent: api.OpIntentInstall, Target: "x", PrincipalID: "alice", State: api.OpStateDone, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	store.operations["op-failed"] = api.Operation{Schema: api.SchemaVersion, OperationID: "op-failed", Intent: api.OpIntentInstall, Target: "x", PrincipalID: "alice", State: api.OpStateFailed, ErrorCode: api.OpCodeProbeFailed, CreatedAt: old, UpdatedAt: old}
	path := filepath.Join(t.TempDir(), "store.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	if len(store.operations) != 1 {
		t.Fatalf("terminal ops not compacted: %d", len(store.operations))
	}
	if _, ok := store.operations["op-failed"]; ok {
		t.Fatal("aged failed op survived compaction")
	}
	if _, ok := store.onboardings["dead-old"]; ok {
		t.Fatal("aged tombstone survived compaction")
	}
	if _, ok := store.onboardings["dead-new"]; !ok {
		t.Fatal("fresh tombstone compacted")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "op-9999") {
		t.Fatal("snapshot file still carries aged operations")
	}
}

// S4.6: enable pins the admitted tool surface on the binding; a drifted
// tools/list marks the connection degraded and notifies the owner.
func TestToolSurfaceDriftDegrades(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	binding, err := store.EnableReady(context.Background(), auth, "google-work", "2.0.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if binding.ToolDigest == "" {
		t.Fatal("enable did not pin the tool surface")
	}
	drifted := append([]api.ToolSpec(nil), store.definitions[definitionKey("google-work", "2.0.0")].Tools...)
	drifted[0].Name = "search-renamed"
	var notices []string
	r := &Reconciler{
		Store:     store,
		Scan:      func(context.Context, map[string]string) ([]api.LabeledResource, error) { return nil, nil },
		Purge:     func(context.Context, string, string, map[string]string) error { return nil },
		CredAlive: func(context.Context, string) (bool, error) { return true, nil },
		Surface:   func(context.Context, api.Connection) ([]api.ToolSpec, error) { return drifted, nil },
		Notify: func(_ context.Context, principal, kind string, _ map[string]any) error {
			notices = append(notices, kind+":"+principal)
			return nil
		},
	}
	if err := r.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	connection, _ := store.connectionFor("google-work")
	if connection.Status != api.DegradedStatus {
		t.Fatalf("tool drift not degraded: %s", connection.Status)
	}
	if !containsString(notices, "drift:alice") {
		t.Fatalf("notices=%v", notices)
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func containsLabel(argv []string, want string) bool {
	for i, arg := range argv {
		if arg == "--label" && i+1 < len(argv) && argv[i+1] == want {
			return true
		}
	}
	return false
}
