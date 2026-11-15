package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/locale"
)

// hubctl audit groups the ledger utilities: rollover migrates a stopped
// legacy audit, pending lists unsettled operation intents with safe identity
// fields only (arguments and results never reach the ledger, so they can
// never be printed), and resolve settles one operation on evidence the
// operator actually verified. Unsettled records have no TTL — they close only
// on an explicit outcome.
func runAudit(_ context.Context, args []string) error {
	lang, ok := locale.Parse(os.Getenv("LANG"))
	if !ok {
		lang = locale.Default
	}
	if len(args) == 0 {
		return errors.New(locale.T(lang, "hubctl.audit-usage"))
	}
	switch args[0] {
	case "rollover":
		return runAuditRollover(lang, args[1:])
	case "pending", "resolve":
	default:
		return errors.New(locale.T(lang, "hubctl.audit-usage"))
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
	switch sub {
	case "pending":
		if f.NArg() != 0 || *operation != "" || *outcome != "" || *evidence != "" {
			return errors.New(locale.T(lang, "hubctl.audit-pending-usage"))
		}
	case "resolve":
		if f.NArg() != 0 || *operation == "" || *outcome == "" || *evidence == "" {
			return errors.New(locale.T(lang, "hubctl.audit-resolve-usage"))
		}
	}
	if *dir == "" {
		*dir = filepath.Join("spaces", *profile)
	}
	dirAbs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	// The ToolHub container mounts the space's runtime/ tree at /state and
	// appends to /state/toolhub/audit.jsonl (HUB_AUDIT_LEDGER), so the host
	// path is <space>/runtime/toolhub/audit.jsonl.
	ledger, err := audit.Open(filepath.Join(dirAbs, "runtime", "toolhub", "audit.jsonl"))
	if err != nil {
		return err
	}
	switch sub {
	case "pending":
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
		return ledger.Resolve(audit.Scope{PrincipalID: *profile}, *operation, *outcome, *evidence)
	}
	return errors.New(locale.T(lang, "hubctl.audit-usage"))
}

func runAuditRollover(lang locale.Lang, args []string) error {
	usage := locale.T(lang, "hubctl.audit-rollover-usage")
	f := flag.NewFlagSet("audit rollover", flag.ContinueOnError)
	ledger := f.String("ledger", "", locale.T(lang, "hubctl.audit-rollover-ledger"))
	apply := f.Bool("apply", false, locale.T(lang, "hubctl.audit-rollover-apply"))
	digest := f.String("digest", "", locale.T(lang, "hubctl.audit-rollover-digest"))
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *ledger == "" || *apply && *digest == "" {
		return errors.New(usage)
	}
	var plan audit.LegacyPlan
	var err error
	if *apply {
		plan, err = audit.RolloverLegacy(*ledger, *digest)
	} else {
		plan, err = audit.PlanLegacyRollover(*ledger)
	}
	if err != nil {
		switch {
		case errors.Is(err, audit.ErrStaleLegacyPlan):
			return errors.New(locale.T(lang, "hubctl.audit-rollover-stale"))
		case errors.Is(err, audit.ErrInvalid):
			return errors.New(locale.T(lang, "hubctl.audit-rollover-invalid"))
		default:
			return errors.New(locale.T(lang, "hubctl.audit-rollover-failed"))
		}
	}
	return json.NewEncoder(os.Stdout).Encode(plan)
}
