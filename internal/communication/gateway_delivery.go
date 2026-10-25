package communication

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/diagnostics"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// editThrottle bounds status-message edits to one per interval per chat
// (SPEC-0053 R2.2) behind its own lock.
type editThrottle struct {
	mu   sync.Mutex
	last map[int64]time.Time
}

func (g *Gateway) queueDelivery(ctx context.Context, key string, chatID, topicID int64, text string) error {
	return g.queueDeliveryChannel(ctx, key, "telegram_bot", chatID, topicID, text)
}

// queueDeliveryChannel is queueDelivery for non-Telegram surfaces: command
// replies on the mock channel must capture into mock-sent, not call Telegram.
func (g *Gateway) queueDeliveryChannel(_ context.Context, key, channel string, chatID, topicID int64, text string) error {
	id := key
	if id == "" {
		id = newID()
	}
	return g.spool.EnqueueDelivery(Delivery{ID: id, IdempotencyKey: key, Channel: channel, ChatID: chatID, TopicID: topicID, Text: text, CreatedAt: g.now().UTC()})
}

func (g *Gateway) worker(ctx context.Context) {
	// Fallback tick only; work arrives through the spool's notify channel.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var nextObservation time.Time
	for {
		g.deliverOne(ctx)
		if now := g.now().UTC(); g.config.RuntimeURL != "" && !now.Before(nextObservation) {
			_ = g.spool.RequeueObservations(now, g.config.Supervised)
			nextObservation = now.Add(5 * time.Second)
		}
		if job, err := g.spool.ClaimJob(); err == nil && job != nil {
			if job.Text != "" && g.authorizeGroupMapping(mappingFromJob(*job, g.now())) != nil {
				log.Printf("gateway job_id=%q channel=%q status=denied reason=group_authorization", job.ID, job.Channel)
				_ = g.spool.FailJob(job.ID)
				continue
			}
			if job.Trigger == "cron" && job.Text != "" && g.authorizeOccurrence(*job) != nil {
				_ = g.spool.FailJob(job.ID)
				continue
			}
			if job.Trigger == "goal" && !g.goalTurnClaimable(*job) {
				// A turn queued under a superseded epoch (or a cleared/paused
				// goal) no-ops: the model is never invoked for a goal that
				// already moved on (SPEC-0074).
				_ = g.spool.CompleteJob(job.ID)
				continue
			}
			g.busy.Add(1)
			var response string
			var outcome RunOutcome
			var runErr error
			if detailed, ok := g.runner.(outcomeRunner); ok {
				outcome, runErr = detailed.RunOutcome(ctx, *job)
				response = outcome.Text
			} else {
				response, runErr = g.runner.Run(ctx, *job, g.user(job.UserID))
				outcome = RunOutcome{Text: response, Status: "completed", LastEvent: "run.completed"}
				if runErr != nil {
					outcome.Status = "failed"
				}
			}
			g.busy.Add(-1)
			if runErr != nil || outcome.Status == "uncertain" {
				if mapping, found, err := g.spool.Mapping(job.ID); err == nil && found && terminalStatus(mapping.Status) {
					outcome = RunOutcome{Text: mapping.Result, JobID: mapping.JobID, RunID: mapping.RunID, SessionID: mapping.SessionID, RuntimeGeneration: mapping.RuntimeGeneration, Status: mapping.Status, LastEvent: mapping.LastKnownEvent}
					response = outcome.Text
					if mapping.Status == "completed" {
						runErr = nil
					} else {
						runErr = errors.New("run ended without completion")
					}
				}
			}
			if runErr != nil || outcome.Status == "uncertain" {
				if runErr == nil {
					runErr = ErrUncertain
				}
				if outcome.Status != "" && outcome.Status != "uncertain" {
					_ = g.spool.RecordOutcome(job.ID, outcome)
				}
				if errors.Is(runErr, ErrUncertain) || outcome.Status == "uncertain" {
					_ = g.spool.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, SessionID: outcome.SessionID, RunID: outcome.RunID, RuntimeGeneration: outcome.RuntimeGeneration, Status: "uncertain", LastEvent: "run.unknown"})
				}
				_ = g.spool.FailJob(job.ID)
				// A plain run.failed on a user message or goal turn is usually
				// a transient model/provider outage: retry once as a fresh run
				// before telling the owner. Uncertain outcomes reconcile instead.
				retried := false
				if retryableJobFailure(*job, outcome.Status, runErr) {
					retry := *job
					retry.ID = job.ID + "-r1"
					retry.IdempotencyKey = job.IdempotencyKey + "-r1"
					retry.CreatedAt = g.now().UTC()
					if accepted, err := g.spool.Enqueue(retry); err == nil && accepted {
						retried = true
						if chatID, messageID := g.spool.CloseStatusDelivery(job.ID); messageID > 0 {
							_ = g.api.DeleteMessage(ctx, chatID, messageID)
						}
					}
				}
				// A goal turn that failed for good must not leave the record
				// "active" with nothing queued — that zombie reports live in
				// /goal status while the loop never runs again.
				goalPaused := false
				if !retried && job.Trigger == "goal" && !errors.Is(runErr, ErrUncertain) && outcome.Status != "uncertain" {
					goalPaused = g.pauseGoalAfterRunFailure(*job)
				}
				key, message := "job-"+job.ID+"-error", failureMessage(job.ID, outcome.Status)
				if goalPaused {
					message += " Цель приостановлена — /goal resume продолжит."
				}
				if errors.Is(runErr, ErrUncertain) || outcome.Status == "uncertain" {
					key = "job-" + job.ID + "-uncertain"
					message = "Исход задачи пока неизвестен. Повторный запуск может продублировать действие. ID: " + job.ID
					if outcome.RunID != "" && g.config.RuntimeURL != "" {
						message = "Проверяю исходную задачу после потери соединения. ID: " + job.ID
					}
				}
				// Routines dedupe owner-facing errors per schedule: an identical
				// failure on every tick notifies once until a success resets it.
				deliver := true
				if errors.Is(runErr, ErrUncertain) || outcome.Status == "uncertain" {
					// SPEC-0053 R3.2: while the reconciliation window is still
					// open the owner is not told the outcome is unknown — the
					// reconciler or a named cause settles it first. The window
					// is anchored at the first uncertain outcome, not the
					// latest re-observation attempt.
					if mapping, ok, merr := g.spool.Mapping(job.ID); merr == nil && ok {
						since := mapping.UncertainSince
						if since.IsZero() {
							since = mapping.UpdatedAt
						}
						if g.now().Before(since.Add(hubruntime.RunBudget())) {
							deliver = false
						}
					}
				}
				if deliver && job.Trigger == "cron" {
					if o, ok := g.occurrenceFor(job.ID); ok {
						if fresh, derr := g.spool.RecordRoutineFailure(o.ScheduleID, routineFailureDigest(job.ID, message)); derr == nil {
							deliver = fresh
						}
					}
				}
				if deliver && !retried {
					_ = g.spool.EnqueueDelivery(Delivery{ID: key, IdempotencyKey: key, JobID: job.ID, Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, Text: message, CreatedAt: g.now().UTC()})
				}
				if outcome.Status == "" {
					outcome.Status = "failed"
				}
			} else {
				_ = g.spool.RecordOutcome(job.ID, outcome)
				if job.Trigger == "cron" {
					if o, ok := g.occurrenceFor(job.ID); ok {
						_ = g.spool.ClearRoutineFailure(o.ScheduleID)
					}
				}
				_ = g.spool.EnqueueDelivery(Delivery{ID: "job-" + job.ID + "-response", JobID: job.ID, Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, Text: response, Format: formatMarkdown, Artifacts: outcome.Artifacts, Voice: outcome.Voice, CreatedAt: g.now().UTC()})
				_ = g.spool.CompleteJob(job.ID)
				g.maybeAutoTitle(ctx, *job)
				g.goalAfterJob(ctx, *job, response)
			}
			g.recordJob(*job, outcome)
			if job.Sensitive {
				_ = g.api.DeleteMessage(ctx, job.ChatID, job.MessageID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-g.spool.Notify():
		case <-ticker.C:
		}
	}
}

// retryableJobFailure bounds automatic retries to user messages and goal
// turns whose run simply failed; uncertain outcomes reconcile, cancels and
// interrupts are intentional, and cron/prepare continuations report instead.
func retryableJobFailure(job Job, status string, err error) bool {
	if (job.Trigger != "message" && job.Trigger != "goal") || strings.HasSuffix(job.ID, "-r1") {
		return false
	}
	if errors.Is(err, ErrUncertain) || status == "uncertain" || status == "cancelled" || status == "interrupted" {
		return false
	}
	return true
}

// deliveryLoop drains the outbox on the spool's notify signal with a
// one-second fallback tick (SPEC-0053 R2.3).
func (g *Gateway) deliveryLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		g.deliverOne(ctx)
		select {
		case <-ctx.Done():
			return
		case <-g.spool.Notify():
		case <-ticker.C:
		}
	}
}

func (g *Gateway) deliverOne(ctx context.Context) {
	delivery, err := g.spool.ClaimDelivery()
	if err != nil || delivery == nil {
		return
	}
	if delivery.ApprovalJobID != "" || len(delivery.Buttons) > 0 {
		mapping, found, mapErr := g.spool.Mapping(delivery.ApprovalJobID)
		if mapErr != nil || !found {
			_ = g.spool.UncertainDelivery(delivery.ID)
			return
		}
		if terminalStatus(mapping.Status) || mapping.CancelRequested || mapping.ApprovalID != delivery.ApprovalID || mapping.ApprovalState != "pending" || !g.now().Before(mapping.ApprovalDeadline) || mapping.Control != nil {
			_ = g.spool.CompleteDelivery(delivery.ID)
			return
		}
		user := g.user(mapping.PrincipalID)
		current := user.Envelope(1)
		if delivery.ApprovalRunID == "" || delivery.ApprovalGeneration == "" || delivery.ApprovalRunID != mapping.RunID || delivery.ApprovalGeneration != mapping.RuntimeGeneration || delivery.ConversationID != mapping.ConversationID || delivery.DeliveryTargetID != mapping.DeliveryTargetID || !user.Enabled || current.RuntimeID != mapping.RuntimeID || current.PolicyVersion != mapping.PolicyVersion {
			_ = g.spool.UncertainDelivery(delivery.ID)
			return
		}
	}
	if delivery.JobID != "" {
		mapping, found, err := g.spool.Mapping(delivery.JobID)
		if err != nil {
			_ = g.spool.UncertainDelivery(delivery.ID)
			return
		}
		if found {
			if g.config.Supervised {
				holds, holdErr := g.masterHolds(ctx)
				if holdErr != nil {
					delivery.BlockedBy = "control_unavailable"
					delivery.NotBefore = g.now().Add(5 * time.Second)
					if g.spool.UpdateSending(*delivery) == nil {
						_ = g.spool.RetryDelivery(delivery.ID)
					}
					return
				}
				for _, hold := range holds {
					if hold.PrincipalID == mapping.PrincipalID && hold.ContextID == mapping.ContextID && (hold.Active || hold.Epoch != mapping.OperationalEpoch) {
						delivery.BlockedBy = "stale_epoch"
						if hold.Active {
							delivery.BlockedBy = "administrative_hold"
						}
						if g.spool.UpdateSending(*delivery) == nil {
							_ = g.spool.UncertainDelivery(delivery.ID)
						}
						return
					}
				}
			}
			if g.authorizeGroupMapping(mapping) != nil {
				_ = g.spool.UncertainDelivery(delivery.ID)
				return
			}
			user := g.user(mapping.PrincipalID)
			current := user.Envelope(1)
			if !user.Enabled || user.ID != mapping.PrincipalID || current.RuntimeID != mapping.RuntimeID || current.PolicyVersion != mapping.PolicyVersion || (delivery.DeliveryTargetID != "" && delivery.DeliveryTargetID != mapping.DeliveryTargetID) {
				_ = g.spool.UncertainDelivery(delivery.ID)
				return
			}
			if v, ok := g.channel(mapping.Channel).(deliveryTargetVerifier); ok && v != nil {
				if err := v.VerifyDeliveryTarget(delivery, user.ID); err != nil {
					_ = g.spool.UncertainDelivery(delivery.ID)
					return
				}
			}
		}
	}
	if delivery.OccurrenceID != "" {
		g.spool.mu.Lock()
		valid := g.occurrenceRefAuthorizedLocked(delivery.OccurrenceID)
		g.spool.mu.Unlock()
		if !valid {
			_ = g.spool.UncertainDelivery(delivery.ID)
			return
		}
	}
	delivery.Attempts++
	ch := g.channel(delivery.Channel)
	if ch == nil {
		log.Printf("gateway delivery channel=%q job_id=%q delivery_id=%q status=unrouted", delivery.Channel, delivery.JobID, delivery.ID)
		_ = g.spool.UncertainDelivery(delivery.ID)
		return
	}
	sendErr := ch.Deliver(ctx, delivery)
	if errors.Is(sendErr, errDeliveryRetried) {
		return
	}
	if sendErr == nil && ch.Caps().Artifacts && delivery.SentArtifacts < len(delivery.Artifacts) {
		sendErr = g.sendDeliveryArtifacts(ctx, ch, delivery)
	}
	if sendErr != nil {
		log.Printf("gateway delivery channel=%q job_id=%q delivery_id=%q status=uncertain", delivery.Channel, delivery.JobID, delivery.ID)
		// A timeout may have sent the message. Keep it failed so a restart never
		// blindly duplicates an uncertain Telegram delivery.
		_ = g.spool.UncertainDelivery(delivery.ID)
		return
	}
	if delivery.Channel == "telegram_bot" && os.Getenv("HUB_DIAGNOSTICS_ENABLED") != "false" {
		visible := limitTelegramText(delivery.Text)
		if strings.HasSuffix(delivery.ID, "-secret") || strings.Contains(delivery.Text, "/credentials/") || strings.Contains(delivery.Text, "/telegram-auth/") || strings.Contains(delivery.Text, "form_url=") {
			visible = "[credential response redacted]"
		}
		field := g.contentField(visible)
		if delivery.JobID != "" {
			field = fmt.Sprintf("text_len=%d", len(visible))
		}
		log.Printf("gateway telegram-sent job_id=%q delivery_id=%q chat_id=%d %s", delivery.JobID, delivery.ID, delivery.ChatID, field)
	}
	_ = g.spool.CompleteDelivery(delivery.ID)
	if delivery.JobID != "" && (strings.HasSuffix(delivery.ID, "-response") || strings.HasSuffix(delivery.ID, "-error")) {
		// The answer now stands alone; retire the editable status message
		// and close its receipt so late progress cannot resurrect it.
		if chatID, messageID := g.spool.CloseStatusDelivery(delivery.JobID); messageID > 0 {
			_ = g.api.DeleteMessage(ctx, chatID, messageID)
		}
	}
	if delivery.JobID != "" {
		if g.restart != nil {
			request, ok := g.restartRequest(*delivery)
			if ok {
				_ = g.restart(ctx, request)
			}
		} else {
			user := g.userByChatID(delivery.ChatID)
			if user.StateDir != "" {
				_ = signalRestartAfterDelivery(user.StateDir)
			}
		}
	}
}

// sendParts delivers ordered message parts, persisting the sent boundary after
// each part so a failure never silently loses or blindly resends earlier parts.
func (g *Gateway) sendParts(delivery *Delivery, parts []string, send func(string) error) error {
	for i := delivery.SentParts; i < len(parts); i++ {
		if err := send(parts[i]); err != nil {
			_ = g.spool.UpdateSending(*delivery)
			return err
		}
		delivery.SentParts, delivery.PartCount = i+1, len(parts)
		if err := g.spool.UpdateSending(*delivery); err != nil {
			return err
		}
	}
	return nil
}

// sendTelegramMarkdown renders a model-authored delivery to Telegram HTML parts
// and appends the raw source as a document when rendering degraded content.
func (g *Gateway) sendTelegramMarkdown(ctx context.Context, delivery *Delivery) error {
	parts, degraded := renderTelegram(delivery.Text)
	err := g.sendParts(delivery, parts, func(part string) error {
		return g.api.SendMessage(ctx, delivery.ChatID, delivery.TopicID, part, "HTML")
	})
	if err != nil {
		return err
	}
	if degraded && !delivery.SourceSent {
		if err := g.api.SendDocument(ctx, delivery.ChatID, delivery.TopicID, "answer.md", "", []byte(delivery.Text)); err != nil {
			return err
		}
		delivery.SourceSent = true
		if err := g.spool.UpdateSending(*delivery); err != nil {
			return err
		}
	}
	return nil
}

// sendDeliveryArtifacts ships staged run artifacts after the text parts, in
// order, with a durable boundary. A ref with Error still produces one explicit
// notice: a generated file that never arrives is never silent.
func (g *Gateway) sendDeliveryArtifacts(ctx context.Context, ch Channel, delivery *Delivery) error {
	for i := delivery.SentArtifacts; i < len(delivery.Artifacts); i++ {
		if err := ch.DeliverArtifact(ctx, delivery, delivery.Artifacts[i]); err != nil {
			_ = g.spool.UpdateSending(*delivery)
			return err
		}
		delivery.SentArtifacts = i + 1
		if err := g.spool.UpdateSending(*delivery); err != nil {
			return err
		}
	}
	return nil
}

// voicePlan decides what is spoken and whether the voice message replaces
// text delivery. Speech is a per-reply tool: only an upstream VOICE: marker
// requests it (marker-only replies are voice-only, text+marker sends both).
// There is no conversation-level voice mode.
func (g *Gateway) voicePlan(delivery Delivery) (speak string, voiceOnly bool) {
	if speak = strings.TrimSpace(delivery.Voice); speak != "" {
		return speak, strings.TrimSpace(delivery.Text) == ""
	}
	return "", false
}

// sendVoiceReply synthesizes the payload and posts it as a voice message.
// The audio lives only in memory — nothing is stored after sending; the
// text transcript remains the canonical record.
func (g *Gateway) sendVoiceReply(ctx context.Context, delivery Delivery, text string) error {
	if g.synthesizer == nil || strings.TrimSpace(text) == "" {
		return errors.New("tts is not configured")
	}
	if strings.TrimSpace(g.config.TTSUploadURL) != "" {
		// Third-party upload is skipped unless a future explicit upload worker is configured.
		return errors.New("voice upload is not configured")
	}
	audio, _, err := g.synthesizer.Synthesize(ctx, text)
	if err != nil || len(audio) == 0 || len(audio) > mediaSizeLimit {
		log.Printf("gateway voice synth failed delivery_id=%q err=%v bytes=%d", delivery.ID, err, len(audio))
		return errors.New("tts failed")
	}
	if err := g.api.SendVoice(ctx, delivery.ChatID, delivery.TopicID, audio, ""); err != nil {
		log.Printf("gateway voice send uncertain delivery_id=%q", delivery.ID)
		return fmt.Errorf("%w: voice send failed", ErrUncertain)
	}
	return nil
}

func (g *Gateway) recordJob(job Job, outcome RunOutcome) {
	if g == nil {
		return
	}
	status := outcome.Status
	if status == "" {
		status = "completed"
	}
	diagnostics.Logger("gateway").Info("job outcome", "job_id", job.ID, "run_id", outcome.RunID, "principal", job.UserID, "status", status)
}
