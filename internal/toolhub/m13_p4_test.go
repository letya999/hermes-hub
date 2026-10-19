package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		Binding:   api.ToolBinding{PrincipalID: "principal-1", ToolBindingID: "bind_9"},
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

func containsLabel(argv []string, want string) bool {
	for i, arg := range argv {
		if arg == "--label" && i+1 < len(argv) && argv[i+1] == want {
			return true
		}
	}
	return false
}
