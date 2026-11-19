package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/stack"
)

type userDisableOptions struct {
	Dir            string
	Environment    string
	ContextID      string
	SupervisorURL  string
	GatewayURL     string
	ToolHubURL     string
	SupervisorAuth string
	GatewayAuth    string
	ToolHubAuth    string
	Timeout        time.Duration
	Client         *http.Client
	AuditPath      string
}

type revocationCall struct {
	Stage string
	URL   string
	Token string
	Body  map[string]any
}

// runUser owns the operator-facing principal lifecycle verbs. `disable` is a
// synchronous composition of existing authority surfaces; the durable truth
// stays in each service's own store instead of a new revocation FSM.
func runUser(ctx context.Context, args []string) error {
	lang := locale.Operator()
	usage := locale.T(lang, "hubctl.user-disable-usage")
	if len(args) == 0 || args[0] != "disable" {
		return fmt.Errorf("%s", usage)
	}
	f := flag.NewFlagSet("user disable", flag.ContinueOnError)
	opts := userDisableOptions{
		Environment:   "dev",
		SupervisorURL: "http://127.0.0.1:8876",
		GatewayURL:    "http://127.0.0.1:8081",
		ToolHubURL:    "http://127.0.0.1:8090",
		Timeout:       15 * time.Second,
	}
	f.StringVar(&opts.Dir, "dir", "", "space directory")
	f.StringVar(&opts.Environment, "env", "dev", "deployment env")
	f.StringVar(&opts.ContextID, "context", "", "context id (default: space user)")
	f.StringVar(&opts.SupervisorURL, "supervisor-url", opts.SupervisorURL, "supervisor HTTP base URL")
	f.StringVar(&opts.GatewayURL, "gateway-url", opts.GatewayURL, "communication gateway base URL")
	f.StringVar(&opts.ToolHubURL, "toolhub-url", opts.ToolHubURL, "ToolHub control base URL")
	f.StringVar(&opts.SupervisorAuth, "supervisor-auth", "", "supervisor admin bearer")
	f.StringVar(&opts.GatewayAuth, "gateway-auth", "", "communication service-control bearer")
	f.StringVar(&opts.ToolHubAuth, "toolhub-auth", "", "ToolHub service-control bearer")
	f.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "per-stage HTTP timeout")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if opts.Dir == "" || f.NArg() != 0 {
		return fmt.Errorf("%s", usage)
	}
	return runUserDisable(ctx, os.Stdout, opts)
}

func runUserDisable(ctx context.Context, out io.Writer, opts userDisableOptions) error {
	lang := locale.Operator()
	if opts.Environment != "dev" && opts.Environment != "prod" {
		return fmt.Errorf("%s", locale.T(lang, "hubctl.user-disable-env"))
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return err
	}
	settings, err := stack.ReadEffective(dir, opts.Environment)
	if err != nil {
		return err
	}
	principal := settings.User
	contextID := strings.TrimSpace(opts.ContextID)
	if contextID == "" {
		contextID = principal
	}
	if !identity.ValidID(principal) || !identity.ValidID(contextID) {
		return fmt.Errorf("%s", locale.T(lang, "hubctl.user-disable-scope"))
	}
	if opts.SupervisorAuth == "" {
		opts.SupervisorAuth = strings.TrimSpace(os.Getenv("HUB_SUPERVISOR_ADMIN_AUTH"))
	}
	if opts.GatewayAuth == "" {
		opts.GatewayAuth = resolveGatewayToken(dir, opts.Environment, resolveStatusToken(dir))
	}
	if opts.ToolHubAuth == "" {
		opts.ToolHubAuth = opts.GatewayAuth
	}
	if opts.SupervisorAuth == "" || opts.GatewayAuth == "" || opts.ToolHubAuth == "" {
		return fmt.Errorf("%s", locale.T(lang, "hubctl.user-disable-auth"))
	}
	auditPath := opts.AuditPath
	if auditPath == "" {
		auditPath = filepath.Join(dir, "runtime", "toolhub", "audit.jsonl")
	}
	ledger, err := audit.Open(auditPath)
	if err != nil {
		return fmt.Errorf("principal revocation audit: %w", err)
	}
	operationID := audit.NewEvent("principal-revocation", principal, "started").EventID
	calls := []revocationCall{
		{Stage: "ingress", URL: opts.SupervisorURL + "/v1/admin/holds", Token: opts.SupervisorAuth, Body: map[string]any{"principal_id": principal, "context_id": contextID, "reason": "manual", "defer_containment": true}},
		{Stage: "identity", URL: opts.SupervisorURL + "/v1/admin/identity/deactivate", Token: opts.SupervisorAuth, Body: map[string]any{"principal_id": principal, "environment": opts.Environment}},
		{Stage: "jobs", URL: opts.GatewayURL + "/v1/admin/revocations", Token: opts.GatewayAuth, Body: map[string]any{"principal_id": principal, "context_id": contextID, "stage": "jobs"}},
		{Stage: "runs", URL: opts.SupervisorURL + "/v1/admin/holds", Token: opts.SupervisorAuth, Body: map[string]any{"principal_id": principal, "context_id": contextID, "reason": "manual", "defer_containment": false}},
		{Stage: "toolhub", URL: opts.ToolHubURL + "/v1/admin/revocations", Token: opts.ToolHubAuth, Body: map[string]any{"principal_id": principal, "context_id": contextID, "stage": "toolhub"}},
		{Stage: "connectors", URL: opts.ToolHubURL + "/v1/admin/revocations", Token: opts.ToolHubAuth, Body: map[string]any{"principal_id": principal, "context_id": contextID, "stage": "connectors"}},
		{Stage: "cron", URL: opts.ToolHubURL + "/v1/admin/revocations", Token: opts.ToolHubAuth, Body: map[string]any{"principal_id": principal, "context_id": contextID, "stage": "cron"}},
		{Stage: "oauth", URL: opts.ToolHubURL + "/v1/admin/revocations", Token: opts.ToolHubAuth, Body: map[string]any{"principal_id": principal, "context_id": contextID, "stage": "oauth"}},
		{Stage: "delivery", URL: opts.GatewayURL + "/v1/admin/revocations", Token: opts.GatewayAuth, Body: map[string]any{"principal_id": principal, "context_id": contextID, "stage": "delivery"}},
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: opts.Timeout}
	}
	for _, call := range calls {
		if err := appendRevocationAudit(ledger, principal, contextID, opts.Environment, operationID, call.Stage, "started"); err != nil {
			return err
		}
		response, callErr := postRevocation(ctx, client, call, principal)
		outcome := "succeeded"
		if callErr != nil {
			outcome = "failed"
		}
		if err := appendRevocationAudit(ledger, principal, contextID, opts.Environment, operationID, call.Stage, outcome); err != nil {
			return err
		}
		if callErr != nil {
			return fmt.Errorf("%s: %w", locale.T(lang, "hubctl.user-disable-stage-failed", call.Stage), callErr)
		}
		fmt.Fprintf(out, "%s\n", locale.T(lang, "hubctl.user-disable-stage", call.Stage, string(response)))
	}
	if err := appendRevocationAudit(ledger, principal, contextID, opts.Environment, operationID, "complete", "started"); err != nil {
		return err
	}
	if err := appendRevocationAudit(ledger, principal, contextID, opts.Environment, operationID, "complete", "succeeded"); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", locale.T(lang, "hubctl.user-disable-complete", principal, contextID))
	return nil
}

func appendRevocationAudit(ledger *audit.Ledger, principal, contextID, environment, operationID, stage, outcome string) error {
	eventOutcome := outcome
	if outcome == "started" {
		eventOutcome = "admitted"
	}
	event := audit.NewEvent("principal-revocation", principal, eventOutcome)
	event.ContextID = contextID
	event.Environment = environment
	event.OperationID = operationID + ":" + stage
	event.CorrelationID = operationID
	event.Action = stage
	event.Authorization = "allow"
	if outcome == "started" {
		event.Phase = "admitted"
		event.Execution = "unknown"
		event.Delivery = "unknown"
	} else {
		event.Phase = "completed"
		event.Execution = outcome
		event.Delivery = "not_applicable"
	}
	event.Name = operatorActor()
	if err := ledger.Append(event); err != nil {
		return fmt.Errorf("principal revocation audit: %w", err)
	}
	return nil
}

func postRevocation(ctx context.Context, client *http.Client, call revocationCall, principal string) ([]byte, error) {
	body, err := json.Marshal(call.Body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, call.URL, bytes.NewReader(body)) // #nosec G704 -- operator-configured control-plane origin
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+call.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Principal", principal)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte(`{"status":"ok"}`)
	}
	return data, nil
}
