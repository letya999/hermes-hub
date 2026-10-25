package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// A plan carrying proxy grants must spawn a cred-proxy sidecar on the
// workload's internal network, rewrite the declared endpoint env at it, keep
// every secret out of argv/inspect, and fail closed when the sidecar path is
// unavailable.
func TestGenericStartWiresCredProxySidecar(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	d.RuntimeEnvironment = map[string]string{"ACME_API": "https://api.acme.example/v1", "OTHER": "static"}
	d.Execution.Egress = append(d.Execution.Egress, "api.acme.example")
	cellinit := filepath.Join(root, "cli-cellinit")
	if err := os.WriteFile(cellinit, []byte(api.LinuxELFMagic+"seeded"), 0700); err != nil {
		t.Fatal(err)
	}
	c, err := newGenericController(GenericControllerConfig{
		StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d,
		MaxActive: 1, IdleTTLSeconds: 60,
		CLI: &api.CLIControllerConfig{
			ToolsImage:    "hermes-hub-cli-tools:test",
			CellInit:      cellinit,
			AllowedEgress: []string{"*"},
			Ceiling:       api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 65536, CPUMillis: 1000, MemoryMiB: 256, MaxPIDs: 64, Egress: []string{"api.acme.example"}},
			UserCommands:  []string{"ls"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.cellInitOK = true
	plan := genericPlan(d)
	plan.ProxyGrants = []api.ProxyGrant{{EnvName: "ACME_TOKEN", Reference: "cred_1", Hosts: []string{"api.acme.example"}, Prefix: "Bearer ", Value: "real-secret-token"}}

	help := []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit --shm-size --volume")
	containers, _ := json.Marshal(genericContainers(plan, parts[1], plan.WorkloadID+"-egress-config"))
	var credCreate, runArgs, rulesBody string
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == parts[0] {
			if args[0] == "version" {
				return []byte("ToolHive v0.48.0"), nil
			}
			if args[0] == "run" && len(args) > 1 {
				return help, nil
			}
		}
		if binary == "docker" {
			switch args[0] {
			case "image":
				return []byte("sha256:" + strings.Repeat("9", 64)), nil
			case "create":
				if slices.Contains(args, plan.WorkloadID+"-cred") {
					credCreate = strings.Join(args, " ")
				}
			case "inspect":
				if len(args) >= 4 {
					return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
				}
				return containers, nil
			case "exec":
				return []byte("match"), nil
			}
		}
		return []byte(`[]`), nil
	}
	// Rules ride stdin into the sidecar's tmpfs (`docker exec -i <cred>
	// /cellinit write /tmp/rules.json`): the read-only rootfs refuses docker
	// cp, and a host temp file would leave the secret on disk.
	c.commandStdin = func(_ context.Context, stdin io.Reader, binary string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if binary == "docker" && strings.HasPrefix(joined, "exec -i "+plan.WorkloadID+"-cred") && strings.Contains(joined, "write /tmp/rules.json") {
			body, rerr := io.ReadAll(stdin)
			if rerr == nil {
				rulesBody = string(body)
			}
		}
		return []byte(`[]`), nil
	}
	runCalls := 0
	c.commandEnv = func(_ context.Context, _ map[string]string, _ string, args ...string) ([]byte, error) {
		runCalls++
		if runCalls == 1 {
			runArgs = strings.Join(args, " ")
		}
		return nil, nil
	}
	c.runtimes = map[string]bool{"runc": true}
	c.runtimesOK = true

	workload, err := c.start(context.Background(), plan)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if workload.credName != plan.WorkloadID+"-cred" {
		t.Fatalf("cred sidecar=%q", workload.credName)
	}
	if credCreate == "" {
		t.Fatal("no cred-proxy container created")
	}
	for _, want := range []string{"cred-proxy", "--rules", "/tmp/rules.json"} {
		if !strings.Contains(credCreate, want) {
			t.Fatalf("cred create missing %q: %s", want, credCreate)
		}
	}
	if strings.Contains(credCreate, "real-secret") || strings.Contains(runArgs, "real-secret") {
		t.Fatal("secret reached container argv")
	}
	if !strings.Contains(rulesBody, "real-secret-token") || !strings.Contains(rulesBody, "api.acme.example") {
		t.Fatalf("rules file did not carry the grant: %q", rulesBody)
	}
	if !strings.Contains(runArgs, "ACME_API=http://"+plan.WorkloadID+"-cred:3129/api.acme.example/v1") {
		t.Fatalf("endpoint env not rewritten at cred-proxy: %s", runArgs)
	}
	if !strings.Contains(runArgs, "NO_PROXY=") || !strings.Contains(runArgs, plan.WorkloadID+"-cred") {
		t.Fatalf("workload missing NO_PROXY for the sidecar: %s", runArgs)
	}
	if strings.Contains(runArgs, "ACME_API=https://api.acme.example") {
		t.Fatal("real upstream URL leaked past the rewrite")
	}
}

// With no CLI sidecar support configured a proxy grant cannot be satisfied —
// the workload must fail closed instead of shipping a dead stub.
func TestGenericStartProxyGrantFailsClosedWithoutCLI(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	d.Execution.Egress = append(d.Execution.Egress, "api.acme.example")
	c, err := newGenericController(GenericControllerConfig{
		StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d,
		MaxActive: 1, IdleTTLSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	plan.ProxyGrants = []api.ProxyGrant{{EnvName: "ACME_TOKEN", Hosts: []string{"api.acme.example"}, Value: "v"}}
	c.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "version" {
			return []byte("ToolHive v0.48.0"), nil
		}
		if args[0] == "run" {
			return []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit --shm-size --volume"), nil
		}
		return []byte(`[]`), nil
	}
	if _, err := c.start(context.Background(), plan); err == nil {
		t.Fatal("proxy grant admitted without sidecar support")
	}
}

// A grant toward a host outside the reviewed egress list is a plan forgery —
// the controller re-validates instead of trusting the plan body. Value and
// prefix bounds match the bounded-cli contract so a hostile plan cannot smuggle
// an unbounded rules file either.
func TestGenericPlanRejectsGrantOutsideEgress(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	c, err := newGenericController(GenericControllerConfig{
		StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d,
		MaxActive: 1, IdleTTLSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	plan.ProxyGrants = []api.ProxyGrant{{EnvName: "ACME_TOKEN", Hosts: []string{"evil.example"}, Value: "v"}}
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("grant for undeclared egress host accepted")
	}
	plan.ProxyGrants = []api.ProxyGrant{{EnvName: "ACME_TOKEN", Hosts: []string{"service.example.com"}, Value: strings.Repeat("x", 17000)}}
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("oversized grant value accepted")
	}
	plan.ProxyGrants = []api.ProxyGrant{{EnvName: "ACME_TOKEN", Hosts: []string{"service.example.com"}, Value: ""}}
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("empty grant value accepted")
	}
	plan.ProxyGrants = []api.ProxyGrant{{EnvName: "ACME_TOKEN", Hosts: []string{"service.example.com"}, Prefix: "a\rb", Value: "v"}}
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("unsafe prefix accepted")
	}
	plan.ProxyGrants = []api.ProxyGrant{{EnvName: "not a name!", Hosts: []string{"service.example.com"}, Value: "v"}}
	if err := c.validatePlan(plan); err == nil {
		t.Fatal("unsafe env name accepted")
	}
}

// A released workload must take its cred-proxy sidecar down with it — a
// stranded <wl>-cred container would keep serving the stale credential.
func TestGenericRemoveWorkloadDropsCredSidecar(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	c, err := newGenericController(GenericControllerConfig{
		StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d,
		MaxActive: 1, IdleTTLSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	var rmArgs string
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == "docker" && len(args) > 0 && args[0] == "rm" {
			rmArgs = strings.Join(args, " ")
		}
		return []byte(`[]`), nil
	}
	workload := genericWorkload{plan: plan, proxyName: plan.WorkloadID + "-egress", credName: plan.WorkloadID + "-cred"}
	if err := c.removeWorkload(context.Background(), workload, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rmArgs, plan.WorkloadID+"-cred") {
		t.Fatalf("cred sidecar survived removal: %s", rmArgs)
	}
}

// The orphan-purge path (release for a workload the controller forgot) must
// also drop a leftover cred-proxy container, not just the egress proxy.
func TestGenericPurgeDropsCredSidecar(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	c, err := newGenericController(GenericControllerConfig{
		StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d,
		MaxActive: 1, IdleTTLSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	var rmArgs string
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == "docker" && len(args) > 0 && args[0] == "rm" {
			rmArgs = strings.Join(args, " ")
		}
		return []byte(`[]`), nil
	}
	if err := c.purgeWorkloadByName(context.Background(), "generic-workload"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rmArgs, "generic-workload-cred") {
		t.Fatalf("purge left cred sidecar: %s", rmArgs)
	}
}

// When the workload spawn fails after the cred sidecar is already up, the
// rollback defer must remove the sidecar too — otherwise a credential-
// injecting container stays running with no workload.
func TestGenericStartRollbackDropsCredSidecar(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	d.Execution.Egress = append(d.Execution.Egress, "api.acme.example")
	cellinit := filepath.Join(root, "cli-cellinit")
	if err := os.WriteFile(cellinit, []byte(api.LinuxELFMagic+"seeded"), 0700); err != nil {
		t.Fatal(err)
	}
	c, err := newGenericController(GenericControllerConfig{
		StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d,
		MaxActive: 1, IdleTTLSeconds: 60,
		CLI: &api.CLIControllerConfig{
			ToolsImage:    "hermes-hub-cli-tools:test",
			CellInit:      cellinit,
			AllowedEgress: []string{"*"},
			Ceiling:       api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 65536, CPUMillis: 1000, MemoryMiB: 256, MaxPIDs: 64, Egress: []string{"api.acme.example"}},
			UserCommands:  []string{"ls"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.cellInitOK = true
	plan := genericPlan(d)
	plan.ProxyGrants = []api.ProxyGrant{{EnvName: "ACME_TOKEN", Hosts: []string{"api.acme.example"}, Value: "v"}}

	var rmArgs string
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == parts[0] {
			if args[0] == "version" {
				return []byte("ToolHive v0.48.0"), nil
			}
			if args[0] == "run" {
				return []byte("--read-only --security-opt --cpus --memory --pids-limit --cap-drop --user --timeout --output-limit --shm-size --volume"), nil
			}
		}
		if binary == "docker" {
			switch args[0] {
			case "image":
				return []byte("sha256:" + strings.Repeat("9", 64)), nil
			case "inspect":
				if len(args) >= 4 {
					return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
				}
			case "exec":
				return []byte("match"), nil
			case "rm":
				rmArgs = strings.Join(args, " ")
			}
		}
		return []byte(`[]`), nil
	}
	c.commandStdin = func(_ context.Context, _ io.Reader, _ string, _ ...string) ([]byte, error) {
		return []byte(""), nil
	}
	c.commandEnv = func(_ context.Context, _ map[string]string, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("toolhive run failed")
	}
	c.runtimes = map[string]bool{"runc": true}
	c.runtimesOK = true

	if _, err := c.start(context.Background(), plan); err == nil {
		t.Fatal("expected start failure")
	}
	if !strings.Contains(rmArgs, plan.WorkloadID+"-cred") {
		t.Fatalf("rollback left cred sidecar running: %s", rmArgs)
	}
}
