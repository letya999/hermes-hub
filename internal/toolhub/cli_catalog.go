package toolhub

// The shipped bounded-cli catalog. Definitions are immutable release content:
// they name binaries already pinned inside the ToolHub image, declare only
// --flag scalar inputs and run per-user so the workspace cwd never crosses
// principals. Registered by the operator (hubctl connector catalog-cli);
// visibility follows the existing catalog grant model.

// CLICatalogDefinitions returns the release's immutable catalog CLI set.
func CLICatalogDefinitions() []ToolDefinition {
	return []ToolDefinition{cliGitLsRemoteDefinition(), cliRipgrepDefinition()}
}

// cliExecution is the uniform bounded-cli resource plan: every bound maps
// one-to-one onto the cell's cgroup limits, and the loopback-only egress
// keeps the cell on --network none (no interface, no proxy).
func cliExecution() ExecutionPolicy {
	return ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: []string{"127.0.0.1"}}
}

// cliGitLsRemoteDefinition lists refs of a caller-named public repository.
// The URL operand is pattern-locked to https on the reviewed egress hosts,
// so the cell cannot be steered to file://, ssh:// or an off-allowlist host;
// anonymous fetch needs no credential, which keeps the tool bindable before
// any git connection is onboarded.
func cliGitLsRemoteDefinition() ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "cli-git-ls-remote", Version: "1.0.2", Transport: BoundedCLI,
		Source:             DefinitionSource{Command: "git", Args: []string{"ls-remote", "--heads", "--tags"}},
		RuntimeEnvironment: map[string]string{"GIT_TERMINAL_PROMPT": "0"},
		Tools: []ToolSpec{
			{Name: "ls-remote", Description: "List branch and tag refs of a public repository over https. Works anonymously; private repositories need a connected git credential.", Effect: ReadEffect,
				CapabilityID: "cli.git", Uses: []CapabilityUse{{Action: "read", Resource: "remotes"}},
				Arguments: []CLIArgument{
					{Name: "repository", Type: "string", Required: true, Pattern: `^https://(github\.com|gitlab\.com|bitbucket\.org)/[A-Za-z0-9_.\-/]+(\.git)?$`},
				}},
		},
		Workload:  WorkloadPolicy{Class: PerUser, WorkspaceScope: "none", Rationale: "pure remote operation; no workspace bind needed"},
		Execution: ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: []string{"github.com", "gitlab.com", "bitbucket.org"}},
		Health:    HealthProbe{Kind: "exec", Value: "git", TimeoutSeconds: 5},
	}
}

// cliRipgrepDefinition searches the per-principal workload workspace. Only
// content flags are exposed — no --pre, no --file, no path operands — so the
// search root stays the workspace cwd.
func cliRipgrepDefinition() ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "cli-rg-search", Version: "1.0.2", Transport: BoundedCLI,
		Source: DefinitionSource{Command: "rg", Args: []string{"--no-heading", "--color=never", "--line-number", "--max-count", "200"}},
		Tools: []ToolSpec{
			{Name: "search", Description: "Search the caller's workspace with ripgrep; bounded to 200 matches.", Effect: ReadEffect,
				CapabilityID: "cli.search", Uses: []CapabilityUse{{Action: "search", Resource: "files"}},
				Arguments: []CLIArgument{
					{Name: "pattern", Flag: "--regexp", Type: "string", Required: true},
					{Name: "type", Flag: "--type", Type: "string"},
					{Name: "context", Flag: "--context", Type: "integer"},
				}},
		},
		Workload:  WorkloadPolicy{Class: PerUser, WorkspaceScope: "principal", WorkspaceAccess: "ro", Rationale: "read-only search over the caller's own workspace; no state between calls"},
		Execution: cliExecution(),
		Health:    HealthProbe{Kind: "exec", Value: "rg", TimeoutSeconds: 5},
	}
}
