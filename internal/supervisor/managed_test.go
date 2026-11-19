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

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// publishAliceRevision authors the six passports and runs the real
// authored → compile → publish path (SPEC-0095 R1): the fixture exercises
// the same revision the supervisor's loadSelected reads, with the retired
// settings.yaml/agent.yaml out of the loop entirely. mutate may adjust the
// authored docs before compilation.
func publishAliceRevision(t *testing.T, ctxRoot string, mutate func(map[string]string)) {
	t.Helper()
	publishSpaceRevision(t, ctxRoot, "alice", mutate)
}

func publishSpaceRevision(t *testing.T, ctxRoot, user string, mutate func(map[string]string)) {
	t.Helper()
	docs := map[string]string{
		"stack": "identity: {user: " + user + ", infra: false}\nservices:\n" +
			"  hermes-runtime: {mode: supervised}\n" +
			"  model-relay: {mode: per-space}\n" +
			"  toolhub-relay: {mode: per-space}\n" +
			"  toolhub: {mode: shared}\n" +
			"  credential-broker: {mode: shared}\n" +
			"  communication-hub: {mode: shared}\n" +
			"  supervisor: {mode: shared}\n" +
			"  cliproxy: {mode: per-space}\n",
		"config": "runtime: {timezone: UTC}\nmemory: {enabled: false}\n",
		"models": "default: synthetic\nroutes: {synthetic: {ladder: [cliproxy]}}\nservices:\n" +
			"  cliproxy:\n    via: cliproxy\n    endpoint: http://cliproxy:8317/v1\n" +
			"    auth: {proxy_access: {kind: broker-key, ref: cliproxy}}\n    models: [{id: synthetic}]\n",
		"workspace": "writable: [/workspace]\nread_only: [/vault/memory]\n",
		"tools":     "binding: {profile_id: " + user + "-default, generation: 1}\ncore:\n  files: {via: toolhub}\n",
		"grants":    "allow: {}\n",
	}
	if mutate != nil {
		mutate(docs)
	}
	if err := os.MkdirAll(filepath.Join(ctxRoot, "passports"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		if err := os.WriteFile(filepath.Join(ctxRoot, "passports", name+".yaml"), []byte(docs[name]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	rev, err := passport.CompileAuthored(ctxRoot, "prod", time.Now(), catalogDocs, stack.ReservedSiblingPorts(ctxRoot), map[string][]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Publish(ctxRoot); err != nil {
		t.Fatal(err)
	}
}

// writeManagedFixtures installs the host-side files a managed supervised
// context needs: a published six-passport revision, the managed env file,
// a zero-template source config and the supervisor execution selection.
func writeManagedFixtures(t *testing.T, ctxRoot string) stack.Settings {
	t.Helper()
	publishAliceRevision(t, ctxRoot, nil)
	if err := os.WriteFile(filepath.Join(ctxRoot, "managed-runtime.prod.env"), []byte("HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"), 0600); err != nil {
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
	settings, err := stack.ReadEnvironment(ctxRoot, "prod")
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

// writeManagedRevisionFixtures installs a managed space carried entirely by
// the published six-document revision: settings.yaml, agent.yaml and
// workspace.yaml are retired (SPEC-0092 R8.1, SPEC-0095).
func writeManagedRevisionFixtures(t *testing.T, ctxRoot string) {
	t.Helper()
	publishAliceRevision(t, ctxRoot, nil)
	if err := os.WriteFile(filepath.Join(ctxRoot, "managed-runtime.prod.env"), []byte("HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"), 0600); err != nil {
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
}

func argAfter(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func TestSpawnedRuntimePreservesComposeUser(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	settings := writeManagedFixtures(t, root)
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(bound, "hub-group-test", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	service := stack.RuntimeService(settings, "", root)
	if got := argAfter(args, "--user"); got != service["user"] {
		t.Fatalf("spawned runtime user %s differs from rendered user %v; group-readable config views become inaccessible", got, service["user"])
	}
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
	for _, pair := range [][2]string{{"runtime", "/state"}, {"hermes", "/state/hermes"}, {"home", "/state/home"}, {"cache", "/state/cache"}, {"workspace", "/workspace"}} {
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
	if !slices.Contains(args, "type=bind,src="+stack.GeneratedEffectiveSkillsDir(root)+",dst=/skills,readonly") {
		t.Fatalf("managed run args missing the read-only effective view mount: %s", joined)
	}
	if _, err := os.Stat(filepath.Join(stack.GeneratedEffectiveSkillsDir(root), "hermes-hub-system-guide", "SKILL.md")); err != nil {
		t.Fatalf("managed spawn did not build the effective view: %v", err)
	}
	if strings.Contains(joined, "HUB_CAPABILITY_MODE") {
		t.Fatal("spawned runtime still receives the retired capability mode switch")
	}
	for _, want := range []string{"HUB_CAPABILITY_PROFILE_ID=alice-default", "HUB_CAPABILITY_GENERATION=1", "HUB_MANAGED_MODEL_URL=http://model-relay:8318/v1", "HUB_RUNTIME_GENERATION=gen-1"} {
		if !slices.Contains(args, want) {
			t.Fatalf("managed environment missing %s", want)
		}
	}
	if args[len(args)-2] != "hermes:test" || args[len(args)-1] != "serve" {
		t.Fatalf("managed run tail wrong: %v", args[len(args)-2:])
	}
}

func TestManagedRunArgsMountsConnectionConfigs(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	settings, err := stack.ReadEffective(root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Tools["exposure"] = stack.ToolEntry{Via: stack.ToolViaToolHub}
	settings.Tools["ssh"] = stack.ToolEntry{Via: stack.ToolViaToolHub}
	configDir := filepath.Join(root, "connections", "exposure")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	sshDir := filepath.Join(root, "connections", "ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	args, err := m.managedRunArgs(binding(root), settings, "fixture", "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "type=bind,src=" + configDir + ",dst=/state/exposure,readonly"
	if !slices.Contains(args, want) || !slices.Contains(args, "HUB_EXPOSURE_CONFIG=/state/exposure/config.yaml") ||
		!slices.Contains(args, "type=bind,src="+sshDir+",dst=/state/ssh,readonly") || !slices.Contains(args, "HUB_SSH_CONFIG=/state/ssh/config.yaml") {
		t.Fatal("managed connection configs missing from spawn")
	}
}

func TestManagedSpawnRejectsMissingConnectionConfig(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	publishAliceRevision(t, root, func(docs map[string]string) {
		docs["tools"] += "  exposure: {via: toolhub}\n"
	})
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.runArgsWithGeneration(bound, "fixture", 0, "gen-1"); err == nil || !strings.Contains(err.Error(), "exposure config") {
		t.Fatalf("managed spawn accepted missing exposure config: %v", err)
	}
}

// SPEC-0053 R1.8: managed runtimes mount the rendered effective SOUL at
// /state/hermes/SOUL.md — the raw persona alone is not what Hermes reads.
func TestManagedRunArgsMountsEffectiveSoul(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	if err := os.MkdirAll(filepath.Join(root, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	effective := filepath.Join(root, "generated", "soul-effective.prod.md")
	if err := os.WriteFile(effective, []byte("persona\ncontract"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SOUL.md"), []byte("persona"), 0600); err != nil {
		t.Fatal(err)
	}
	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(bound, "hub-test-container", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !slices.Contains(args, "type=bind,src="+effective+",dst=/state/hermes/SOUL.md,readonly") {
		t.Fatalf("managed run args missing effective SOUL mount: %s", joined)
	}
	if strings.Contains(joined, "src="+filepath.Join(root, "SOUL.md")+",dst=/state/hermes/SOUL.md") {
		t.Fatalf("managed run args mount the raw persona as the agent SOUL: %s", joined)
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

// The managed spawn path must read the published revision, not the retired
// files: a space carried entirely by agent.yaml + generated/passports still
// resolves as managed (SPEC-0092 R8.1).
func TestManagedEnsureConsumesPublishedRevision(t *testing.T) {
	command := func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case args[0] == "inspect" && args[2] == "{{.State.Status}}":
			return nil, errors.New("no such container")
		case args[0] == "network" && args[1] == "inspect" && strings.Contains(strings.Join(args, " "), "Internal"):
			return []byte("true"), nil
		case args[0] == "network" && args[1] == "inspect":
			return []byte("existing-id"), nil
		case args[0] == "run":
			return []byte("cid"), nil
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, func(context.Context, string, string) error { return nil })
	writeManagedRevisionFixtures(t, root)
	runtime, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.Managed {
		t.Fatal("passport-carried space did not resolve as managed")
	}
}

// Policy resolution hashes what a render would consume: the published
// revision, not a retired file (SPEC-0092 R8.1).
func TestManagedPolicyVersionReadsPublishedRevision(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedRevisionFixtures(t, root)
	if v := m.currentPolicyVersion("alice"); v == "" {
		t.Fatal("managed revision produced no policy version")
	}
}

func TestManagedEnsureRequiresSupervisorSelection(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	// The stack passport is itself the spawn contract (SPEC-0092 R3.2):
	// dropping the supervised hermes-runtime declaration leaves the space
	// with no authority to spawn once the execution file is gone too.
	publishAliceRevision(t, root, func(docs map[string]string) {
		docs["stack"] = strings.Replace(docs["stack"], "  hermes-runtime: {mode: supervised}\n", "", 1)
	})
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

func TestManagedMaterializeCarriesDeclaredContext(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	// Context policy is authored in the config passport and carried into
	// materialization through the effective read — the retired
	// settings.yaml can no longer inject it.
	publishAliceRevision(t, root, func(docs map[string]string) {
		docs["config"] = "runtime:\n" +
			"  timezone: UTC\n" +
			"  compression: {threshold_tokens: 150000}\n" +
			"  context: {compaction: hub, engines: [hub]}\n" +
			"  goals: {engine: hub, judge: aux, max_turns: 10}\n" +
			"  auxiliary: {summary_model: small-summary}\n" +
			"memory: {enabled: false}\n"
	})
	configPath, err := m.materializeHermesConfig(binding(root), "prod")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"threshold_tokens: 150000", "engine: hub", "judge: aux", "max_turns: 10", "model: small-summary"} {
		if !strings.Contains(text, want) {
			t.Fatalf("materialized config missing %q:\n%s", want, text)
		}
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
	// Both removals go through the immutable container IDs the fake docker
	// minted at run time — never the mutable names.
	runtimeID := hex.EncodeToString(hashBytes(runtime.Container + runtime.Generation))
	relayID := hex.EncodeToString(hashBytes(runtime.Container + "-ctl" + runtime.Generation))
	if len(removed) != 2 || removed[0] != runtimeID || removed[1] != relayID {
		t.Fatalf("managed reap did not remove runtime then its relay by ID: %v", removed)
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

// R5.1: managed runtimes get the same writable owner store beside the
// read-only effective view — tools behave identically in both modes.
func TestManagedRunArgsMountsWritableSkillsStore(t *testing.T) {
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
	want := "type=bind,src=" + filepath.Join(root, "skills") + ",dst=/skills-store"
	if !strings.Contains(joined, want) {
		t.Fatalf("managed run args missing writable skills store mount: %s", joined)
	}
	// The owner store — not a private managed/<env>/skills copy — so drafts,
	// publishes and the effective view all see the same store.
	if strings.Contains(joined, "managed"+string(filepath.Separator)+"prod"+string(filepath.Separator)+"skills") {
		t.Fatal("managed /skills-store must not bind the private managed store")
	}
	for _, env := range []string{"HUB_SKILLS_STORE=/skills-store", "HUB_SKILLS_EFFECTIVE=/skills"} {
		if !strings.Contains(joined, env) {
			t.Fatalf("managed run args missing %s", env)
		}
	}
}

// The post-publish rebuild contract rides managed spawns too: platform
// staging read-only, the view a second writable bind, the render snapshot.
func TestManagedRunArgsMountsViewRebuildContract(t *testing.T) {
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
	for _, want := range []string{
		"type=bind,src=" + stack.GeneratedSkillsDir(root) + ",dst=/skills-platform,readonly",
		"type=bind,src=" + stack.GeneratedEffectiveSkillsDir(root) + ",dst=/skills-effective-rw",
		"type=bind,src=" + stack.GeneratedSkillsMetaDir(root) + ",dst=/skills-meta,readonly",
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("managed run args missing %s", want)
		}
	}
}

// R5.2: managed org-bound spawns mount the writable org store identically.
func TestManagedRunArgsMountsOrgSkillsStoreWhenOrgScoped(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	orgDir := filepath.Join(filepath.Dir(root), "organizations", "acme")
	for _, name := range []string{"docs", "skills"} {
		if err := os.MkdirAll(filepath.Join(orgDir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	orgSettings := `schema: 1
organization: acme
members:
  alice: owner
features: [workspace]
`
	if err := os.WriteFile(filepath.Join(orgDir, "settings.yaml"), []byte(orgSettings), 0600); err != nil {
		t.Fatal(err)
	}
	// Organization membership is authored in the stack passport identity;
	// the org scope document carries the policy.
	publishAliceRevision(t, root, func(docs map[string]string) {
		docs["stack"] = strings.Replace(docs["stack"], "identity: {user: alice, infra: false}", "identity: {user: alice, infra: false, organization: acme}", 1)
	})
	bound := binding(root)
	bound.OrganizationID = "acme"
	normalized, err := m.normalize(bound)
	if err != nil {
		t.Fatal(err)
	}
	args, err := m.runArgsWithGeneration(normalized, "hub-test-container", 0, "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	want := "type=bind,src=" + filepath.Join(orgDir, "skills") + ",dst=/skills-store-org"
	if !strings.Contains(joined, want) {
		t.Fatalf("managed org-bound run args missing org store mount: %s", joined)
	}
	if !strings.Contains(joined, "HUB_SKILLS_ORG=/skills-store-org") {
		t.Fatalf("managed org-bound run args missing HUB_SKILLS_ORG: %s", joined)
	}
}

// The host-side materialization for a managed space must carry the render's
// browser grant into the effective config the spawned runtime mounts: the
// review surface (governance decisions, browser_act) is evaluated on the
// fully loaded Settings here, never re-derived in the container.
// A memory-vault + knowledge managed spawn replays the reviewed subsystem
// flags: the materialized effective config keeps the evicted native memory
// backend while open_knowledge can never appear — the retired endpoint is
// drift no replay produces (M32/#520).
func TestManagedMaterializeCarriesFeatureFlags(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	// Native pseudo-toolsets are authored in the tools passport; the
	// ToolHub governance doc still carries the section grants.
	publishAliceRevision(t, root, func(docs map[string]string) {
		docs["tools"] = strings.Replace(docs["tools"], "files: {via: toolhub}", "files: {via: toolhub}\n  knowledge_ui: {via: native}\n  memory_vault: {via: native}", 1)
		// memory.enabled is the passport's own vault switch — the native
		// toolset entry alone does not flip Has("memory_vault").
		docs["config"] = strings.Replace(docs["config"], "memory: {enabled: false}", "memory: {enabled: true}", 1)
	})
	doc := api.NewGovernance()
	now := time.Now().UTC()
	for _, section := range []string{"native:knowledge_ui", "native:memory_vault"} {
		grant := api.NewToolGrant("grant-alice-"+strings.ReplaceAll(section, ":", "-"), "alice", section, "test grant", "operator", now.Add(time.Hour), nil)
		if err := api.Confirm(&grant, "operator", now); err != nil {
			t.Fatal(err)
		}
		if err := doc.PutToolGrant(grant, now); err != nil {
			t.Fatal(err)
		}
		stamped, err := doc.RequestToolGrant("alice", grant.GrantID, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doc.AcknowledgeToolGrant("alice", grant.GrantID, stamped.RequestNonce, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.SaveGovernance(filepath.Join(root, "runtime", "toolhub", "governance.json"), doc); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, root)
	configPath, err := m.materializeHermesConfig(binding(root), "prod")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"memory_enabled: false", "toolhub"} {
		if !strings.Contains(text, want) {
			t.Fatalf("materialized config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "open_knowledge") {
		t.Fatalf("retired open_knowledge endpoint must never materialize:\n%s", text)
	}
}

func TestManagedMaterializeRoutesBrowserThroughToolHub(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("ok"), nil }, nil)
	writeManagedFixtures(t, root)
	// The browser toolset rides the tools passport with its mutation member;
	// the hub:browser / hub:browser_guest sections stay granted through the
	// ToolHub governance doc the reviewed render consulted.
	publishAliceRevision(t, root, func(docs map[string]string) {
		docs["tools"] = strings.Replace(docs["tools"], "files: {via: toolhub}", "files: {via: toolhub}\n  browser: {via: toolhub, tools: {include: [browser_open, browser_read, browser_act, browser_capture]}}", 1)
		docs["stack"] += "  browser: {mode: per-space}\n"
		docs["config"] = strings.Replace(docs["config"], "memory: {enabled: false}", "ports: {browser-vnc: 6080}\nmemory: {enabled: false}", 1)
	})
	doc := api.NewGovernance()
	now := time.Now().UTC()
	for _, section := range []string{api.SectionHubBrowser, "hub:browser_guest"} {
		grant := api.NewToolGrant("grant-alice-"+strings.ReplaceAll(section, ":", "-"), "alice", section, "test grant", "operator", now.Add(time.Hour), nil)
		if err := api.Confirm(&grant, "operator", now); err != nil {
			t.Fatal(err)
		}
		if err := doc.PutToolGrant(grant, now); err != nil {
			t.Fatal(err)
		}
		stamped, err := doc.RequestToolGrant("alice", grant.GrantID, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doc.AcknowledgeToolGrant("alice", grant.GrantID, stamped.RequestNonce, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.SaveGovernance(filepath.Join(root, "runtime", "toolhub", "governance.json"), doc); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, root)
	configPath, err := m.materializeHermesConfig(binding(root), "prod")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "toolhub") || strings.Contains(text, "browser-alice:8931/mcp") || strings.Contains(text, "browser-alice:8932/mcp") || strings.Contains(text, "browser_click") {
		t.Fatal("managed browser escaped the ToolHub route")
	}
}
