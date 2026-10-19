package api

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"io"
	"net/netip"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

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
	Schema       int               `json:"schema"`
	DefinitionID string            `json:"definition_id"`
	Version      string            `json:"version"`
	Transport    Transport         `json:"transport"`
	Source       DefinitionSource  `json:"source"`
	Tools        []ToolSpec        `json:"tools"`
	Credentials  []CredentialInput `json:"credentials,omitempty"`
	// CredentialGroups names OR alternatives: a definition is satisfied when
	// every member of at least one group is present. Empty means every
	// required input must be present individually.
	CredentialGroups           [][]string        `json:"credential_groups,omitempty"`
	CredentialContractID       string            `json:"credential_contract_id,omitempty"`
	CredentialContractRevision int               `json:"credential_contract_revision,omitempty"`
	CredentialContractEnv      map[string]string `json:"credential_contract_env,omitempty"`
	Environment                []string          `json:"environment,omitempty"`
	RuntimeEnvironment         map[string]string `json:"runtime_environment,omitempty"`
	// ProxyEnvironment names env vars the artifact expects to carry its
	// outbound proxy URL, or paired *_PROXY_HOST / *_PROXY_PORT settings.
	// The runtime fills each with the audited egress
	// proxy; neither the definition nor the owner supplies the value, so a
	// reviewed name can never redirect egress off the allowlisted proxy.
	ProxyEnvironment []string        `json:"proxy_environment,omitempty"`
	Workload         WorkloadPolicy  `json:"workload"`
	Execution        ExecutionPolicy `json:"execution"`
	Health           HealthProbe     `json:"health"`
	// Toolsets groups the surface for staged projection: DefaultEnabled is
	// the set an install projects, DefaultExcluded tools stay hidden until a
	// binding explicitly enables a toolset containing them. Empty metadata
	// means the whole surface projects (pre-toolset definitions).
	Toolsets        map[string][]string `json:"toolsets,omitempty"`
	DefaultEnabled  []string            `json:"default_enabled,omitempty"`
	DefaultExcluded []string            `json:"default_excluded,omitempty"`
}

// ProjectedToolSpecs filters a definition's surface for one binding: the
// default-enabled set plus the members of the binding's enabled toolsets.
// default_excluded suppresses a tool only from the default projection — an
// explicitly enabled toolset still surfaces it. Definitions without any
// toolset metadata project the whole surface (pre-toolset records).
func (d ToolDefinition) ProjectedToolSpecs(binding ToolBinding) []ToolSpec {
	if len(d.Toolsets) == 0 && len(d.DefaultEnabled) == 0 && len(d.DefaultExcluded) == 0 {
		return d.Tools
	}
	enabled := map[string]bool{}
	for _, name := range d.DefaultEnabled {
		enabled[name] = true
	}
	for _, set := range binding.EnabledToolsets {
		for _, name := range d.Toolsets[set] {
			enabled[name] = true
		}
	}
	excluded := map[string]bool{}
	for _, name := range d.DefaultExcluded {
		excluded[name] = true
	}
	for _, set := range binding.EnabledToolsets {
		for _, name := range d.Toolsets[set] {
			delete(excluded, name)
		}
	}
	out := make([]ToolSpec, 0, len(d.Tools))
	for _, tool := range d.Tools {
		if enabled[tool.Name] && !excluded[tool.Name] {
			out = append(out, tool)
		}
	}
	return out
}

type DefinitionSource struct {
	URL        string   `json:"url,omitempty"`
	TLSMode    string   `json:"tls_mode,omitempty"`
	Image      string   `json:"image,omitempty"`
	Digest     string   `json:"digest,omitempty"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Subfolder  string   `json:"subfolder,omitempty"`
	CommitSHA  string   `json:"commit_sha,omitempty"`
	// ReleaseTag/ReleaseAsset/AssetDigest record a github-release source pin:
	// the tag is mutable, so the resolved asset name and its verified sha256
	// are the immutable evidence (CommitSHA stays empty on release installs).
	ReleaseTag   string `json:"release_tag,omitempty"`
	ReleaseAsset string `json:"release_asset,omitempty"`
	AssetDigest  string `json:"asset_digest,omitempty"`
	// PackageRegistry/PackageName/PackageVersion record a registry package
	// pin (pypi:/npm:): the resolved exact version is the immutable evidence
	// alongside the built image digest.
	PackageRegistry    string `json:"package_registry,omitempty"`
	PackageName        string `json:"package_name,omitempty"`
	PackageVersion     string `json:"package_version,omitempty"`
	ArchiveDigest      string `json:"archive_digest,omitempty"`
	ProvenanceDigest   string `json:"provenance_digest,omitempty"`
	SBOMDigest         string `json:"sbom_digest,omitempty"`
	RecipeDigest       string `json:"recipe_digest,omitempty"`
	ReviewDigest       string `json:"review_digest,omitempty"`
	ToolContractDigest string `json:"tool_contract_digest,omitempty"`
	ToolContractSource string `json:"tool_contract_source,omitempty"`
	// NetworkTransport is set by preflight when the artifact's entrypoint
	// declares an HTTP/SSE transport and the server proved it cannot speak
	// stdio. Runtime then launches the original entrypoint and the companion
	// bridges it as an MCP client instead of rewriting args to stdio.
	NetworkTransport string `json:"network_transport,omitempty"`
	// ContextSources records the reviewed overlay pins merged into the source
	// build context; re-review recomputes the digest from the same records.
	ContextSources []ArtifactOverlay `json:"context_sources,omitempty"`
}

// GroupedCredentialInput reports whether the credential name is a member of an
// either/or group — such members may legitimately stay undelivered when a
// different group was satisfied.
func GroupedCredentialInput(d ToolDefinition, name string) bool {
	for _, group := range d.CredentialGroups {
		if slices.Contains(group, name) {
			return true
		}
	}
	return false
}

// CredentialsSatisfied reports whether the required credential inputs are met
// by the present set. A required input is met when present itself, or when it
// belongs to an OR group and at least one complete group is present.
func (d ToolDefinition) CredentialsSatisfied(present func(string) bool) bool {
	inGroup := map[string]bool{}
	groupComplete := false
	for _, group := range d.CredentialGroups {
		complete := len(group) > 0
		for _, name := range group {
			inGroup[name] = true
			if !present(name) {
				complete = false
			}
		}
		groupComplete = groupComplete || complete
	}
	for _, input := range d.Credentials {
		if input.Required && !present(input.Name) && !(inGroup[input.Name] && groupComplete) {
			return false
		}
	}
	return true
}

func (d ToolDefinition) Validate() error {
	if d.Schema != SchemaVersion || !identity.ValidID(d.DefinitionID) || !VersionPattern.MatchString(d.Version) {
		return fmt.Errorf("%w: definition schema, id or version", ErrInvalid)
	}
	if d.Workload.Class != Shared && d.Workload.Class != PerUser && d.Workload.Class != PerJob {
		return fmt.Errorf("%w: unknown workload class", ErrInvalid)
	}
	if strings.TrimSpace(d.Workload.Rationale) == "" || len(d.Workload.Rationale) > 512 {
		return fmt.Errorf("%w: workload rationale is required", ErrInvalid)
	}
	if err := d.Source.Validate(d.Transport); err != nil {
		return err
	}
	if len(d.Tools) == 0 || len(d.Tools) > 256 {
		return fmt.Errorf("%w: tools must be bounded and non-empty", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, tool := range d.Tools {
		if err := validateToolCapability(tool); err != nil {
			return err
		}
		if len(tool.InputSchema) > 65536 || (len(tool.InputSchema) > 0 && (!json.Valid(tool.InputSchema) || (d.Transport != RemoteMCP && d.Transport != ContainerMCP))) {
			return fmt.Errorf("%w: MCP input schema", ErrInvalid)
		}
		if !McpToolNamePattern.MatchString(tool.Name) || (tool.Effect != ReadEffect && tool.Effect != WriteEffect) || seen[tool.Name] {
			return fmt.Errorf("%w: invalid or duplicate tool %q", ErrInvalid, tool.Name)
		}
		if len(tool.Description) > 1024 || len(tool.Arguments) > 32 {
			return fmt.Errorf("%w: tool metadata is too large", ErrInvalid)
		}
		if tool.Sandboxed && d.Transport != AgentTools {
			return fmt.Errorf("%w: sandboxed tool %q requires the agent-tools transport", ErrInvalid, tool.Name)
		}
		argumentNames := map[string]bool{}
		for _, argument := range tool.Arguments {
			validType := validCLIType(argument.Type) || ((argument.Type == "array" || argument.Type == "object") && d.Transport == AgentTools)
			if !toolNamePattern.MatchString(argument.Name) || argumentNames[argument.Name] || !validType {
				return fmt.Errorf("%w: invalid tool argument %q", ErrInvalid, argument.Name)
			}
			if argument.Pattern != "" {
				// Pattern only makes sense on string arguments, and a
				// positional operand (Flag == "") is meaningless without one:
				// it is the only thing keeping a caller-controlled argv value
				// from smuggling a flag.
				if argument.Type != "string" || len(argument.Pattern) > 512 {
					return fmt.Errorf("%w: invalid CLI argument pattern %q", ErrInvalid, argument.Name)
				}
				if _, err := regexp.Compile(argument.Pattern); err != nil {
					return fmt.Errorf("%w: invalid CLI argument pattern %q", ErrInvalid, argument.Name)
				}
			}
			switch d.Transport {
			case BoundedCLI:
				if argument.Flag == "" {
					if argument.Pattern == "" {
						return fmt.Errorf("%w: positional CLI argument %q requires a pattern", ErrInvalid, argument.Name)
					}
				} else if !validCLIFlag(argument.Flag) {
					return fmt.Errorf("%w: invalid CLI argument %q", ErrInvalid, argument.Name)
				}
			case ProviderAPI, AgentTools:
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
		// NODE_USE_ENV_PROXY is a boolean switch, not a proxy URL: it makes
		// Node's fetch honor the controller-injected HTTP(S)_PROXY values.
		// Without it a Node server ignores the egress proxy and fails on the
		// isolated workload network; it can never redirect egress.
		proxyName := strings.Contains(name, "PROXY") && name != "NODE_USE_ENV_PROXY"
		if !CredentialPattern.MatchString(name) || len(value) > 1024 || strings.ContainsAny(value, "\x00\r\n") || proxyName || name == "NODE_OPTIONS" || name == "LD_PRELOAD" {
			return fmt.Errorf("%w: invalid runtime environment", ErrInvalid)
		}
	}
	for _, name := range d.Environment {
		if !CredentialPattern.MatchString(name) || seen[name] {
			return fmt.Errorf("%w: invalid or duplicate environment parameter %q", ErrInvalid, name)
		}
		seen[name] = true
	}
	for _, input := range d.Credentials {
		if _, exists := d.RuntimeEnvironment[input.Name]; exists {
			return fmt.Errorf("%w: runtime environment overlaps credential", ErrInvalid)
		}
		if !CredentialPattern.MatchString(input.Name) || seen[input.Name] {
			return fmt.Errorf("%w: invalid or duplicate credential input %q", ErrInvalid, input.Name)
		}
		switch input.Delivery {
		case "", "env":
			if input.Target != "" || input.Prefix != "" {
				return fmt.Errorf("%w: credential header fields require http_header delivery", ErrInvalid)
			}
		case "http_header":
			if d.Transport != RemoteMCP {
				return fmt.Errorf("%w: header credential delivery requires remote-mcp", ErrInvalid)
			}
			if err := validateHeaderDelivery(input); err != nil {
				return err
			}
		case "brokered":
			// Bounded-cli only: the cell sees a cred-proxy URL, and the
			// cell-side proxy injects the real credential on the reviewed
			// target host. The secret never enters container config or env.
			if d.Transport != BoundedCLI {
				return fmt.Errorf("%w: brokered credential delivery requires bounded-cli", ErrInvalid)
			}
			if !HostPattern.MatchString(strings.ToLower(input.Target)) || strings.Contains(input.Target, "/") {
				return fmt.Errorf("%w: brokered credential target must be a host", ErrInvalid)
			}
			if len(input.Prefix) > 128 || strings.ContainsAny(input.Prefix, "\x00\r\n") {
				return fmt.Errorf("%w: invalid brokered credential prefix", ErrInvalid)
			}
		case "file":
			// Container-mcp only: the value is file content the runtime
			// materializer bind-mounts read-only at Target; the env var then
			// carries the in-container path. Only a container can take a mount.
			if d.Transport != ContainerMCP {
				return fmt.Errorf("%w: file credential delivery requires container-mcp", ErrUnsupportedCredentialContract)
			}
			if input.Prefix != "" || !ValidContainerMountTarget(input.Target) {
				return fmt.Errorf("%w: invalid file credential target %q", ErrInvalid, input.Target)
			}
		case "json":
			// A JSON document carried in the workload environment, exactly
			// like env; the runtime may still file-mount CREDENTIALS-class
			// values for servers that read a path. Target restates the name.
			if input.Target != "" || input.Prefix != "" {
				return fmt.Errorf("%w: json credential carries no header/mount target", ErrInvalid)
			}
		case "oauth":
			// A browser flow mints these at runtime; the value store and the
			// credential form cannot satisfy a static oauth field.
			return fmt.Errorf("%w: credential %s mints through a reviewed OAuth flow, not the form", ErrUnsupportedAuth, input.Name)
		default:
			return fmt.Errorf("%w: %q", ErrUnsupportedCredentialContract, input.Delivery)
		}
		if d.Workload.Class == Shared && !input.PerRequest && !d.Workload.Sharable {
			return fmt.Errorf("%w: shared workload requires per-request credentials", ErrInvalid)
		}
		seen[input.Name] = true
	}
	if len(d.ProxyEnvironment) > 8 {
		return fmt.Errorf("%w: too many proxy environment parameters", ErrInvalid)
	}
	for _, name := range d.ProxyEnvironment {
		if !CredentialPattern.MatchString(name) || !(strings.HasSuffix(name, "_PROXY") || strings.HasSuffix(name, "_PROXY_HOST") || strings.HasSuffix(name, "_PROXY_PORT")) || strings.Contains(name, "NO_PROXY") || seen[name] {
			return fmt.Errorf("%w: invalid or duplicate proxy environment parameter %q", ErrInvalid, name)
		}
		seen[name] = true
	}
	for _, name := range d.ProxyEnvironment {
		if strings.HasSuffix(name, "_PROXY_HOST") && !seen[strings.TrimSuffix(name, "HOST")+"PORT"] || strings.HasSuffix(name, "_PROXY_PORT") && !seen[strings.TrimSuffix(name, "PORT")+"HOST"] {
			return fmt.Errorf("%w: incomplete proxy address pair", ErrInvalid)
		}
	}
	for _, group := range d.CredentialGroups {
		if len(group) == 0 {
			return fmt.Errorf("%w: empty credential group", ErrInvalid)
		}
		members := map[string]bool{}
		for _, name := range group {
			if members[name] || !slices.ContainsFunc(d.Credentials, func(input CredentialInput) bool { return input.Name == name && input.Required }) {
				return fmt.Errorf("%w: invalid credential group member %q", ErrInvalid, name)
			}
			members[name] = true
		}
	}
	if d.CredentialContractID != "" {
		if !identity.ValidID(d.CredentialContractID) || d.CredentialContractRevision < 1 || d.CredentialContractRevision > 100000 {
			return fmt.Errorf("%w: invalid credential broker contract", ErrInvalid)
		}
		for name, target := range d.CredentialContractEnv {
			if !CredentialPattern.MatchString(name) || !CredentialPattern.MatchString(target) || !slices.ContainsFunc(d.Credentials, func(input CredentialInput) bool { return input.Name == name }) {
				return fmt.Errorf("%w: invalid credential broker delivery mapping", ErrInvalid)
			}
		}
		for _, input := range d.Credentials {
			if input.Required && d.CredentialContractEnv[input.Name] == "" && !d.CredentialsSatisfied(func(name string) bool { return d.CredentialContractEnv[name] != "" }) {
				return fmt.Errorf("%w: missing credential broker delivery for %s", ErrInvalid, input.Name)
			}
		}
	} else if d.CredentialContractRevision != 0 || len(d.CredentialContractEnv) != 0 {
		return fmt.Errorf("%w: credential broker mapping needs a contract", ErrInvalid)
	}
	if d.Workload.Class == Shared && (d.Workload.Stateful || len(d.Execution.Mounts) > 0) {
		return fmt.Errorf("%w: shared workload cannot own state or mounts", ErrInvalid)
	}
	if d.Workload.Class == PerJob && d.Workload.Stateful && !HasMountSource(d.Execution.Mounts, "job-state") {
		return fmt.Errorf("%w: stateful per-job workload needs job-state", ErrInvalid)
	}
	if d.Transport == ProviderAPI {
		if d.Workload.Class != PerUser || d.Workload.Stateful || len(d.Execution.Mounts) > 0 {
			return fmt.Errorf("%w: provider API is a stateless per-user data plane", ErrInvalid)
		}
		if !slices.ContainsFunc(d.Credentials, func(input CredentialInput) bool { return input.Required && !input.PerRequest }) {
			return fmt.Errorf("%w: provider API needs a required owner credential", ErrInvalid)
		}
		if !slices.ContainsFunc(d.Execution.Egress, func(host string) bool { return strings.EqualFold(strings.TrimSpace(host), HostOfURL(d.Source.URL)) }) {
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
			if at <= 0 || !DigestPattern.MatchString(image[at+1:]) || strings.ContainsAny(image[:at], " \t\r\n") || strings.Contains(image[:at], "..") || strings.Contains(image, ":latest") || seen[image] {
				return fmt.Errorf("%w: sidecar image must be unique and digest-pinned", ErrInvalid)
			}
			seen[image] = true
		}
	}
	if err := d.Execution.Validate(d.Workload.Class); err != nil {
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
	return d.validateCLICellPolicy()
}

// validateCLICellPolicy bounds the cell-only workload fields: they exist so
// the controller can place a bounded-cli call on the right lifecycle tier and
// workspace bind; every other transport must leave them unset.
func (d ToolDefinition) validateCLICellPolicy() error {
	W := d.Workload
	if W.Lifecycle == "" && !W.Stateless && !W.InstallPackages && W.Toolbox == "" && W.WorkspaceScope == "" && W.WorkspaceAccess == "" {
		return nil
	}
	if d.Transport != BoundedCLI {
		return fmt.Errorf("%w: cell lifecycle fields are bounded-cli only", ErrInvalid)
	}
	if W.InstallPackages && (W.Stateless || W.Lifecycle == CLILifecycleSharedPool || W.Lifecycle == CLILifecycleToolbox) {
		return fmt.Errorf("%w: package installation needs a private non-stateless cell", ErrInvalid)
	}
	switch W.Lifecycle {
	case "", CLILifecycleEphemeral:
	case CLILifecycleTask:
		if W.Class != PerJob {
			return fmt.Errorf("%w: task lifecycle keys on the job and needs per-job class", ErrInvalid)
		}
	case CLILifecycleBinding:
		if W.Class != PerUser {
			return fmt.Errorf("%w: binding lifecycle needs per-user class", ErrInvalid)
		}
	case CLILifecycleToolbox:
		if W.Class != PerUser || W.Toolbox == "" {
			return fmt.Errorf("%w: toolbox lifecycle needs per-user class and a toolbox id", ErrInvalid)
		}
	case CLILifecycleSharedPool:
		if !W.Stateless || W.Class != Shared {
			return fmt.Errorf("%w: pool cells require a stateless shared definition", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown cli lifecycle %q", ErrInvalid, W.Lifecycle)
	}
	if W.Toolbox != "" {
		// Toolbox cells execute their own member; a binding cell with a
		// toolbox id only mounts the members read-only so the caller (for
		// example a cell shell) sees the same tool artifact set on PATH.
		if W.Lifecycle != CLILifecycleToolbox && W.Lifecycle != CLILifecycleBinding {
			return fmt.Errorf("%w: toolbox id requires toolbox or binding lifecycle", ErrInvalid)
		}
		if !identity.ValidID(W.Toolbox) {
			return fmt.Errorf("%w: toolbox id", ErrInvalid)
		}
	}
	switch W.WorkspaceScope {
	case "", "none", "binding", "principal":
	default:
		return fmt.Errorf("%w: unknown workspace scope %q", ErrInvalid, W.WorkspaceScope)
	}
	switch W.WorkspaceAccess {
	case "", "ro", "rw":
	default:
		return fmt.Errorf("%w: unknown workspace access %q", ErrInvalid, W.WorkspaceAccess)
	}
	scope := W.WorkspaceScope
	if scope == "" {
		if W.Class == Shared {
			scope = "none"
		} else {
			scope = "binding"
		}
	}
	if scope == "none" && W.WorkspaceAccess == "rw" {
		return fmt.Errorf("%w: workspace write without a workspace", ErrInvalid)
	}
	if scope == "binding" && W.Class == Shared {
		return fmt.Errorf("%w: shared workload cannot hold a binding workspace", ErrInvalid)
	}
	if W.Lifecycle == CLILifecycleSharedPool && scope != "none" {
		return fmt.Errorf("%w: pool cells carry no workspace bind", ErrInvalid)
	}
	return nil
}

func (s DefinitionSource) Validate(transport Transport) error {
	if s.CommitSHA != "" && !gitSHAPattern.MatchString(s.CommitSHA) {
		return fmt.Errorf("%w: source commit must be an exact lowercase SHA", ErrInvalid)
	}
	for name, value := range map[string]string{"archive_digest": s.ArchiveDigest, "provenance_digest": s.ProvenanceDigest, "sbom_digest": s.SBOMDigest, "recipe_digest": s.RecipeDigest, "review_digest": s.ReviewDigest, "tool_contract_digest": s.ToolContractDigest} {
		if value != "" && !DigestPattern.MatchString(value) {
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
	if s.NetworkTransport != "" && transport != ContainerMCP {
		return fmt.Errorf("%w: network transport requires a container MCP", ErrInvalid)
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
	case AgentTools:
		if !reflect.DeepEqual(s, DefinitionSource{}) {
			return fmt.Errorf("%w: agent-tools source is the in-process executor and must be empty", ErrInvalid)
		}
	case RemoteMCP, ProviderAPI:
		if s.URL == "" || values != 1 || s.TLSMode != "required" || s.Digest != "" || len(s.Args) != 0 || s.Repository != "" || s.CommitSHA != "" || s.ArchiveDigest != "" || s.ProvenanceDigest != "" || s.SBOMDigest != "" || s.RecipeDigest != "" || s.ReviewDigest != "" {
			return fmt.Errorf("%w: %s needs an HTTPS URL and required TLS", ErrInvalid, transport)
		}
		u, err := url.Parse(s.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%w: %s URL must be HTTPS without credentials, query or fragment", ErrInvalid, transport)
		}
	case ContainerMCP:
		if s.Image == "" || s.URL != "" || !DigestPattern.MatchString(s.Digest) || s.TLSMode != "" || strings.Contains(s.Image, "@") || strings.ContainsAny(s.Image, " \t\r\n") || strings.Contains(s.Image, "..") {
			return fmt.Errorf("%w: container MCP needs a digest-pinned image", ErrInvalid)
		}
		if i := strings.LastIndex(s.Image, ":"); i > strings.LastIndex(s.Image, "/") {
			return fmt.Errorf("%w: mutable container image tag", ErrInvalid)
		}
		if s.Command != "" && !ValidCommand(s.Command) {
			return fmt.Errorf("%w: invalid container command", ErrInvalid)
		}
		switch s.NetworkTransport {
		case "", "sse", "http", "streamable-http":
		default:
			return fmt.Errorf("%w: unknown container network transport", ErrInvalid)
		}
	case BoundedCLI:
		// Shell names stay denied for artifact guest paths; a bare shell
		// command is legal only on the controller-allowlisted plan path —
		// the cell is the boundary either way.
		if s.Command == "" || s.URL != "" || s.TLSMode != "" || !(ValidCommand(s.Command) || s.Image == "" && validShellCommand(s.Command)) {
			return fmt.Errorf("%w: bounded CLI needs a command and no remote source", ErrInvalid)
		}
		if s.Image != "" {
			// Cell-image artifact: the verified OCI image is the artifact, the
			// digest pins its manifest, and the command is the guest path of
			// the tool inside that image. Provenance rides the image chain.
			if !DigestPattern.MatchString(s.Digest) || strings.Contains(s.Image, "@") || strings.ContainsAny(s.Image, " \t\r\n") || strings.Contains(s.Image, "..") {
				return fmt.Errorf("%w: CLI artifact requires a digest-pinned image", ErrInvalid)
			}
			if i := strings.LastIndex(s.Image, ":"); i > strings.LastIndex(s.Image, "/") {
				return fmt.Errorf("%w: mutable CLI image tag", ErrInvalid)
			}
			if !strings.HasPrefix(s.Command, "/") {
				return fmt.Errorf("%w: CLI artifact command must be a guest path", ErrInvalid)
			}
			if (s.Repository == "" && s.PackageName == "") || !DigestPattern.MatchString(s.ArchiveDigest) || !DigestPattern.MatchString(s.RecipeDigest) || !DigestPattern.MatchString(s.ReviewDigest) {
				return fmt.Errorf("%w: CLI artifact requires a complete pinned provenance chain", ErrInvalid)
			}
			switch {
			case s.ReleaseTag != "":
				// github-release: the resolved asset name + verified sha256
				// stand in for the commit pin; the tag is recorded, not trusted.
				if !releaseTagPattern.MatchString(s.ReleaseTag) || s.ReleaseAsset == "" || !DigestPattern.MatchString(s.AssetDigest) || s.CommitSHA != "" || s.Subfolder != "" {
					return fmt.Errorf("%w: CLI release artifact requires tag, resolved asset and asset digest", ErrInvalid)
				}
			case s.PackageName != "":
				// Registry package: registry + name + resolved exact version
				// are the pin; they never mix with repository or release
				// provenance.
				if (s.PackageRegistry != "pypi" && s.PackageRegistry != "npm") || !validPackageName(s.PackageRegistry, s.PackageName) || !packageVersPattern.MatchString(s.PackageVersion) || s.Repository != "" || s.CommitSHA != "" || s.Subfolder != "" || s.ReleaseAsset != "" || s.AssetDigest != "" {
					return fmt.Errorf("%w: CLI package artifact requires registry, name and resolved version", ErrInvalid)
				}
			case s.ReleaseAsset != "" || s.AssetDigest != "" || s.PackageRegistry != "" || s.PackageVersion != "":
				return fmt.Errorf("%w: partial source evidence without a release tag or package name", ErrInvalid)
			case !gitSHAPattern.MatchString(s.CommitSHA):
				return fmt.Errorf("%w: CLI artifact requires a complete pinned provenance chain", ErrInvalid)
			}
		} else {
			// Catalog command on the shared tools image: a bare command name
			// carries no provenance of its own — the image digest is the pin.
			if values != 1 || s.Digest != "" || s.Repository != "" || s.CommitSHA != "" || s.ReleaseTag != "" || s.ReleaseAsset != "" || s.AssetDigest != "" || s.PackageRegistry != "" || s.PackageName != "" || s.PackageVersion != "" || s.ArchiveDigest != "" || s.ProvenanceDigest != "" || s.SBOMDigest != "" || s.RecipeDigest != "" || s.ReviewDigest != "" || len(s.ContextSources) != 0 {
				return fmt.Errorf("%w: catalog CLI command cannot carry artifact provenance", ErrInvalid)
			}
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

func (e ExecutionPolicy) Validate(class WorkloadClass) error {
	if e.TimeoutSeconds < 1 || e.TimeoutSeconds > MaxExecutionTimeout || e.OutputBytes < 1 || e.OutputBytes > MaxOutputBytes || e.CPUMillis < 1 || e.CPUMillis > MaxCPUMillis || e.MemoryMiB < 16 || e.MemoryMiB > MaxMemoryMiB || e.MaxPIDs < 1 || e.MaxPIDs > MaxPIDs || len(e.Egress) == 0 || len(e.Egress) > 32 || len(e.Mounts) > 8 || len(e.TCPForwards) > 8 {
		return fmt.Errorf("%w: execution limits are missing or unbounded", ErrInvalid)
	}
	listenPorts := map[int]bool{}
	forwardTargets := map[string]bool{}
	for _, forward := range e.TCPForwards {
		// The companion runs unprivileged and listens on loopback only, so
		// privileged ports can never bind; each listener and each target
		// environment variable must be unique and credential-shaped.
		if forward.Listen < 1024 || forward.Listen > 65535 || listenPorts[forward.Listen] || !CredentialPattern.MatchString(forward.TargetEnv) || forwardTargets[forward.TargetEnv] {
			return fmt.Errorf("%w: invalid tcp forward", ErrInvalid)
		}
		listenPorts[forward.Listen] = true
		forwardTargets[forward.TargetEnv] = true
	}
	seen := map[string]bool{}
	for _, host := range e.Egress {
		host = strings.ToLower(strings.TrimSpace(host))
		prefix, prefixErr := netip.ParsePrefix(host)
		validPrefix := prefixErr == nil && prefix.Addr().Is4() && prefix.Bits() >= 8 && prefix == prefix.Masked()
		if (!HostPattern.MatchString(host) && !validPrefix) || seen[host] {
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

// HostOfURL returns the lowercase host of an already validated HTTPS URL.
func HostOfURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func HasMountSource(mounts []Mount, source string) bool {
	return slices.ContainsFunc(mounts, func(m Mount) bool { return m.Source == source })
}

func ValidCommand(command string) bool {
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
	if filepath.IsAbs(command) || strings.HasPrefix(command, "/") {
		return true
	}
	return CommandPattern.MatchString(command)
}

// validShellCommand admits a bare shell name for a bounded-cli command plan.
// Registration is still gated by the operator allowlist
// (HUB_CLI_ALLOWLIST/user_commands) — this only lifts the structural deny so
// an operator can expose a deliberate `sh -c` cell surface.
func validShellCommand(command string) bool {
	if strings.Contains(command, "/") {
		return false
	}
	switch strings.TrimSuffix(strings.ToLower(command), ".exe") {
	case "sh", "bash", "zsh", "cmd", "powershell", "pwsh":
		return CommandPattern.MatchString(command)
	}
	return false
}

func validCLIFlag(flag string) bool {
	if !strings.HasPrefix(flag, "--") || len(flag) < 3 || len(flag) > 64 {
		return false
	}
	return CommandPattern.MatchString(strings.TrimPrefix(flag, "--"))
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
