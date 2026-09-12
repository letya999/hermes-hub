package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type streamReceipt struct {
	JobID            string                     `json:"job_id"`
	Position         uint64                     `json:"position"`
	Event            hubruntime.ExecuteResponse `json:"event"`
	Delivery         *Delivery                  `json:"delivery,omitempty"`
	ApprovalDeadline time.Time                  `json:"approval_deadline,omitempty"`
}

func terminalStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "interrupted"
}

func outcomeFromEvent(event hubruntime.ExecuteResponse) RunOutcome {
	return RunOutcome{JobID: event.JobID, SessionID: event.SessionID, RunID: event.RunID, RuntimeGeneration: event.RuntimeGeneration, Status: event.Status, LastEvent: event.LastEvent, Text: event.Text}
}

func ownsStreamMapping(job Job, mapping JobMapping) bool {
	return ownsMapping(job.Envelope, mapping) && job.ActorID == mapping.ActorID && job.UserID == mapping.UserID && job.OrganizationID == mapping.OrganizationID && job.ScopeID == mapping.ScopeID && job.Channel == mapping.Channel && job.Trigger == mapping.Trigger && job.IdempotencyKey == mapping.IdempotencyKey
}

// A response to the explicit supervisor recovery API may change only compute
// generation. The admitted Hermes run and original delivery audience stay fixed.
func (s *Spool) RebindObservation(job Job, event hubruntime.ExecuteResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.loadMappingLocked(job.ID)
	if err != nil {
		return err
	}
	if !ownsStreamMapping(job, m) || m.RunID != event.RunID || m.SessionID != event.SessionID || event.JobID != job.ID || event.RuntimeGeneration == "" || terminalStatus(m.Status) {
		return errors.New("recovery binding mismatch")
	}
	m.RuntimeGeneration = event.RuntimeGeneration
	return s.writeMappingLocked(m)
}

func (s *Spool) RecordStreamEvent(job Job, event hubruntime.ExecuteResponse) error {
	if event.JobID != job.ID || event.EventID == "" || len(event.EventID) > 256 || event.RuntimeGeneration == "" || event.RunID == "" || event.SessionID == "" || len(event.Text) > 2*1024*1024 {
		return errors.New("invalid stream identity or output")
	}
	switch event.LastEvent {
	case "run.admitted", "run.started", "tool.start", "tool.end", "approval.request", "run.completed", "run.failed", "run.cancelled", "run.interrupted", "run.unknown":
	default:
		return errors.New("unsupported projected event")
	}
	if terminalStatus(event.Status) && event.LastEvent != "run."+event.Status {
		return errors.New("terminal projection mismatch")
	}
	if event.LastEvent == "approval.request" {
		if event.ApprovalID == "" || len(event.ApprovalID) > 256 {
			return errors.New("invalid approval request identity")
		}
		choices := make([]string, 0, 2)
		for _, choice := range event.ApprovalChoices {
			if choice == "once" || choice == "session" || choice == "always" || choice == "deny" {
				choices = append(choices, choice)
			}
		}
		event.ApprovalChoices = choices
	}
	if event.Status != "completed" {
		event.Text = ""
	}
	s.mu.Lock()
	mapping, err := s.loadMappingLocked(job.ID)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if !ownsStreamMapping(job, mapping) || (mapping.RuntimeGeneration != "" && mapping.RuntimeGeneration != event.RuntimeGeneration) || (mapping.RunID != "" && mapping.RunID != event.RunID) || (mapping.SessionID != "" && mapping.SessionID != event.SessionID) {
		s.mu.Unlock()
		return errors.New("stream binding mismatch")
	}
	if terminalStatus(mapping.Status) && mapping.Status != event.Status {
		s.mu.Unlock()
		return errors.New("terminal run cannot return to active state")
	}
	if terminalStatus(mapping.Status) && mapping.Result != "" && event.Text != mapping.Result {
		s.mu.Unlock()
		return errors.New("terminal result cannot change")
	}
	hash := sha256.Sum256([]byte(job.ID + "\x00" + event.EventID))
	path := filepath.Join(s.root, "events", hex.EncodeToString(hash[:])+".json")
	var receipt streamReceipt
	if b, readErr := os.ReadFile(path); readErr == nil {
		err = json.Unmarshal(b, &receipt)
		if err == nil && (receipt.JobID != job.ID || receipt.Event.EventID != event.EventID) {
			err = errors.New("invalid stream receipt")
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
		receipt = streamReceipt{JobID: job.ID, Position: mapping.EventPosition + 1, Event: event}
		if event.LastEvent == "approval.request" {
			receipt.ApprovalDeadline = time.Now().UTC().Add(2 * time.Minute)
		}
		text := ""
		id := "job-" + job.ID + "-event-" + hex.EncodeToString(hash[:])
		switch {
		case event.Status == "completed":
			text, id = event.Text, "job-"+job.ID+"-response"
		case terminalStatus(event.Status):
			text, id = "Не удалось завершить задачу.", "job-"+job.ID+"-error"
			if event.Status == "cancelled" {
				text = "Задача отменена. ID: " + job.ID
			}
		case event.LastEvent == "approval.request":
			text = "Требуется подтверждение действия.\n/approve " + job.ID + " " + event.ApprovalID + " <choice>\nВарианты: " + strings.Join(event.ApprovalChoices, ", ") + "\nОтмена: /cancel " + job.ID
		case event.LastEvent == "tool.start" || event.LastEvent == "tool.end" || event.LastEvent == "run.started":
			text = "Выполняю запрос."
		}
		if text != "" {
			receipt.Delivery = &Delivery{ID: id, JobID: job.ID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, ChatID: job.ChatID, Text: text, CreatedAt: time.Now().UTC()}
			if event.Status != "completed" {
				receipt.Delivery.JobID = ""
			} // Only final handoff may trigger a requested runtime restart.
		}
		err = atomicJSON(path, receipt) // Write-ahead receipt repairs a crash before mapping/outbox handoff.
	} else {
		err = readErr
	}
	if err == nil {
		err = s.applyStreamReceiptLocked(receipt)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if receipt.Delivery != nil {
		return s.EnqueueDelivery(*receipt.Delivery)
	}
	return nil
}

func (s *Spool) applyStreamReceiptLocked(receipt streamReceipt) error {
	mapping, err := s.loadMappingLocked(receipt.JobID)
	if err != nil {
		return err
	}
	if receipt.Event.JobID != receipt.JobID || receipt.Event.EventID == "" {
		return errors.New("invalid receipt event binding")
	}
	if d := receipt.Delivery; d != nil && (d.ConversationID != mapping.ConversationID || d.DeliveryTargetID != mapping.DeliveryTargetID || (d.JobID != "" && d.JobID != receipt.JobID)) {
		return errors.New("receipt delivery audience mismatch")
	}
	if receipt.Position <= mapping.EventPosition {
		return nil
	}
	if err := s.updateMappingLocked(receipt.JobID, outcomeFromEvent(receipt.Event), receipt.Event.Status, terminalStatus(receipt.Event.Status)); err != nil {
		return err
	}
	mapping, err = s.loadMappingLocked(receipt.JobID)
	if err != nil {
		return err
	}
	mapping.EventPosition, mapping.LastEventID = receipt.Position, receipt.Event.EventID
	if receipt.Event.LastEvent == "approval.request" {
		mapping.ApprovalID, mapping.ApprovalChoices, mapping.ApprovalDeadline, mapping.ApprovalState = receipt.Event.ApprovalID, receipt.Event.ApprovalChoices, receipt.ApprovalDeadline, "pending"
	}
	if terminalStatus(receipt.Event.Status) && mapping.ApprovalID != "" {
		mapping.ApprovalState = "closed"
	}
	return s.writeMappingLocked(mapping)
}

func (s *Spool) recoverStreamEvents() error {
	entries, err := os.ReadDir(filepath.Join(s.root, "events"))
	if err != nil {
		return err
	}
	var receipts []streamReceipt
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root, "events", entry.Name()))
		if err != nil {
			return err
		}
		var receipt streamReceipt
		if err := json.Unmarshal(b, &receipt); err != nil || receipt.JobID == "" || receipt.Position == 0 {
			return errors.New("invalid stream receipt")
		}
		receipts = append(receipts, receipt)
	}
	sort.Slice(receipts, func(i, j int) bool {
		if receipts[i].JobID == receipts[j].JobID {
			return receipts[i].Position < receipts[j].Position
		}
		return receipts[i].JobID < receipts[j].JobID
	})
	for _, receipt := range receipts {
		if err := s.applyStreamReceiptLocked(receipt); err != nil {
			return err
		}
		if receipt.Delivery != nil {
			if err := s.EnqueueDelivery(*receipt.Delivery); err != nil {
				return err
			}
		}
	}
	return nil
}
