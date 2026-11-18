package toolhub

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"strconv"
)

// LedgerAuditWriter projects gateway outcomes into the durable, scoped operation ledger.
func LedgerAuditWriter(ledger *audit.Ledger) func(string, map[string]string) error {
	return func(event string, fields map[string]string) error {
		record := audit.NewEvent("tool-call", fields["principal_id"], fields["outcome"])
		if event == "admin.diagnostics" {
			record.PrincipalID, record.Outcome, record.Name = fields["admin"], "read", fields["target"]
		}
		record.ContextID = fields["context_id"]
		record.RuntimeID = fields["runtime_id"]
		record.ConnectionID = fields["connection_id"]
		record.SenderID = fields["sender_id"]
		record.TeamID = fields["team_id"]
		record.BindingID = fields["binding_id"]
		record.PolicyRevision = fields["policy_version"]
		record.JobID = fields["job_id"]
		record.HermesRunID = fields["hermes_run_id"]
		record.ToolCallID = fields["tool_call_id"]
		record.CorrelationID = fields["correlation_id"]
		record.Receipt = api.SafeReceipt(fields["receipt"])
		record.Reason = fields["reason"]
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
		switch event {
		case "admit", "allow", "not-started", "uncertain":
			record.Action = "tool.call"
			record.OperationID = toolOperationID(record.PrincipalID, record.ContextID, record.Environment, record.JobID, record.ToolCallID)
			if record.CorrelationID == "" {
				record.CorrelationID = record.OperationID
			}
			record.Authorization = "allow"
			switch event {
			case "admit":
				record.Phase = "admitted"
				record.Execution, record.Delivery, record.Outcome, record.Receipt = "unknown", "unknown", "admitted", ""
			case "uncertain":
				record.Phase = "uncertain"
				record.Execution, record.Delivery, record.Receipt = "unknown", "unknown", ""
			case "not-started":
				record.Phase = "completed"
				record.Execution, record.Delivery = "not_started", "not_started"
			case "allow":
				record.Phase = "completed"
				record.Execution, record.Delivery = fields["execution"], "not_started"
			}
		case "deny", "reject":
			record.Phase = "decision"
			record.OperationID = record.EventID
			record.Action = "tool.call"
			record.Authorization, record.Execution, record.Delivery = "deny", "not_started", "not_applicable"
			if event == "reject" {
				record.Authorization = "allow"
			}
		}
		return ledger.Append(record)
	}
}

func toolOperationID(principal, contextID, environment, jobID, callID string) string {
	sum := sha256.Sum256([]byte(principal + "\x00" + contextID + "\x00" + environment + "\x00" + jobID + "\x00" + callID))
	return "tool-" + hex.EncodeToString(sum[:])
}

// writeCorrelationID is the stable logical identity of a write call: the same
// admitted operation replayed after a lost response digests identically, so
// the audit ledger's unsettled-correlation fence refuses the duplicate effect.
// Only the digest of the canonical admitted arguments participates — argument
// values themselves never reach the ledger.
func writeCorrelationID(auth identity.Envelope, corr api.CallCorrelation, effective api.EffectiveBinding, projected api.ProjectedTool, canonical []byte) string {
	sum := sha256.New()
	for _, part := range []string{auth.PrincipalID, auth.ContextID, auth.Environment, corr.JobID, corr.HermesRunID, effective.Binding.ToolBindingID, projected.Name} {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	sum.Write(canonical)
	return "call-" + hex.EncodeToString(sum.Sum(nil))
}
