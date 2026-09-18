package toolhub

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
)

type EndpointConfig struct {
	Listen        string
	Token         string
	Auth          identity.Envelope
	Backend       ToolBackend
	BrokerControl *credentialbroker.Config
	BrokerRuntime *credentialbroker.Config
}

func EndpointConfigFromEnv() (EndpointConfig, error) {
	tokenEnv := os.Getenv("HUB_TOOLHUB_TOKEN_ENV")
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH" // #nosec G101 -- this is an environment variable name, never a credential value.
	}
	if !credentialPattern.MatchString(tokenEnv) {
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
	admission, err := ControllerAdmissionVerifierFromEnv()
	if err != nil {
		return EndpointConfig{}, fmt.Errorf("ToolHive admission: %w", err)
	}
	auth := identity.Envelope{
		Schema:             identity.Schema,
		PrincipalID:        principal,
		ExternalIdentityID: envOr("HUB_EXTERNAL_ID", principal),
		ContextID:          contextID,
		RuntimeID:          runtimeID,
		ConversationID:     envOr("HUB_CONVERSATION_ID", "toolhub"),
		DeliveryTargetID:   envOr("HUB_DELIVERY_TARGET_ID", "toolhub"),
		PolicyVersion:      policy,
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
	release, err := ControllerAdmissionReleaserFromEnv()
	if err != nil {
		return EndpointConfig{}, fmt.Errorf("ToolHive release: %w", err)
	}
	return EndpointConfig{
		Listen: envOr("HUB_TOOLHUB_LISTEN", "127.0.0.1:8090"),
		Token:  token,
		Auth:   auth,
		Backend: RoutingBackend{
			Provider: PersonalProviderBackend{},
			MCP:      MCPBackend{Token: os.Getenv("TOOLHIVE_VMCP_TOKEN"), AdmissionVerifier: admission, AdmissionRelease: release, Root: envOr("HUB_STATE", "/state")},
			CLI:      CLIRunner{Root: envOr("HUB_STATE", "/state")},
		},
		BrokerControl: func() *credentialbroker.Config {
			if controlBroker.Enabled() {
				return &controlBroker
			}
			return nil
		}(),
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
		return nil, fmt.Errorf("%w: nil ToolHub store", ErrInvalid)
	}
	if len(config.Token) < 32 || strings.ContainsAny(config.Token, "\r\n") {
		return nil, fmt.Errorf("%w: ToolHub token", ErrInvalid)
	}
	if err := config.Auth.Validate(config.Auth.PrincipalID, config.Auth.ContextID, config.Auth.RuntimeID, config.Auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: ToolHub identity: %v", ErrInvalid, err)
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("%w: nil ToolHub backend", ErrInvalid)
	}
	if (config.BrokerControl == nil) != (config.BrokerRuntime == nil) {
		return nil, fmt.Errorf("%w: Credential Broker control/runtime configuration must be paired", ErrInvalid)
	}
	gateway := &Gateway{
		Store: store, Backend: config.Backend, Tokens: map[string]identity.Envelope{config.Token: config.Auth},
		DisableLocalhostProtection: nonLoopbackListen(config.Listen),
	}
	var secrets credstore.Backend
	var injector CredentialInjector
	var err error
	if config.BrokerControl == nil {
		secrets, injector, err = credentialServicesFromEnv()
		if err != nil {
			return nil, err
		}
	}
	gateway.Injector = mergeCredentialInjectors(injector, brokerRuntimeInjector(config.BrokerControl, config.BrokerRuntime), config.BrokerControl != nil)
	control := &ControlPlane{Store: store, Secrets: secrets, Listen: config.Listen, WorkloadRoot: envOr("HUB_STATE", ""), Broker: config.BrokerControl}
	if ready := readinessBackend(config.Backend); ready != nil {
		control.Ready = func(ctx context.Context, effective EffectiveBinding) error {
			if gateway.Injector == nil {
				return ready(ctx, effective, nil)
			}
			injection, err := gateway.Injector(ctx, effective)
			if err != nil {
				return err
			}
			if injection.Cleanup != nil {
				defer injection.Cleanup()
			}
			if len(injection.Mounts) > 0 {
				// File deliveries are deliberately one-shot: the per-call path
				// owns the Broker lease and releases the workload after the call.
				return nil
			}
			return ready(ctx, effective, injection.Environment)
		}
	}
	stateRoot := strings.TrimSpace(os.Getenv("HUB_STATE"))
	if store.Reconnect == nil && filepath.IsAbs(stateRoot) {
		store.Reconnect = &ReconnectController{Store: store, Auth: config.Auth, OnChange: func(change ProjectionChange) error {
			return WriteReconnectMarker(stateRoot, change)
		}}
	}
	control.SourceResolver = ResolveGitHubSource
	control.FormOrigin = formOriginForListen(config.Listen)
	artifacts, seccomp := controlArtifactPaths(control.WorkloadRoot)
	control.Reviewer = DefaultSourceReviewer(artifacts, seccomp)
	control.OAuth = oauth.NewBroker(secrets, []string{control.origin() + "/oauth/callback"})
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
	return gateway.Handler()
}

func formOriginForListen(listen string) string {
	listen = strings.TrimRight(strings.TrimSpace(listen), "/")
	if strings.Contains(listen, "://") {
		return listen
	}
	host, port, err := net.SplitHostPort(listen)
	if err == nil && (host == "" || host == "0.0.0.0" || host == "::" || host == "[::]") {
		return "http://127.0.0.1:" + port
	}
	if listen == "" {
		return "http://127.0.0.1"
	}
	return "http://" + listen
}

type workloadReadiness func(context.Context, EffectiveBinding, map[string]string) error

func readinessBackend(backend ToolBackend) workloadReadiness {
	switch value := backend.(type) {
	case MCPBackend:
		return value.EnsureReady
	case *MCPBackend:
		if value != nil {
			return value.EnsureReady
		}
	case RoutingBackend:
		return readinessBackend(value.MCP)
	case *RoutingBackend:
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

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func credentialServicesFromEnv() (credstore.Backend, CredentialInjector, error) {
	path := strings.TrimSpace(os.Getenv("HUB_CREDENTIAL_STORE"))
	if path == "" {
		return nil, nil, nil
	}
	backend, err := credstore.Open(credstore.Options{Path: path, KeyFile: os.Getenv("HUB_CREDENTIAL_KEY_FILE")})
	if err != nil {
		return nil, nil, err
	}
	return backend, func(ctx context.Context, effective EffectiveBinding) (CredentialInjection, error) {
		env, err := DecryptAuthorized(backend, effective)
		return CredentialInjection{Environment: env}, err
	}, nil
}
