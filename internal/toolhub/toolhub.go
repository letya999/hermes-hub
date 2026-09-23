// Package toolhub contains the versioned ToolHub catalog and authorization
// model. It deliberately stops before provider execution and secret material.
package toolhub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
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
	versionPattern         = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-+][0-9A-Za-z.-]+)?$`)
	toolHiveVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	toolNamePattern        = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	mcpToolNamePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	credentialPattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	hostPattern            = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,252}(:[0-9]{1,5})?$`)
	digestPattern          = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	commandPattern         = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
)

var (
	ErrInvalid      = errors.New("invalid toolhub record")
	ErrNotFound     = errors.New("toolhub record not found")
	ErrConflict     = errors.New("toolhub record conflict")
	ErrUnauthorized = errors.New("toolhub authorization denied")
	ErrStale        = errors.New("toolhub record is stale")
	ErrRevoked      = errors.New("toolhub record is revoked")
	ErrDegraded     = errors.New("toolhub connection is degraded")
)

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

func (o OwnerRef) Validate() error {
	if (o.Type != PrincipalOwner && o.Type != ContextOwner) || !identity.ValidID(o.ID) {
		return fmt.Errorf("%w: invalid owner", ErrInvalid)
	}
	return nil
}

func (o OwnerRef) Matches(e identity.Envelope) bool {
	switch o.Type {
	case PrincipalOwner:
		return o.ID == e.PrincipalID
	case ContextOwner:
		return o.ID == e.ContextID
	default:
		return false
	}
}

type ToolDefinition struct {
	Schema                     int               `json:"schema"`
	DefinitionID               string            `json:"definition_id"`
	Version                    string            `json:"version"`
	Transport                  Transport         `json:"transport"`
	Source                     DefinitionSource  `json:"source"`
	Tools                      []ToolSpec        `json:"tools"`
	Credentials                []CredentialInput `json:"credentials,omitempty"`
	CredentialContractID       string            `json:"credential_contract_id,omitempty"`
	CredentialContractRevision int               `json:"credential_contract_revision,omitempty"`
	CredentialContractEnv      map[string]string `json:"credential_contract_env,omitempty"`
	Environment                []string          `json:"environment,omitempty"`
	RuntimeEnvironment         map[string]string `json:"runtime_environment,omitempty"`
	Workload                   WorkloadPolicy    `json:"workload"`
	Execution                  ExecutionPolicy   `json:"execution"`
	Health                     HealthProbe       `json:"health"`
}

type DefinitionSource struct {
	URL                string   `json:"url,omitempty"`
	TLSMode            string   `json:"tls_mode,omitempty"`
	Image              string   `json:"image,omitempty"`
	Digest             string   `json:"digest,omitempty"`
	Command            string   `json:"command,omitempty"`
	Args               []string `json:"args,omitempty"`
	Repository         string   `json:"repository,omitempty"`
	Subfolder          string   `json:"subfolder,omitempty"`
	CommitSHA          string   `json:"commit_sha,omitempty"`
	ArchiveDigest      string   `json:"archive_digest,omitempty"`
	ProvenanceDigest   string   `json:"provenance_digest,omitempty"`
	SBOMDigest         string   `json:"sbom_digest,omitempty"`
	RecipeDigest       string   `json:"recipe_digest,omitempty"`
	ReviewDigest       string   `json:"review_digest,omitempty"`
	ToolContractDigest string   `json:"tool_contract_digest,omitempty"`
	ToolContractSource string   `json:"tool_contract_source,omitempty"`
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Effect      Effect          `json:"effect"`
	Arguments   []CLIArgument   `json:"arguments,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// CLIArgument is the only model-controlled input accepted by a bounded CLI
// tool. It is converted to one argv value; it is never interpolated into a
// command string.
type CLIArgument struct {
	Name     string `json:"name"`
	Flag     string `json:"flag"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type CredentialInput struct {
	Name       string `json:"name"`
	Required   bool   `json:"required"`
	PerRequest bool   `json:"per_request"`
}

type WorkloadPolicy struct {
	Class           WorkloadClass `json:"class"`
	Stateful        bool          `json:"stateful"`
	Rationale       string        `json:"rationale"`
	ToolHiveVersion string        `json:"toolhive_version,omitempty"`
	SidecarImages   []string      `json:"sidecar_images,omitempty"`
}

type ExecutionPolicy struct {
	TimeoutSeconds int      `json:"timeout_seconds"`
	OutputBytes    int      `json:"output_bytes"`
	CPUMillis      int      `json:"cpu_millis"`
	MemoryMiB      int      `json:"memory_mib"`
	MaxPIDs        int      `json:"max_pids"`
	Egress         []string `json:"egress"`
	Mounts         []Mount  `json:"mounts,omitempty"`
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

func (d ToolDefinition) Validate() error {
	if d.Schema != SchemaVersion || !identity.ValidID(d.DefinitionID) || !versionPattern.MatchString(d.Version) {
		return fmt.Errorf("%w: definition schema, id or version", ErrInvalid)
	}
	if d.Workload.Class != Shared && d.Workload.Class != PerUser && d.Workload.Class != PerJob {
		return fmt.Errorf("%w: unknown workload class", ErrInvalid)
	}
	if strings.TrimSpace(d.Workload.Rationale) == "" || len(d.Workload.Rationale) > 512 {
		return fmt.Errorf("%w: workload rationale is required", ErrInvalid)
	}
	if err := d.Source.validate(d.Transport); err != nil {
		return err
	}
	if len(d.Tools) == 0 || len(d.Tools) > 256 {
		return fmt.Errorf("%w: tools must be bounded and non-empty", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, tool := range d.Tools {
		if len(tool.InputSchema) > 65536 || (len(tool.InputSchema) > 0 && (!json.Valid(tool.InputSchema) || (d.Transport != RemoteMCP && d.Transport != ContainerMCP))) {
			return fmt.Errorf("%w: MCP input schema", ErrInvalid)
		}
		if !mcpToolNamePattern.MatchString(tool.Name) || (tool.Effect != ReadEffect && tool.Effect != WriteEffect) || seen[tool.Name] {
			return fmt.Errorf("%w: invalid or duplicate tool %q", ErrInvalid, tool.Name)
		}
		if len(tool.Description) > 1024 || len(tool.Arguments) > 32 {
			return fmt.Errorf("%w: tool metadata is too large", ErrInvalid)
		}
		argumentNames := map[string]bool{}
		for _, argument := range tool.Arguments {
			if !toolNamePattern.MatchString(argument.Name) || argumentNames[argument.Name] || !validCLIType(argument.Type) {
				return fmt.Errorf("%w: invalid tool argument %q", ErrInvalid, argument.Name)
			}
			switch d.Transport {
			case BoundedCLI:
				if !validCLIFlag(argument.Flag) {
					return fmt.Errorf("%w: invalid CLI argument %q", ErrInvalid, argument.Name)
				}
			case ProviderAPI:
				if argument.Flag != "" {
					return fmt.Errorf("%w: provider argument %q cannot carry a CLI flag", ErrInvalid, argument.Name)
				}
			default:
				return fmt.Errorf("%w: transport %s cannot declare tool arguments", ErrInvalid, d.Transport)
			}
			argumentNames[argument.Name] = true
		}
		seen[tool.Name] = true
	}
	seen = map[string]bool{}
	for name, value := range d.RuntimeEnvironment {
		if !credentialPattern.MatchString(name) || len(value) > 1024 || strings.ContainsAny(value, "\x00\r\n") || strings.Contains(name, "PROXY") || name == "NODE_OPTIONS" || name == "LD_PRELOAD" {
			return fmt.Errorf("%w: invalid runtime environment", ErrInvalid)
		}
	}
	for _, name := range d.Environment {
		if !credentialPattern.MatchString(name) || seen[name] {
			return fmt.Errorf("%w: invalid or duplicate environment parameter %q", ErrInvalid, name)
		}
		seen[name] = true
	}
	for _, input := range d.Credentials {
		if _, exists := d.RuntimeEnvironment[input.Name]; exists {
			return fmt.Errorf("%w: runtime environment overlaps credential", ErrInvalid)
		}
		if !credentialPattern.MatchString(input.Name) || seen[input.Name] {
			return fmt.Errorf("%w: invalid or duplicate credential input %q", ErrInvalid, input.Name)
		}
		if d.Workload.Class == Shared && !input.PerRequest {
			return fmt.Errorf("%w: shared workload requires per-request credentials", ErrInvalid)
		}
		seen[input.Name] = true
	}
	if d.CredentialContractID != "" {
		if !identity.ValidID(d.CredentialContractID) || d.CredentialContractRevision < 1 || d.CredentialContractRevision > 100000 {
			return fmt.Errorf("%w: invalid credential broker contract", ErrInvalid)
		}
		for name, target := range d.CredentialContractEnv {
			if !credentialPattern.MatchString(name) || !credentialPattern.MatchString(target) || !slices.ContainsFunc(d.Credentials, func(input CredentialInput) bool { return input.Name == name }) {
				return fmt.Errorf("%w: invalid credential broker delivery mapping", ErrInvalid)
			}
		}
		for _, input := range d.Credentials {
			if input.Required && d.CredentialContractEnv[input.Name] == "" {
				return fmt.Errorf("%w: missing credential broker delivery for %s", ErrInvalid, input.Name)
			}
		}
	} else if d.CredentialContractRevision != 0 || len(d.CredentialContractEnv) != 0 {
		return fmt.Errorf("%w: credential broker mapping needs a contract", ErrInvalid)
	}
	if d.Workload.Class == Shared && (d.Workload.Stateful || len(d.Execution.Mounts) > 0) {
		return fmt.Errorf("%w: shared workload cannot own state or mounts", ErrInvalid)
	}
	if d.Workload.Class == PerJob && d.Workload.Stateful && !hasMountSource(d.Execution.Mounts, "job-state") {
		return fmt.Errorf("%w: stateful per-job workload needs job-state", ErrInvalid)
	}
	if d.Transport == ProviderAPI {
		if d.Workload.Class != PerUser || d.Workload.Stateful || len(d.Execution.Mounts) > 0 {
			return fmt.Errorf("%w: provider API is a stateless per-user data plane", ErrInvalid)
		}
		if !slices.ContainsFunc(d.Credentials, func(input CredentialInput) bool { return input.Required && !input.PerRequest }) {
			return fmt.Errorf("%w: provider API needs a required owner credential", ErrInvalid)
		}
		if !slices.ContainsFunc(d.Execution.Egress, func(host string) bool { return strings.EqualFold(strings.TrimSpace(host), hostOfURL(d.Source.URL)) }) {
			return fmt.Errorf("%w: provider API egress must include its declared host", ErrInvalid)
		}
	}
	if d.Transport == ContainerMCP {
		if !toolHiveVersionPattern.MatchString(d.Workload.ToolHiveVersion) || len(d.Workload.SidecarImages) == 0 || len(d.Workload.SidecarImages) > 8 {
			return fmt.Errorf("%w: container MCP needs a pinned ToolHive version and sidecars", ErrInvalid)
		}
		seen = map[string]bool{}
		for _, image := range d.Workload.SidecarImages {
			at := strings.LastIndex(image, "@")
			if at <= 0 || !digestPattern.MatchString(image[at+1:]) || strings.ContainsAny(image[:at], " \t\r\n") || strings.Contains(image[:at], "..") || strings.Contains(image, ":latest") || seen[image] {
				return fmt.Errorf("%w: sidecar image must be unique and digest-pinned", ErrInvalid)
			}
			seen[image] = true
		}
	}
	if err := d.Execution.validate(d.Workload.Class); err != nil {
		return err
	}
	for _, host := range d.Execution.Egress {
		if strings.Contains(host, "/") && d.Transport != ContainerMCP {
			return fmt.Errorf("%w: CIDR egress requires a container network controller", ErrInvalid)
		}
	}
	if err := d.Health.Validate(d.Transport); err != nil {
		return err
	}
	return nil
}

func (s DefinitionSource) validate(transport Transport) error {
	if s.CommitSHA != "" && !gitSHAPattern.MatchString(s.CommitSHA) {
		return fmt.Errorf("%w: source commit must be an exact lowercase SHA", ErrInvalid)
	}
	for name, value := range map[string]string{"archive_digest": s.ArchiveDigest, "provenance_digest": s.ProvenanceDigest, "sbom_digest": s.SBOMDigest, "recipe_digest": s.RecipeDigest, "review_digest": s.ReviewDigest, "tool_contract_digest": s.ToolContractDigest} {
		if value != "" && !digestPattern.MatchString(value) {
			return fmt.Errorf("%w: source %s must be a sha256 digest", ErrInvalid, name)
		}
	}
	if s.ToolContractSource != "" && s.ToolContractSource != ToolContractPreflight && s.ToolContractSource != ToolContractReviewManifest {
		return fmt.Errorf("%w: unknown tool contract source", ErrInvalid)
	}
	if s.Repository != "" {
		if _, err := (ArtifactSource{Repository: s.Repository, Subfolder: s.Subfolder, CommitSHA: "0000000000000000000000000000000000000000"}).ArchiveURL(); err != nil {
			return fmt.Errorf("%w: canonical artifact repository required", ErrInvalid)
		}
	} else if s.Subfolder != "" {
		return fmt.Errorf("%w: source subfolder requires a repository", ErrInvalid)
	}
	values := 0
	if s.URL != "" {
		values++
	}
	if s.Image != "" {
		values++
	}
	if s.Command != "" {
		values++
	}
	switch transport {
	case RemoteMCP, ProviderAPI:
		if s.URL == "" || values != 1 || s.TLSMode != "required" || s.Digest != "" || len(s.Args) != 0 || s.Repository != "" || s.CommitSHA != "" || s.ArchiveDigest != "" || s.ProvenanceDigest != "" || s.SBOMDigest != "" || s.RecipeDigest != "" || s.ReviewDigest != "" {
			return fmt.Errorf("%w: %s needs an HTTPS URL and required TLS", ErrInvalid, transport)
		}
		u, err := url.Parse(s.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%w: %s URL must be HTTPS without credentials, query or fragment", ErrInvalid, transport)
		}
	case ContainerMCP:
		if s.Image == "" || s.URL != "" || !digestPattern.MatchString(s.Digest) || s.TLSMode != "" || strings.Contains(s.Image, "@") || strings.ContainsAny(s.Image, " \t\r\n") || strings.Contains(s.Image, "..") {
			return fmt.Errorf("%w: container MCP needs a digest-pinned image", ErrInvalid)
		}
		if i := strings.LastIndex(s.Image, ":"); i > strings.LastIndex(s.Image, "/") {
			return fmt.Errorf("%w: mutable container image tag", ErrInvalid)
		}
		if s.Command != "" && !validCommand(s.Command) {
			return fmt.Errorf("%w: invalid container command", ErrInvalid)
		}
	case BoundedCLI:
		if s.Command == "" || values != 1 || s.Image != "" || s.URL != "" || s.Digest != "" || s.TLSMode != "" || s.Repository != "" || s.CommitSHA != "" || s.ArchiveDigest != "" || s.ProvenanceDigest != "" || s.SBOMDigest != "" || s.RecipeDigest != "" || s.ReviewDigest != "" || !validCommand(s.Command) {
			return fmt.Errorf("%w: bounded CLI needs a command and no mutable source", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown transport", ErrInvalid)
	}
	for _, arg := range s.Args {
		if len(arg) > 256 || strings.ContainsAny(arg, "\r\n") || strings.Contains(arg, "${") {
			return fmt.Errorf("%w: CLI/container args cannot contain secret references", ErrInvalid)
		}
	}
	return nil
}

func (e ExecutionPolicy) validate(class WorkloadClass) error {
	if e.TimeoutSeconds < 1 || e.TimeoutSeconds > MaxExecutionTimeout || e.OutputBytes < 1 || e.OutputBytes > MaxOutputBytes || e.CPUMillis < 1 || e.CPUMillis > MaxCPUMillis || e.MemoryMiB < 16 || e.MemoryMiB > MaxMemoryMiB || e.MaxPIDs < 1 || e.MaxPIDs > MaxPIDs || len(e.Egress) == 0 || len(e.Egress) > 32 || len(e.Mounts) > 8 {
		return fmt.Errorf("%w: execution limits are missing or unbounded", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, host := range e.Egress {
		host = strings.ToLower(strings.TrimSpace(host))
		prefix, prefixErr := netip.ParsePrefix(host)
		validPrefix := prefixErr == nil && prefix.Addr().Is4() && prefix.Bits() >= 8 && prefix == prefix.Masked()
		if (!hostPattern.MatchString(host) && !validPrefix) || seen[host] {
			return fmt.Errorf("%w: invalid or duplicate egress host", ErrInvalid)
		}
		seen[host] = true
	}
	if class == Shared && len(e.Mounts) != 0 {
		return fmt.Errorf("%w: shared workloads cannot mount state", ErrInvalid)
	}
	seen = map[string]bool{}
	for _, mount := range e.Mounts {
		if seen[mount.Target] || !strings.HasPrefix(mount.Target, "/") || strings.Contains(mount.Target, "..") || mount.Source == "" {
			return fmt.Errorf("%w: unsafe or duplicate mount", ErrInvalid)
		}
		switch mount.Source {
		case "connection-state":
			if class != PerUser || mount.ReadOnly {
				return fmt.Errorf("%w: connection-state is writable per-user state", ErrInvalid)
			}
		case "workspace-readonly":
			if !mount.ReadOnly || class == Shared {
				return fmt.Errorf("%w: workspace mount must be read-only and isolated", ErrInvalid)
			}
		case "job-state":
			if class != PerJob || mount.ReadOnly {
				return fmt.Errorf("%w: job-state is writable per-job state", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: host mounts are not allowed", ErrInvalid)
		}
		seen[mount.Target] = true
	}
	return nil
}

func (h HealthProbe) Validate(transport Transport) error {
	if h.TimeoutSeconds < 1 || h.TimeoutSeconds > 30 || h.Value == "" || strings.ContainsAny(h.Value, "\r\n") {
		return fmt.Errorf("%w: bounded health probe is required", ErrInvalid)
	}
	if (transport == RemoteMCP || transport == ProviderAPI) && (h.Kind != "http" || !strings.HasPrefix(h.Value, "/")) {
		return fmt.Errorf("%w: %s health probe must be an HTTP path", ErrInvalid, transport)
	}
	if transport != RemoteMCP && transport != ProviderAPI && h.Kind != "exec" {
		return fmt.Errorf("%w: container and CLI health probes must be exec", ErrInvalid)
	}
	return nil
}

// hostOfURL returns the lowercase host of an already validated HTTPS URL.
func hostOfURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func hasMountSource(mounts []Mount, source string) bool {
	return slices.ContainsFunc(mounts, func(m Mount) bool { return m.Source == source })
}

func validCommand(command string) bool {
	if command == "" || strings.Contains(command, "..") || strings.ContainsAny(command, "\t\r\n;|&$`()") {
		return false
	}
	base := strings.ToLower(filepath.Base(command))
	if strings.HasSuffix(base, ".bat") || strings.HasSuffix(base, ".cmd") {
		return false
	}
	base = strings.TrimSuffix(base, ".exe")
	if base == "sh" || base == "bash" || base == "zsh" || base == "cmd" || base == "powershell" || base == "pwsh" {
		return false
	}
	if filepath.IsAbs(command) {
		return true
	}
	return commandPattern.MatchString(command)
}

func validCLIFlag(flag string) bool {
	if !strings.HasPrefix(flag, "--") || len(flag) < 3 || len(flag) > 64 {
		return false
	}
	return commandPattern.MatchString(strings.TrimPrefix(flag, "--"))
}

func validCLIType(value string) bool {
	switch value {
	case "string", "integer", "number", "boolean":
		return true
	default:
		return false
	}
}

func DecodeDefinition(data []byte) (ToolDefinition, error) {
	var d ToolDefinition
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return d, fmt.Errorf("%w: definition JSON: %v", ErrInvalid, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return d, fmt.Errorf("%w: one definition JSON document expected", ErrInvalid)
	}
	return d, d.Validate()
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

func (r CredentialReference) Validate() error {
	if r.Schema != SchemaVersion || !identity.ValidID(r.CredentialRefID) || !identity.ValidID(r.ConnectionID) || r.Revision == 0 || r.Backend == "" || r.Locator == "" || len(r.Keys) == 0 || (r.Status != ActiveStatus && r.Status != RevokedStatus) {
		return fmt.Errorf("%w: credential reference metadata", ErrInvalid)
	}
	if strings.ContainsAny(r.Locator, "\r\n=") || len(r.Locator) > 256 {
		return fmt.Errorf("%w: credential locator must be opaque metadata", ErrInvalid)
	}
	if r.Backend == "credential-broker" {
		if (r.BrokerGrantID != "" && !identity.ValidID(r.BrokerGrantID)) || !identity.ValidID(r.BrokerContractID) || r.BrokerContractRevision < 1 {
			return fmt.Errorf("%w: credential broker reference", ErrInvalid)
		}
		for key, target := range r.BrokerEnv {
			if !credentialPattern.MatchString(key) || !credentialPattern.MatchString(target) {
				return fmt.Errorf("%w: credential broker environment mapping", ErrInvalid)
			}
		}
	}
	seen := map[string]bool{}
	for _, key := range r.Keys {
		if !credentialPattern.MatchString(key) || seen[key] {
			return fmt.Errorf("%w: invalid or duplicate credential key", ErrInvalid)
		}
		seen[key] = true
	}
	return nil
}

func CredentialReferenceID(connectionID string, revision uint64) string {
	return deterministicID("cred", connectionID, fmt.Sprint(revision))
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

func (c Connection) Validate() error {
	if c.Schema != SchemaVersion || !identity.ValidID(c.ConnectionID) || !identity.ValidID(c.DefinitionID) || c.Revision == 0 || (c.Status != ActiveStatus && c.Status != DisabledStatus && c.Status != RevokedStatus && c.Status != DegradedStatus) {
		return fmt.Errorf("%w: connection metadata", ErrInvalid)
	}
	if err := c.Owner.Validate(); err != nil {
		return err
	}
	if c.CredentialRefID != "" && !identity.ValidID(c.CredentialRefID) {
		return fmt.Errorf("%w: invalid credential reference", ErrInvalid)
	}
	if len(c.Metadata) > 32 {
		return fmt.Errorf("%w: too much connection metadata", ErrInvalid)
	}
	for key, value := range c.Metadata {
		if !regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`).MatchString(key) || len(value) > 256 || strings.ContainsAny(value, "\r\n") || strings.Contains(strings.ToLower(key), "token") || strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "password") {
			return fmt.Errorf("%w: secret-like connection metadata is not allowed", ErrInvalid)
		}
	}
	if endpoint := c.Metadata["mcp_endpoint"]; endpoint != "" {
		if err := ValidateBackendEndpoint(endpoint); err != nil {
			return err
		}
	}
	return nil
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
}

func (b ToolBinding) Validate() error {
	if b.Schema != SchemaVersion || !identity.ValidID(b.ToolBindingID) || !identity.ValidID(b.PrincipalID) || !identity.ValidID(b.ContextID) || !identity.ValidID(b.RuntimeID) || !identity.ValidID(b.DefinitionID) || !versionPattern.MatchString(b.DefinitionVersion) || !identity.ValidID(b.PolicyVersion) || b.Revision == 0 || b.ProjectionRevision == 0 {
		return fmt.Errorf("%w: binding identity or revision", ErrInvalid)
	}
	if b.WorkloadClass != Shared && b.WorkloadClass != PerUser && b.WorkloadClass != PerJob {
		return fmt.Errorf("%w: invalid binding workload class", ErrInvalid)
	}
	if b.Status != ActiveStatus && b.Status != DisabledStatus && b.Status != RevokedStatus {
		return fmt.Errorf("%w: invalid binding status", ErrInvalid)
	}
	if b.ConnectionID == "" && (b.ConnectionRevision != 0 || b.CredentialRefID != "" || b.CredentialRevision != 0) {
		return fmt.Errorf("%w: credential revision without connection", ErrInvalid)
	}
	if b.ConnectionID != "" && (!identity.ValidID(b.ConnectionID) || b.ConnectionRevision == 0) {
		return fmt.Errorf("%w: invalid binding connection", ErrInvalid)
	}
	if b.CredentialRefID != "" && (!identity.ValidID(b.CredentialRefID) || b.CredentialRevision == 0) {
		return fmt.Errorf("%w: invalid binding credential", ErrInvalid)
	}
	return nil
}

func DeterministicBindingID(principalID, contextID, runtimeID, definitionID, definitionVersion, connectionID, credentialRefID string) string {
	return deterministicID("bind", principalID, contextID, runtimeID, definitionID, definitionVersion, connectionID, credentialRefID)
}

func ProjectedToolName(definitionID, version, toolName string) string {
	return "hub-" + slug(definitionID) + "-" + slug(toolName) + "-" + shortHash(definitionID, version, toolName)
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

func (w WorkloadInstance) Validate() error {
	if w.Schema != SchemaVersion || !identity.ValidID(w.WorkloadID) || !identity.ValidID(w.BindingID) || !identity.ValidID(w.DefinitionID) || !versionPattern.MatchString(w.DefinitionVersion) || w.Generation == 0 {
		return fmt.Errorf("%w: workload identity", ErrInvalid)
	}
	if w.Class != Shared && w.Class != PerUser && w.Class != PerJob {
		return fmt.Errorf("%w: workload class", ErrInvalid)
	}
	if w.Status != StartingStatus && w.Status != RunningStatus && w.Status != StoppedStatus && w.Status != FailedStatus && w.Status != ExpiredStatus {
		return fmt.Errorf("%w: workload status", ErrInvalid)
	}
	switch w.Class {
	case Shared:
		if w.Owner != nil || w.RuntimeID != "" || w.JobID != "" {
			return fmt.Errorf("%w: shared workload has an owner or runtime", ErrInvalid)
		}
	case PerUser:
		if w.Owner == nil || w.Owner.Type != ContextOwner || !identity.ValidID(w.Owner.ID) || !identity.ValidID(w.RuntimeID) || w.JobID != "" {
			return fmt.Errorf("%w: per-user workload identity", ErrInvalid)
		}
	case PerJob:
		if w.Owner == nil || w.Owner.Type != ContextOwner || !identity.ValidID(w.Owner.ID) || !identity.ValidID(w.RuntimeID) || !identity.ValidID(w.JobID) || w.ExpiresAt.IsZero() {
			return fmt.Errorf("%w: per-job workload needs owner, job and expiry", ErrInvalid)
		}
	}
	return nil
}

func WorkloadInstanceID(definitionID string, class WorkloadClass, ownerID, jobID string) string {
	return deterministicID("work", definitionID, string(class), ownerID, jobID)
}

func NewWorkloadInstance(binding ToolBinding, owner *OwnerRef, jobID string, generation uint64, startedAt, expiresAt time.Time) (WorkloadInstance, error) {
	w := WorkloadInstance{Schema: SchemaVersion, BindingID: binding.ToolBindingID, DefinitionID: binding.DefinitionID, DefinitionVersion: binding.DefinitionVersion, Class: binding.WorkloadClass, Owner: owner, JobID: jobID, Generation: generation, Status: StartingStatus, StartedAt: startedAt, ExpiresAt: expiresAt}
	if owner != nil {
		w.RuntimeID = binding.RuntimeID
	}
	ownerID := ""
	if owner != nil {
		ownerID = owner.ID
	}
	if w.Class == PerUser && binding.ConnectionID != "" {
		ownerID += ":" + binding.PrincipalID + ":" + binding.ConnectionID
	}
	if w.Class == PerUser && binding.ConnectionID == "" {
		ownerID = binding.ToolBindingID
	}
	w.WorkloadID = WorkloadInstanceID(w.DefinitionID, w.Class, ownerID, jobID)
	return w, w.Validate()
}

type EffectiveBinding struct {
	Binding    ToolBinding
	Definition ToolDefinition
	Connection *Connection
	Credential *CredentialReference
	WorkloadID string
	// CredentialMounts are runtime-only paths returned by Credential Broker.
	// They never enter the persisted projection or audit ledger.
	CredentialMounts []Mount `json:"-"`
}

type WorkloadStopper interface {
	Stop(workloadID string) error
}

type Store struct {
	mu                  sync.RWMutex
	path                string
	savedPath           string
	diskDigest          [32]byte
	Reconnect           *ReconnectController
	Stopper             WorkloadStopper
	definitions         map[string]ToolDefinition
	connections         map[string]Connection
	credentials         map[string]CredentialReference
	bindings            map[string]ToolBinding
	workloads           map[string]WorkloadInstance
	projectionRevisions map[string]uint64
	grants              map[string]Grant
	onboardings         map[string]Onboarding
	publications        map[string]DefinitionPublication
	sharedPolicies      map[string]SharedCredentialPolicy
}

func NewStore() *Store {
	return &Store{definitions: map[string]ToolDefinition{}, connections: map[string]Connection{}, credentials: map[string]CredentialReference{}, bindings: map[string]ToolBinding{}, workloads: map[string]WorkloadInstance{}, projectionRevisions: map[string]uint64{}, grants: map[string]Grant{}, onboardings: map[string]Onboarding{}, publications: map[string]DefinitionPublication{}, sharedPolicies: map[string]SharedCredentialPolicy{}}
}

func projectionKey(principalID, contextID, runtimeID string) string {
	return principalID + "\x00" + contextID + "\x00" + runtimeID
}

func (s *Store) touchProjectionLocked(binding *ToolBinding) uint64 {
	key := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
	next := s.projectionRevisions[key] + 1
	if next <= binding.ProjectionRevision {
		next = binding.ProjectionRevision + 1
	}
	s.projectionRevisions[key] = next
	binding.ProjectionRevision = next
	return next
}

// ProjectionRevision returns the monotonic revision for one exact runtime
// projection. It is persisted with the registry so reconnect decisions do not
// reset after a process restart.
func (s *Store) ProjectionRevision(auth identity.Envelope) (uint64, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.projectionRevisions[projectionKey(auth.PrincipalID, auth.ContextID, auth.RuntimeID)], nil
}

func definitionKey(id, version string) string { return id + "@" + version }

func (s *Store) RegisterDefinition(definition ToolDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := definitionKey(definition.DefinitionID, definition.Version)
	if existing, ok := s.definitions[key]; ok && !definitionsEqual(existing, definition) {
		return fmt.Errorf("%w: immutable definition %s", ErrConflict, key)
	}
	s.definitions[key] = definition
	return nil
}

func (s *Store) PutCredentialReference(reference CredentialReference) error {
	if err := reference.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.credentials[reference.CredentialRefID]; ok && !recordsEqual(existing, reference) {
		return fmt.Errorf("%w: immutable credential reference", ErrConflict)
	}
	s.credentials[reference.CredentialRefID] = reference
	return nil
}

func (s *Store) PutConnection(connection Connection) error {
	if err := connection.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasDefinitionID(connection.DefinitionID) {
		return fmt.Errorf("%w: connection definition", ErrNotFound)
	}
	if connection.CredentialRefID != "" {
		reference, ok := s.credentials[connection.CredentialRefID]
		if !ok || reference.ConnectionID != connection.ConnectionID {
			return fmt.Errorf("%w: connection credential reference", ErrInvalid)
		}
	}
	if existing, ok := s.connections[connection.ConnectionID]; ok && !recordsEqual(existing, connection) {
		return fmt.Errorf("%w: connection is immutable within a revision", ErrConflict)
	}
	s.connections[connection.ConnectionID] = connection
	return nil
}

func (s *Store) hasDefinitionID(id string) bool {
	for key := range s.definitions {
		if strings.HasPrefix(key, id+"@") {
			return true
		}
	}
	return false
}

func (s *Store) PutBinding(binding ToolBinding) error {
	if binding.ToolBindingID == "" {
		binding.ToolBindingID = DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	definition, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
	if !ok {
		return fmt.Errorf("%w: binding definition", ErrNotFound)
	}
	if definition.Workload.Class != binding.WorkloadClass {
		return fmt.Errorf("%w: binding workload class", ErrInvalid)
	}
	var connection Connection
	var hasConnection bool
	var reference CredentialReference
	if binding.ConnectionID != "" {
		connection, hasConnection = s.connections[binding.ConnectionID]
		if !hasConnection || connection.DefinitionID != binding.DefinitionID {
			return fmt.Errorf("%w: binding connection", ErrInvalid)
		}
		if binding.ConnectionRevision != connection.Revision {
			return fmt.Errorf("%w: binding connection revision", ErrStale)
		}
		if binding.CredentialRefID != "" {
			var ok bool
			reference, ok = s.credentials[binding.CredentialRefID]
			if !ok || reference.ConnectionID != connection.ConnectionID || reference.Revision != binding.CredentialRevision || connection.CredentialRefID != reference.CredentialRefID {
				return fmt.Errorf("%w: binding credential revision", ErrStale)
			}
		} else if len(definition.Credentials) > 0 {
			return fmt.Errorf("%w: credential-bearing binding needs a credential reference", ErrInvalid)
		}
	} else if len(definition.Credentials) > 0 {
		return fmt.Errorf("%w: credential-bearing definition needs a connection", ErrInvalid)
	} else if binding.CredentialRefID != "" {
		return fmt.Errorf("%w: anonymous binding cannot carry a credential reference", ErrInvalid)
	}
	expectedID := DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	if binding.ToolBindingID != expectedID {
		return fmt.Errorf("%w: nondeterministic binding id", ErrInvalid)
	}
	if existing, ok := s.bindings[binding.ToolBindingID]; ok && existing != binding {
		return fmt.Errorf("%w: binding is immutable within a revision", ErrConflict)
	}
	key := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
	if binding.ProjectionRevision > s.projectionRevisions[key] {
		s.projectionRevisions[key] = binding.ProjectionRevision
	}
	s.bindings[binding.ToolBindingID] = binding
	return nil
}

func (s *Store) PutWorkloadInstance(workload WorkloadInstance) error {
	if err := workload.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[workload.BindingID]
	if !ok || binding.DefinitionID != workload.DefinitionID || binding.DefinitionVersion != workload.DefinitionVersion || binding.WorkloadClass != workload.Class {
		return fmt.Errorf("%w: workload binding", ErrInvalid)
	}
	if existing, ok := s.workloads[workload.WorkloadID]; ok && !recordsEqual(existing, workload) {
		return fmt.Errorf("%w: workload identity", ErrConflict)
	}
	s.workloads[workload.WorkloadID] = workload
	return nil
}

func (s *Store) RotateCredential(connectionID, backend, locator string, keys []string) (CredentialReference, error) {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: connection", ErrNotFound)
	}
	if connection.Status != ActiveStatus {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: connection is not active", ErrRevoked)
	}
	next := connection.Revision + 1
	reference := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID(connectionID, next), ConnectionID: connectionID, Revision: next, Backend: backend, Locator: locator, Keys: append([]string(nil), keys...), Status: ActiveStatus}
	return s.rotateCredentialLocked(connection, reference)
}

// RotateCredentialRecord replaces the connection's credential with a fully
// populated reference (broker backend fields included). The caller sets
// Revision to the expected next connection revision so a concurrent rotation
// fails instead of recording a grant bound to the wrong reference.
func (s *Store) RotateCredentialRecord(reference CredentialReference) (CredentialReference, error) {
	s.mu.Lock()
	connection, ok := s.connections[reference.ConnectionID]
	if !ok {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: connection", ErrNotFound)
	}
	if connection.Status != ActiveStatus {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: connection is not active", ErrRevoked)
	}
	next := connection.Revision + 1
	if reference.Revision != next || reference.CredentialRefID != CredentialReferenceID(reference.ConnectionID, next) {
		s.mu.Unlock()
		return CredentialReference{}, fmt.Errorf("%w: credential rotation conflict", ErrConflict)
	}
	reference.Schema = SchemaVersion
	reference.Status = ActiveStatus
	return s.rotateCredentialLocked(connection, reference)
}

func (s *Store) rotateCredentialLocked(connection Connection, reference CredentialReference) (CredentialReference, error) {
	if err := reference.Validate(); err != nil {
		s.mu.Unlock()
		return CredentialReference{}, err
	}
	if connection.CredentialRefID != "" {
		old := s.credentials[connection.CredentialRefID]
		old.Status = RevokedStatus
		s.credentials[old.CredentialRefID] = old
	}
	s.credentials[reference.CredentialRefID] = reference
	connection.CredentialRefID = reference.CredentialRefID
	connection.Revision = reference.Revision
	s.connections[connection.ConnectionID] = connection
	for id, binding := range s.bindings {
		if binding.ConnectionID == connection.ConnectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connection.ConnectionID)
	s.mu.Unlock()
	s.stopWorkloads(stopped)
	if err := s.persistAndNotify(); err != nil {
		_ = s.MarkDegraded(connection.ConnectionID)
		return CredentialReference{}, err
	}
	return reference, nil
}

func (s *Store) SetConnectionStatus(connectionID string, status Status) error {
	if status != ActiveStatus && status != DisabledStatus && status != RevokedStatus {
		return fmt.Errorf("%w: connection status", ErrInvalid)
	}
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", ErrNotFound)
	}
	if connection.Status == RevokedStatus && status == ActiveStatus {
		s.mu.Unlock()
		return fmt.Errorf("%w: revoked connection cannot be re-enabled", ErrRevoked)
	}
	connection.Status = status
	connection.Revision++
	s.connections[connectionID] = connection
	for id, binding := range s.bindings {
		if binding.ConnectionID == connectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connectionID)
	s.mu.Unlock()
	s.stopWorkloads(stopped)
	return s.persistAndNotify()
}

func (s *Store) MarkDegraded(connectionID string) error {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", ErrNotFound)
	}
	connection.Status = DegradedStatus
	connection.Revision++
	s.connections[connectionID] = connection
	for id, binding := range s.bindings {
		if binding.ConnectionID == connectionID {
			s.touchProjectionLocked(&binding)
			s.bindings[id] = binding
		}
	}
	stopped := s.stopAffectedLocked(connectionID)
	s.mu.Unlock()
	s.stopWorkloads(stopped)
	return s.persistAndNotify()
}

func (s *Store) SetTerminalExposure(connectionID string, enabled bool) error {
	s.mu.Lock()
	connection, ok := s.connections[connectionID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: connection", ErrNotFound)
	}
	connection.TerminalExposure = enabled
	connection.Revision++
	s.connections[connectionID] = connection
	s.mu.Unlock()
	return s.persistAndNotify()
}

func (s *Store) SetBindingStatus(bindingID string, status Status) error {
	if status != ActiveStatus && status != DisabledStatus && status != RevokedStatus {
		return fmt.Errorf("%w: binding status", ErrInvalid)
	}
	s.mu.Lock()
	binding, ok := s.bindings[bindingID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: binding", ErrNotFound)
	}
	if binding.Status == RevokedStatus && status == ActiveStatus {
		s.mu.Unlock()
		return fmt.Errorf("%w: revoked binding cannot be re-enabled", ErrRevoked)
	}
	binding.Status = status
	binding.Revision++
	s.touchProjectionLocked(&binding)
	s.bindings[bindingID] = binding
	stopped := s.stopBindingLocked(bindingID)
	s.mu.Unlock()
	s.stopWorkloads(stopped)
	return s.persistAndNotify()
}

func (s *Store) Resolve(auth identity.Envelope, bindingID string) (EffectiveBinding, error) {
	if err := s.Reload(); err != nil {
		return EffectiveBinding{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resolveLocked(auth, bindingID)
}

// Authorize holds the registry read lock through the execution admission
// callback. This makes revoke/rotate win before backend admission, while an
// already admitted call finishes under its recorded revision.
// ponytail: one registry lock serializes authorization and lifecycle mutations;
// use per-connection locks only if measured connector concurrency needs it.
func (s *Store) Authorize(auth identity.Envelope, bindingID string, admit func(EffectiveBinding) error) error {
	if admit == nil {
		return fmt.Errorf("%w: nil admission callback", ErrInvalid)
	}
	if err := s.Reload(); err != nil {
		return err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	effective, err := s.resolveLocked(auth, bindingID)
	if err != nil {
		return err
	}
	return admit(effective)
}

func (s *Store) resolveLocked(auth identity.Envelope, bindingID string) (EffectiveBinding, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return EffectiveBinding{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	if !identity.ValidID(bindingID) {
		return EffectiveBinding{}, fmt.Errorf("%w: binding id", ErrInvalid)
	}
	binding, ok := s.bindings[bindingID]
	if !ok {
		return EffectiveBinding{}, fmt.Errorf("%w: binding", ErrNotFound)
	}
	if binding.Status == RevokedStatus {
		return EffectiveBinding{}, fmt.Errorf("%w: binding", ErrRevoked)
	}
	if binding.Status != ActiveStatus {
		return EffectiveBinding{}, fmt.Errorf("%w: binding disabled", ErrUnauthorized)
	}
	if binding.PrincipalID != auth.PrincipalID || binding.ContextID != auth.ContextID || binding.RuntimeID != auth.RuntimeID || binding.PolicyVersion != auth.PolicyVersion {
		return EffectiveBinding{}, fmt.Errorf("%w: exact runtime owner", ErrUnauthorized)
	}
	definition, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
	if !ok {
		return EffectiveBinding{}, fmt.Errorf("%w: definition", ErrStale)
	}
	if definition.Workload.Class != binding.WorkloadClass {
		return EffectiveBinding{}, fmt.Errorf("%w: workload class changed", ErrStale)
	}
	effective := EffectiveBinding{Binding: binding, Definition: definition}
	if binding.ConnectionID == "" {
		if len(definition.Credentials) > 0 {
			return EffectiveBinding{}, fmt.Errorf("%w: missing connection", ErrUnauthorized)
		}
		ownerID := ""
		if definition.Workload.Class == PerUser {
			ownerID = binding.ToolBindingID
		}
		effective.WorkloadID = WorkloadInstanceID(definition.DefinitionID, definition.Workload.Class, ownerID, "")
		return effective, nil
	}
	connection, ok := s.connections[binding.ConnectionID]
	if !ok {
		return EffectiveBinding{}, fmt.Errorf("%w: connection", ErrStale)
	}
	if !connection.Owner.Matches(auth) {
		return EffectiveBinding{}, fmt.Errorf("%w: exact connection owner", ErrUnauthorized)
	}
	if connection.Status == RevokedStatus {
		return EffectiveBinding{}, fmt.Errorf("%w: connection", ErrRevoked)
	}
	if connection.Status == DegradedStatus {
		return EffectiveBinding{}, fmt.Errorf("%w: connection", ErrDegraded)
	}
	if connection.Status != ActiveStatus || connection.Revision != binding.ConnectionRevision || connection.DefinitionID != definition.DefinitionID {
		return EffectiveBinding{}, fmt.Errorf("%w: current connection revision", ErrStale)
	}
	effective.Connection = &connection
	if len(definition.Credentials) > 0 {
		if binding.CredentialRefID == "" || connection.CredentialRefID != binding.CredentialRefID || binding.CredentialRevision == 0 {
			return EffectiveBinding{}, fmt.Errorf("%w: current credential reference", ErrStale)
		}
		reference, ok := s.credentials[binding.CredentialRefID]
		if !ok || reference.Status != ActiveStatus || reference.ConnectionID != connection.ConnectionID || reference.Revision != binding.CredentialRevision {
			return EffectiveBinding{}, fmt.Errorf("%w: current credential revision", ErrStale)
		}
		keys := map[string]bool{}
		for _, key := range reference.Keys {
			keys[key] = true
		}
		for _, input := range definition.Credentials {
			if input.Required && !keys[input.Name] {
				return EffectiveBinding{}, fmt.Errorf("%w: missing credential input", ErrUnauthorized)
			}
		}
		effective.Credential = &reference
	}
	ownerID := auth.ContextID
	if definition.Workload.Class == Shared {
		ownerID = ""
	}
	if definition.Workload.Class == PerUser {
		ownerID += ":" + auth.PrincipalID + ":" + connection.ConnectionID
	}
	effective.WorkloadID = WorkloadInstanceID(definition.DefinitionID, definition.Workload.Class, ownerID, "")
	return effective, nil
}

type snapshot struct {
	Schema              int                      `json:"schema"`
	Definitions         []ToolDefinition         `json:"definitions"`
	Connections         []Connection             `json:"connections"`
	Credentials         []CredentialReference    `json:"credential_references"`
	Bindings            []ToolBinding            `json:"bindings"`
	Workloads           []WorkloadInstance       `json:"workloads"`
	ProjectionRevisions map[string]uint64        `json:"projection_revisions,omitempty"`
	Grants              []Grant                  `json:"grants,omitempty"`
	Onboardings         []Onboarding             `json:"onboardings,omitempty"`
	Publications        []DefinitionPublication  `json:"publications,omitempty"`
	SharedPolicies      []SharedCredentialPolicy `json:"shared_credential_policies,omitempty"`
}

func (s *Store) Save(path string) error {
	if err := safeStorePath(path); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := safeStorePath(path + ".lock"); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	previous, err := os.ReadFile(path) // #nosec G304 -- trusted, symlink-checked registry path.
	if err == nil && (s.savedPath != path || sha256.Sum256(previous) != s.diskDigest) {
		return ErrConflict
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	state := snapshot{Schema: SchemaVersion, ProjectionRevisions: map[string]uint64{}}
	for _, d := range s.definitions {
		state.Definitions = append(state.Definitions, d)
	}
	for _, c := range s.connections {
		state.Connections = append(state.Connections, c)
	}
	for _, r := range s.credentials {
		state.Credentials = append(state.Credentials, r)
	}
	for _, b := range s.bindings {
		state.Bindings = append(state.Bindings, b)
	}
	for _, w := range s.workloads {
		state.Workloads = append(state.Workloads, w)
	}
	for key, revision := range s.projectionRevisions {
		state.ProjectionRevisions[key] = revision
	}
	for _, grant := range s.grants {
		state.Grants = append(state.Grants, grant)
	}
	for _, onboarding := range s.onboardings {
		state.Onboardings = append(state.Onboardings, onboarding)
	}
	for _, pub := range s.publications {
		state.Publications = append(state.Publications, pub)
	}
	for _, policy := range s.sharedPolicies {
		state.SharedPolicies = append(state.SharedPolicies, policy)
	}
	slices.SortFunc(state.Definitions, func(a, b ToolDefinition) int {
		return strings.Compare(definitionKey(a.DefinitionID, a.Version), definitionKey(b.DefinitionID, b.Version))
	})
	slices.SortFunc(state.Connections, func(a, b Connection) int { return strings.Compare(a.ConnectionID, b.ConnectionID) })
	slices.SortFunc(state.Credentials, func(a, b CredentialReference) int { return strings.Compare(a.CredentialRefID, b.CredentialRefID) })
	slices.SortFunc(state.Bindings, func(a, b ToolBinding) int { return strings.Compare(a.ToolBindingID, b.ToolBindingID) })
	slices.SortFunc(state.Workloads, func(a, b WorkloadInstance) int { return strings.Compare(a.WorkloadID, b.WorkloadID) })
	slices.SortFunc(state.Grants, func(a, b Grant) int { return strings.Compare(a.GrantID, b.GrantID) })
	slices.SortFunc(state.Onboardings, func(a, b Onboarding) int { return strings.Compare(a.OnboardingID, b.OnboardingID) })
	slices.SortFunc(state.Publications, func(a, b DefinitionPublication) int {
		return strings.Compare(definitionKey(a.DefinitionID, a.Version), definitionKey(b.DefinitionID, b.Version))
	})
	slices.SortFunc(state.SharedPolicies, func(a, b SharedCredentialPolicy) int { return strings.Compare(a.PolicyID, b.PolicyID) })
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".toolhub-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(body)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	s.savedPath, s.diskDigest = path, sha256.Sum256(body)
	return nil
}

func Load(path string) (*Store, error) {
	if err := safeStorePath(path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path) // #nosec G304 -- the trusted registry path is checked for symlink components above
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	var state snapshot
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("%w: store JSON: %v", ErrInvalid, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: one store JSON document expected", ErrInvalid)
	}
	if state.Schema != SchemaVersion {
		return nil, fmt.Errorf("%w: store schema", ErrInvalid)
	}
	store := NewStore()
	for _, definition := range state.Definitions {
		if err := store.RegisterDefinition(definition); err != nil {
			return nil, err
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, reference := range state.Credentials {
		if err := reference.Validate(); err != nil {
			return nil, err
		}
		store.credentials[reference.CredentialRefID] = reference
	}
	for _, connection := range state.Connections {
		if err := connection.Validate(); err != nil {
			return nil, err
		}
		if !store.hasDefinitionID(connection.DefinitionID) {
			return nil, fmt.Errorf("%w: connection definition", ErrNotFound)
		}
		if connection.CredentialRefID != "" {
			reference, ok := store.credentials[connection.CredentialRefID]
			if !ok || reference.ConnectionID != connection.ConnectionID {
				return nil, fmt.Errorf("%w: connection credential", ErrInvalid)
			}
		}
		store.connections[connection.ConnectionID] = connection
	}
	for _, binding := range state.Bindings {
		if err := binding.Validate(); err != nil {
			return nil, err
		}
		store.bindings[binding.ToolBindingID] = binding
	}
	for _, workload := range state.Workloads {
		if err := workload.Validate(); err != nil {
			return nil, err
		}
		store.workloads[workload.WorkloadID] = workload
	}
	for key, revision := range state.ProjectionRevisions {
		if revision == 0 {
			return nil, fmt.Errorf("%w: persisted projection revision", ErrInvalid)
		}
		store.projectionRevisions[key] = revision
	}
	for _, grant := range state.Grants {
		if err := grant.Validate(); err != nil {
			return nil, err
		}
		store.grants[grant.GrantID] = grant
	}
	for _, onboarding := range state.Onboardings {
		if err := onboarding.Validate(); err != nil {
			return nil, err
		}
		store.onboardings[onboarding.OnboardingID] = onboarding
	}
	for _, pub := range state.Publications {
		if err := pub.Validate(); err != nil {
			return nil, err
		}
		store.publications[definitionKey(pub.DefinitionID, pub.Version)] = pub
	}
	for _, policy := range state.SharedPolicies {
		if err := policy.Validate(); err != nil {
			return nil, err
		}
		store.sharedPolicies[policy.PolicyID] = policy
	}
	for _, binding := range store.bindings {
		key := projectionKey(binding.PrincipalID, binding.ContextID, binding.RuntimeID)
		if binding.ProjectionRevision > store.projectionRevisions[key] {
			store.projectionRevisions[key] = binding.ProjectionRevision
		}
	}
	if err := store.validateLocked(); err != nil {
		return nil, err
	}
	store.path = path
	store.savedPath, store.diskDigest = path, sha256.Sum256(b)
	return store, nil
}

func (s *Store) persist() error {
	if s == nil || s.path == "" {
		return nil
	}
	return s.Save(s.path)
}

func (s *Store) stopAffectedLocked(connectionID string) []string {
	ids := make([]string, 0)
	for id, workload := range s.workloads {
		binding, ok := s.bindings[workload.BindingID]
		if !ok || binding.ConnectionID != connectionID {
			continue
		}
		if workload.Status == RunningStatus || workload.Status == StartingStatus {
			workload.Status = StoppedStatus
			s.workloads[id] = workload
			ids = append(ids, workload.WorkloadID)
		}
	}
	return ids
}

func (s *Store) stopBindingLocked(bindingID string) []string {
	ids := make([]string, 0)
	for id, workload := range s.workloads {
		if workload.BindingID != bindingID {
			continue
		}
		if workload.Status == RunningStatus || workload.Status == StartingStatus {
			workload.Status = StoppedStatus
			s.workloads[id] = workload
			ids = append(ids, workload.WorkloadID)
		}
	}
	return ids
}

func (s *Store) stopWorkloads(ids []string) {
	if s == nil || s.Stopper == nil {
		return
	}
	for _, id := range ids {
		_ = s.Stopper.Stop(id)
	}
}

func (s *Store) persistAndNotify() error {
	if err := s.persist(); err != nil {
		return err
	}
	if s == nil || s.Reconnect == nil {
		return nil
	}
	_, _, err := s.Reconnect.Reconcile()
	return err
}

// Reload replaces in-memory state from the file-backed snapshot. In-memory
// stores (no path) are unchanged so tests can mutate without a round-trip.
func (s *Store) Reload() error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	fresh, err := Load(s.path)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	// Reads must not replace pending in-process mutations with the unchanged
	// disk snapshot, nor race a Save and restore its older predecessor.
	if fresh.diskDigest == s.diskDigest {
		s.mu.Unlock()
		return nil
	}
	stopped := make([]string, 0)
	for id, old := range s.workloads {
		if old.Status != RunningStatus && old.Status != StartingStatus {
			continue
		}
		next, ok := fresh.workloads[id]
		if !ok || (next.Status != RunningStatus && next.Status != StartingStatus) {
			stopped = append(stopped, id)
		}
	}
	s.definitions = fresh.definitions
	s.connections = fresh.connections
	s.credentials = fresh.credentials
	s.bindings = fresh.bindings
	s.workloads = fresh.workloads
	s.projectionRevisions = fresh.projectionRevisions
	s.grants = fresh.grants
	s.onboardings = fresh.onboardings
	s.publications = fresh.publications
	s.sharedPolicies = fresh.sharedPolicies
	s.savedPath, s.diskDigest = fresh.savedPath, fresh.diskDigest
	s.mu.Unlock()
	s.stopWorkloads(stopped)
	return nil
}

func (s *Store) validateLocked() error {
	for _, binding := range s.bindings {
		definition, ok := s.definitions[definitionKey(binding.DefinitionID, binding.DefinitionVersion)]
		if !ok || definition.Workload.Class != binding.WorkloadClass {
			return fmt.Errorf("%w: persisted binding definition", ErrInvalid)
		}
		if binding.ConnectionID != "" {
			connection, ok := s.connections[binding.ConnectionID]
			if !ok || connection.DefinitionID != binding.DefinitionID {
				return fmt.Errorf("%w: persisted binding connection", ErrInvalid)
			}
		}
	}
	for _, workload := range s.workloads {
		binding, ok := s.bindings[workload.BindingID]
		if !ok || binding.DefinitionID != workload.DefinitionID || binding.DefinitionVersion != workload.DefinitionVersion {
			return fmt.Errorf("%w: persisted workload binding", ErrInvalid)
		}
	}
	return nil
}

func safeStorePath(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: store path must be absolute", ErrInvalid)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%w: store path: %v", ErrInvalid, err)
	}
	volume := filepath.VolumeName(abs)
	root := volume + string(filepath.Separator)
	current := root
	for _, part := range strings.Split(strings.TrimPrefix(abs, root), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: store path: %v", ErrInvalid, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: store path contains symlink", ErrInvalid)
		}
	}
	return nil
}

func definitionsEqual(a, b ToolDefinition) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func recordsEqual(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func deterministicID(prefix string, values ...string) string {
	h := sha256.New()
	for _, value := range values {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "-" + hex.EncodeToString(h.Sum(nil))[:24]
}

func shortHash(values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(digest[:])[:8]
}

func slug(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "tool"
	}
	if len(result) > 20 {
		return result[:20]
	}
	return result
}
