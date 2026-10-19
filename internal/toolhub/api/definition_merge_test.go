package api

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

// ToolDefinition.Validate is the admission gate: schema, workload class,
// source pin, bounded tools, credential delivery, execution and health
// bounds, and the CLI cell policy all fail closed.
func TestToolDefinitionValidateBranches(t *testing.T) {
	valid := func(mut func(*ToolDefinition)) ToolDefinition {
		d := ToolDefinition{
			Schema:       SchemaVersion,
			DefinitionID: "grep",
			Version:      "1.0.0",
			Transport:    BoundedCLI,
			Source:       DefinitionSource{Command: "grep"},
			Tools:        []ToolSpec{{Name: "grep", Effect: ReadEffect}},
			Workload:     WorkloadPolicy{Class: PerJob, Rationale: "searches local text"},
			Execution:    ExecutionPolicy{TimeoutSeconds: 10, OutputBytes: 4096, CPUMillis: 250, MemoryMiB: 64, MaxPIDs: 8, Egress: []string{"proxy.internal"}},
			Health:       HealthProbe{Kind: "exec", Value: "grep --version", TimeoutSeconds: 5},
		}
		if mut != nil {
			mut(&d)
		}
		return d
	}
	if err := valid(nil).Validate(); err != nil {
		t.Fatalf("minimal bounded-cli definition rejected: %v", err)
	}

	cases := []struct {
		name string
		d    ToolDefinition
	}{
		{"schema", valid(func(d *ToolDefinition) { d.Schema = 9 })},
		{"id", valid(func(d *ToolDefinition) { d.DefinitionID = "Bad!" })},
		{"version", valid(func(d *ToolDefinition) { d.Version = "v1" })},
		{"workload-class", valid(func(d *ToolDefinition) { d.Workload.Class = "mystery" })},
		{"rationale", valid(func(d *ToolDefinition) { d.Workload.Rationale = " " })},
		{"source-empty", valid(func(d *ToolDefinition) { d.Source = DefinitionSource{} })},
		{"tools-empty", valid(func(d *ToolDefinition) { d.Tools = nil })},
		{"tool-dup", valid(func(d *ToolDefinition) { d.Tools = append(d.Tools, d.Tools[0]) })},
		{"tool-input-schema", valid(func(d *ToolDefinition) { d.Tools[0].InputSchema = []byte(`{"type":"object"}`) })},
		{"tool-bad-name", valid(func(d *ToolDefinition) { d.Tools[0].Name = "Bad Name" })},
		{"tool-bad-effect", valid(func(d *ToolDefinition) { d.Tools[0].Effect = "destroy" })},
		{"tool-sandboxed", valid(func(d *ToolDefinition) { d.Tools[0].Sandboxed = true })},
		{"arg-positional-no-pattern", valid(func(d *ToolDefinition) { d.Tools[0].Arguments = []CLIArgument{{Name: "query", Type: "string"}} })},
		{"arg-bad-flag", valid(func(d *ToolDefinition) {
			d.Tools[0].Arguments = []CLIArgument{{Name: "query", Type: "string", Flag: "-q"}}
		})},
		{"arg-bad-type", valid(func(d *ToolDefinition) {
			d.Tools[0].Arguments = []CLIArgument{{Name: "query", Type: "enum", Flag: "--query"}}
		})},
		{"arg-pattern-on-int", valid(func(d *ToolDefinition) {
			d.Tools[0].Arguments = []CLIArgument{{Name: "count", Type: "integer", Flag: "--count", Pattern: "[0-9]+"}}
		})},
		{"arg-pattern-broken", valid(func(d *ToolDefinition) {
			d.Tools[0].Arguments = []CLIArgument{{Name: "query", Type: "string", Flag: "--query", Pattern: "["}}
		})},
		{"runtime-env-proxy", valid(func(d *ToolDefinition) { d.RuntimeEnvironment = map[string]string{"HTTP_PROXY": "x"} })},
		{"runtime-env-node-options", valid(func(d *ToolDefinition) { d.RuntimeEnvironment = map[string]string{"NODE_OPTIONS": "--inspect"} })},
		{"runtime-env-overlap-credential", valid(func(d *ToolDefinition) {
			d.RuntimeEnvironment = map[string]string{"API_TOKEN": "x"}
			d.Credentials = []CredentialInput{{Name: "API_TOKEN", Required: true}}
		})},
		{"env-name", valid(func(d *ToolDefinition) { d.Environment = []string{"lower_case"} })},
		{"env-dup", valid(func(d *ToolDefinition) { d.Environment = []string{"A", "A"} })},
		{"credential-name", valid(func(d *ToolDefinition) { d.Credentials = []CredentialInput{{Name: "bad-name"}} })},
		{"credential-env-with-target", valid(func(d *ToolDefinition) { d.Credentials = []CredentialInput{{Name: "TOKEN", Target: "X-Header"}} })},
		{"credential-unknown-delivery", valid(func(d *ToolDefinition) { d.Credentials = []CredentialInput{{Name: "TOKEN", Delivery: "magic"}} })},
		{"credential-header-on-cli", valid(func(d *ToolDefinition) {
			d.Credentials = []CredentialInput{{Name: "TOKEN", Delivery: "http_header", Target: "X-Auth"}}
		})},
		{"credential-brokered-bad-host", valid(func(d *ToolDefinition) {
			d.Credentials = []CredentialInput{{Name: "TOKEN", Delivery: "brokered", Target: "evil/path"}}
		})},
		{"credential-brokered-bad-prefix", valid(func(d *ToolDefinition) {
			d.Credentials = []CredentialInput{{Name: "TOKEN", Delivery: "brokered", Target: "api.example.com", Prefix: "x\ny"}}
		})},
		{"credential-shared-not-per-request", valid(func(d *ToolDefinition) {
			d.Workload.Class = Shared
			d.Credentials = []CredentialInput{{Name: "TOKEN", Required: true}}
		})},
		{"proxy-env-name", valid(func(d *ToolDefinition) { d.ProxyEnvironment = []string{"lowercase"} })},
		{"proxy-env-no-proxy", valid(func(d *ToolDefinition) { d.ProxyEnvironment = []string{"NO_PROXY"} })},
		{"proxy-env-unpaired", valid(func(d *ToolDefinition) { d.ProxyEnvironment = []string{"MY_PROXY_HOST"} })},
		{"credential-group-empty", valid(func(d *ToolDefinition) { d.CredentialGroups = [][]string{{}} })},
		{"credential-group-nonrequired", valid(func(d *ToolDefinition) {
			d.Credentials = []CredentialInput{{Name: "OPT"}}
			d.CredentialGroups = [][]string{{"OPT"}}
		})},
		{"credential-group-dup-member", valid(func(d *ToolDefinition) {
			d.Credentials = []CredentialInput{{Name: "TOKEN", Required: true}}
			d.CredentialGroups = [][]string{{"TOKEN", "TOKEN"}}
		})},
		{"contract-bad-id", valid(func(d *ToolDefinition) { d.CredentialContractID = "Bad!"; d.CredentialContractRevision = 1 })},
		{"contract-env-unmapped", valid(func(d *ToolDefinition) {
			d.CredentialContractID = "contract"
			d.CredentialContractRevision = 1
			d.CredentialContractEnv = map[string]string{"GHOST": "X"}
		})},
		{"contract-missing-required", valid(func(d *ToolDefinition) {
			d.CredentialContractID = "contract"
			d.CredentialContractRevision = 1
			d.Credentials = []CredentialInput{{Name: "TOKEN", Required: true}}
		})},
		{"contract-env-without-contract", valid(func(d *ToolDefinition) { d.CredentialContractEnv = map[string]string{"A": "B"} })},
		{"shared-stateful", valid(func(d *ToolDefinition) { d.Workload.Class = Shared; d.Workload.Stateful = true })},
		{"per-job-stateful-no-job-state", valid(func(d *ToolDefinition) { d.Workload.Stateful = true })},
		{"execution-unbounded", valid(func(d *ToolDefinition) { d.Execution.TimeoutSeconds = 0 })},
		{"egress-dup", valid(func(d *ToolDefinition) { d.Execution.Egress = []string{"a.example.com", "a.example.com"} })},
		{"egress-cidr-on-cli", valid(func(d *ToolDefinition) { d.Execution.Egress = []string{"10.0.0.0/8"} })},
		{"health-missing", valid(func(d *ToolDefinition) { d.Health = HealthProbe{} })},
		{"health-wrong-kind", valid(func(d *ToolDefinition) { d.Health.Kind = "http" })},
		{"lifecycle-on-remote", valid(func(d *ToolDefinition) {
			d.Transport = RemoteMCP
			d.Source = DefinitionSource{URL: "https://mcp.example.com/x", TLSMode: "required"}
			d.Health = HealthProbe{Kind: "http", Value: "/healthz", TimeoutSeconds: 5}
			d.Workload.Class = PerUser
			d.Workload.Lifecycle = CLILifecycleEphemeral
		})},
		{"install-on-pool", valid(func(d *ToolDefinition) { d.Workload.Stateless = true; d.Workload.InstallPackages = true })},
		{"task-on-per-user", valid(func(d *ToolDefinition) { d.Workload.Class = PerUser; d.Workload.Lifecycle = CLILifecycleTask })},
		{"binding-on-per-job", valid(func(d *ToolDefinition) { d.Workload.Lifecycle = CLILifecycleBinding })},
		{"toolbox-no-id", valid(func(d *ToolDefinition) { d.Workload.Class = PerUser; d.Workload.Lifecycle = CLILifecycleToolbox })},
		{"pool-not-stateless", valid(func(d *ToolDefinition) { d.Workload.Class = Shared; d.Workload.Lifecycle = CLILifecycleSharedPool })},
		{"lifecycle-unknown", valid(func(d *ToolDefinition) { d.Workload.Lifecycle = "eternal" })},
		{"toolbox-id-on-ephemeral", valid(func(d *ToolDefinition) { d.Workload.Toolbox = "tb1" })},
		{"workspace-scope-unknown", valid(func(d *ToolDefinition) { d.Workload.WorkspaceScope = "universe" })},
		{"workspace-access-unknown", valid(func(d *ToolDefinition) { d.Workload.WorkspaceAccess = "admin" })},
		{"workspace-none-rw", valid(func(d *ToolDefinition) { d.Workload.WorkspaceScope = "none"; d.Workload.WorkspaceAccess = "rw" })},
		{"shared-binding-scope", valid(func(d *ToolDefinition) { d.Workload.Class = Shared; d.Workload.WorkspaceScope = "binding" })},
		{"pool-with-scope", valid(func(d *ToolDefinition) {
			d.Workload.Class = Shared
			d.Workload.Stateless = true
			d.Workload.Lifecycle = CLILifecycleSharedPool
			d.Workload.WorkspaceScope = "binding"
		})},
		// shareable:connection shares one workload between members — it only
		// exists on a Shared-class definition; a per-user class would silently
		// fork the "shared" workload per member.
		{"shareable-connection-per-user", valid(func(d *ToolDefinition) { d.Workload.Class = PerUser; d.Workload.Shareable = "connection" })},
		{"shareable-unknown", valid(func(d *ToolDefinition) { d.Workload.Shareable = "team" })},
	}
	for _, tc := range cases {
		if err := tc.d.Validate(); err == nil {
			t.Fatalf("%s: invalid definition accepted", tc.name)
		}
	}
	// shareable:credential keeps per-member workloads — PerUser stays valid.
	if err := valid(func(d *ToolDefinition) { d.Workload.Class = PerUser; d.Workload.Shareable = "credential" }).Validate(); err != nil {
		t.Fatalf("per-user shareable:credential rejected: %v", err)
	}
	// shareable:connection on a Shared class is the valid pairing.
	if err := valid(func(d *ToolDefinition) { d.Workload.Class = Shared; d.Workload.Shareable = "connection" }).Validate(); err != nil {
		t.Fatalf("shared shareable:connection rejected: %v", err)
	}

	// Valid alternatives: OR-grouped credentials, brokered delivery, remote
	// transport with header credential, sandboxed agent-tools tool.
	ok := valid(func(d *ToolDefinition) {
		d.Credentials = []CredentialInput{{Name: "OAUTH_TOKEN", Required: true}, {Name: "API_KEY", Required: true}}
		d.CredentialGroups = [][]string{{"OAUTH_TOKEN"}, {"API_KEY"}}
	})
	if err := ok.Validate(); err != nil {
		t.Fatalf("grouped credentials rejected: %v", err)
	}
	ok = valid(func(d *ToolDefinition) {
		d.Credentials = []CredentialInput{{Name: "TOKEN", Required: true, Delivery: "brokered", Target: "api.example.com"}}
	})
	if err := ok.Validate(); err != nil {
		t.Fatalf("brokered credential rejected: %v", err)
	}
	ok = valid(func(d *ToolDefinition) {
		d.Transport = RemoteMCP
		d.Source = DefinitionSource{URL: "https://mcp.example.com/x", TLSMode: "required"}
		d.Health = HealthProbe{Kind: "http", Value: "/healthz", TimeoutSeconds: 5}
		d.Workload.Class = PerUser
		d.Tools[0].InputSchema = []byte(`{"type":"object"}`)
		d.Credentials = []CredentialInput{{Name: "TOKEN", Required: true, Delivery: "http_header", Target: "Authorization", Prefix: "Bearer "}}
	})
	if err := ok.Validate(); err != nil {
		t.Fatalf("remote-mcp definition rejected: %v", err)
	}
	ok = valid(func(d *ToolDefinition) {
		d.Transport = AgentTools
		d.Source = DefinitionSource{}
		d.Tools[0].Sandboxed = true
		d.Tools[0].Arguments = []CLIArgument{{Name: "path", Type: "array"}}
		d.Health = HealthProbe{Kind: "exec", Value: "true", TimeoutSeconds: 5}
	})
	if err := ok.Validate(); err != nil {
		t.Fatalf("agent-tools definition rejected: %v", err)
	}

	// Credential group satisfaction: any complete group meets a required
	// input; an ungrouped required input still needs its own value.
	d := valid(func(d *ToolDefinition) {
		d.Credentials = []CredentialInput{{Name: "OAUTH_TOKEN", Required: true}, {Name: "API_KEY", Required: true}}
		d.CredentialGroups = [][]string{{"OAUTH_TOKEN"}, {"API_KEY"}}
	})
	present := func(names ...string) func(string) bool {
		return func(n string) bool {
			for _, name := range names {
				if n == name {
					return true
				}
			}
			return false
		}
	}
	if !d.CredentialsSatisfied(present("API_KEY")) || d.CredentialsSatisfied(present()) {
		t.Fatal("credential group satisfaction wrong")
	}
	if !GroupedCredentialInput(d, "API_KEY") || GroupedCredentialInput(d, "MISSING") {
		t.Fatal("grouped input detection wrong")
	}

	if err := (OwnerRef{Type: PrincipalOwner, ID: "alice"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (OwnerRef{Type: "weird", ID: "alice"}).Validate(); err == nil {
		t.Fatal("bad owner type")
	}
	e := identity.Envelope{PrincipalID: "alice", ContextID: "ctx1"}
	if !(OwnerRef{Type: PrincipalOwner, ID: "alice"}).Matches(e) || !(OwnerRef{Type: ContextOwner, ID: "ctx1"}).Matches(e) || (OwnerRef{Type: "weird", ID: "x"}).Matches(e) {
		t.Fatal("owner match wrong")
	}

	if !ValidCommand("grep") || !ValidCommand("/usr/bin/grep") || ValidCommand("sh") || ValidCommand("run.bat") || !validShellCommand("bash") || validShellCommand("/bin/bash") {
		t.Fatal("command validation wrong")
	}
	if !validCLIFlag("--query") || validCLIFlag("-q") || validCLIFlag("--bad flag") {
		t.Fatal("flag validation wrong")
	}
	if !validCLIType("string") || validCLIType("enum") {
		t.Fatal("type validation wrong")
	}

	defJSON := `{"schema":1,"definition_id":"grep","version":"1.0.0","transport":"bounded-cli","source":{"command":"grep"},"tools":[{"name":"grep","effect":"read"}],"workload":{"class":"per-job","rationale":"search"},"execution":{"timeout_seconds":10,"output_bytes":4096,"cpu_millis":250,"memory_mib":64,"max_pids":8,"egress":["proxy.internal"]},"health":{"kind":"exec","value":"grep --version","timeout_seconds":5}}`
	if _, err := DecodeDefinition([]byte(defJSON)); err != nil {
		t.Fatalf("valid definition JSON rejected: %v", err)
	}
	if _, err := DecodeDefinition([]byte(defJSON + "\n{}")); err == nil {
		t.Fatal("two JSON documents accepted")
	}
	if _, err := DecodeDefinition([]byte(`{"schema":1,"unknown_field":true}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}
