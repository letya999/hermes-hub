package api

import (
	"slices"
	"strings"
	"testing"
)

func preparedEntryFor(t *testing.T, id string) PreparedEntry {
	t.Helper()
	entries, err := PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("prepared entry %q missing from embedded catalog", id)
	return PreparedEntry{}
}

// S8.4: the playwright-mcp catalog entry is a real T0 recipe — pinned source,
// pinned CI image, web egress class, per-connection state volume, staged
// toolsets and a reviewed browser-session contract.
func TestPreparedPlaywrightEntryShape(t *testing.T) {
	entry := preparedEntryFor(t, "playwright-mcp")
	if entry.Tier != TierT0 || entry.Image == nil || entry.Source.CommitSHA == "" {
		t.Fatalf("playwright-mcp must be a T0 source+image pin: tier=%q image=%+v", entry.Tier, entry.Image)
	}
	if !strings.HasPrefix(entry.Image.Ref, "mcr.microsoft.com/playwright/mcp@sha256:") || !DigestPattern.MatchString(entry.Image.Digest) || !strings.HasSuffix(entry.Image.Ref, entry.Image.Digest) {
		t.Fatalf("image pin is not a digest-bound mcr ref: %+v", entry.Image)
	}
	if !entry.Stateful || entry.StateTarget != "/state/browser-profile" || entry.EgressClass != EgressWeb {
		t.Fatalf("browser entry must be stateful web-egress: %+v", entry)
	}
	if len(entry.Egress) != 0 {
		t.Fatalf("web egress entries carry deny entries only, got allowlist %v", entry.Egress)
	}
	for _, set := range []string{"act", "eval", "network"} {
		if len(entry.Toolsets[set]) == 0 {
			t.Fatalf("toolset %q missing", set)
		}
	}
	for _, name := range entry.Toolsets["eval"] {
		if !slices.Contains(entry.DefaultExcluded, name) {
			t.Fatalf("eval tool %q must stay default-excluded", name)
		}
	}
	// Default projection is the reviewed read set; interaction, eval and
	// network tools stay hidden behind their own toolsets.
	for _, name := range entry.DefaultEnabled {
		if !slices.Contains(entry.ReadTools, name) {
			t.Fatalf("default-enabled %q is not a reviewed read tool", name)
		}
	}
}

// S8.4: Apply projects the reviewed browser shape — web egress, budgets, and
// a connection-state volume at the state target because no Broker state block
// manages it.
func TestPreparedPlaywrightApply(t *testing.T) {
	entry := preparedEntryFor(t, "playwright-mcp")
	config := entry.Apply(DefaultSelfInstallConfig(entry.Source))
	if config.Execution.EgressClass != EgressWeb || len(config.Execution.Egress) != 0 {
		t.Fatalf("execution: %+v", config.Execution)
	}
	if config.Execution.ShmSizeMiB < 512 {
		t.Fatalf("chromium needs shared memory headroom: %+v", config.Execution)
	}
	if config.Execution.MemoryMiB < 1024 || config.Execution.MaxPIDs < 128 {
		t.Fatalf("browser budgets too small for chromium: %+v", config.Execution)
	}
	mounted := slices.ContainsFunc(config.Execution.Mounts, func(m Mount) bool {
		return m.Source == "connection-state" && m.Target == "/state/browser-profile" && !m.ReadOnly
	})
	if !mounted {
		t.Fatalf("browser profile must ride a writable connection-state volume: %+v", config.Execution.Mounts)
	}
	workload := config.Workload
	workload.ToolHiveVersion = "v0.48.0"
	workload.SidecarImages = []string{"squid@sha256:" + strings.Repeat("c", 64)}
	definition := ToolDefinition{Schema: SchemaVersion, DefinitionID: entry.ID, Version: config.Version, Transport: ContainerMCP,
		Source:      DefinitionSource{Image: config.Image, Digest: entry.Image.Digest},
		Credentials: config.Credentials, Tools: []ToolSpec{{Name: "browser_navigate", Effect: ReadEffect}},
		Workload: workload, Execution: config.Execution, Health: config.Health}
	if err := definition.Validate(); err != nil {
		t.Fatalf("applied browser definition invalid: %v", err)
	}
}

// S8.4: google-calendar's state target is Broker-managed — Apply must not
// also hand it a named volume over the same path.
func TestApplySkipsMountForBrokerManagedState(t *testing.T) {
	entry := preparedEntryFor(t, "google-calendar")
	config := entry.Apply(DefaultSelfInstallConfig(entry.Source))
	if slices.ContainsFunc(config.Execution.Mounts, func(m Mount) bool { return m.Target == entry.StateTarget }) {
		t.Fatalf("broker-managed state target must not become a named volume: %+v", config.Execution.Mounts)
	}
}

// S8.4: default projection shows read tools; act shows on its toolset; eval
// stays hidden until its own set is enabled — even when act is enabled.
func TestBrowserToolsetProjection(t *testing.T) {
	entry := preparedEntryFor(t, "playwright-mcp")
	config := entry.Apply(DefaultSelfInstallConfig(entry.Source))
	var tools []ToolSpec
	all := append([]string(nil), config.DefaultEnabled...)
	for _, set := range entry.Toolsets {
		all = append(all, set...)
	}
	for _, name := range all {
		effect := ReadEffect
		if slices.Contains(entry.Toolsets["act"], name) || slices.Contains(entry.Toolsets["eval"], name) {
			effect = WriteEffect
		}
		tools = append(tools, ToolSpec{Name: name, Effect: effect})
	}
	definition := ToolDefinition{Tools: tools, Toolsets: config.Toolsets, DefaultEnabled: config.DefaultEnabled, DefaultExcluded: config.DefaultExcluded}
	names := func(specs []ToolSpec) []string {
		out := make([]string, 0, len(specs))
		for _, spec := range specs {
			out = append(out, spec.Name)
		}
		return out
	}
	projected := names(definition.ProjectedToolSpecs(ToolBinding{}))
	for _, name := range config.DefaultEnabled {
		if !slices.Contains(projected, name) {
			t.Fatalf("read tool %q missing from default projection", name)
		}
	}
	for _, hidden := range append(append([]string(nil), entry.Toolsets["act"]...), entry.Toolsets["eval"]...) {
		if slices.Contains(projected, hidden) {
			t.Fatalf("%q must stay hidden by default", hidden)
		}
	}
	act := names(definition.ProjectedToolSpecs(ToolBinding{EnabledToolsets: []string{"act"}}))
	if !slices.Contains(act, "browser_click") || slices.Contains(act, "browser_evaluate") {
		t.Fatalf("act toolset must surface clicks but keep eval hidden: %v", act)
	}
	both := names(definition.ProjectedToolSpecs(ToolBinding{EnabledToolsets: []string{"act", "eval"}}))
	if !slices.Contains(both, "browser_evaluate") || slices.Contains(both, "browser_network_requests") {
		t.Fatalf("eval surfaces only on its own toolset; network stays hidden: %v", both)
	}
}

// S8.5: shm_size_mib is bounded — chromium needs it, the platform caps it.
func TestShmSizeValidation(t *testing.T) {
	base := ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 2 << 20, CPUMillis: 1000, MemoryMiB: 2048, MaxPIDs: 128, EgressClass: EgressWeb}
	if err := base.Validate(PerUser); err != nil {
		t.Fatal(err)
	}
	for _, shm := range []int{-1, 4097, 4096} {
		bad := base
		bad.ShmSizeMiB = shm
		if bad.ShmSizeMiB > bad.MemoryMiB {
			if err := bad.Validate(PerUser); err == nil {
				t.Fatalf("shm %d above memory accepted", shm)
			}
			continue
		}
		if err := bad.Validate(PerUser); err == nil {
			t.Fatalf("shm %d accepted", shm)
		}
	}
	ok := base
	ok.ShmSizeMiB = 1024
	if err := ok.Validate(PerUser); err != nil {
		t.Fatalf("shm 1024 under memory rejected: %v", err)
	}
	a, b := base, base
	b.ShmSizeMiB = 1024
	if ReflectExecution(a, b) {
		t.Fatal("shm drift must break plan equality")
	}
}

// S8.4: the browser-session contract is reviewed — storage-state import is an
// optional file credential, origin lists are optional env strings; no secret
// lands in a ledger or tool result.
func TestBrowserSessionContract(t *testing.T) {
	entry := preparedEntryFor(t, "playwright-mcp")
	brokerContract, err := entry.BrokerContract()
	if err != nil {
		t.Fatal(err)
	}
	if brokerContract.ID != entry.ContractID || brokerContract.State != nil {
		t.Fatalf("browser contract must not claim a managed state dir: %+v", brokerContract.State)
	}
	var fileDelivery, envDeliveries int
	for _, delivery := range brokerContract.Deliveries {
		switch delivery.Type {
		case "file":
			fileDelivery++
			if delivery.EnvName != "PLAYWRIGHT_MCP_STORAGE_STATE" || delivery.Target != "/run/mcp-secrets/browser/storage-state.json" {
				t.Fatalf("storage-state file delivery: %+v", delivery)
			}
		case "env":
			envDeliveries++
		}
	}
	if fileDelivery != 1 || envDeliveries != 2 {
		t.Fatalf("contract deliveries: %+v", brokerContract.Deliveries)
	}
	for _, field := range brokerContract.Fields {
		if field.Required {
			t.Fatalf("browser session fields stay optional: %+v", field)
		}
	}
}

// S8.4: agent-browser has no published connector image — the slot is
// discoverable and honestly blocked, not a fake pin.
func TestBlockedAgentBrowser(t *testing.T) {
	entries, err := PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ID == "agent-browser" {
			t.Fatal("agent-browser must not be installable without a reviewed image")
		}
	}
	blocked, err := BlockedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range blocked {
		if entry.ID == "agent-browser" {
			found = true
			if entry.Reason == "" || entry.Runbook == "" || entry.Handoff == "" {
				t.Fatalf("incomplete blocked entry: %+v", entry)
			}
		}
	}
	if !found {
		t.Fatal("agent-browser blocked slot missing")
	}
}
