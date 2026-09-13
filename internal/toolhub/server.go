package toolhub

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
)

type EndpointConfig struct {
	Listen  string
	Token   string
	Auth    identity.Envelope
	Backend ToolBackend
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
	return EndpointConfig{
		Listen: envOr("HUB_TOOLHUB_LISTEN", "127.0.0.1:8090"),
		Token:  token,
		Auth:   auth,
		Backend: RoutingBackend{
			MCP: MCPBackend{Token: os.Getenv("TOOLHIVE_VMCP_TOKEN"), AdmissionVerifier: admission},
			CLI: CLIRunner{Root: envOr("HUB_STATE", "/state")},
		},
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
	gateway := &Gateway{
		Store: store, Backend: config.Backend, Tokens: map[string]identity.Envelope{config.Token: config.Auth},
		DisableLocalhostProtection: nonLoopbackListen(config.Listen),
	}
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
