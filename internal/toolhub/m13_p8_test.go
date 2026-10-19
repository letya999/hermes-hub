package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func browserDefinition() api.ToolDefinition {
	return api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "browser", Version: "1.0.0", Transport: api.ContainerMCP,
		Source:    api.DefinitionSource{Image: "ghcr.io/o/browser", Digest: "sha256:" + strings.Repeat("a", 64)},
		Workload:  api.WorkloadPolicy{Class: api.PerUser, Stateful: true, Rationale: "browser profile", ToolHiveVersion: "v0.12.1", SidecarImages: []string{"ghcr.io/o/squid@sha256:" + strings.Repeat("b", 64)}},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 300, OutputBytes: 8192, CPUMillis: 1000, MemoryMiB: 2048, MaxPIDs: 256, EgressClass: api.EgressWeb},
		Tools:     []api.ToolSpec{{Name: "navigate", Effect: api.ReadEffect}},
		Health:    api.HealthProbe{Kind: "exec", Value: "true", TimeoutSeconds: 5},
	}
}

func browserConnection() api.Connection {
	return api.Connection{
		Schema: api.SchemaVersion, ConnectionID: "conn-browser", DefinitionID: "browser",
		Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, Revision: 1, Status: api.ActiveStatus,
	}
}

// S8.3: the viewer link is one-time, owner-bound and short-TTL — mint by a
// non-owner or a non-browser connection fails closed, a spent or expired
// link reports gone, a wrong nonce is unauthorized.
func TestViewerLinkLifecycle(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(browserDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(browserConnection()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	nonce, expires, err := store.MintViewerLink(aliceAuth(), "conn-browser", "op-1", now, 10*time.Minute)
	if err != nil || nonce == "" || !expires.After(now) {
		t.Fatalf("mint: nonce=%q expires=%v err=%v", nonce, expires, err)
	}
	if _, _, err := store.MintViewerLink(bobAuth(), "conn-browser", "op-1", now, 10*time.Minute); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("non-owner mint: %v", err)
	}
	if _, err := store.SpendViewerLink("conn-browser", "wrong-nonce", now); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("wrong nonce: %v", err)
	}
	if _, err := store.SpendViewerLink("conn-browser", nonce, now); err != nil {
		t.Fatalf("first spend: %v", err)
	}
	if _, err := store.SpendViewerLink("conn-browser", nonce, now); !errors.Is(err, api.ErrLinkGone) {
		t.Fatalf("second spend: %v", err)
	}
	nonce2, _, err := store.MintViewerLink(aliceAuth(), "conn-browser", "op-2", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SpendViewerLink("conn-browser", nonce2, now.Add(2*time.Minute)); !errors.Is(err, api.ErrLinkGone) {
		t.Fatalf("expired spend: %v", err)
	}
	// A non-web connection can never carry a viewer link.
	other := browserConnection()
	other.ConnectionID = "conn-plain"
	other.DefinitionID = "generic-mcp"
	def := browserDefinition()
	def.DefinitionID = "generic-mcp"
	def.Execution.EgressClass = ""
	def.Execution.Egress = []string{"api.example.com"}
	if err := store.RegisterDefinition(def); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(other); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MintViewerLink(aliceAuth(), "conn-plain", "op-3", now, time.Minute); err == nil {
		t.Fatal("viewer minted for a bounded-egress connection")
	}
}

// S8.3: GET /viewer/<id>?t=<nonce> serves the owner viewer once — a repeat
// visit gets 410 gone, a wrong nonce 403.
func TestServeViewerLinkContract(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(browserDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(browserConnection()); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	nonce, _, err := store.MintViewerLink(aliceAuth(), "conn-browser", "op-1", time.Now(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/conn-browser?t="+nonce, nil)
	req.Host = "127.0.0.1"
	gateway.serveViewer(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer status=%d body=%s", rec.Code, rec.Body.String())
	}
	again := httptest.NewRecorder()
	gateway.serveViewer(again, req)
	if again.Code != http.StatusGone {
		t.Fatalf("reused link status=%d", again.Code)
	}
	forged := httptest.NewRecorder()
	forgedReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/conn-browser?t=forged", nil)
	forgedReq.Host = "127.0.0.1"
	gateway.serveViewer(forged, forgedReq)
	if forged.Code != http.StatusForbidden {
		t.Fatalf("forged nonce status=%d", forged.Code)
	}
}

// S8.3: connector_status on a browser connection mints the one-time viewer
// link and posts it to the owner's channel — the tool result itself carries
// no link material.
func TestConnectorStatusMintsViewerLink(t *testing.T) {
	store := NewStore()
	if err := store.RegisterDefinition(browserDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(browserConnection()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutOnboarding(api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onb-browser", PrincipalID: "alice",
		ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Revision: 1,
		Mode: api.OnboardingCatalog, Phase: api.PhaseEnabled,
		DefinitionID: "browser", DefinitionVersion: "1.0.0", ConnectionID: "conn-browser",
	}); err != nil {
		t.Fatal(err)
	}
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&posted)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "test-token")
	t.Setenv("HUB_PUBLIC_ORIGIN", "https://hub.example.com")
	control := &ControlPlane{Store: store, WorkloadRoot: t.TempDir(), Now: time.Now}
	result, err := control.connectorStatus(t.Context(), aliceAuth(), "conn-browser", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Summary, "t=") || strings.Contains(result.Summary, "/viewer/") {
		t.Fatalf("link material leaked into tool result: %q", result.Summary)
	}
	viewerURL, _ := posted["viewer_url"].(string)
	if !strings.Contains(viewerURL, "/viewer/conn-browser?t=") {
		t.Fatalf("notice missing viewer url: %v", posted)
	}
	// The minted link is spendable exactly once.
	if _, err := store.SpendViewerLink("conn-browser", strings.Split(viewerURL, "t=")[1], time.Now()); err != nil {
		t.Fatalf("minted link not spendable: %v", err)
	}
}

// S8.5: a shared-memory budget is a browser workload requirement the runtime
// must honor exactly — Docker fallback expresses it through --shm-size on the
// workload container only, and the ToolHive path must probe the flag before
// trusting it.
func TestGenericFallbackAppliesShmSizeToWorkload(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	plan.Execution.ShmSizeMiB = 1024
	var calls []string
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		calls = append(calls, binary+" "+strings.Join(args, " "))
		switch {
		case binary != "docker" && len(args) > 0 && args[0] == "version":
			return []byte("ToolHive v0.48.0"), nil
		case binary != "docker" && len(args) > 0 && args[0] == "run":
			return []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit"), nil
		case binary == "docker" && len(args) > 0 && args[0] == "context":
			return []byte("npipe://./pipe/dockerDesktopLinuxEngine"), nil
		case binary == "docker" && len(args) > 0 && args[0] == "info":
			return []byte("linux"), nil
		case binary == "docker" && len(args) > 1 && args[0] == "image" && args[1] == "inspect":
			if strings.Contains(strings.Join(args, " "), "{{json .Config}}") {
				return []byte(`{"Entrypoint":["/usr/bin/serve"],"Cmd":[]}`), nil
			}
			return []byte("sha256:" + strings.Repeat("a", 64) + "\n"), nil
		case binary == "docker" && len(args) > 0 && args[0] == "inspect":
			return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
		case binary == "docker" && len(args) > 2 && args[0] == "create" && args[2] == "generic-workload":
			return nil, errors.New("stop at bridge create")
		}
		return nil, nil
	}
	if _, err := c.start(t.Context(), plan); err == nil {
		t.Fatal("expected stop at bridge create")
	}
	joined := strings.Join(calls, "\n")
	bridgeCreate := ""
	proxyCreate := ""
	for _, call := range calls {
		if strings.Contains(call, "create --name generic-workload ") {
			bridgeCreate = call
		}
		if strings.Contains(call, "create --name generic-workload-egress ") {
			proxyCreate = call
		}
	}
	if bridgeCreate == "" || !strings.Contains(bridgeCreate, "--shm-size 1024m") {
		t.Fatalf("workload create missing --shm-size: %s", joined)
	}
	if strings.Contains(proxyCreate, "--shm-size") {
		t.Fatalf("egress proxy must not inherit the workload shm budget: %s", proxyCreate)
	}
}

func TestToolHiveShmSizeGate(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")

	// Fail closed: shm is required, ToolHive cannot express it, no fallback.
	closed, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	closed.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "version" {
			return []byte("ToolHive v0.48.0"), nil
		}
		return []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit"), nil
	}
	plan := genericPlan(d)
	plan.Execution.ShmSizeMiB = 512
	if _, err := closed.start(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "shm") {
		t.Fatalf("shm-less ToolHive accepted the budget: %v", err)
	}
	plan.Execution.ShmSizeMiB = 0
	if _, err := closed.start(t.Context(), plan); err == nil || strings.Contains(err.Error(), "shm") {
		t.Fatalf("shm-free workload must not be gated by the shm check: %v", err)
	}

	// Capable ToolHive: the flag is advertised and forwarded verbatim.
	capable, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	var runArgs string
	capable.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		switch {
		case binary != "docker" && len(args) > 0 && args[0] == "version":
			return []byte("ToolHive v0.48.0"), nil
		case binary != "docker" && len(args) > 0 && args[0] == "run":
			return []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit --shm-size"), nil
		case binary == "docker" && len(args) > 1 && args[0] == "image" && args[1] == "inspect":
			if strings.Contains(strings.Join(args, " "), "{{json .Config}}") {
				return []byte(`{"Entrypoint":["/usr/bin/serve"],"Cmd":[]}`), nil
			}
			return []byte("sha256:" + strings.Repeat("a", 64) + "\n"), nil
		case binary == "docker" && len(args) > 0 && args[0] == "inspect":
			return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
		}
		return nil, nil
	}
	capable.commandEnv = func(_ context.Context, _ map[string]string, binary string, args ...string) ([]byte, error) {
		if binary != "docker" && len(args) > 0 && args[0] == "run" {
			runArgs = strings.Join(args, " ")
			return nil, errors.New("stop after thv run")
		}
		return nil, nil
	}
	plan = genericPlan(d)
	plan.Execution.ShmSizeMiB = 512
	if _, err := capable.start(t.Context(), plan); err == nil {
		t.Fatal("expected stop after thv run")
	}
	if !strings.Contains(runArgs, "--shm-size 512m") {
		t.Fatalf("shm budget not forwarded to ToolHive: %s", runArgs)
	}
}

// S8.4: a self-installed browser connector must honor the definition's staged
// toolsets on both the list and the dispatch path of the managed projection —
// read tools alone by default, interaction tools only after the toolset is on
// and writes are confirmed, eval only after its own set.
func TestManagedSelfInstallHonorsToolsetProjection(t *testing.T) {
	store := NewStore()
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "runtime", ConversationID: "c", DeliveryTargetID: "c", PolicyVersion: "policy-1", CapabilityProfile: "alice-default", Environment: "dev", Generation: 1}
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "playwright-mcp", Version: "0.0.83",
		Transport: api.AgentTools,
		Workload:  api.WorkloadPolicy{Class: api.PerUser, Rationale: "test"},
		Execution: api.ExecutionPolicy{TimeoutSeconds: 120, OutputBytes: 1 << 20, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 256, ShmSizeMiB: 1024, Egress: []string{"example.com"}},
		Health:    api.HealthProbe{Kind: "exec", Value: "true", TimeoutSeconds: 5},
		Tools: []api.ToolSpec{
			{Name: "browser_navigate", Effect: api.ReadEffect},
			{Name: "browser_snapshot", Effect: api.ReadEffect},
			{Name: "browser_click", Effect: api.WriteEffect},
			{Name: "browser_evaluate", Effect: api.WriteEffect},
		},
		Toolsets:        map[string][]string{"act": {"browser_click"}, "eval": {"browser_evaluate"}},
		DefaultEnabled:  []string{"browser_navigate", "browser_snapshot"},
		DefaultExcluded: []string{"browser_click", "browser_evaluate"},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: api.DeterministicID("onboard", "alice", "browser"),
		PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingSelfInstall, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		Phase: api.PhaseEnabled, BindingID: binding.ToolBindingID, Revision: 1, CreatedAt: time.Now()}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	profile := api.CapabilityProfile{SelfInstall: true}
	names := func() []string {
		out := []string{}
		for _, tool := range store.managedSelfInstallToolsLocked(auth, profile) {
			out = append(out, tool.Tool.Name)
		}
		slices.Sort(out)
		return out
	}
	if got := names(); !slices.Equal(got, []string{"browser_navigate", "browser_snapshot"}) {
		t.Fatalf("default projection must be the read set only: %v", got)
	}
	if err := store.EnableToolsets(auth, binding.ToolBindingID, []string{"act"}); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"browser_navigate", "browser_snapshot"}) {
		t.Fatalf("act without confirmed writes must not project: %v", got)
	}
	onboarding.WritesEnabled = true
	onboarding.Revision++
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"browser_click", "browser_navigate", "browser_snapshot"}) {
		t.Fatalf("confirmed writes + act set: %v", got)
	}
	evalName := api.ProjectedToolName(definition.DefinitionID, definition.Version, "browser_evaluate")
	if _, _, err := store.managedSelfInstallToolLocked(auth, profile, evalName); err == nil {
		t.Fatal("eval tool dispatchable without its toolset")
	}
	clickName := api.ProjectedToolName(definition.DefinitionID, definition.Version, "browser_click")
	if _, _, err := store.managedSelfInstallToolLocked(auth, profile, clickName); err != nil {
		t.Fatalf("confirmed act tool must dispatch: %v", err)
	}
}
