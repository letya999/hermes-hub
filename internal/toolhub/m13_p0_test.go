package toolhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
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
	status, err := control.Invoke(context.Background(), auth, "status", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "confirm", map[string]any{"onboarding_id": prepared["onboarding_id"], "nonce": status["nonce"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "enable", map[string]any{"onboarding_id": prepared["onboarding_id"]}); err != nil {
		t.Fatal(err)
	}
	preview, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Invoke(context.Background(), auth, "remove", map[string]any{"onboarding_id": prepared["onboarding_id"], "remove_nonce": preview["remove_nonce"]}); err != nil {
		t.Fatal(err)
	}
	if len(purged) == 0 {
		t.Fatal("remove released the workload without the purge flag")
	}
	if len(kept) != 0 {
		t.Fatalf("remove used keep-state release: %v", kept)
	}
}
