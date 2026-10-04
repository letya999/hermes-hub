package supervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
)

// writeManagedFixtures installs the host-side files a managed supervised
// context needs: reviewed settings, the managed env file, a zero-template
// source config and the supervisor execution selection.
func writeManagedFixtures(t *testing.T, ctxRoot string) stack.Settings {
	t.Helper()
	settings := stack.Settings{Schema: 1, User: "alice", Environment: "prod", Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1}
	body, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "settings.yaml"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "managed-runtime.prod.env"), []byte("HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := yaml.Marshal(stack.Config(settings))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, "hermes.prod.yaml"), source, 0600); err != nil {
		t.Fatal(err)
	}
	selection := stack.ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "supervisor", SupervisorURL: "http://localhost:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
	selectionBody, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctxRoot, stack.ExecutionPath("prod")), selectionBody, 0600); err != nil {
		t.Fatal(err)
	}
	return settings
}

func argAfter(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func TestManagedRunArgsUseIsolatedAgentNetwork(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(bound, "hub-test-container", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if argAfter(args, "--network") != "hermes-hub-agent-alice-prod" || argAfter(args, "--network-alias") != "hub-test-container" {
		t.Fatalf("managed runtime is not pinned to the private agent network: %s", joined)
	}
	for _, denied := range []string{"-p ", "--add-host", "dst=/scope", "dst=/org", "dst=/archive", "dst=/state/google", "broker-secrets", "/opt/hub/skills", "host.docker.internal"} {
		if strings.Contains(joined, denied) {
			t.Fatalf("managed run args carry denied authority %q: %s", denied, joined)
		}
	}
	envFiles := []string{}
	for i, arg := range args {
		if arg == "--env-file" && i+1 < len(args) {
			envFiles = append(envFiles, filepath.Base(args[i+1]))
		}
	}
	if !slices.Equal(envFiles, []string{"managed-runtime.prod.env", "runtime.auth"}) {
		t.Fatalf("managed env files wrong: %v", envFiles)
	}
	managedDir := filepath.Join(root, "managed", "prod")
	for _, pair := range [][2]string{{"runtime", "/state"}, {"hermes", "/state/hermes"}, {"home", "/state/home"}, {"cache", "/state/cache"}} {
		want := "type=bind,src=" + filepath.Join(managedDir, pair[0]) + ",dst=" + pair[1]
		if !slices.Contains(args, want) {
			t.Fatalf("managed mount missing %s: %s", want, joined)
		}
	}
	if !slices.Contains(args, "type=bind,src="+filepath.Join(root, "generated", "hermes-effective.prod.yaml")+",dst=/state/hermes/config.yaml,readonly") {
		t.Fatal("read-only effective config mount missing")
	}
	for _, name := range managedExtensionRootTmpfs {
		if !slices.Contains(args, "/state/hermes/"+name+":ro,mode=0555") {
			t.Fatalf("sealed extension root %s missing", name)
		}
	}
	for _, want := range []string{"HUB_CAPABILITY_MODE=managed", "HUB_CAPABILITY_PROFILE_ID=alice-default", "HUB_CAPABILITY_GENERATION=1", "HUB_MANAGED_MODEL_URL=http://model-relay:8318/v1", "HUB_RUNTIME_GENERATION=gen-1"} {
		if !slices.Contains(args, want) {
			t.Fatalf("managed environment missing %s", want)
		}
	}
	if args[len(args)-2] != "hermes:test" || args[len(args)-1] != "serve" {
		t.Fatalf("managed run tail wrong: %v", args[len(args)-2:])
	}
}

func TestManagedNormalizeCreatesSeparateStateAndRejectsMissingEnv(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	if _, err := m.normalize(binding(root)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime", "hermes", "home", "cache"} {
		if info, err := os.Stat(filepath.Join(root, "managed", "prod", name)); err != nil || !info.IsDir() {
			t.Fatalf("managed state directory %s missing", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "connections")); !os.IsNotExist(err) {
		t.Fatal("managed normalize created legacy shared state")
	}
	if err := os.Remove(filepath.Join(root, "managed-runtime.prod.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.normalize(binding(root)); err == nil {
		t.Fatal("managed context normalized without its env file")
	}
}

func TestManagedEnsureRequiresSupervisorSelection(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	if err := os.Remove(filepath.Join(root, stack.ExecutionPath("prod"))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), binding(root)); err == nil || !strings.Contains(err.Error(), "supervisor execution selection") {
		t.Fatalf("managed context accepted without supervisor selection: %v", err)
	}
}

func TestManagedEnsureSpawnsControlRelayAndIsolatedRuntime(t *testing.T) {
	var runs, connects, creates [][]string
	command := func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case args[0] == "inspect" && args[2] == "{{.State.Status}}":
			return nil, errors.New("no such container")
		case args[0] == "network" && args[1] == "inspect" && strings.Contains(strings.Join(args, " "), "Internal"):
			return []byte("true"), nil
		case args[0] == "network" && args[1] == "inspect":
			return nil, errors.New("no such network")
		case args[0] == "network" && args[1] == "create":
			creates = append(creates, append([]string{}, args...))
			return []byte("id"), nil
		case args[0] == "network" && args[1] == "connect":
			connects = append(connects, append([]string{}, args...))
			return []byte(""), nil
		case args[0] == "run":
			runs = append(runs, append([]string{}, args...))
			return []byte("cid"), nil
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, func(context.Context, string, string) error { return nil })
	writeManagedFixtures(t, root)
	runtime, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.Managed || runtime.Container == "" {
		t.Fatalf("managed runtime not flagged: %+v", runtime)
	}
	if len(runs) != 2 {
		t.Fatalf("expected relay+runtime spawns, got %d", len(runs))
	}
	relay, agent := runs[0], runs[1]
	relayName, runtimeName := argAfter(relay, "--name"), argAfter(agent, "--name")
	if relayName != runtimeName+"-ctl" {
		t.Fatalf("control relay not bound to runtime name: %s vs %s", relayName, runtimeName)
	}
	if runtimeName != runtime.Container {
		t.Fatalf("recorded container mismatch: %s vs %s", runtimeName, runtime.Container)
	}
	if argAfter(relay, "--network") != "hermes-hub-agent-alice-prod" {
		t.Fatal("control relay is not on the agent network")
	}
	port := strings.TrimPrefix(runtime.Address, "http://127.0.0.1:")
	if argAfter(relay, "-p") != "127.0.0.1:"+port+":8091" {
		t.Fatalf("control relay publish wrong: %s", strings.Join(relay, " "))
	}
	target := "HUB_CONTROL_RELAY_TARGET=http://" + runtimeName + ":8080"
	if !slices.Contains(relay, target) {
		t.Fatalf("control relay target wrong: %s", strings.Join(relay, " "))
	}
	if argAfter(relay, "--entrypoint") != "hub-runtime" || relay[len(relay)-1] != "control-relay" {
		t.Fatal("control relay does not run the reviewed entrypoint")
	}
	if len(creates) != 1 || creates[0][len(creates[0])-1] != "hermes-hub-control" {
		t.Fatalf("control network not created once: %v", creates)
	}
	if len(connects) != 1 || connects[0][2] != "hermes-hub-control" || connects[0][3] != relayName {
		t.Fatalf("control relay not dual-homed onto control network: %v", connects)
	}
	if argAfter(agent, "--network") != "hermes-hub-agent-alice-prod" || slices.Contains(agent, "-p") {
		t.Fatal("managed runtime published a port or left the agent network")
	}
}

func TestManagedRelayFailureRemovesSidecar(t *testing.T) {
	var removed []string
	command := func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case args[0] == "inspect" && args[2] == "{{.State.Status}}":
			return nil, errors.New("no such container")
		case args[0] == "network" && args[1] == "inspect" && strings.Contains(strings.Join(args, " "), "Internal"):
			return []byte("true"), nil
		case args[0] == "network" && args[1] == "inspect":
			return []byte("existing-id"), nil
		case args[0] == "network" && args[1] == "connect":
			return nil, errors.New("connect refused")
		case args[0] == "run":
			return []byte("cid"), nil
		case args[0] == "rm":
			removed = append(removed, args[len(args)-1])
			return []byte(""), nil
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, func(context.Context, string, string) error { return nil })
	writeManagedFixtures(t, root)
	if _, err := m.Ensure(context.Background(), binding(root)); err == nil || !strings.Contains(err.Error(), "managed topology") {
		t.Fatalf("managed spawn ignored relay failure: %v", err)
	}
	if len(removed) == 0 || !strings.HasSuffix(removed[0], "-ctl") {
		t.Fatalf("control relay sidecar not removed after connect failure: %v", removed)
	}
}

func TestManagedReapRemovesRuntimeAndSidecar(t *testing.T) {
	var removed []string
	command := func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case args[0] == "inspect" && args[2] == "{{.State.Status}}":
			return nil, errors.New("no such container")
		case args[0] == "network" && args[1] == "inspect" && strings.Contains(strings.Join(args, " "), "Internal"):
			return []byte("true"), nil
		case args[0] == "network" && args[1] == "inspect":
			return []byte("existing-id"), nil
		case args[0] == "rm":
			removed = append(removed, args[len(args)-1])
			return []byte(""), nil
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, func(context.Context, string, string) error { return nil })
	writeManagedFixtures(t, root)
	runtime, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ReleaseBinding(binding(root)); err != nil {
		t.Fatal(err)
	}
	if err := m.Reap(context.Background(), m.cfg.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0] == runtime.Container+"-ctl" || removed[1] != runtime.Container+"-ctl" {
		t.Fatalf("managed reap did not remove runtime then its relay: %v", removed)
	}
}

func TestManagedNonInternalAgentNetworkRefuses(t *testing.T) {
	command := func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case args[0] == "inspect" && args[2] == "{{.State.Status}}":
			return nil, errors.New("no such container")
		case args[0] == "network" && args[1] == "inspect" && strings.Contains(strings.Join(args, " "), "Internal"):
			return []byte("false"), nil
		case args[0] == "network" && args[1] == "inspect":
			return []byte("id"), nil
		case args[0] == "ps":
			return []byte(""), nil
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, func(context.Context, string, string) error { return nil })
	writeManagedFixtures(t, root)
	if _, err := m.Ensure(context.Background(), binding(root)); err == nil || !strings.Contains(err.Error(), "not internal") {
		t.Fatalf("managed runtime spawned on a routed network: %v", err)
	}
}

func TestManagedRuntimeSkipsScopeMountOwnership(t *testing.T) {
	var mounted bool
	command := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" && args[2] == "{{json .Mounts}}" {
			mounted = true
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, nil)
	bound := binding(root)
	key := runtimeKey(bound)
	runtime := Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-1", Container: "hub-x", Managed: true}
	if _, err := m.command(context.Background(), "run", "--name", "hub-x",
		"--label", "hermes-hub.owner="+m.ownerID(),
		"--label", "hermes-hub.context="+hex.EncodeToString(hashBytes(key)),
		"--label", "hermes-hub.generation=gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyOwnership(context.Background(), runtime); err != nil {
		t.Fatalf("managed ownership rejected without /scope: %v", err)
	}
	if mounted {
		t.Fatal("managed ownership still inspected /scope mounts")
	}
}
