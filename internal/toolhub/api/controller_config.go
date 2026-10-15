package api

import ()

// CLIControllerConfig is the operator-approved envelope for bounded-cli cell
// execution. Every admitted call runs in a sibling container the controller
// creates, inspects and destroys; nothing here is model-supplied.
type CLIControllerConfig struct {
	AllowPackageInstall bool `json:"allow_package_install,omitempty"`
	// ToolsImage is the digest-pinned image catalog commands run on.
	ToolsImage string `json:"tools_image"`
	// CellInit is the host path of the statically-linked cellinit helper;
	// it is copied into the state root and bind-mounted read-only as the
	// cell's PID 1 and canary agent.
	CellInit string `json:"cell_init"`
	// ProxyImage is the digest-pinned egress proxy image; required when any
	// admitted plan declares non-loopback egress or brokered credentials.
	ProxyImage string `json:"proxy_image,omitempty"`
	// Runtime selects the OCI runtime tier: empty or "runc" (default),
	// "runsc" or "kata"; the daemon must list the runtime before a cell
	// may start on it.
	Runtime string `json:"runtime,omitempty"`
	// PerPrincipalUID maps each principal to a stable cell uid (20000+) so
	// files a read-write cell leaves in a workspace carry a per-principal
	// owner instead of the shared cli uid.
	PerPrincipalUID bool `json:"per_principal_uid,omitempty"`
	// SpacesRoot is the host root for principal workspaces; required when a
	// definition requests a principal-scoped workspace bind.
	SpacesRoot string `json:"spaces_root,omitempty"`
	// Environment names the managed deployment tier (dev/prod). When set and
	// <spaces_root>/<principal>/managed/<environment>/workspace exists, the
	// principal scope binds that managed workspace; otherwise it falls back
	// to <spaces_root>/<principal>/workspace for unmanaged principals.
	Environment string `json:"environment,omitempty"`
	// AllowedEgress is the union of egress hosts any CLI plan may declare.
	// Cells with non-loopback egress get a per-cell internal network plus a
	// CONNECT-allowlisted Squid proxy; all other cells are network none.
	AllowedEgress []string `json:"allowed_egress"`
	// Ceiling bounds every admitted execution policy; the plan may be
	// stricter but never looser.
	Ceiling ExecutionPolicy `json:"ceiling"`
	// Approved are the immutable CLI plans the controller will attest:
	// definition id+version, command, optional image+digest and the exact
	// execution policy.
	Approved []CLIApproval `json:"approved"`
	// UserCommands admits owner-registered catalog command plans: the bare
	// command name must be listed and the policy must fit Ceiling. Entries
	// are reviewed for exec-capability (hooks, preprocessors, subcommands
	// that spawn binaries); a binary that can run other code never belongs
	// here. Image-carrying artifact plans are never admitted this way.
	UserCommands []string `json:"user_commands,omitempty"`
	// Toolboxes are operator-pinned toolsets for toolbox-lifecycle cells:
	// name -> digest-pinned member images mounted read-only at /tools/<i>.
	Toolboxes map[string][]string `json:"toolboxes,omitempty"`
	// PoolSize caps live cells (idle + claimed) per stateless pool key;
	// callers past the cap wait up to PoolClaimWaitSeconds then fail closed.
	PoolSize             int `json:"pool_size,omitempty"`
	PoolClaimWaitSeconds int `json:"pool_claim_wait_seconds,omitempty"`
	// WarmMaxAgeSeconds and WarmMaxReuse bound warm-cell reuse; a cell past
	// either limit is rotated before another exec claims it.
	WarmMaxAgeSeconds int `json:"warm_max_age_seconds,omitempty"`
	WarmMaxReuse      int `json:"warm_max_reuse,omitempty"`
}
