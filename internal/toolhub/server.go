package toolhub

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
	"github.com/letya999/hermes-hub/internal/serviceauth"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type EndpointConfig struct {
	Listen string
	Token  string
	Auth   identity.Envelope
	// TokensFile is an optional JSON object mapping additional bearer tokens to
	// identity envelopes, one per secondary space's runtime token. The single
	// deployed ToolHub authenticates every principal this way; there is no
	// per-user ToolHub instance.
	TokensFile     string
	Backend        api.ToolBackend
	RecipeCatalogs []api.RecipeCatalog
	BrokerControl  *credentialbroker.Config
	BrokerRuntime  *credentialbroker.Config
	Release        func(context.Context, api.ControllerRelease) error
	// CLIRelease drops bounded-cli cells by principal/binding/job selector.
	CLIRelease func(context.Context, api.CliReleaseRequest) error
}

func scopedLabelOps(project string, query func(context.Context, map[string]string) (map[string][]string, error), purge func(context.Context, string, string, map[string]string) error) (func(context.Context, map[string]string) (map[string][]string, error), func(context.Context, string, string, map[string]string) error) {
	return func(ctx context.Context, selector map[string]string) (map[string][]string, error) {
			scoped := map[string]string{}
			for key, value := range selector {
				scoped[key] = value
			}
			scoped["hermes-hub.scope"] = project
			return query(ctx, scoped)
		}, func(ctx context.Context, kind, name string, selector map[string]string) error {
			scoped := map[string]string{}
			for key, value := range selector {
				scoped[key] = value
			}
			scoped["hermes-hub.scope"] = project
			return purge(ctx, kind, name, scoped)
		}
}

// scopedLabelScan pins the full-label scan to this deployment's scope the
// same way scopedLabelOps pins query and purge — the reconciler's orphan
// pass only ever sees objects this deployment labelled.
func scopedLabelScan(project string, scan func(context.Context, map[string]string) ([]api.LabeledResource, error)) func(context.Context, map[string]string) ([]api.LabeledResource, error) {
	return func(ctx context.Context, selector map[string]string) ([]api.LabeledResource, error) {
		scoped := map[string]string{}
		for key, value := range selector {
			scoped[key] = value
		}
		scoped["hermes-hub.scope"] = project
		return scan(ctx, scoped)
	}
}

// surfaceProbe is the production live-surface seam: remote MCP connections
// are re-listed through the SSRF-guarded probe carrying the stored
// credential env; every other transport replays the admitted definition
// surface — the drift pin was minted from it, so a definition rewrite still
// trips the check and an unobservable endpoint skips rather than wedging
// the pass.
func surfaceProbe(secrets credstore.Backend, store *Store, probe func(context.Context, api.ToolDefinition, map[string]string) ([]api.ToolSpec, error)) func(context.Context, api.Connection, api.ToolDefinition) ([]api.ToolSpec, error) {
	return func(ctx context.Context, connection api.Connection, definition api.ToolDefinition) ([]api.ToolSpec, error) {
		if definition.Transport != api.RemoteMCP {
			return definition.Tools, nil
		}
		var env map[string]string
		if secrets != nil && connection.CredentialRefID != "" {
			if ref, ok := store.credentialFor(connection.CredentialRefID); ok && ref.Locator != "" {
				if values, err := secrets.Get(ref.Locator, store.credentialOwner(connection.ConnectionID)); err == nil {
					env = values
				}
			}
		}
		tools, err := probe(ctx, definition, env)
		if err != nil {
			return definition.Tools, nil
		}
		return tools, nil
	}
}

// wrapKeepRelease adapts the controller releaser to the backend's keep-state
// release hook: backend probe cleanup never purges state volumes.
func wrapKeepRelease(release func(context.Context, api.ControllerRelease) error) func(context.Context, string) error {
	if release == nil {
		return nil
	}
	return func(ctx context.Context, workloadID string) error {
		return release(ctx, api.ControllerRelease{WorkloadID: workloadID})
	}
}

func EndpointConfigFromEnv() (EndpointConfig, error) {
	tokenEnv := os.Getenv("HUB_TOOLHUB_TOKEN_ENV")
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH" // #nosec G101 -- this is an environment variable name, never a credential value.
	}
	if !api.CredentialPattern.MatchString(tokenEnv) {
		return EndpointConfig{}, fmt.Errorf("invalid ToolHub token environment name")
	}
	token := os.Getenv(tokenEnv)
	if token == "" {
		return EndpointConfig{}, fmt.Errorf("ToolHub token environment %s is empty", tokenEnv)
	}
	principal := os.Getenv("HUB_USER_ID")
	if principal == "" {
		return EndpointConfig{}, fmt.Errorf("HUB_USER_ID is required")
	}
	contextID := os.Getenv("HUB_CONTEXT_ID")
	if contextID == "" {
		contextID = principal
	}
	runtimeID := os.Getenv("HUB_RUNTIME_ID")
	if runtimeID == "" {
		runtimeID = principal
	}
	policy := os.Getenv("HUB_POLICY_VERSION")
	if policy == "" {
		policy = "policy-1"
	}
	capabilityMode := os.Getenv("HUB_CAPABILITY_MODE")
	if capabilityMode != "" && capabilityMode != "managed" {
		return EndpointConfig{}, fmt.Errorf("invalid capability mode")
	}
	managed := capabilityMode == "managed"
	var generation uint64
	if managed {
		var parseErr error
		generation, parseErr = strconv.ParseUint(os.Getenv("HUB_CAPABILITY_GENERATION"), 10, 64)
		if parseErr != nil || generation == 0 || !identity.ValidID(os.Getenv("HUB_CAPABILITY_PROFILE_ID")) ||
			(os.Getenv("HUB_CAPABILITY_ENVIRONMENT") != "dev" && os.Getenv("HUB_CAPABILITY_ENVIRONMENT") != "prod") {
			return EndpointConfig{}, fmt.Errorf("managed ToolHub identity is incomplete")
		}
	}
	admission, err := api.ControllerAdmissionVerifierFromEnv()
	if err != nil {
		return EndpointConfig{}, fmt.Errorf("ToolHive admission: %w", err)
	}
	auth := identity.Envelope{
		Schema:             identity.Schema,
		PrincipalID:        principal,
		ExternalIdentityID: api.EnvOr("HUB_EXTERNAL_ID", principal),
		ContextID:          contextID,
		RuntimeID:          runtimeID,
		ConversationID:     api.EnvOr("HUB_CONVERSATION_ID", "toolhub"),
		DeliveryTargetID:   api.EnvOr("HUB_DELIVERY_TARGET_ID", "toolhub"),
		PolicyVersion:      policy,
	}
	if managed {
		auth.CapabilityProfile, auth.Environment, auth.Generation = os.Getenv("HUB_CAPABILITY_PROFILE_ID"), os.Getenv("HUB_CAPABILITY_ENVIRONMENT"), generation
	}
	controlBroker, err := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_CONTROL_")
	if err != nil {
		return EndpointConfig{}, err
	}
	runtimeBroker, err := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_RUNTIME_")
	if err != nil {
		return EndpointConfig{}, err
	}
	if controlBroker.Enabled() != runtimeBroker.Enabled() {
		return EndpointConfig{}, fmt.Errorf("ToolHub requires both Credential Broker control and runtime clients")
	}
	release, err := api.ControllerAdmissionReleaserFromEnv()
	if err != nil {
		return EndpointConfig{}, fmt.Errorf("ToolHive release: %w", err)
	}
	cliRelease, err := api.CLIReleaseFromEnv()
	if err != nil {
		return EndpointConfig{}, fmt.Errorf("cli release: %w", err)
	}
	catalogs, err := api.RecipeCatalogsFromEnv()
	if err != nil {
		return EndpointConfig{}, err
	}
	cli, err := api.CLIRunnerFromEnv()
	if err != nil {
		return EndpointConfig{}, fmt.Errorf("bounded CLI: %w", err)
	}
	return EndpointConfig{
		Listen:     api.EnvOr("HUB_TOOLHUB_LISTEN", "127.0.0.1:8090"),
		Token:      token,
		Auth:       auth,
		TokensFile: os.Getenv("HUB_TOOLHUB_TOKENS_FILE"),
		Backend: api.RoutingBackend{
			Provider: api.PersonalProviderBackend{},
			MCP:      api.MCPBackend{Token: os.Getenv("TOOLHIVE_VMCP_TOKEN"), AdmissionVerifier: admission, AdmissionRelease: wrapKeepRelease(release), Root: api.EnvOr("HUB_STATE", "/state")},
			CLI:      cli,
			Agent:    agentBackendFromEnv(),
		},
		RecipeCatalogs: catalogs,
		BrokerControl: func() *credentialbroker.Config {
			if controlBroker.Enabled() {
				return &controlBroker
			}
			return nil
		}(),
		Release:    release,
		CLIRelease: cliRelease,
		BrokerRuntime: func() *credentialbroker.Config {
			if runtimeBroker.Enabled() {
				return &runtimeBroker
			}
			return nil
		}(),
	}, nil
}

func NewEndpointHandler(config EndpointConfig, store *Store) (http.Handler, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: nil ToolHub store", api.ErrInvalid)
	}
	if len(config.Token) < 32 || strings.ContainsAny(config.Token, "\r\n") {
		return nil, fmt.Errorf("%w: ToolHub token", api.ErrInvalid)
	}
	if err := config.Auth.Validate(config.Auth.PrincipalID, config.Auth.ContextID, config.Auth.RuntimeID, config.Auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: ToolHub identity: %v", api.ErrInvalid, err)
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("%w: nil ToolHub backend", api.ErrInvalid)
	}
	if routing, ok := config.Backend.(api.RoutingBackend); ok {
		if agent, ok := routing.Agent.(api.AgentExecBackend); ok && agent.Fence == nil {
			// Export and apply paths re-verify the admitted authority against
			// the live store so a mid-flight revocation stops the write.
			agent.Fence = func(_ context.Context, effective api.EffectiveBinding) error {
				return store.ReverifyEffective(effective)
			}
			routing.Agent = agent
			config.Backend = routing
		}
	}
	if (config.BrokerControl == nil) != (config.BrokerRuntime == nil) {
		return nil, fmt.Errorf("%w: Credential Broker control/runtime configuration must be paired", api.ErrInvalid)
	}
	tokens := map[string]identity.Envelope{config.Token: config.Auth}
	if config.TokensFile != "" {
		extra, err := loadTokenEnvelopes(config.TokensFile)
		if err != nil {
			return nil, err
		}
		for token, auth := range extra {
			if _, dup := tokens[token]; dup {
				return nil, fmt.Errorf("%w: duplicate ToolHub token", api.ErrInvalid)
			}
			tokens[token] = auth
		}
	}
	gateway := &Gateway{
		Store: store, Backend: config.Backend, Tokens: tokens,
		primaryToken: config.Token, tokensFile: config.TokensFile,
		DisableLocalhostProtection: nonLoopbackListen(config.Listen),
		// Rendered as the supervisor bearer so communication-hub can list a
		// principal's connectors without holding that principal's token.
		ControlToken: strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_AUTH")),
		// Tool governance sits beside the registry; the env pin exists for
		// deployments that keep the policy document elsewhere.
		Governance: NewGovernanceStore(api.EnvOr("HUB_TOOL_GOVERNANCE", GovernanceStorePath(os.Getenv("HUB_TOOLHUB_STORE")))),
		// Job-end cell release shares the controller release channel.
		CLIRelease: config.CLIRelease,
	}
	var secrets credstore.Backend
	var injector api.CredentialInjector
	var err error
	secrets, injector, err = credentialServicesFromEnv()
	if err != nil {
		return nil, err
	}
	gateway.Injector = mergeCredentialInjectors(injector, brokerRuntimeInjector(config.BrokerControl, config.BrokerRuntime), config.BrokerControl != nil)
	control := &ControlPlane{Store: store, Secrets: secrets, Listen: config.Listen, WorkloadRoot: api.EnvOr("HUB_STATE", ""), DiagnosticsDir: api.EnvOr("HUB_DIAGNOSTICS_DIR", ""), Broker: config.BrokerControl, BrokerRuntime: config.BrokerRuntime, RecipeCatalogs: config.RecipeCatalogs, Release: config.Release, CLIRelease: config.CLIRelease}
	if project := os.Getenv("HUB_DOCKER_SCOPE"); project != "" {
		control.LabelQuery, control.LabelPurge = scopedLabelOps(project, api.DockerLabelQuery, api.DockerLabelPurge)
		control.LabelScan = scopedLabelScan(project, api.DockerLabelScan)
	}
	if ready := readinessBackend(config.Backend); ready != nil {
		control.Ready = func(ctx context.Context, effective api.EffectiveBinding) (readyErr error) {
			if gateway.Injector == nil {
				return ready(ctx, effective, nil)
			}
			injection, err := gateway.Injector(ctx, effective)
			if err != nil {
				return err
			}
			if injection.Cleanup != nil {
				defer func() { readyErr = errors.Join(readyErr, injection.Cleanup()) }()
			}
			effective.CredentialMounts = append([]api.Mount(nil), injection.Mounts...)
			if err := ready(ctx, effective, injection.Environment); err != nil {
				return err
			}
			if injection.Checkpoint != nil {
				return injection.Checkpoint()
			}
			return nil
		}
	}
	if store.Reconnect == nil {
		store.Reconnect = &ReconnectController{Store: store, Auth: config.Auth, OnChange: func(change api.ProjectionChange) error {
			return gateway.RefreshProjection()
		}}
	}
	control.SourceResolver = api.ResolveGitHubSource
	control.FormOrigin = api.FormOriginForListen(config.Listen)
	if public := api.PublicOrigin(); public != "" {
		control.FormOrigin = public
	}
	artifacts, seccomp := api.ControlArtifactPaths(control.WorkloadRoot)
	control.Reviewer = api.DefaultSourceReviewerWithCatalogs(artifacts, seccomp, control.RecipeCatalogs)
	cliArtifacts, err := CLIArtifactPipelineFromEnv()
	if err != nil {
		return nil, fmt.Errorf("cli artifact pipeline: %w", err)
	}
	control.CLIArtifacts = cliArtifacts
	control.AdmitWithCredentials = func(ctx context.Context, definition api.ToolDefinition, secrets map[string]string) (api.ToolDefinition, error) {
		if definition.Transport == api.RemoteMCP {
			return admitRemoteDefinition(ctx, definition, secrets)
		}
		return api.AdmitWithSubmittedCredentials(ctx, artifacts, definition, secrets)
	}
	control.SurfaceProbe = surfaceProbe(secrets, store, func(ctx context.Context, definition api.ToolDefinition, env map[string]string) ([]api.ToolSpec, error) {
		probe, err := api.ProbeRemoteMCP(ctx, definition, env)
		if err != nil {
			return nil, err
		}
		return probe.Tools, nil
	})
	control.OAuth = oauth.NewBroker(secrets, []string{control.origin() + "/oauth/callback"})
	gateway.OAuthRefresh = remoteOAuthRefresh(secrets, control.OAuth, store)
	control.Injector = gateway.Injector
	control.PrepareDone = gateway.notifyPrepareDone
	control.Governance = gateway.Governance
	gateway.Control = control
	if path := os.Getenv("HUB_AUDIT_LEDGER"); path != "" {
		ledger, err := audit.Open(path)
		if err != nil {
			return nil, err
		}
		gateway.AuditWrite = func(event string, fields map[string]string) error {
			record := audit.NewEvent("tool-call", fields["principal_id"], fields["outcome"])
			record.ContextID = fields["context_id"]
			record.RuntimeID = fields["runtime_id"]
			record.ConnectionID = fields["connection_id"]
			record.PolicyRevision = fields["policy_version"]
			record.JobID = fields["job_id"]
			record.HermesRunID = fields["hermes_run_id"]
			record.ToolCallID = fields["tool_call_id"]
			record.CorrelationID = fields["correlation_id"]
			record.Receipt = fields["receipt"]
			record.CapabilityID = fields["capability_id"]
			record.CapabilityProfile = fields["capability_profile"]
			record.ImplementationDigest = fields["implementation_digest"]
			record.Environment = fields["environment"]
			record.Generation, _ = strconv.ParseUint(fields["generation"], 10, 64)
			record.CapabilityPolicyRevision, _ = strconv.ParseUint(fields["capability_policy_revision"], 10, 64)
			record.CapabilityProfileRevision, _ = strconv.ParseUint(fields["capability_profile_revision"], 10, 64)
			if fields["credential_revision"] != "" {
				n, _ := strconv.ParseUint(fields["credential_revision"], 10, 64)
				record.CredentialRevision = n
			}
			if fields["projection_rev"] != "" {
				n, _ := strconv.ParseUint(fields["projection_rev"], 10, 64)
				record.ProjectionRevision = n
			}
			return ledger.Append(record)
		}
	}
	handler, err := gateway.Handler()
	if err != nil {
		return nil, err
	}
	return &projectionEndpoint{Handler: handler, gateway: gateway}, nil
}

type projectionEndpoint struct {
	http.Handler
	gateway *Gateway
}

func (e *projectionEndpoint) ResumeRemovals(ctx context.Context) error {
	return e.gateway.Control.ResumeRemovals(ctx)
}

// Reconcile runs one convergence pass: orphan purge, credential liveness,
// recipe/credential-recipe drift and the live surface check — the single
// production reconciler #285 schedules beside the removal retry loop.
func (e *projectionEndpoint) Reconcile(ctx context.Context) error {
	return e.gateway.Control.NewReconciler().ScanOnce(ctx)
}

// PublicHandler is the opt-in public listener surface — only human-action
// routes, no control or gateway paths.
func (e *projectionEndpoint) PublicHandler() http.Handler {
	return e.gateway.PublicMux()
}

func (h *projectionEndpoint) RefreshProjection() error { return h.gateway.RefreshProjection() }

type workloadReadiness func(context.Context, api.EffectiveBinding, map[string]string) error

func readinessBackend(backend api.ToolBackend) workloadReadiness {
	switch value := backend.(type) {
	case api.MCPBackend:
		return value.EnsureReady
	case *api.MCPBackend:
		if value != nil {
			return value.EnsureReady
		}
	case api.RoutingBackend:
		return readinessBackend(value.MCP)
	case *api.RoutingBackend:
		if value != nil {
			return readinessBackend(value.MCP)
		}
	}
	return nil
}

func nonLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "" || strings.EqualFold(host, "localhost") {
		return host == ""
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// agentBackendFromEnv wires the private agent-tools executor. The channel is
// `docker exec` into the owning runtime container — the only control-plane
// path that never crosses the agent network, so an agent cannot invoke the
// executor or forge capability scopes. Unconfigured stays nil and dispatch
// fails closed.
func agentBackendFromEnv() api.ToolBackend {
	docker := api.EnvOr("HUB_DOCKER_BIN", "docker")
	// The docker socket sees every host container; scope checks keep exec,
	// inspect and lifecycle ops inside this stack's own project.
	api.SetDockerScope(os.Getenv("HUB_DOCKER_SCOPE"), os.Getenv("HUB_DOCKER_AGENT_NET"))
	// Durable executor leases sit next to the ToolHub store: a restart keeps
	// quarantining bindings whose sandbox stop was never confirmed.
	if store := os.Getenv("HUB_TOOLHUB_STORE"); store != "" {
		if err := api.SetScratchLeasePath(filepath.Join(filepath.Dir(store), "executor-leases.json")); err != nil {
			fmt.Fprintf(os.Stderr, "toolhub: scratch lease file disabled: %v\n", err)
		}
	}
	if container := os.Getenv("HUB_AGENT_EXEC_CONTAINER"); container != "" {
		resolve := api.FixedAgentContainer(container)
		return api.AgentExecBackend{Exec: api.DaemonAgentExec([]string{docker}, resolve), Scratch: api.DockerScratchExec([]string{docker}, resolve, api.DockerAgentExec([]string{docker}, resolve))}
	}
	if os.Getenv("HUB_AGENT_EXEC_MODE") == "supervisor" {
		return api.AgentExecBackend{Exec: api.DaemonAgentExec([]string{docker}, api.ManagedAgentContainer), Scratch: api.DockerScratchExec([]string{docker}, api.ManagedAgentContainer, api.DockerAgentExec([]string{docker}, api.ManagedAgentContainer))}
	}
	return nil
}

// loadTokenEnvelopes reads a JSON object mapping additional bearer tokens to
// identity envelopes: {"<token>": {"identity_schema": 1, "principal_id": ...}}.
// The endpoint holds one identity per principal; entries are checked the same
// way as the primary env token before the gateway accepts them.
func loadTokenEnvelopes(path string) (map[string]identity.Envelope, error) {
	entries, err := serviceauth.ReadTokenEnvelopes(path)
	if err != nil {
		return nil, fmt.Errorf("%w: ToolHub tokens file: %v", api.ErrInvalid, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: ToolHub tokens file must map tokens to envelopes", api.ErrInvalid)
	}
	for token := range entries {
		if len(token) < 32 {
			return nil, fmt.Errorf("%w: ToolHub tokens file token", api.ErrInvalid)
		}
	}
	return entries, nil
}

func credentialServicesFromEnv() (credstore.Backend, api.CredentialInjector, error) {
	path := strings.TrimSpace(os.Getenv("HUB_CREDENTIAL_STORE"))
	if path == "" {
		return nil, nil, nil
	}
	backend, err := credstore.Open(credstore.Options{Path: path, KeyFile: os.Getenv("HUB_CREDENTIAL_KEY_FILE")})
	if err != nil {
		return nil, nil, err
	}
	return backend, LocalCredentialInjector(backend), nil
}

// LocalCredentialInjector materializes admitted credentials from the local
// encrypted store. Proxy-delivered inputs emit only the hub-managed stub in
// env; the egress-proxy plan rides along on the injection.
func LocalCredentialInjector(backend credstore.Backend) api.CredentialInjector {
	return func(ctx context.Context, effective api.EffectiveBinding) (api.CredentialInjection, error) {
		env, err := api.DecryptAuthorized(backend, effective)
		if err != nil {
			return api.CredentialInjection{}, err
		}
		return api.CredentialInjection{Environment: env, Proxy: api.ProxyStubPlan(effective, env)}, nil
	}
}
