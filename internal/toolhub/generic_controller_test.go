package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func genericDefinition(t *testing.T) (ToolDefinition, string, string) {
	t.Helper()
	d := statefulContainerDefinition()
	d.DefinitionID, d.Version = "generic-mcp", "1.0.0"
	d.Source.Image = "hermes-artifact/generic-mcp"
	d.Source.Repository = "https://github.com/example/generic-mcp"
	d.Source.CommitSHA = strings.Repeat("a", 40)
	d.Source.ArchiveDigest = "sha256:" + strings.Repeat("1", 64)
	d.Source.ProvenanceDigest = "sha256:" + strings.Repeat("2", 64)
	d.Source.SBOMDigest = "sha256:" + strings.Repeat("3", 64)
	d.Source.RecipeDigest = "sha256:" + strings.Repeat("4", 64)
	d.Source.ReviewDigest = "sha256:" + strings.Repeat("5", 64)
	d.Source.ToolContractSource = ToolContractPreflight
	digest, err := confirmedToolContractDigest(ConfirmedToolContract{Source: ToolContractPreflight, Tools: d.Tools})
	if err != nil {
		t.Fatal(err)
	}
	d.Source.ToolContractDigest = digest
	d.Credentials = nil
	d.Workload.Stateful = false
	d.Execution.Mounts = nil
	root := t.TempDir()
	toolhive := filepath.Join(root, "thv")
	seccomp := filepath.Join(root, "seccomp.json")
	if err := os.WriteFile(toolhive, []byte(linuxELFMagic+"trusted"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seccomp, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	return d, root, toolhive + "\x00" + seccomp
}

func genericPlan(d ToolDefinition) controllerPlan {
	return controllerPlan{WorkloadID: "generic-workload", DefinitionID: d.DefinitionID, DefinitionVersion: d.Version, Image: d.Source.Image, Digest: d.Source.Digest, ToolHiveVersion: d.Workload.ToolHiveVersion, SidecarImages: d.Workload.SidecarImages, Execution: d.Execution, Definition: d}
}

func TestGenericControllerAcceptsAuthenticatedDynamicDefinitions(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DynamicDefinitions: true, MaxActive: 2, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	if err := c.validatePlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.Definition.Source.Digest = "sha256:" + strings.Repeat("f", 64)
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("dynamic definition drift accepted")
	}
	plan = genericPlan(d)
	plan.Definition.Source.ProvenanceDigest = ""
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("untrusted dynamic definition accepted")
	}
}

func TestGenericControllerCredentialMountBoundary(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	mountRoot := filepath.Join(root, "broker-materialized")
	if err := os.Mkdir(mountRoot, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(mountRoot, "file-00")
	if err := os.WriteFile(secret, []byte("synthetic"), 0400); err != nil {
		t.Fatal(err)
	}
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], CredentialMountRoot: mountRoot, Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	plan.CredentialMounts = []Mount{{Source: secret, Target: "/run/secrets/config.json", ReadOnly: true}}
	if err := c.validatePlan(plan); err != nil {
		t.Fatalf("reviewed broker mount rejected: %v", err)
	}
	plan.CredentialMounts[0].Source = filepath.Join(root, "outside")
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("credential mount outside broker root accepted")
	}
	plan = genericPlan(d)
	plan.CredentialMounts = []Mount{{Source: mountRoot, Target: "/run/secrets/config.json", ReadOnly: true}}
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("credential mount directory accepted")
	}
	plan = genericPlan(d)
	plan.CredentialMounts = []Mount{{Source: secret, Target: "relative", ReadOnly: true}}
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("relative credential mount target accepted")
	}
	withoutRoot, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if err := withoutRoot.validateCredentialMounts([]Mount{{Source: secret, Target: "/run/secrets/config.json", ReadOnly: true}}); err == nil {
		t.Fatal("credential mount without an approved root accepted")
	}
	plan = genericPlan(d)
	plan.Execution.Mounts = []Mount{{Source: "connection-state", Target: "/state", ReadOnly: true}}
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("host state mount accepted without Docker fallback")
	}
	c.config.DockerFallback = true
	plan.Execution.Mounts[0].Source = "host-path"
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("unreviewed host state mount accepted")
	}
	c.config.DockerFallback = false
	c.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) == 1 && args[0] == "version" {
			return nil, nil
		}
		return []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit"), nil
	}
	plan = genericPlan(d)
	plan.CredentialMounts = []Mount{{Source: mountRoot, Target: "/run/secrets/config.json", ReadOnly: true}}
	if _, err := c.start(context.Background(), plan); err == nil {
		t.Fatal("directory credential mount reached workload startup")
	}
	plan.CredentialMounts[0].Source = secret
	if _, err := c.start(context.Background(), plan); err == nil {
		t.Fatal("ToolHive without volume support accepted credential mount")
	}
	if !credentialMountsOK([]genericMount{{Type: "bind", Source: secret, Destination: "/run/secrets/config.json", RW: false}}, []Mount{{Source: secret, Target: "/run/secrets/config.json", ReadOnly: true}}) {
		t.Fatal("valid read-only credential mount was not recognized")
	}
	if credentialMountsOK(nil, []Mount{{Source: secret, Target: "/run/secrets/config.json", ReadOnly: true}}) {
		t.Fatal("missing credential mount was accepted")
	}
	if credentialMountsOK([]genericMount{{Type: "bind", Source: secret, Destination: "/run/secrets/config.json", RW: true}}, []Mount{{Source: secret, Target: "/run/secrets/config.json", ReadOnly: true}}) {
		t.Fatal("writable credential mount was accepted")
	}
	if credentialMountsOK([]genericMount{{Type: "bind", Source: filepath.Join(mountRoot, "other"), Destination: "/run/secrets/config.json"}}, []Mount{{Source: secret, Target: "/run/secrets/config.json", ReadOnly: true}}) {
		t.Fatal("unmatched credential mount was accepted")
	}
	workload := genericWorkload{bridgeVol: "bridge-volume", plan: controllerPlan{CredentialMounts: []Mount{{Source: secret, Target: "/run/secrets/config.json", ReadOnly: true}}}}
	validFallback := []genericMount{{Type: "volume", Name: "bridge-volume", Destination: "/hermes-bridge"}, {Type: "bind", Source: secret, Destination: "/run/secrets/config.json"}}
	if !fallbackBridgeMountsOK(validFallback, workload) {
		t.Fatal("valid fallback credential mount was not recognized")
	}
	validFallback[1].Source = filepath.Join(mountRoot, "other")
	if fallbackBridgeMountsOK(validFallback, workload) {
		t.Fatal("unmatched fallback credential mount was accepted")
	}
}

func genericContainers(plan controllerPlan, seccomp, proxyVol string) []map[string]any {
	network := "hermes-" + plan.WorkloadID
	host := map[string]any{"NanoCpus": int64(plan.Execution.CPUMillis) * 1000000, "Memory": int64(plan.Execution.MemoryMiB) * 1048576, "PidsLimit": int64(plan.Execution.MaxPIDs), "Privileged": false, "ReadonlyRootfs": true, "NetworkMode": network, "PidMode": "", "CapAdd": []string{}, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges=true", "seccomp=" + seccomp}, "Binds": []string{}}
	return []map[string]any{
		{"Image": "mcp-image-id", "State": map[string]any{"Running": true}, "Config": map[string]any{"User": "10001:10001", "Image": plan.Image + "@" + plan.Digest}, "HostConfig": host, "Mounts": []any{}, "NetworkSettings": map[string]any{"Networks": map[string]any{network: map[string]string{"IPAddress": "172.20.0.3"}}}},
		{"Image": "proxy-image-id", "State": map[string]any{"Running": true}, "Config": map[string]any{"User": "31:31", "Image": plan.SidecarImages[0]}, "HostConfig": host, "Mounts": []map[string]any{{"Type": "volume", "Source": proxyVol, "Destination": "/etc/squid", "RW": true}}, "NetworkSettings": map[string]any{"Networks": map[string]any{network: map[string]string{"IPAddress": "172.20.0.2"}, "bridge": map[string]string{"IPAddress": "172.17.0.3"}}}},
	}
}

func TestGenericControllerPlanReceiptAndReuse(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	budget, err := NewWorkloadBudget(2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	var calls []string
	containers, _ := json.Marshal(genericContainers(plan, parts[1], plan.WorkloadID+"-egress-config"))
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		calls = append(calls, binary+" "+strings.Join(args, " "))
		if binary == parts[0] && args[0] == "version" {
			return []byte("ToolHive v0.48.0"), nil
		}
		if binary == parts[0] && args[0] == "run" {
			return []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit"), nil
		}
		if args[0] == "inspect" && len(args) >= 4 {
			return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
		}
		if args[0] == "inspect" && len(args) == 3 {
			return containers, nil
		}
		if args[0] == "inspect" && len(args) == 2 {
			return []byte(`[]`), nil
		}
		return nil, nil
	}
	c.commandEnv = func(_ context.Context, env map[string]string, binary string, args ...string) ([]byte, error) {
		if len(env) != 0 {
			t.Fatal("unexpected secret environment")
		}
		calls = append(calls, binary+" "+strings.Join(args, " "))
		return nil, nil
	}
	token := strings.Repeat("x", 32)
	body, _ := json.Marshal(plan)
	request := httptest.NewRequest("POST", "/admit", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	c.handler(token).ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("first admission: %d", recorder.Code)
	}
	var receipt AdmissionReceipt
	if json.Unmarshal(recorder.Body.Bytes(), &receipt) != nil || receipt.Endpoint == "" {
		t.Fatalf("missing endpoint receipt: %s", recorder.Body.String())
	}
	request = httptest.NewRequest("POST", "/admit", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	c.handler(token).ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("reuse admission: %d", recorder.Code)
	}
	plan.Digest = "sha256:" + strings.Repeat("f", 64)
	body, _ = json.Marshal(plan)
	request = httptest.NewRequest("POST", "/admit", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	c.handler(token).ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatalf("drift admission: %d", recorder.Code)
	}
	if len(calls) == 0 {
		t.Fatal("controller did not invoke ToolHive")
	}
	if toolHiveSupportsRuntime("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit") != true || toolHiveSupportsRuntime("--read-only") {
		t.Fatal("ToolHive capability gate broken")
	}
}

func TestGenericControllerRejectsProfilesAndInputs(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	budget, _ := NewWorkloadBudget(1, time.Minute)
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	workload := genericWorkload{plan: plan, endpoint: "http://127.0.0.1:18080/mcp", proxyName: "generic-workload-egress", proxyVol: "generic-workload-egress-config", dockerFallback: true, relayName: "generic-workload-relay", relayVol: "generic-workload-relay-bin", bridgeVol: "generic-workload-bridge"}
	containers := genericContainers(plan, parts[1], workload.proxyVol)
	network := "hermes-" + plan.WorkloadID
	containers[0]["Mounts"] = []map[string]any{{"Type": "volume", "Source": workload.bridgeVol, "Destination": "/hermes-bridge", "RW": false}}
	containers = append(containers, map[string]any{
		"Image": "relay-image-id", "State": map[string]any{"Running": true},
		"Config":          map[string]any{"User": "10001:10001", "Image": plan.SidecarImages[0], "Env": []string{"HERMES_BRIDGE_TOKEN=" + strings.Repeat("b", 32)}},
		"HostConfig":      containers[0]["HostConfig"],
		"Mounts":          []map[string]any{{"Type": "volume", "Source": workload.relayVol, "Destination": "/hermes-relay", "RW": false}},
		"NetworkSettings": map[string]any{"Networks": map[string]any{network: map[string]string{"IPAddress": "172.20.0.4"}, "bridge": map[string]string{"IPAddress": "172.17.0.4"}}},
	})
	valid, _ := json.Marshal(containers)
	for name, mutate := range map[string]func([]map[string]any){
		"readonly": func(v []map[string]any) { v[0]["HostConfig"].(map[string]any)["ReadonlyRootfs"] = false },
		"seccomp": func(v []map[string]any) {
			v[0]["HostConfig"].(map[string]any)["SecurityOpt"] = []string{"no-new-privileges=true"}
		},
		"bind":   func(v []map[string]any) { v[0]["HostConfig"].(map[string]any)["Binds"] = []string{"C:\\Users"} },
		"volume": func(v []map[string]any) { v[1]["Mounts"] = []any{} },
		"mcp-secret": func(v []map[string]any) {
			v[0]["Config"].(map[string]any)["Env"] = []string{"HERMES_BRIDGE_TOKEN=" + strings.Repeat("s", 32)}
		},
	} {
		var containers []map[string]any
		_ = json.Unmarshal(valid, &containers)
		mutate(containers)
		body, _ := json.Marshal(containers)
		c.command = func(context.Context, string, ...string) ([]byte, error) { return body, nil }
		if _, err := c.inspect(context.Background(), workload); err == nil {
			t.Fatalf("%s profile accepted", name)
		}
	}
	if _, err := genericProxyConfig([]string{"service.example.com:443", "10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	for _, egress := range [][]string{nil, {"bad host"}, {"2001:db8::1"}} {
		if _, err := genericProxyConfig(egress); err == nil {
			t.Fatal("unsafe egress accepted")
		}
	}
	tooMany := make([]string, 33)
	if _, err := genericProxyConfig(tooMany); err == nil {
		t.Fatal("oversized egress accepted")
	}
	if err := c.validatePlan(controllerPlan{WorkloadID: "bad", DefinitionID: d.DefinitionID}); err == nil {
		t.Fatal("incomplete plan accepted")
	}
	if err := c.validatePlan(func() controllerPlan {
		p := genericPlan(d)
		p.Execution.Mounts = []Mount{{Source: "connection-state", Target: "/state"}}
		return p
	}()); err == nil {
		t.Fatalf("host mount accepted: %v", err)
	}
}

func TestGenericSecretFileAndBudgetCleanup(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	d.Credentials = []CredentialInput{{Name: "SERVICE_TOKEN", Required: true}}
	file := filepath.Join(root, "credentials.env")
	if err := os.WriteFile(file, []byte("SERVICE_TOKEN=private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := readGenericSecrets(file, d)
	if err != nil || values["SERVICE_TOKEN"] != "private" {
		t.Fatal(values, err)
	}
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGenericSecrets(file, d); err == nil {
		t.Fatal("missing required secret accepted")
	}
	for _, body := range []string{"SERVICE_TOKEN=\n", "UNKNOWN=x\n", "SERVICE_TOKEN=x\nSERVICE_TOKEN=y\n"} {
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readGenericSecrets(file, d); err == nil {
			t.Fatal("invalid secret file accepted")
		}
	}
	budget, _ := NewWorkloadBudget(1, time.Minute)
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.Acquire(context.Background(), "generic-workload"); err != nil {
		t.Fatal(err)
	}
	c.workloads["generic-workload"] = genericWorkload{plan: genericPlan(d), proxyName: "generic-workload-egress", proxyVol: "generic-workload-egress-config"}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if got := c.CleanupIdle(context.Background(), time.Now().Add(2*time.Minute)); len(got) != 1 || got[0] != "generic-workload" {
		t.Fatal(got)
	}
	if RunGenericController(context.Background(), GenericControllerConfig{}, "0.0.0.0:1", strings.Repeat("x", 32)) == nil {
		t.Fatal("unsafe listener accepted")
	}
	if RunGenericController(context.Background(), GenericControllerConfig{}, "127.0.0.1:0", strings.Repeat("x", 32)) == nil {
		t.Fatal("invalid controller config accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunGenericController(cancelled, GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 1}, "127.0.0.1:0", strings.Repeat("x", 32))
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("controller did not stop")
	}
}

func TestGenericControllerConstructionAndCommandEnv(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	if _, err := localCommandEnv(context.Background(), map[string]string{"HERMES_TEST_ENV": "ok"}, "go", "version"); err != nil {
		t.Fatal(err)
	}
	if _, err := localCommandEnv(context.Background(), nil, "missing-hermes-command"); err == nil {
		t.Fatal("missing command succeeded")
	}
	if _, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 2, IdleTTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	for _, config := range []GenericControllerConfig{
		{StateRoot: "relative", ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 1},
		{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d},
		{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: filepath.Join(root, "missing-seccomp"), Definition: d, MaxActive: 1, IdleTTLSeconds: 1},
	} {
		if _, err := newGenericController(config); err == nil {
			t.Fatal("unsafe controller configuration accepted")
		}
	}
	if _, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 0, IdleTTLSeconds: 0}); err == nil {
		t.Fatal("missing budget accepted")
	}
	if _, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1001, IdleTTLSeconds: 1}); err == nil {
		t.Fatal("unbounded budget accepted")
	}
	if _, err := newGenericController(GenericControllerConfig{StateRoot: filepath.Join(root, "missing-root"), ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 1}); err == nil {
		t.Fatal("missing state root accepted")
	}
	badDefinition := d
	badDefinition.Source.CommitSHA = "mutable"
	if _, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: badDefinition, MaxActive: 1, IdleTTLSeconds: 1}); err == nil {
		t.Fatal("untrusted definition accepted")
	}
	if port, err := reserveLoopbackPort(); err != nil || port < 1024 || port > 65535 {
		t.Fatalf("loopback port allocation: %d %v", port, err)
	}
	shared := d
	shared.Workload.Class, shared.Credentials = Shared, []CredentialInput{{Name: "SERVICE_TOKEN", Required: true}}
	if _, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: shared, MaxActive: 1, IdleTTLSeconds: 1}); err == nil {
		t.Fatal("shared credential workload accepted")
	}
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 1})
	if err != nil {
		t.Fatal(err)
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("down") }
	if _, err := c.start(context.Background(), genericPlan(d)); err == nil {
		t.Fatal("failed ToolHive start accepted")
	}
	if toolHiveSupportsRuntime("--read-only") || !toolHiveSupportsRuntime("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit") {
		t.Fatal("ToolHive capability gate broken")
	}
	for _, value := range []string{"", "not-json", `{"x":{"IPAddress":"172.20.0.2"}}`} {
		c.command = func(context.Context, string, ...string) ([]byte, error) { return []byte(value), nil }
		if _, err := c.proxyIP(context.Background(), "proxy", "network"); err == nil {
			t.Fatal("invalid proxy address accepted")
		}
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("docker down") }
	if _, err := c.proxyIP(context.Background(), "proxy", "network"); err == nil {
		t.Fatal("proxy command failure accepted")
	}
	if reflectMounts(nil, []Mount{{Source: "connection-state", Target: "/state"}}) || reflectMounts([]Mount{{Source: "connection-state", Target: "/state"}}, []Mount{{Source: "job-state", Target: "/state"}}) {
		t.Fatal("mount comparison broken")
	}
	withCredentials := d
	withCredentials.Credentials = []CredentialInput{{Name: "SERVICE_TOKEN", Required: true}}
	workspace := filepath.Join(root, "credential-workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "credentials.env"), []byte("SERVICE_TOKEN=private\n"), 0600); err != nil {
		t.Fatal(err)
	}
	budget, _ := NewWorkloadBudget(1, time.Minute)
	c, err = newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: withCredentials, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(withCredentials)
	plan.WorkspacePath = workspace
	containers, _ := json.Marshal(genericContainers(plan, parts[1], plan.WorkloadID+"-egress-config"))
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == parts[0] && args[0] == "version" {
			return []byte("ToolHive v0.48.0"), nil
		}
		if binary == parts[0] && args[0] == "run" {
			return []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit"), nil
		}
		if args[0] == "inspect" && len(args) >= 4 {
			return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
		}
		if args[0] == "inspect" && len(args) == 3 {
			return containers, nil
		}
		return []byte(`[]`), nil
	}
	var forwarded map[string]string
	c.commandEnv = func(_ context.Context, env map[string]string, _ string, args ...string) ([]byte, error) {
		forwarded = env
		if !contains(args, "--secret") {
			return nil, errors.New("secret flag missing")
		}
		return nil, nil
	}
	if _, err := c.start(context.Background(), plan); err != nil || forwarded["TOOLHIVE_SECRETS_PROVIDER"] != "environment" || forwarded["TOOLHIVE_SECRET_HERMES_GENERIC_WORKLOAD_SERVICE_TOKEN"] != "private" {
		t.Fatalf("credential forwarding: %v %#v", err, forwarded)
	}
}

func TestGenericControllerRequiresOneContainerProxy(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	d.Transport = RemoteMCP
	d.Source = DefinitionSource{URL: "https://mcp.example", TLSMode: "required"}
	if _, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 60}); err == nil {
		t.Fatal("non-container transport accepted")
	}
	d, _, _ = genericDefinition(t)
	d.Workload.SidecarImages = append(d.Workload.SidecarImages, d.Workload.SidecarImages[0])
	if _, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 60}); err == nil {
		t.Fatal("multiple proxy images accepted")
	}
}

func TestGenericControllerHandlerDenyPaths(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	budget, _ := NewWorkloadBudget(1, time.Minute)
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	body, _ := json.Marshal(plan)
	token := strings.Repeat("x", 32)
	for _, tc := range []struct {
		method, path, auth, origin, payload string
	}{
		{"GET", "/admit", "", "", ""},
		{"POST", "/other", token, "", string(body)},
		{"POST", "/admit", "bad", "", string(body)},
		{"POST", "/admit", token, "https://evil", string(body)},
		{"POST", "/admit", token, "", "{}"},
		{"POST", "/admit", token, "", string(body) + "{}"},
		{"POST", "/admit", token, "", strings.Repeat("x", 65537)},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.payload))
		r.Header.Set("Authorization", "Bearer "+tc.auth)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.handler(token).ServeHTTP(w, r)
		if w.Code == http.StatusOK {
			t.Fatalf("deny path admitted: %+v", tc)
		}
	}
	if err := budget.Acquire(context.Background(), "other-workload"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("POST", "/admit", strings.NewReader(string(body))).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	c.handler(token).ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("budget denial status: %d", w.Code)
	}
	budget.Release("other-workload")
	c.workloads[plan.WorkloadID] = genericWorkload{plan: plan, endpoint: "http://127.0.0.1:18080/mcp"}
	conflict := plan
	conflict.WorkspacePath = filepath.Join(root, "different")
	if err := os.Mkdir(conflict.WorkspacePath, 0700); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(conflict)
	r = httptest.NewRequest("POST", "/admit", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	c.handler(token).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("workload conflict status: %d", w.Code)
	}
	for _, payload := range []string{`{}`, `{"workload_id":"../escape"}`, `{"workload_id":"release-workload","extra":true}`} {
		r = httptest.NewRequest("POST", "/release", strings.NewReader(payload))
		r.Header.Set("Authorization", "Bearer "+token)
		w = httptest.NewRecorder()
		c.handler(token).ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("invalid release accepted: payload=%s status=%d", payload, w.Code)
		}
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	c.workloads["release-workload"] = genericWorkload{plan: genericPlan(d)}
	r = httptest.NewRequest("POST", "/release", strings.NewReader(`{"workload_id":"release-workload"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	c.handler(token).ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("valid release status: %d", w.Code)
	}
	if _, ok := c.workloads["release-workload"]; ok {
		t.Fatal("released workload remained registered")
	}
}

func TestGenericControllerPlanPathAndInspectParsing(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	budget, _ := NewWorkloadBudget(1, time.Minute)
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	for _, path := range []string{filepath.Join(root, "missing"), filepath.Join(t.TempDir(), "foreign")} {
		bad := plan
		bad.WorkspacePath = path
		if err := c.validatePlan(bad); err == nil {
			t.Fatal("unsafe workspace accepted")
		}
	}
	for _, body := range [][]byte{nil, []byte("{}"), []byte("not-json")} {
		c.command = func(context.Context, string, ...string) ([]byte, error) { return body, nil }
		if _, err := c.inspect(context.Background(), genericWorkload{plan: plan, proxyName: "proxy", proxyVol: "volume"}); err == nil {
			t.Fatal("invalid inspect payload accepted")
		}
	}
	for _, body := range [][]byte{[]byte(`[]`), []byte(`[{}]`)} {
		c.command = func(context.Context, string, ...string) ([]byte, error) { return body, nil }
		if _, err := c.inspect(context.Background(), genericWorkload{plan: plan, proxyName: "proxy", proxyVol: "volume"}); err == nil {
			t.Fatal("incomplete inspect payload accepted")
		}
	}
	if _, err := readGenericSecrets(filepath.Join(root, "credentials.txt"), d); err == nil {
		t.Fatal("wrong secret extension accepted")
	}
	if _, err := readGenericSecrets(filepath.Join(root, "missing.env"), d); err == nil {
		t.Fatal("missing secret file accepted")
	}
}

func TestGenericControllerStartFailureBoundaries(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	help := []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit")
	for failAt := 1; failAt <= 10; failAt++ {
		budget, _ := NewWorkloadBudget(1, time.Minute)
		c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, Budget: budget})
		if err != nil {
			t.Fatal(err)
		}
		plan := genericPlan(d)
		containers, _ := json.Marshal(genericContainers(plan, parts[1], plan.WorkloadID+"-egress-config"))
		calls := 0
		c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
			calls++
			if calls >= failAt {
				return nil, errors.New("injected failure")
			}
			if binary == parts[0] && args[0] == "version" {
				return []byte("ToolHive v0.48.0"), nil
			}
			if binary == parts[0] && args[0] == "run" {
				return help, nil
			}
			if args[0] == "inspect" && len(args) >= 4 {
				return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
			}
			if args[0] == "inspect" && len(args) == 3 {
				return containers, nil
			}
			return []byte(`[]`), nil
		}
		c.commandEnv = func(_ context.Context, _ map[string]string, _ string, _ ...string) ([]byte, error) {
			calls++
			if calls >= failAt {
				return nil, errors.New("injected failure")
			}
			return nil, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, err = c.start(ctx, plan)
		cancel()
		if err == nil {
			t.Fatalf("failure %d was accepted", failAt)
		}
	}
}

func TestGenericControllerCapabilityGateFailsBeforeDocker(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	newController := func() *genericController {
		controller, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
		if err != nil {
			t.Fatal(err)
		}
		return controller
	}
	plan := genericPlan(d)
	c := newController()
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == parts[0] && len(args) > 0 && args[0] == "version" {
			return []byte("ToolHive v0.49.0"), nil
		}
		if binary == parts[0] && len(args) > 0 && args[0] == "run" {
			return nil, errors.New("help unavailable")
		}
		return nil, errors.New("unexpected Docker command")
	}
	if _, err := c.start(context.Background(), plan); err == nil {
		t.Fatal("ToolHive help failure accepted")
	}
	c = newController()
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == parts[0] && len(args) > 0 && args[0] == "version" {
			return []byte("ToolHive v0.49.0"), nil
		}
		if binary == parts[0] && len(args) > 0 && args[0] == "run" {
			return []byte("--read-only"), nil
		}
		return nil, errors.New("unexpected Docker command")
	}
	if _, err := c.start(context.Background(), plan); err == nil {
		t.Fatal("unsupported ToolHive profile accepted")
	}
	c = newController()
	plan.Execution.Mounts = []Mount{{Source: "state", Target: "/state"}}
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == parts[0] && len(args) > 0 && args[0] == "version" {
			return []byte("ToolHive v0.49.0"), nil
		}
		return nil, errors.New("unexpected Docker command")
	}
	if _, err := c.start(context.Background(), plan); err == nil {
		t.Fatal("host mount accepted before capability gate")
	}
}
