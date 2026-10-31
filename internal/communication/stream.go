package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type streamReceipt struct {
	Binding          string                     `json:"binding,omitempty"`
	FinalState       string                     `json:"final_state,omitempty"`
	JobID            string                     `json:"job_id"`
	Position         uint64                     `json:"position"`
	Event            hubruntime.ExecuteResponse `json:"event"`
	Delivery         *Delivery                  `json:"delivery,omitempty"`
	ApprovalDeadline time.Time                  `json:"approval_deadline,omitempty"`
	// StatusDelivery marks the receipt's delivery as part of the single
	// editable status message (SPEC-0053 R2.2): replays route through
	// UpsertStatusDelivery so a recovered event can never duplicate or rewind
	// the rendered status.
	StatusDelivery bool `json:"status_delivery,omitempty"`
}

func terminalStatus(status string) bool { return commodel.TerminalStatus(status) }

func outcomeFromEvent(event hubruntime.ExecuteResponse) RunOutcome {
	return RunOutcome{JobID: event.JobID, SessionID: event.SessionID, RunID: event.RunID, RuntimeGeneration: event.RuntimeGeneration, Status: event.Status, LastEvent: event.LastEvent, Text: event.Text, Artifacts: event.Artifacts, Voice: event.Voice}
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
	if event.LastEvent == "run.heartbeat" {
		// Transport keepalive: binding-checked, never persisted or delivered.
		if event.JobID != job.ID {
			return errors.New("invalid stream identity or output")
		}
		return nil
	}
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
	// Runtime progress text is already a bounded, redacted projection; preserve
	// it so the channel can explain long-running MCP preparation. Other
	// non-terminal event text is not user-facing.
	if event.Status != "completed" && event.LastEvent != "tool.start" && event.LastEvent != "tool.end" && event.LastEvent != "run.started" {
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
	lang := locale.Fallback()
	if s.langOf != nil {
		lang = s.langOf(mapping.PrincipalID)
	}
	hash := sha256.Sum256([]byte(job.ID + "\x00" + event.EventID))
	path := filepath.Join(s.root, "run-events", hex.EncodeToString(hash[:])+".json")
	var receipt streamReceipt
	if b, readErr := s.fsys.ReadFile(path); readErr == nil {
		err = json.Unmarshal(b, &receipt)
		if err == nil && (receipt.JobID != job.ID || receipt.Event.EventID != event.EventID) {
			err = errors.New("invalid stream receipt")
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
		receipt = streamReceipt{JobID: job.ID, Position: mapping.EventPosition + 1, Event: event, Binding: receiptBinding(mapping)}
		if event.LastEvent == "approval.request" {
			receipt.ApprovalDeadline = time.Now().UTC().Add(2 * time.Minute)
		}
		text := ""
		id := "job-" + job.ID + "-event-" + hex.EncodeToString(hash[:])
		markdown := false
		statusDelivery := false
		switch {
		case event.Status == "completed":
			text, id, markdown = event.Text, "job-"+job.ID+"-response", true
		case terminalStatus(event.Status):
			text, id = failureMessage(lang, job.ID, event.Status), "job-"+job.ID+"-error"
			if event.Status == "cancelled" && mapping.ApprovalID != "" && !mapping.ApprovalDeadline.IsZero() && !time.Now().Before(mapping.ApprovalDeadline) {
				key := "job.approval-expired"
				if mapping.Control != nil && mapping.Control.Choice == "deny" {
					key = "job.approval-denied"
				}
				text = locale.T(lang, key, job.ID)
			}
		case event.LastEvent == "approval.request":
			text = locale.T(lang, "job.approval-needed")
			if event.ApprovalSummary != "" {
				text += "\n" + event.ApprovalSummary
			}
			text += "\n" + locale.T(lang, "job.approval-window", receipt.ApprovalDeadline.Format("15:04 UTC"))
			text += "\n" + locale.T(lang, "job.approval-hint", job.ID, event.ApprovalID, strings.Join(event.ApprovalChoices, ", "), job.ID)
		case event.LastEvent == "tool.start" || event.LastEvent == "tool.end" || event.LastEvent == "run.started":
			text, markdown = strings.TrimSpace(event.Text), true
			if text == "" && event.LastEvent == "run.started" {
				text = locale.T(lang, "chat.working")
			}
			if text == strings.TrimSpace(mapping.Result) {
				text = ""
			}
			if text != "" {
				if ch := channelFor(nil, job.Channel); ch != nil && ch.Caps().EditableStatus {
					// One editable status message per job (SPEC-0053 R2.2): the
					// pending record coalesces to the latest text, and once sent
					// the done record's message ID becomes the edit target.
					id, markdown, statusDelivery = "job-"+job.ID+"-status", false, true
				}
			}
		}
		if job.AssignmentID != "" && (strings.TrimSpace(text) == "NO_ACTIONABLE_EVENT" || event.LastEvent == "tool.start" || event.LastEvent == "tool.end" || event.LastEvent == "run.started") {
			text = ""
		}
		if text != "" || event.Status == "completed" && (event.Voice != "" || len(event.Artifacts) > 0) {
			receipt.StatusDelivery = statusDelivery
			receipt.Delivery = &Delivery{ID: id, JobID: job.ID, Channel: job.Channel, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, Text: text, StreamPosition: receipt.Position, CreatedAt: time.Now().UTC()}
			if markdown {
				receipt.Delivery.Format = formatMarkdown
			}
			if event.Status == "completed" {
				receipt.Delivery.Artifacts = event.Artifacts
				receipt.Delivery.Voice = event.Voice
			}
			if event.Status != "completed" {
				receipt.Delivery.JobID = ""
			} // Only final handoff may trigger a requested runtime restart.
			if event.LastEvent == "approval.request" {
				receipt.Delivery.ApprovalJobID, receipt.Delivery.ApprovalID = job.ID, event.ApprovalID
				receipt.Delivery.ApprovalRunID, receipt.Delivery.ApprovalGeneration = event.RunID, event.RuntimeGeneration
				buttons, err := s.approvalButtonsLocked(job.ID, event.ApprovalID, event.ApprovalChoices)
				if err != nil {
					s.mu.Unlock()
					return err
				}
				receipt.Delivery.Buttons = buttons
			}
		}
		err = s.writeRunReceipt(path, receipt) // Write-ahead receipt repairs a crash before mapping/outbox handoff.
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
		if receipt.StatusDelivery {
			return s.UpsertStatusDelivery(*receipt.Delivery)
		}
		return s.EnqueueDelivery(*receipt.Delivery)
	}
	return nil
}

// UpsertStatusDelivery maintains the single editable status delivery for a
// job (SPEC-0053 R2.2): a pending record absorbs the newest text, and once
// the status was sent its TelegramMessageID turns later updates into edit
// deliveries on the same message.
func (s *Spool) UpsertStatusDelivery(d Delivery) error {
	var err error
	if d, err = normalizeDelivery(d); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.signal() // a written or coalesced pending record wakes delivery
	statusName := spoolFileID(d.ID) + ".json"
	pending := filepath.Join(s.root, "outbox", "pending", statusName)
	if b, readErr := s.fsys.ReadFile(pending); readErr == nil {
		var p Delivery
		if json.Unmarshal(b, &p) == nil && p.StatusClosed {
			return nil
		}
		return s.writeJSON(pending, d)
	}
	// An in-flight or uncertain send must never be resurrected: the message
	// may already exist and a fresh send would be a blind duplicate.
	for _, dir := range []string{"sending", "failed"} {
		if _, err := s.fsys.Lstat(filepath.Join(s.root, "outbox", dir, statusName)); err == nil {
			return nil
		}
	}
	b, err := s.fsys.ReadFile(filepath.Join(s.root, "outbox", "done", statusName))
	if errors.Is(err, os.ErrNotExist) {
		return s.writeJSON(pending, d)
	}
	if err != nil {
		return err
	}
	var sent Delivery
	if json.Unmarshal(b, &sent) != nil || sent.StatusClosed || sent.TelegramMessageID <= 0 {
		return nil
	}
	if d.StreamPosition < sent.StreamPosition || d.StreamPosition == sent.StreamPosition && d.Text == sent.Text {
		return nil
	}
	// Coalesced edit IDs may predate their payload: fence the actual sent intent.
	for _, dir := range []string{"sending", "done", "failed"} {
		edits, err := filepath.Glob(filepath.Join(s.root, "outbox", dir, spoolFileID(d.ID)+"-edit-*.json"))
		if err != nil {
			return err
		}
		for _, path := range edits {
			body, err := s.readEventRecordLocked(path)
			if err != nil {
				return err
			}
			var previous Delivery
			if strictEventJSON(body, &previous) != nil {
				return errors.New("invalid status edit receipt")
			}
			if previous.StreamPosition >= d.StreamPosition && previous.Text == d.Text {
				return nil
			}
		}
	}
	// A still-pending status edit absorbs the newest text in place so a burst
	// of progress never produces a staircase of stale edits.
	if edits, _ := filepath.Glob(filepath.Join(s.root, "outbox", "pending", spoolFileID(d.ID)+"-edit-*.json")); len(edits) > 0 {
		for i, path := range edits {
			var p Delivery
			pb, perr := s.fsys.ReadFile(path)
			if perr != nil || json.Unmarshal(pb, &p) != nil {
				continue
			}
			if p.StreamPosition >= d.StreamPosition {
				continue
			}
			if i == 0 {
				p.Text, p.EditMessageID, p.StreamPosition = d.Text, sent.TelegramMessageID, d.StreamPosition
				_ = s.writeJSON(path, p)
			} else {
				_ = s.removeOwned(path)
			}
		}
		return nil
	}
	next := d
	digest := sha256.Sum256([]byte(d.Text))
	next.ID = d.ID + "-edit-" + strconv.FormatUint(d.StreamPosition, 10) + "-" + hex.EncodeToString(digest[:12])
	next.EditMessageID = sent.TelegramMessageID
	for _, dir := range []string{"sending", "done", "failed"} {
		if _, err := s.fsys.Lstat(filepath.Join(s.root, "outbox", dir, spoolFileID(next.ID)+".json")); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.writeJSON(filepath.Join(s.root, "outbox", "pending", spoolFileID(next.ID)+".json"), next)
}

// CloseStatusDelivery marks a job's sent status message finalized once the
// terminal response is delivered (SPEC-0053 R2.2) and returns its Telegram
// coordinates exactly once so the worker can delete the message.
func (s *Spool) CloseStatusDelivery(jobID string) (chatID int64, messageID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "outbox", "done", spoolFileID("job-"+jobID+"-status")+".json")
	b, err := s.fsys.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	var sent Delivery
	if json.Unmarshal(b, &sent) != nil || sent.StatusClosed {
		return 0, 0
	}
	sent.StatusClosed = true
	if s.writeJSON(path, sent) != nil {
		return 0, 0
	}
	return sent.ChatID, sent.TelegramMessageID
}

// errStaleReceipt marks a write-ahead straggler: its event predates terminal
// state already recorded for the job, so the receipt carries nothing new.
// Recovery skips it; a live re-apply still reports the conflict.
var errStaleReceipt = errors.New("stale stream receipt")

// staleReceipt reports a receipt whose event predates recorded terminal
// state — the same conflicts updateMappingLocked rejects for live events.
func staleReceipt(mapping JobMapping, event hubruntime.ExecuteResponse) bool {
	if mapping.RuntimeGeneration != "" && event.RuntimeGeneration != "" && mapping.RuntimeGeneration != event.RuntimeGeneration {
		return true
	}
	if !terminalStatus(mapping.Status) {
		return false
	}
	if event.Status != "" && event.Status != mapping.Status {
		return true
	}
	return mapping.Result != "" && event.Text != "" && event.Text != mapping.Result
}

func (s *Spool) applyStreamReceiptLocked(receipt streamReceipt) error {
	mapping, err := s.loadMappingLocked(receipt.JobID)
	if err != nil {
		return err
	}
	if receipt.FinalState != "" && receipt.Binding == "" {
		return errors.New("terminal receipt requires a scope binding")
	}
	if receipt.Binding != "" && receipt.Binding != receiptBinding(mapping) {
		return errors.New("receipt scope mismatch")
	}
	if receipt.Event.JobID != receipt.JobID || receipt.Event.EventID == "" {
		return errors.New("invalid receipt event binding")
	}
	if d := receipt.Delivery; d != nil && (d.ConversationID != mapping.ConversationID || d.DeliveryTargetID != mapping.DeliveryTargetID || (d.JobID != "" && d.JobID != receipt.JobID)) {
		return errors.New("receipt delivery audience mismatch")
	}
	if mapping.RunID != "" && receipt.Event.RunID != "" && mapping.RunID != receipt.Event.RunID || mapping.SessionID != "" && receipt.Event.SessionID != "" && mapping.SessionID != receipt.Event.SessionID {
		return errors.New("receipt run identity mismatch")
	}
	if mapping.RuntimeGeneration != "" && receipt.Event.RuntimeGeneration == "" && receipt.Event.Status == "uncertain" && receipt.Position < mapping.EventPosition {
		return errStaleReceipt
	}
	if receipt.Position <= mapping.EventPosition {
		return nil
	}
	if staleReceipt(mapping, receipt.Event) {
		return errStaleReceipt
	}
	if err := s.updateMappingLocked(receipt.JobID, outcomeFromEvent(receipt.Event), receipt.Event.Status, terminalStatus(receipt.Event.Status)); err != nil {
		return err
	}
	mapping, err = s.loadMappingLocked(receipt.JobID)
	if err != nil {
		return err
	}
	mapping.EventPosition, mapping.LastEventID = receipt.Position, receipt.Event.EventID
	if receipt.StatusDelivery && receipt.Delivery != nil && strings.TrimSpace(receipt.Delivery.Text) != "" {
		mapping.StatusText = receipt.Delivery.Text
	}
	if receipt.Event.LastEvent == "approval.request" {
		mapping.ApprovalID, mapping.ApprovalChoices, mapping.ApprovalDeadline, mapping.ApprovalState = receipt.Event.ApprovalID, receipt.Event.ApprovalChoices, receipt.ApprovalDeadline, "pending"
	}
	if terminalStatus(receipt.Event.Status) && mapping.ApprovalID != "" {
		mapping.ApprovalState = "closed"
	}
	return s.writeMappingLocked(mapping)
}

func (s *Spool) recoverStreamEvents() error {
	entries, err := s.fsys.ReadDir(filepath.Join(s.root, "run-events"))
	if err != nil {
		return err
	}
	var receipts []streamReceipt
	for _, name := range entries {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := s.fsys.ReadFile(filepath.Join(s.root, "run-events", name))
		if err != nil {
			return err
		}
		var receipt streamReceipt
		if err := strictEventJSON(b, &receipt); err != nil || receipt.JobID == "" || receipt.Position == 0 || receipt.Event.JobID != receipt.JobID || receipt.Event.EventID == "" {
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
		err := s.applyStreamReceiptLocked(receipt)
		if errors.Is(err, errStaleReceipt) {
			// The terminal write superseded this straggler; replaying it would
			// rewind recorded history and resend a stale delivery.
			continue
		}
		if err != nil {
			return err
		}
		if receipt.Delivery != nil {
			var deliveryErr error
			if receipt.StatusDelivery {
				deliveryErr = s.UpsertStatusDelivery(*receipt.Delivery)
			} else {
				deliveryErr = s.EnqueueDelivery(*receipt.Delivery)
			}
			if deliveryErr != nil {
				return deliveryErr
			}
		}
		if err := s.finishReceiptJobLocked(receipt); err != nil {
			return err
		}
	}
	return nil
}
