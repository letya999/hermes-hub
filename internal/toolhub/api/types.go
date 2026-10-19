package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const SchemaVersion = 1

const (
	MaxExecutionTimeout = 300
	MaxOutputBytes      = 8 << 20
	MaxMemoryMiB        = 4096
	MaxCPUMillis        = 4000
	MaxPIDs             = 256
)

var (
	VersionPattern         = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-+][0-9A-Za-z.-]+)?$`)
	toolHiveVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	toolNamePattern        = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	McpToolNamePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	CredentialPattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	HostPattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,252}(:[0-9]{1,5})?$`)
	DigestPattern          = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	CommandPattern         = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
)

var (
	ErrInvalid      = errors.New("invalid toolhub record")
	ErrNotFound     = errors.New("toolhub record not found")
	ErrConflict     = errors.New("toolhub record conflict")
	ErrUnauthorized = errors.New("toolhub authorization denied")
	ErrStale        = errors.New("toolhub record is stale")
	ErrRevoked      = errors.New("toolhub record is revoked")
	ErrDegraded     = errors.New("toolhub connection is degraded")
	// Resolution sentinels wrap ErrInvalid so existing fail-closed callers keep
	// failing closed while stable machine-readable codes reach status.
	// ErrReconsent marks a granted-set shortfall (RFC6750 insufficient_scope):
	// the fix is a new consent round, never a token refresh.
	ErrReconsent                     = fmt.Errorf("%w: re-consent required", ErrUnauthorized)
	ErrAmbiguousEntrypoint           = fmt.Errorf("%w: ambiguous MCP entrypoint", ErrInvalid)
	ErrUnsupportedCredentialContract = fmt.Errorf("%w: unsupported credential contract", ErrInvalid)
	ErrUnsupportedAuth               = fmt.Errorf("%w: unsupported authentication contract", ErrInvalid)
)

// ErrorCode maps a resolution or lifecycle failure onto a stable snake_case
// code a chat-facing agent can explain. Unmapped errors return "".
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrAmbiguousEntrypoint):
		return "ambiguous_entrypoint"
	case errors.Is(err, ErrUnsupportedCredentialContract):
		return "unsupported_credential_contract"
	case errors.Is(err, ErrUnsupportedAuth):
		return "unsupported_auth"
	default:
		return ""
	}
}

type WorkloadClass string

const (
	Shared  WorkloadClass = "shared"
	PerUser WorkloadClass = "per-user"
	PerJob  WorkloadClass = "per-job"
)

type Transport string

const (
	RemoteMCP    Transport = "remote-mcp"
	ContainerMCP Transport = "container-mcp"
	BoundedCLI   Transport = "bounded-cli"
	// ProviderAPI is an in-process official HTTPS REST data plane. It starts no
	// child process and owns no filesystem state, so its enforcement is the
	// declared egress allowlist, the authorization fence and bounded output.
	ProviderAPI Transport = "provider-api"
	// AgentTools is an in-process hub executor for the agent-facing local tool
	// domain (files, documents, images, routines, connectors). It has no
	// upstream source: the reviewed definition is the code itself, admission is
	// the capability tuple and the executor scopes os.Root per call.
	AgentTools Transport = "agent-tools"
)

type Effect string

const (
	ReadEffect  Effect = "read"
	WriteEffect Effect = "write"
)

type Status string

const (
	ActiveStatus   Status = "active"
	DisabledStatus Status = "disabled"
	RevokedStatus  Status = "revoked"
	// DeletingStatus marks a connection whose finalizers are running: not
	// Active means Authorize and EffectiveBinding deny it before the ladder
	// finishes.
	DeletingStatus Status = "deleting"
	DegradedStatus Status = "degraded"
	StartingStatus Status = "starting"
	RunningStatus  Status = "running"
	StoppedStatus  Status = "stopped"
	FailedStatus   Status = "failed"
	ExpiredStatus  Status = "expired"
)

type OwnerType string

const (
	PrincipalOwner OwnerType = "principal"
	ContextOwner   OwnerType = "context"
)

type OwnerRef struct {
	Type OwnerType `json:"type"`
	ID   string    `json:"id"`
}

type ToolSpec struct {
	Name           string            `json:"name"`
	Description    string            `json:"description,omitempty"`
	Effect         Effect            `json:"effect"`
	Arguments      []CLIArgument     `json:"arguments,omitempty"`
	InputSchema    json.RawMessage   `json:"input_schema,omitempty"`
	CapabilityID   string            `json:"capability_id,omitempty"`
	Uses           []CapabilityUse   `json:"capability_uses,omitempty"`
	ArgumentEquals map[string]string `json:"argument_equals,omitempty"`
	// Sandboxed marks a tool dispatched to the disposable scratch executor
	// rather than the per-call in-runtime executor. Only the AgentTools
	// transport may carry it: it changes which control channel runs the call.
	Sandboxed bool `json:"sandboxed,omitempty"`
}

// CLIArgument is the only model-controlled input accepted by a bounded CLI
// tool. It is converted to one argv value; it is never interpolated into a
// command string.
type CLIArgument struct {
	Name string `json:"name"`
	// Flag is the rendered "--flag" name; empty renders the value as a
	// positional operand appended after the fixed argv — positional operands
	// require a Pattern so the value can never smuggle a flag.
	Flag     string `json:"flag"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	// Pattern is a mandatory regexp for positional operands and an optional
	// extra constraint for flag values; it applies to string arguments only.
	Pattern string `json:"pattern,omitempty"`
}

type CredentialInput struct {
	Name       string `json:"name"`
	Required   bool   `json:"required"`
	PerRequest bool   `json:"per_request"`
	// Delivery routes the admitted value. Empty and "env" mean workload
	// environment. "http_header" (remote-mcp only) sends the value as the
	// Target header with Prefix prepended — the value never lands on disk.
	Delivery string `json:"delivery,omitempty"`
	Target   string `json:"target,omitempty"`
	Prefix   string `json:"prefix,omitempty"`
	// Source is the metadata file the input was discovered from, carried over
	// from the connection recipe for audit.
	Source string `json:"source,omitempty"`
}

type WorkloadPolicy struct {
	Class           WorkloadClass `json:"class"`
	Stateful        bool          `json:"stateful"`
	Rationale       string        `json:"rationale"`
	ToolHiveVersion string        `json:"toolhive_version,omitempty"`
	SidecarImages   []string      `json:"sidecar_images,omitempty"`
	// Lifecycle selects the bounded-cli cell tier: ephemeral (default),
	// task, binding, toolbox or shared-pool. Empty means ephemeral.
	Lifecycle string `json:"lifecycle,omitempty"`
	// Stateless marks a CLI tool safe for pool cells shared across callers:
	// the definition declares it keeps no workspace and no per-caller state.
	Stateless bool `json:"stateless,omitempty"`
	// InstallPackages grants a cell-private executable home, never a runtime home.
	InstallPackages bool `json:"install_packages,omitempty"`
	// Toolbox groups CLI definitions that share one warm toolbox cell; each
	// member's pinned image is mounted read-only under /tools/<i>.
	Toolbox string `json:"toolbox,omitempty"`
	// WorkspaceScope picks the cell's only filesystem bind: "" (binding for
	// per-user/per-job, none for shared), none, binding or principal.
	WorkspaceScope string `json:"workspace_scope,omitempty"`
	// WorkspaceAccess is "" or "ro" (default) or "rw"; rw must be declared.
	WorkspaceAccess string `json:"workspace_access,omitempty"`
	// Sharable declares that a Shared-class workload may carry stored
	// credentials under a shared-credential policy.
	Sharable bool `json:"sharable,omitempty"`
}

// Bounded-cli cell lifecycle tiers (WorkloadPolicy.Lifecycle).
const (
	CLILifecycleEphemeral  = "ephemeral"
	CLILifecycleTask       = "task"
	CLILifecycleBinding    = "binding"
	CLILifecycleToolbox    = "toolbox"
	CLILifecycleSharedPool = "shared-pool"
)

type ExecutionPolicy struct {
	TimeoutSeconds int          `json:"timeout_seconds"`
	OutputBytes    int          `json:"output_bytes"`
	CPUMillis      int          `json:"cpu_millis"`
	MemoryMiB      int          `json:"memory_mib"`
	MaxPIDs        int          `json:"max_pids"`
	Egress         []string     `json:"egress"`
	TCPForwards    []TCPForward `json:"tcp_forwards,omitempty"`
	Mounts         []Mount      `json:"mounts,omitempty"`
}

// TCPForward binds a loopback listener inside the workload to one reviewed
// host:port destination supplied by a non-secret credential environment
// variable. The companion relays each accepted connection through the
// workload HTTP CONNECT proxy, so native protocols (databases, SSH) keep
// the same reviewed egress boundary instead of bypassing it.
type TCPForward struct {
	Listen    int    `json:"listen"`
	TargetEnv string `json:"target_env"`
}

type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}

type HealthProbe struct {
	Kind           string `json:"kind"`
	Value          string `json:"value"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type CredentialReference struct {
	Schema                 int               `json:"schema"`
	CredentialRefID        string            `json:"credential_ref"`
	ConnectionID           string            `json:"connection_id"`
	Revision               uint64            `json:"revision"`
	Backend                string            `json:"backend"`
	Locator                string            `json:"locator"`
	Keys                   []string          `json:"keys"`
	BrokerGrantID          string            `json:"broker_grant_id,omitempty"`
	BrokerContractID       string            `json:"broker_contract_id,omitempty"`
	BrokerContractRevision int               `json:"broker_contract_revision,omitempty"`
	BrokerEnv              map[string]string `json:"broker_env,omitempty"`
	Status                 Status            `json:"status"`
}

type Connection struct {
	Schema           int               `json:"schema"`
	ConnectionID     string            `json:"connection_id"`
	Owner            OwnerRef          `json:"owner"`
	DefinitionID     string            `json:"definition_id"`
	CredentialRefID  string            `json:"credential_ref,omitempty"`
	Revision         uint64            `json:"revision"`
	Status           Status            `json:"status"`
	TerminalExposure bool              `json:"terminal_exposure,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type ToolBinding struct {
	Schema             int           `json:"schema"`
	ToolBindingID      string        `json:"tool_binding_id"`
	PrincipalID        string        `json:"principal_id"`
	ContextID          string        `json:"context_id"`
	RuntimeID          string        `json:"runtime_id"`
	DefinitionID       string        `json:"definition_id"`
	DefinitionVersion  string        `json:"definition_version"`
	ConnectionID       string        `json:"connection_id,omitempty"`
	ConnectionRevision uint64        `json:"connection_revision,omitempty"`
	CredentialRefID    string        `json:"credential_ref,omitempty"`
	CredentialRevision uint64        `json:"credential_revision,omitempty"`
	PolicyVersion      string        `json:"policy_version"`
	WorkloadClass      WorkloadClass `json:"workload_class"`
	Status             Status        `json:"status"`
	Revision           uint64        `json:"revision"`
	ProjectionRevision uint64        `json:"projection_revision"`
	// ToolDigest pins the admitted tool surface (names, schemas, effects) so
	// a later drift is detectable by one comparison.
	ToolDigest string `json:"tool_digest,omitempty"`
}

type WorkloadInstance struct {
	Schema            int           `json:"schema"`
	WorkloadID        string        `json:"workload_id"`
	BindingID         string        `json:"tool_binding_id"`
	DefinitionID      string        `json:"definition_id"`
	DefinitionVersion string        `json:"definition_version"`
	Class             WorkloadClass `json:"class"`
	Owner             *OwnerRef     `json:"owner,omitempty"`
	RuntimeID         string        `json:"runtime_id,omitempty"`
	JobID             string        `json:"job_id,omitempty"`
	Generation        uint64        `json:"generation"`
	Status            Status        `json:"status"`
	StartedAt         time.Time     `json:"started_at,omitempty"`
	ExpiresAt         time.Time     `json:"expires_at,omitempty"`
}

type EffectiveBinding struct {
	Binding    ToolBinding
	Definition ToolDefinition
	Connection *Connection
	Credential *CredentialReference
	WorkloadID string
	// CredentialMounts are runtime-only paths returned by Credential Broker.
	// They never enter the persisted projection or audit ledger.
	CredentialMounts          []Mount `json:"-"`
	CapabilityID              string  `json:"-"`
	ImplementationDigest      string  `json:"-"`
	CapabilityPolicyID        string  `json:"-"`
	CapabilityProfileID       string  `json:"-"`
	CapabilityPolicyRevision  uint64  `json:"-"`
	CapabilityProfileRevision uint64  `json:"-"`
	// CapabilityScopes are the path-grant boundaries the admission bound for
	// this call; executors must map them to os.Root scopes, never recompute.
	CapabilityScopes []CapabilityScope `json:"-"`
}

type WorkloadStopper interface {
	Stop(workloadID string) error
}

// ProjectionTarget identifies one runtime projection and its monotonic
// revision. Used by the restart notifier to tell the supervisor which runtime
// needs a marker after its projection changed.
type ProjectionTarget struct {
	PrincipalID string `json:"principal_id"`
	ContextID   string `json:"context_id"`
	RuntimeID   string `json:"runtime_id"`
	Revision    uint64 `json:"revision"`
}
