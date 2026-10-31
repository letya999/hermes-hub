package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func receiptPath(root, jobID, eventID string) string {
	sum := sha256.Sum256([]byte(jobID + "\x00" + eventID))
	return filepath.Join(root, "run-events", hex.EncodeToString(sum[:])+".json")
}

func receiptBinding(m JobMapping) string {
	body, _ := json.Marshal([]any{m.IdentitySchema, m.ExternalIdentityID, m.JobID, m.IdempotencyKey, m.Fingerprint, m.PrincipalID, m.ActorID, m.ContextID, m.ScopeID, m.RuntimeID, m.ConversationID, m.DeliveryTargetID, m.OrganizationID, m.UserID, m.PolicyVersion, m.Channel, m.Trigger, m.AssignmentID, m.TaskID, m.TopicID, m.ChatID, m.OperationalEpoch})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// RecordTerminalOutcome makes the existing stream receipt authoritative for
// local/worker outcomes too. Every derivative is replayable without a new run.
func (s *Spool) RecordTerminalOutcome(job Job, outcome RunOutcome, delivery *Delivery) error {
	if outcome.Status == "" {
		outcome.Status = "completed"
	}
	if !terminalStatus(outcome.Status) && outcome.Status != "uncertain" {
		return errors.New("invalid terminal outcome")
	}
	if len(outcome.Text) > 2<<20 || len(outcome.Voice) > 2<<20 || outcome.JobID != "" && outcome.JobID != job.ID {
		return errors.New("invalid terminal payload or job")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, err := s.loadMappingLocked(job.ID)
	if err != nil {
		return err
	}
	if !mapping.MatchesJob(job) || mapping.ChatID != job.ChatID || mapping.TaskID != job.TaskID || mapping.TopicID != job.TopicID || mapping.AssignmentID != job.AssignmentID {
		return errors.New("terminal job binding mismatch")
	}
	if outcome.RunID == "" {
		outcome.RunID = mapping.RunID
	}
	if outcome.SessionID == "" {
		outcome.SessionID = mapping.SessionID
	}
	if outcome.RuntimeGeneration == "" {
		outcome.RuntimeGeneration = mapping.RuntimeGeneration
	}
	if mapping.RunID != "" && outcome.RunID != mapping.RunID || mapping.SessionID != "" && outcome.SessionID != mapping.SessionID || mapping.RuntimeGeneration != "" && outcome.RuntimeGeneration != mapping.RuntimeGeneration {
		return errors.New("terminal runtime binding mismatch")
	}
	if outcome.LastEvent == "" {
		outcome.LastEvent = "run." + outcome.Status
	}
	var desired *Delivery
	if delivery != nil {
		d, err := normalizeDelivery(*delivery)
		if err != nil {
			return err
		}
		if d.JobID != job.ID || d.ConversationID != mapping.ConversationID || d.DeliveryTargetID != mapping.DeliveryTargetID || d.ChatID != mapping.ChatID || d.TaskID != mapping.TaskID || d.TopicID != mapping.TopicID || d.Channel != mapping.Channel || d.SlackChannel != job.SlackChannel || d.SlackThread != job.SlackThread {
			return errors.New("terminal delivery audience mismatch")
		}
		desired = &d
	}
	eventID := "handoff:" + outcome.Status
	if outcome.Status == "uncertain" && (outcome.RunID != "" || outcome.SessionID != "" || outcome.RuntimeGeneration != "") {
		identity, _ := json.Marshal([]string{outcome.RunID, outcome.SessionID, outcome.RuntimeGeneration})
		sum := sha256.Sum256(identity)
		eventID += ":" + hex.EncodeToString(sum[:])
	}
	path := receiptPath(s.root, job.ID, eventID)
	// A streamed terminal record already owns its delivery ID and payload.
	if mapping.Status == outcome.Status && mapping.LastEventID != "" && (terminalStatus(mapping.Status) || mapping.Status == "uncertain") {
		candidate := receiptPath(s.root, job.ID, mapping.LastEventID)
		if body, err := readEventRecord(candidate); err == nil {
			var previous streamReceipt
			if strictEventJSON(body, &previous) != nil || previous.JobID != job.ID || previous.Event.JobID != job.ID || previous.Event.EventID != mapping.LastEventID {
				return errors.New("invalid prior handoff receipt")
			}
			if outcome.Status != "uncertain" || previous.Event.RuntimeGeneration == outcome.RuntimeGeneration {
				path = candidate
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	var receipt streamReceipt
	changed := false
	if body, readErr := readEventRecord(path); readErr == nil {
		if err := strictEventJSON(body, &receipt); err != nil {
			return err
		}
		if receipt.Event.JobID != job.ID || receipt.Event.EventID == "" || receipt.Position == 0 || receiptPath(s.root, job.ID, receipt.Event.EventID) != path {
			return errors.New("invalid terminal receipt identity")
		}
		if receipt.JobID != job.ID || receipt.Event.Status != outcome.Status || receipt.Event.Text != outcome.Text || receipt.Event.RunID != outcome.RunID || receipt.Event.SessionID != outcome.SessionID || receipt.Event.RuntimeGeneration != outcome.RuntimeGeneration || !reflect.DeepEqual(receipt.Event.Artifacts, outcome.Artifacts) || receipt.Event.Voice != outcome.Voice {
			return errors.New("terminal receipt cannot change")
		}
		if receipt.Binding != "" && receipt.Binding != receiptBinding(mapping) {
			return errors.New("terminal receipt scope mismatch")
		}
		if desired != nil {
			desired.StreamPosition = receipt.Position
		}
		if receipt.FinalState == "" {
			receipt.FinalState = terminalJobState(outcome.Status)
			receipt.Binding = receiptBinding(mapping)
			if receipt.Delivery == nil {
				receipt.Delivery = desired
			}
			changed = true
		} else if receipt.Delivery == nil && desired != nil && outcome.Status != "completed" {
			receipt.Delivery = desired
			changed = true
		} else if desired != nil && !sameTerminalDelivery(receipt.Delivery, desired) {
			return errors.New("terminal delivery intent cannot change")
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
		receipt = streamReceipt{JobID: job.ID, Position: mapping.EventPosition + 1, Binding: receiptBinding(mapping), FinalState: terminalJobState(outcome.Status), Event: hubruntime.ExecuteResponse{JobID: job.ID, EventID: eventID, SessionID: outcome.SessionID, RunID: outcome.RunID, RuntimeGeneration: outcome.RuntimeGeneration, Status: outcome.Status, LastEvent: outcome.LastEvent, Text: outcome.Text, Voice: outcome.Voice, Artifacts: outcome.Artifacts}, Delivery: desired}
		if receipt.Delivery != nil {
			receipt.Delivery.StreamPosition = receipt.Position
		}
		changed = true
	} else {
		return readErr
	}
	if changed {
		if err := s.writeRunReceipt(path, receipt); err != nil {
			return err
		}
	}
	if err := s.applyStreamReceiptLocked(receipt); err != nil {
		return err
	}
	if receipt.Delivery != nil {
		if err := s.enqueueDeliveryLocked(*receipt.Delivery); err != nil {
			return err
		}
	}
	if err := s.finishReceiptJobLocked(receipt); err != nil {
		return err
	}
	s.signal()
	return nil
}

func terminalJobState(status string) string {
	if status == "completed" {
		return "done"
	}
	return "failed"
}

func (s *Spool) finishReceiptJobLocked(receipt streamReceipt) error {
	if receipt.FinalState == "" {
		return nil
	}
	if receipt.FinalState != terminalJobState(receipt.Event.Status) {
		return errors.New("invalid terminal job state")
	}
	mapping, err := s.loadMappingLocked(receipt.JobID)
	if err != nil {
		return err
	}
	if mapping.Status != receipt.Event.Status {
		return errStaleReceipt
	}
	file := spoolFileID(receipt.JobID) + ".json"
	source := ""
	for _, dir := range []string{"pending", "running", "done", "failed"} {
		path := filepath.Join(s.root, dir, file)
		body, err := readEventRecord(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var job Job
		if strictEventJSON(body, &job) != nil || !mapping.MatchesJob(job) || job.ChatID != mapping.ChatID || job.TaskID != mapping.TaskID || job.TopicID != mapping.TopicID {
			return errors.New("terminal source job binding mismatch")
		}
		if source != "" {
			return errors.New("duplicate terminal source job")
		}
		source = path
	}
	if source == "" {
		return errors.New("terminal source job missing")
	}
	target := filepath.Join(s.root, receipt.FinalState, file)
	if source != target {
		if err := s.renameOwned(source, target); err != nil {
			return err
		}
		if err := syncSpoolDirectory(filepath.Dir(source)); err != nil {
			return err
		}
		if err := syncSpoolDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	delete(s.secret, receipt.JobID)
	if receipt.Event.Status != "uncertain" {
		s.releaseInFlightLocked(receipt.JobID)
	}
	return nil
}

func sameTerminalDelivery(a, b *Delivery) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, right := *a, *b
	left.CreatedAt = right.CreatedAt
	x, _ := json.Marshal(left)
	y, _ := json.Marshal(right)
	return string(x) == string(y)
}

func writeRunReceipt(path string, receipt streamReceipt) error {
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if len(body) > 4<<20 {
		return errors.New("run receipt exceeds its record limit")
	}
	return atomicJSON(path, receipt)
}

// Reconciliation reads the source receipt, since mapping omits media metadata.
func (s *Spool) savedTerminalOutcome(mapping JobMapping) (RunOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, err := readEventRecord(receiptPath(s.root, mapping.JobID, mapping.LastEventID))
	if err != nil {
		return RunOutcome{}, err
	}
	var r streamReceipt
	if strictEventJSON(body, &r) != nil || r.JobID != mapping.JobID || r.Event.JobID != mapping.JobID || r.Event.EventID != mapping.LastEventID || r.Event.Status != mapping.Status || r.Binding != "" && r.Binding != receiptBinding(mapping) || r.Event.RuntimeGeneration != mapping.RuntimeGeneration || r.Event.RunID != mapping.RunID || r.Event.SessionID != mapping.SessionID {
		return RunOutcome{}, errors.New("invalid terminal source receipt")
	}
	return outcomeFromEvent(r.Event), nil
}
