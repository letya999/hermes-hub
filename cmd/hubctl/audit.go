package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/audit"
)

// hubctl audit is the operator egress for durable tool-call operations:
// pending lists unsettled intents with safe identity fields only (arguments
// and results never reach the ledger, so they can never be printed), and
// resolve settles one operation on evidence the operator actually verified.
// Unsettled records have no TTL — they close only on an explicit outcome.
func runAudit(_ context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("audit requires pending or resolve")
	}
	sub := args[0]
	f := flag.NewFlagSet("audit "+sub, flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	profile := f.String("user", "me", "principal identifier the operations belong to")
	operation := f.String("operation", "", "unsettled operation id to resolve")
	outcome := f.String("outcome", "", "declared outcome: not_started, succeeded or failed")
	evidence := f.String("evidence", "", "bounded receipt reference the operator verified")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *dir == "" {
		*dir = filepath.Join("spaces", *profile)
	}
	ledger, err := audit.Open(filepath.Join(*dir, "state", "audit.jsonl"))
	if err != nil {
		return err
	}
	switch sub {
	case "pending":
		if f.NArg() != 0 || *operation != "" || *outcome != "" || *evidence != "" {
			return fmt.Errorf("usage: hubctl audit pending [--dir d] [--user u]")
		}
		events, err := ledger.Pending(audit.Scope{PrincipalID: *profile}, audit.MaxPageRecords)
		if err != nil {
			return err
		}
		type pendingView struct {
			OperationID   string `json:"operation_id"`
			CorrelationID string `json:"correlation_id,omitempty"`
			Phase         string `json:"phase"`
			Action        string `json:"action,omitempty"`
			At            string `json:"at"`
			ContextID     string `json:"context_id,omitempty"`
			Environment   string `json:"environment,omitempty"`
			JobID         string `json:"job_id,omitempty"`
			RunID         string `json:"hermes_run_id,omitempty"`
			ToolCallID    string `json:"tool_call_id,omitempty"`
			BindingID     string `json:"binding_id,omitempty"`
			CapabilityID  string `json:"capability_id,omitempty"`
		}
		view := make([]pendingView, 0, len(events))
		for _, event := range events {
			view = append(view, pendingView{
				OperationID: event.OperationID, CorrelationID: event.CorrelationID,
				Phase: event.Phase, Action: event.Action, At: event.At.Format("2006-01-02T15:04:05Z"),
				ContextID: event.ContextID, Environment: event.Environment,
				JobID: event.JobID, RunID: event.HermesRunID, ToolCallID: event.ToolCallID,
				BindingID: event.BindingID, CapabilityID: event.CapabilityID,
			})
		}
		return json.NewEncoder(os.Stdout).Encode(view)
	case "resolve":
		if f.NArg() != 0 || *operation == "" || *outcome == "" || *evidence == "" {
			return fmt.Errorf("usage: hubctl audit resolve --operation <id> --outcome not_started|succeeded|failed --evidence <ref> [--dir d] [--user u]")
		}
		return ledger.Resolve(audit.Scope{PrincipalID: *profile}, *operation, *outcome, *evidence)
	}
	return fmt.Errorf("audit requires pending or resolve")
}
