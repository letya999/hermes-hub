package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type BackendResult struct {
	Text       string
	Content    []mcp.Content
	Structured any
	IsError    bool
	// Receipt is the provider's own mutation proof (for example a Google event
	// identity or a Slack channel plus message timestamp). It is metadata only
	// and never carries a credential value.
	Receipt string
}

// ToolBackend is deliberately narrower than an MCP server: the gateway owns
// projection and authorization, while the backend only executes one already
// resolved tool.
type ToolBackend interface {
	Call(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error)
}

type EnvBackend interface {
	CallEnv(context.Context, EffectiveBinding, ToolSpec, map[string]any, map[string]string) (BackendResult, error)
}

// CredentialEgressHosts extracts host[:port] entries from HTTPS URLs found in
// injected credential values (for example a self-hosted API base URL collected
// by a credential form). The workload egress ACL is fixed at definition time,
// so connector endpoints supplied as credentials would otherwise be denied.
// Non-secret *_ENDPOINT / *_ENDPOINT_<n> values carry bare host:port targets
// (database servers, SSH bastions); the same value drives both the Squid ACL
// entry and any companion TCP forward bound to that environment variable.
func CredentialEgressHosts(env map[string]string) []string {
	hosts := make([]string, 0, len(env))
	for name, value := range env {
		if SecretName(name) {
			continue
		}
		if strings.HasSuffix(name, "_ENDPOINT") || strings.Contains(name, "_ENDPOINT_") {
			host, port, err := net.SplitHostPort(strings.TrimSpace(value))
			if err != nil || !endpointHostValid(host) {
				continue
			}
			portNum, err := strconv.Atoi(port)
			if err != nil || portNum < 1 || portNum > 65535 {
				continue
			}
			hosts = append(hosts, strings.ToLower(host)+":"+port)
			continue
		}
		if !strings.HasSuffix(name, "_URL") {
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host == "" || !strings.Contains(host, ".") {
			continue
		}
		if port := parsed.Port(); port != "" {
			host += ":" + port
		}
		hosts = append(hosts, host)
	}
	return hosts
}

// endpointHostValid accepts dotted hostnames and IPv4 literals, the same
// shapes the Squid dstdomain ACL understands; bare single-label names and
// IPv6 literals stay denied.
func endpointHostValid(host string) bool {
	if host == "" || strings.ContainsAny(host, " \t\r\n/@") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4() != nil
	}
	return HostPattern.MatchString(host) && strings.Contains(host, ".")
}

func MergeEgressHosts(egress []string, hosts []string) []string {
	if len(hosts) == 0 {
		return egress
	}
	seen := make(map[string]bool, len(egress)+len(hosts))
	merged := make([]string, 0, len(egress)+len(hosts))
	for _, host := range egress {
		host = strings.ToLower(strings.TrimSpace(host))
		if !seen[host] {
			seen[host] = true
			merged = append(merged, host)
		}
	}
	for _, host := range hosts {
		if !seen[host] {
			seen[host] = true
			merged = append(merged, host)
		}
	}
	return merged
}

// ProxyStubValue is the only value a proxy-delivered credential ever shows
// in workload environment: the real value stays with the Credential Broker,
// and the egress proxy resolves the reference at request time.
const ProxyStubValue = "hub-managed"

// ProxyGrant is one egress-proxy plan entry: which env stub the workload
// sees, the credential reference the proxy resolves, the allowlisted hosts
// the real value may be injected toward, and the value plus prefix the
// cred-proxy writes into its rules file. Value travels the same trusted
// plan channel as CliBrokeredCred.Value — never container env, argv, mounts
// or receipts.
type ProxyGrant struct {
	EnvName   string   `json:"env_name"`
	Reference string   `json:"reference"`
	Hosts     []string `json:"hosts"`
	Prefix    string   `json:"prefix,omitempty"`
	Value     string   `json:"value"`
}

type CredentialInjection struct {
	Environment map[string]string
	Mounts      []Mount
	Proxy       []ProxyGrant
	Cleanup     func() error
	// Checkpoint is called only after the backend has successfully quiesced
	// a workload using Broker state. Cleanup on a failed call never snapshots.
	Checkpoint func() error
}

type CredentialInjector func(context.Context, EffectiveBinding) (CredentialInjection, error)

// RoutingBackend sends bounded CLI calls to the CLI runner, provider API calls
// to the in-process official REST data plane and MCP transports to the private
// ToolHive/vMCP adapter.
type RoutingBackend struct {
	MCP      ToolBackend
	CLI      ToolBackend
	Provider ToolBackend
	// Agent serves the in-process hub agent-tools executor. It stays nil until
	// a host entrypoint wires one, and dispatch fails closed without it.
	Agent ToolBackend
}

func (b RoutingBackend) Call(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any) (BackendResult, error) {
	return b.CallEnv(ctx, effective, tool, arguments, nil)
}

func (b RoutingBackend) CallEnv(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, environment map[string]string) (BackendResult, error) {
	var backend ToolBackend
	switch effective.Definition.Transport {
	case BoundedCLI:
		backend = b.CLI
	case ProviderAPI:
		backend = b.Provider
	case AgentTools:
		backend = b.Agent
	default:
		backend = b.MCP
	}
	if backend == nil {
		if effective.Definition.Transport == BoundedCLI || effective.Definition.Transport == ProviderAPI || effective.Definition.Transport == AgentTools {
			return BackendResult{}, ErrIsolation
		}
		return BackendResult{}, fmt.Errorf("%w: backend connection", ErrInvalid)
	}
	if caller, ok := backend.(EnvBackend); ok {
		return caller.CallEnv(ctx, effective, tool, arguments, environment)
	}
	return backend.Call(ctx, effective, tool, arguments)
}

func CliInputSchema(tool ToolSpec) map[string]any {
	properties := map[string]any{}
	required := make([]string, 0)
	for _, argument := range tool.Arguments {
		properties[argument.Name] = map[string]any{"type": argument.Type}
		if argument.Required {
			required = append(required, argument.Name)
		}
	}
	schema := map[string]any{"type": "object", "additionalProperties": len(tool.Arguments) == 0}
	if len(properties) > 0 {
		schema["properties"] = properties
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// ValidateBackendEndpoint accepts only a private ToolHive/vMCP address. A
// public URL belongs in an immutable definition, not mutable connection
// metadata, and must not become an SSRF primitive in the gateway.
func ValidateBackendEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: private MCP backend URL", ErrInvalid)
	}
	host := strings.ToLower(u.Hostname())
	// host.docker.internal is a Docker-reserved name for the host gateway —
	// private by construction like localhost, and unresolvable without Docker
	// Desktop's DNS (Linux CI hosts), so it must not rely on LookupIP.
	if host == "localhost" || host == "host.docker.internal" || host == "toolhub" || host == "toolhive" || host == "vmcp" || host == "workload-controller" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		return nil
	}
	// Compose-style service names are operator-configured, not user input; they
	// qualify only when every resolved address is loopback or private.
	if ips, err := net.LookupIP(host); err == nil && len(ips) > 0 {
		for _, ip := range ips {
			if !ip.IsLoopback() && !ip.IsPrivate() {
				return fmt.Errorf("%w: backend URL must be private", ErrInvalid)
			}
		}
		return nil
	}
	return fmt.Errorf("%w: backend URL must be private", ErrInvalid)
}

// MCPBackend speaks only MCP over the private endpoint; ToolHive remains the
// workload executor and vMCP remains the aggregator.
type MCPBackend struct {
	HTTPClient        *http.Client
	Token             string
	Root              string
	Admission         WorkloadAdmission
	AdmissionVerifier func(context.Context, EffectiveBinding) (AdmissionReceipt, error)
	AdmissionRelease  func(context.Context, string) error
}

// WorkloadAdmission is the narrow hand-off to the external ToolHive/Docker
// controller. The gateway owns identity and current catalog state; the
// controller owns actual CPU, memory, PID, filesystem and egress enforcement.
type WorkloadAdmission func(context.Context, EffectiveBinding) error

func (b MCPBackend) EnsureReady(ctx context.Context, effective EffectiveBinding, environment map[string]string) (readyErr error) {
	if effective.Definition.Transport == RemoteMCP && remoteDefinitionEndpoint(effective) {
		return b.remoteReady(ctx, effective, environment)
	}
	if effective.Definition.Transport != ContainerMCP {
		return nil
	}
	source := effective.Definition.Source
	prepared, matched, err := PreparedForSource(ArtifactSource{Repository: source.Repository, Subfolder: source.Subfolder, CommitSHA: source.CommitSHA})
	if err != nil {
		return err
	}
	if a := effective.Definition.OAuth; a != nil {
		prepared.ProbeTool = a.ProbeTool
		prepared.ProbeArgs = a.ProbeArgs
		prepared.ProbeTools = nil
		matched = true
	}
	if matched && (prepared.ProbeTool != "" || len(prepared.ProbeTools) > 0) {
		probeName := prepared.ProbeTool
		probeArgs := prepared.ProbeArgs
		for _, candidate := range prepared.ProbeTools {
			if environment[candidate.Env] != "" {
				probeName = candidate.Tool
				probeArgs = candidate.Args
				break
			}
		}
		for _, tool := range effective.Definition.Tools {
			if tool.Name != probeName || probeName == "" {
				continue
			}
			if tool.Effect != ReadEffect {
				return fmt.Errorf("%w: readiness probe must be read-only", ErrUnauthorized)
			}
			result, probeErr := b.CallEnv(ctx, effective, tool, probeArgs, environment)
			if probeErr == nil && result.IsError {
				detail := strings.TrimSpace(result.Text)
				if runes := []rune(detail); len(runes) > 256 {
					detail = string(runes[:256])
				}
				if detail == "" {
					probeErr = fmt.Errorf("%w: provider read probe failed", ErrUnauthorized)
				} else {
					probeErr = fmt.Errorf("%w: provider read probe failed: %s", ErrUnauthorized, detail)
				}
			}
			if probeErr != nil && len(effective.CredentialMounts) == 0 && b.AdmissionRelease != nil {
				probeErr = errors.Join(probeErr, b.AdmissionRelease(context.Background(), effective.WorkloadID))
			}
			return probeErr
		}
		return fmt.Errorf("%w: reviewed provider read probe is missing", ErrStale)
	}
	if len(effective.CredentialMounts) > 0 {
		if b.AdmissionRelease == nil {
			return fmt.Errorf("%w: file credential admission release is not configured", ErrIsolation)
		}
		wipe, err := WriteAuthorizedFiles(ctx, b.Root, effective, environment)
		if err != nil {
			return err
		}
		defer func() { readyErr = errors.Join(readyErr, wipe()) }()
		receipt, err := b.Admit(ctx, effective, environment)
		if err != nil {
			return fmt.Errorf("%w: workload controller: %v", ErrIsolation, err)
		}
		validationErr := receipt.Validate(effective)
		releaseErr := b.AdmissionRelease(context.Background(), effective.WorkloadID)
		if validationErr != nil || releaseErr != nil {
			return fmt.Errorf("%w: file credential readiness: %w", ErrIsolation, errors.Join(validationErr, releaseErr))
		}
		return nil
	}
	wipe, err := WriteAuthorizedFiles(ctx, b.Root, effective, environment)
	if err != nil {
		return err
	}
	defer func() { readyErr = errors.Join(readyErr, wipe()) }()
	// Keep the admission plan and the local receipt check on the same extended
	// egress view; admit applies the identical merge for the submitted plan.
	effective.Definition.Execution.Egress = MergeEgressHosts(effective.Definition.Execution.Egress, CredentialEgressHosts(environment))
	receipt, err := b.Admit(ctx, effective, environment)
	if err != nil {
		return fmt.Errorf("%w: workload controller: %v", ErrIsolation, err)
	}
	if err := receipt.Validate(effective); err != nil {
		return fmt.Errorf("%w: workload controller: %v", ErrIsolation, err)
	}
	return nil
}

func restoreAuthorizedFile(workspacePath string, previous []byte, hadPrevious bool) {
	if workspacePath == "" {
		return
	}
	path := filepath.Join(workspacePath, "credentials.env")
	if hadPrevious {
		_ = os.WriteFile(path, previous, 0600)
	} else {
		_ = os.Remove(path)
	}
}

func (b MCPBackend) Admit(ctx context.Context, effective EffectiveBinding, environment map[string]string) (AdmissionReceipt, error) {
	// Credential-supplied HTTPS endpoints (for example a self-hosted provider
	// URL) arrive after the definition digest is frozen, so every admission
	// extends the egress allowlist from the same injected environment. Keeping
	// this inside admit makes ready-time and per-call plans identical, which
	// the controller receipt check requires.
	effective.Definition.Execution.Egress = MergeEgressHosts(effective.Definition.Execution.Egress, CredentialEgressHosts(environment))
	if err := validateToolHivePolicy(effective.Definition); err != nil {
		return AdmissionReceipt{}, err
	}
	if b.AdmissionVerifier == nil && b.Admission == nil {
		return AdmissionReceipt{}, ErrIsolation
	}
	var receipt AdmissionReceipt
	if b.AdmissionVerifier != nil {
		var err error
		receipt, err = b.AdmissionVerifier(ctx, effective)
		if err != nil {
			return AdmissionReceipt{}, err
		}
		if err := receipt.Validate(effective); err != nil {
			if b.AdmissionRelease != nil {
				err = errors.Join(err, b.AdmissionRelease(context.Background(), effective.WorkloadID))
			}
			return AdmissionReceipt{}, err
		}
	} else {
		if err := b.Admission(ctx, effective); err != nil {
			return AdmissionReceipt{}, err
		}
		return AdmissionReceipt{}, fmt.Errorf("%w: workload controller returned no enforcement proof", ErrIsolation)
	}
	return receipt, nil
}

func validateToolHivePolicy(definition ToolDefinition) error {
	if definition.Transport != ContainerMCP {
		return nil
	}
	return definition.Validate()
}

func (b MCPBackend) Call(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any) (BackendResult, error) {
	return b.CallEnv(ctx, effective, tool, arguments, nil)
}

func (b MCPBackend) CallEnv(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any, environment map[string]string) (BackendResult, error) {
	if effective.Definition.Transport == RemoteMCP && strings.HasPrefix(effective.Definition.DefinitionID, "google-workspace-") {
		return (GoogleWorkspaceBackend{HTTP: b.HTTPClient}).CallEnv(ctx, effective, tool, arguments, environment)
	}
	if effective.Definition.Transport == RemoteMCP && remoteDefinitionEndpoint(effective) {
		return b.CallRemoteMCP(ctx, effective, tool, arguments, environment)
	}
	if effective.Connection == nil && effective.Definition.Transport != ContainerMCP {
		return BackendResult{}, fmt.Errorf("%w: backend connection", ErrInvalid)
	}
	telegram := strings.HasPrefix(effective.Definition.DefinitionID, "telegram-account-")
	if telegram && (effective.Definition.Transport != ContainerMCP || environment["TELEGRAM_ACCOUNT_ID"] == "" || environment["TELEGRAM_ACCOUNT_ID"] != effective.Connection.Metadata["telegram_account"] || !TelegramTool(tool.Name, tool.Effect) || (tool.Effect == WriteEffect && environment["TELEGRAM_WRITE"] != "true")) {
		return BackendResult{}, ErrUnauthorized
	}
	if len(effective.CredentialMounts) > 0 && b.AdmissionRelease == nil {
		return BackendResult{}, fmt.Errorf("%w: file credential admission release is not configured", ErrIsolation)
	}
	var wipe func() error
	if len(environment) > 0 {
		var err error
		wipe, err = WriteAuthorizedFiles(ctx, b.Root, effective, environment)
		if err != nil {
			return BackendResult{}, err
		}
		if wipe != nil {
			defer wipe()
		}
	}
	var receipt AdmissionReceipt
	if effective.Definition.Transport == ContainerMCP {
		var err error
		receipt, err = b.Admit(ctx, effective, environment)
		if err != nil {
			return BackendResult{}, fmt.Errorf("%w: workload controller: %v", ErrIsolation, err)
		}
	}
	released := false
	release := func() error {
		if released || len(effective.CredentialMounts) == 0 {
			return nil
		}
		if err := b.AdmissionRelease(context.Background(), receipt.WorkloadID); err != nil {
			return err
		}
		released = true
		return nil
	}
	defer func() { _ = release() }()
	endpoint := ""
	if effective.Connection != nil {
		endpoint = effective.Connection.Metadata["mcp_endpoint"]
	}
	if endpoint == "" {
		endpoint = receipt.Endpoint
	}
	if err := ValidateBackendEndpoint(endpoint); err != nil {
		return BackendResult{}, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hermes-toolhub-backend", Version: "0.2.0"}, nil)
	httpClient := b.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	clientCopy := *httpClient
	baseTransport := http.RoundTripper(http.DefaultTransport)
	if httpClient.Transport != nil {
		baseTransport = httpClient.Transport
	}
	// Serena 1.5.x and other established MCP servers speak the latest legacy
	// protocol. ToolHub only needs request/response calls, so pin this backend
	// hop to that version instead of letting the SDK's newer handshake break
	// otherwise compatible servers.
	baseTransport = LegacyMCPRoundTripper{Base: baseTransport}
	clientCopy.Transport = bearerRoundTripper{Base: baseTransport, token: b.Token}
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &clientCopy, DisableStandaloneSSE: true}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return BackendResult{}, err
	}
	// A nil map marshals as arguments:null and strict servers (the Node MCP
	// SDK) reject the literal — send an empty record for no-arg calls.
	if arguments == nil {
		arguments = map[string]any{}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: arguments})
	closeErr := session.Close()
	if err != nil {
		return BackendResult{}, err
	}
	if closeErr != nil {
		return BackendResult{}, closeErr
	}
	if err := release(); err != nil {
		return BackendResult{}, fmt.Errorf("%w: file credential workload release", ErrIsolation)
	}
	var text strings.Builder
	for _, content := range result.Content {
		if value, ok := content.(*mcp.TextContent); ok {
			text.WriteString(value.Text)
		}
	}
	out := BackendResult{Text: text.String(), Content: result.Content, Structured: result.StructuredContent, IsError: result.IsError}
	if telegram && tool.Effect == WriteEffect && !result.IsError {
		raw, err := json.Marshal(result.StructuredContent)
		var receipt struct {
			Receipt   string `json:"receipt"`
			PeerID    int64  `json:"peer_id"`
			MessageID int64  `json:"message_id"`
			PTS       int64  `json:"pts"`
		}
		if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.PeerID <= 0 || receipt.MessageID <= 0 {
			return BackendResult{}, fmt.Errorf("telegram mutation returned no provider receipt")
		}
		if fmt.Sprint(arguments["peer_id"]) != fmt.Sprint(receipt.PeerID) || (tool.Name == "delete_message" && fmt.Sprint(arguments["target_id"]) != fmt.Sprint(receipt.MessageID)) {
			return BackendResult{}, ErrInvalid
		}
		want := fmt.Sprintf("%s:%d:%d", environment["TELEGRAM_ACCOUNT_ID"], receipt.PeerID, receipt.MessageID)
		if tool.Name == "delete_message" {
			if receipt.PTS <= 0 {
				return BackendResult{}, ErrInvalid
			}
			want += fmt.Sprintf(":pts:%d", receipt.PTS)
		}
		if receipt.Receipt != want {
			return BackendResult{}, ErrInvalid
		}
		out.Receipt = want
	}
	return out, nil
}

type LegacyMCPRoundTripper struct{ Base http.RoundTripper }

func (t LegacyMCPRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyRequest.Header.Set("MCP-Protocol-Version", LegacyMCPVersion)
	return t.Base.RoundTrip(copyRequest)
}

type bearerRoundTripper struct {
	Base  http.RoundTripper
	token string
}

func (c bearerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	if c.token != "" {
		copyRequest.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.Base.RoundTrip(copyRequest)
}
