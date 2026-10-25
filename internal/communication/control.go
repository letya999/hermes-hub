package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func ownsMapping(caller identity.Envelope, mapping JobMapping) bool {
	return caller.Schema != 0 && caller.Schema == mapping.IdentitySchema && caller.ExternalIdentityID != "" && caller.ExternalIdentityID == mapping.ExternalIdentityID && caller.PrincipalID == mapping.PrincipalID && caller.ContextID == mapping.ContextID && caller.ConversationID == mapping.ConversationID && caller.DeliveryTargetID == mapping.DeliveryTargetID && caller.RuntimeID == mapping.RuntimeID && caller.PolicyVersion == mapping.PolicyVersion
}

// RequestCancel serializes with ClaimJob. Only an unclaimed job can be declared
// cancelled locally; admitted or claimed work still requires runtime confirmation.
func (s *Spool) RequestCancel(jobID string, caller identity.Envelope) (JobMapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, err := s.loadMappingLocked(jobID)
	if err != nil {
		return JobMapping{}, err
	}
	if !ownsMapping(caller, mapping) {
		return JobMapping{}, errors.New("job ownership mismatch")
	}
	if terminalStatus(mapping.Status) || mapping.CancelRequested {
		return mapping, nil
	}
	mapping.CancelRequested = true
	mapping.Control = &PendingControl{Caller: caller, Action: "cancel", State: "pending"}
	mapping.UpdatedAt = time.Now().UTC()
	if mapping.Status == "accepted" && mapping.RunID == "" {
		mapping.Status = "cancelled"
		mapping.TerminalAt = mapping.UpdatedAt
		mapping.LastKnownEvent = "job.cancelled"
	}
	if err = s.writeMappingLocked(mapping); err != nil {
		return JobMapping{}, err
	}
	s.signal() // a pending control request wakes the control loop (SPEC-0053 R2.3)
	return mapping, nil
}

func (s *Spool) RequestApproval(jobID string, caller identity.Envelope, requestID, choice string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, err := s.loadMappingLocked(jobID)
	if err != nil {
		return err
	}
	if !ownsMapping(caller, mapping) {
		return errors.New("job ownership mismatch")
	}
	if mapping.Control != nil && mapping.Control.Action == "approve" && mapping.Control.RequestID == requestID {
		if (mapping.Control.State == "pending" || mapping.Control.State == "uncertain") && !time.Now().Before(mapping.ApprovalDeadline) {
			return errors.New("approval is expired")
		}
		if mapping.Control.Choice == choice {
			return nil
		}
		return errors.New("conflicting approval")
	}
	allowed := false
	for _, value := range mapping.ApprovalChoices {
		if value == choice {
			allowed = true
		}
	}
	if mapping.CancelRequested || mapping.ApprovalState != "pending" || requestID == "" || mapping.ApprovalID != requestID || !allowed || !time.Now().Before(mapping.ApprovalDeadline) {
		return errors.New("approval is stale or invalid")
	}
	mapping.Control = &PendingControl{Caller: caller, Action: "approve", RequestID: requestID, Choice: choice, State: "pending"}
	if err := s.writeMappingLocked(mapping); err != nil {
		return err
	}
	s.signal()
	return nil
}

// controlLoop handles pending control records on the spool's notify signal
// with a one-second fallback tick (SPEC-0053 R2.3).
func (g *Gateway) controlLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastCompact time.Time
	for {
		g.controlOne(ctx)
		if g.now().Sub(lastCompact) >= time.Hour {
			retention := g.config.SpoolRetention
			if retention <= 0 {
				retention = 336 * time.Hour
			}
			_ = g.spool.Compact(g.now(), retention)
			lastCompact = g.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-g.spool.Notify():
		case <-ticker.C:
		}
	}
}

// controlOne is independent of the execution worker. Intent becomes uncertain
// before HTTP dispatch so a restart cannot blindly repeat a side effect.
func (g *Gateway) controlOne(ctx context.Context) {
	g.controlPolls.Add(1)
	g.closeApprovalMessages(ctx)
	ids, err := g.spool.activeMappingIDs()
	if err != nil {
		return
	}
	for _, id := range ids {
		g.spool.mu.Lock()
		mapping, err := g.spool.loadMappingLocked(id)
		if err != nil || mapping.Control == nil || (mapping.Control.State != "pending" && mapping.Control.State != "uncertain") || time.Now().Before(mapping.Control.NextAttemptAt) || terminalStatus(mapping.Status) || (mapping.Control.Action != "cancel" && (mapping.RunID == "" || mapping.SessionID == "" || mapping.RuntimeGeneration == "")) {
			g.spool.mu.Unlock()
			continue
		}
		preAdmission := mapping.RunID == ""
		if mapping.Control.Action == "approve" && g.authorizeGroupMapping(mapping) != nil {
			mapping.CancelRequested = true
			mapping.Control = &PendingControl{Caller: mapping.Control.Caller, Action: "cancel", State: "pending"}
		}
		reconciling := mapping.Control.State == "uncertain" && !preAdmission
		control := *mapping.Control
		control.NextAttemptAt = time.Now().Add(5 * time.Second)
		control.State = "uncertain"
		mapping.Control = &control
		err = g.spool.writeMappingLocked(mapping)
		g.spool.mu.Unlock()
		if err != nil {
			return
		}
		request := hubruntime.RunControl{Reconcile: reconciling, RunReference: hubruntime.RunReference{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: control.Caller, JobID: mapping.JobID, OrganizationID: mapping.OrganizationID, UserID: mapping.UserID, ActorID: mapping.ActorID, ScopeID: mapping.ScopeID, Channel: mapping.Channel, Trigger: mapping.Trigger, IdempotencyKey: mapping.IdempotencyKey}, RunID: mapping.RunID, SessionID: mapping.SessionID, RuntimeGeneration: mapping.RuntimeGeneration}, Action: control.Action, RequestID: control.RequestID, Choice: control.Choice, Deadline: mapping.ApprovalDeadline}
		if preAdmission {
			request.RunID, request.SessionID, request.RuntimeGeneration = "", "", ""
		}
		body, _ := json.Marshal(request)
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		req, err := http.NewRequestWithContext(callCtx, http.MethodPost, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/control", bytes.NewReader(body))
		if err != nil {
			cancel()
			return
		}
		req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		var outcome hubruntime.ExecuteResponse
		applied := false
		if err == nil {
			applied = response.StatusCode == 200 && json.NewDecoder(http.MaxBytesReader(nil, response.Body, 2*1024*1024+64*1024)).Decode(&outcome) == nil && outcome.JobID == mapping.JobID && outcome.RunID == mapping.RunID && outcome.SessionID == mapping.SessionID && outcome.RuntimeGeneration == mapping.RuntimeGeneration && (terminalStatus(outcome.Status) || outcome.Status == "running" || outcome.Status == "waiting_for_approval")
			_ = response.Body.Close()
		}
		cancel()
		if preAdmission {
			g.spool.mu.Lock()
			latest, loadErr := g.spool.loadMappingLocked(mapping.JobID)
			if loadErr == nil && latest.Control != nil && latest.Control.Action == "cancel" && !terminalStatus(latest.Status) {
				copy := *latest.Control
				copy.State = "pending"
				latest.Control = &copy
				_ = g.spool.writeMappingLocked(latest)
			}
			g.spool.mu.Unlock()
			return
		}
		if applied && terminalStatus(outcome.Status) {
			applied = g.spool.recordControlTerminal(mapping, outcome) == nil
		}
		if applied {
			g.spool.mu.Lock()
			latest, err := g.spool.loadMappingLocked(mapping.JobID)
			if err == nil && latest.Control != nil && latest.Control.Action == control.Action && latest.Control.RequestID == control.RequestID && latest.Control.Choice == control.Choice {
				copy := *latest.Control
				copy.State = "applied"
				if reconciling || terminalStatus(latest.Status) {
					copy.State = "closed"
				}
				latest.Control = &copy
				_ = g.spool.writeMappingLocked(latest)
			}
			g.spool.mu.Unlock()
		}
		return
	}
}

func (s *Spool) recordControlTerminal(mapping JobMapping, outcome hubruntime.ExecuteResponse) error {
	s.mu.Lock()
	var job Job
	found := false
	for _, dir := range []string{"running", "pending", "failed", "done"} {
		b, err := os.ReadFile(filepath.Join(s.root, dir, spoolFileID(mapping.JobID)+".json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || json.Unmarshal(b, &job) != nil || job.ID != mapping.JobID || jobFingerprint(job) != mapping.Fingerprint {
			s.mu.Unlock()
			return errors.New("control result audience unavailable")
		}
		found = true
		break
	}
	s.mu.Unlock()
	if !found {
		return errors.New("control result job unavailable")
	}
	outcome.EventID = "terminal:" + outcome.Status
	outcome.LastEvent = "run." + outcome.Status
	return s.RecordStreamEvent(job, outcome)
}
