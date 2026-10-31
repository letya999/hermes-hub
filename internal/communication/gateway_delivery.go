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
	"github.com/letya999/hermes-hub/internal/locale"
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

// deliveryLang resolves the owning user's language for an outbound record.
// Direct sends map the channel transport address back to the owner;
// unresolvable targets fall back to the operator default.
func (g *Gateway) deliveryLang(d *Delivery) locale.Lang {
	principal := ""
	if d.JobID != "" {
		if m, ok, err := g.spool.Mapping(d.JobID); err == nil && ok {
			principal = m.PrincipalID
		}
	} else if d.Channel == "mock" {
		if owner, err := g.mockLinkOwner(d.ChatID); err == nil {
			principal = owner
		}
	} else if u := g.userByChatID(d.ChatID); u.ID != "" {
		principal = u.ID
	}
	if u := g.user(principal); u.ID != "" && u.WorkspaceDir != "" {
		return locale.LangOf(u.WorkspaceDir)
	}
	return locale.Fallback()
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
			admission, found, admissionErr := g.spool.Mapping(job.ID)
			if admissionErr != nil || !found || !admission.MatchesJob(*job) {
				log.Printf("gateway job_id=%q status=blocked reason=admission_binding", job.ID)
				continue
			}
			if job.Text != "" && g.authorizeGroupMapping(mappingFromJob(*job, g.now())) != nil {
				log.Printf("gateway job_id=%q channel=%q status=denied reason=group_authorization", job.ID, job.Channel)
				if err := g.spool.RecordTerminalOutcome(*job, RunOutcome{Status: "cancelled", LastEvent: "run.cancelled"}, nil); err != nil {
					log.Printf("gateway job_id=%q status=uncertain reason=terminal_handoff", job.ID)
				}
				continue
			}
			if job.Trigger == "cron" && job.Text != "" && g.authorizeOccurrence(*job) != nil {
				if err := g.spool.RecordTerminalOutcome(*job, RunOutcome{Status: "cancelled", LastEvent: "run.cancelled"}, nil); err != nil {
					log.Printf("gateway job_id=%q status=uncertain reason=terminal_handoff", job.ID)
				}
				continue
			}
			if job.Trigger == "goal" && !g.goalTurnClaimable(*job) {
				// A turn queued under a superseded epoch (or a cleared/paused
				// goal) no-ops: the model is never invoked for a goal that
				// already moved on (SPEC-0074).
				if err := g.spool.RecordTerminalOutcome(*job, RunOutcome{Status: "cancelled", LastEvent: "run.cancelled"}, nil); err != nil {
					log.Printf("gateway job_id=%q status=uncertain reason=terminal_handoff", job.ID)
				}
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
			if runErr == nil && outcome.Status != "" && outcome.Status != "completed" {
				if terminalStatus(outcome.Status) {
					runErr = errors.New("run ended without completion")
				} else {
					runErr = ErrUncertain
				}
			}
			if runErr != nil || outcome.Status == "uncertain" {
				if mapping, found, err := g.spool.Mapping(job.ID); err == nil && found && terminalStatus(mapping.Status) {
					saved, saveErr := g.spool.savedTerminalOutcome(mapping)
					if saveErr != nil {
						log.Printf("gateway job_id=%q status=uncertain reason=terminal_source", job.ID)
						continue
					}
					outcome = saved
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
				if errors.Is(runErr, ErrUncertain) || outcome.Status == "uncertain" {
					outcome.Status, outcome.LastEvent = "uncertain", "run.unknown"
				} else if outcome.Status == "" {
					outcome.Status, outcome.LastEvent = "failed", "run.failed"
				}
				// A goal turn that failed for good must not leave the record
				// "active" with nothing queued — that zombie reports live in
				// /goal status while the loop never runs again.
				goalPaused := false
				if job.Trigger == "goal" && !errors.Is(runErr, ErrUncertain) && outcome.Status != "uncertain" {
					goalPaused = g.pauseGoalAfterRunFailure(*job)
				}
				lang := locale.LangOf(g.user(job.UserID).WorkspaceDir)
				key, message := "job-"+job.ID+"-error", failureMessage(lang, job.ID, outcome.Status)
				if goalPaused {
					message += " " + locale.T(lang, "job.goal-paused")
				}
				if errors.Is(runErr, ErrUncertain) || outcome.Status == "uncertain" {
					key = "job-" + job.ID + "-uncertain"
					message = locale.T(lang, "job.uncertain", job.ID)
					if outcome.RunID != "" && g.config.RuntimeURL != "" {
						message = locale.T(lang, "job.recheck", job.ID)
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
							since = g.now()
						}
						if g.now().Before(since.Add(hubruntime.RunBudget())) {
							deliver = false
						}
					}
				}
				if deliver && job.Trigger == "cron" {
					if o, ok := g.occurrenceFor(job.ID); ok {
						// The pending marker suppresses duplicates only while a
						// notice is in flight; a notice that dies unconfirmed
						// releases it and the delivered digest is recorded by
						// deliverOne once the notice reaches the owner.
						if fresh, ferr := g.spool.MarkRoutineFailurePending(o.ScheduleID, routineFailureDigest(job.ID, message)); ferr == nil {
							deliver = fresh
						}
					}
				}
				var delivery *Delivery
				if deliver {
					delivery = &Delivery{ID: key, IdempotencyKey: key, JobID: job.ID, Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, Text: message, CreatedAt: g.now().UTC()}
				}
				if err := g.spool.RecordTerminalOutcome(*job, outcome, delivery); err != nil {
					log.Printf("gateway job_id=%q status=uncertain reason=terminal_handoff", job.ID)
					continue
				}

			} else {
				var delivery *Delivery
				if job.AssignmentID == "" || strings.TrimSpace(response) != "NO_ACTIONABLE_EVENT" {
					delivery = &Delivery{ID: "job-" + job.ID + "-response", JobID: job.ID, Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, Text: response, Format: formatMarkdown, Artifacts: outcome.Artifacts, Voice: outcome.Voice, CreatedAt: g.now().UTC()}
				}
				if err := g.spool.RecordTerminalOutcome(*job, outcome, delivery); err != nil {
					log.Printf("gateway job_id=%q status=uncertain reason=terminal_handoff", job.ID)
					continue
				}
				if job.Trigger == "cron" {
					if o, ok := g.occurrenceFor(job.ID); ok {
						_ = g.spool.ClearRoutineFailure(o.ScheduleID)
					}
				}
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

// uncertainDelivery settles a claimed delivery as failed-unconfirmed; for a
// routine failure notice it also releases the schedule's in-flight digest so
// the next identical failure notifies again. The delivered digest is never
// touched — only the pending marker, and only while it still names this
// exact failure.
func (g *Gateway) uncertainDelivery(d Delivery) {
	if err := g.spool.UncertainDelivery(d.ID); err != nil {
		return
	}
	var scheduleID, digest string
	switch {
	case d.JobID != "" && (strings.HasSuffix(d.ID, "-error") || strings.HasSuffix(d.ID, "-uncertain")):
		o, ok := g.occurrenceFor(d.JobID)
		if !ok || o.Job.Trigger != "cron" {
			return
		}
		scheduleID, digest = o.ScheduleID, routineFailureDigest(d.JobID, d.Text)
	case d.JobID == "" && d.OccurrenceID != "" && strings.HasPrefix(d.ID, "watch-fail-"):
		o, ok := g.occurrenceFor(d.OccurrenceID)
		if !ok {
			return
		}
		scheduleID, digest = o.ScheduleID, routineFailureDigest(d.OccurrenceID, d.Text)
	default:
		return
	}
	_ = g.spool.ReleaseRoutineFailurePending(scheduleID, digest)
}

func (g *Gateway) deliverOne(ctx context.Context) {
	delivery, err := g.spool.ClaimDelivery()
	if err != nil || delivery == nil {
		return
	}
	delivery.Lang = g.deliveryLang(delivery)
	_ = g.spool.UpdateSending(*delivery)
	if delivery.ApprovalJobID != "" || (len(delivery.Buttons) > 0 && delivery.Kind == "") {
		mapping, found, mapErr := g.spool.Mapping(delivery.ApprovalJobID)
		if mapErr != nil || !found {
			g.uncertainDelivery(*delivery)
			return
		}
		if terminalStatus(mapping.Status) || mapping.CancelRequested || mapping.ApprovalID != delivery.ApprovalID || mapping.ApprovalState != "pending" || !g.now().Before(mapping.ApprovalDeadline) || mapping.Control != nil {
			_ = g.spool.CompleteDelivery(delivery.ID)
			return
		}
		user := g.user(mapping.PrincipalID)
		current := user.Envelope(1)
		if delivery.ApprovalRunID == "" || delivery.ApprovalGeneration == "" || delivery.ApprovalRunID != mapping.RunID || delivery.ApprovalGeneration != mapping.RuntimeGeneration || delivery.ConversationID != mapping.ConversationID || delivery.DeliveryTargetID != mapping.DeliveryTargetID || !user.Enabled || current.RuntimeID != mapping.RuntimeID || current.PolicyVersion != mapping.PolicyVersion {
			g.uncertainDelivery(*delivery)
			return
		}
	}
	if delivery.JobID != "" {
		mapping, found, err := g.spool.Mapping(delivery.JobID)
		if err != nil {
			g.uncertainDelivery(*delivery)
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
							g.uncertainDelivery(*delivery)
						}
						return
					}
				}
			}
			if g.authorizeGroupMapping(mapping) != nil {
				g.uncertainDelivery(*delivery)
				return
			}
			user := g.user(mapping.PrincipalID)
			current := user.Envelope(1)
			if !user.Enabled || user.ID != mapping.PrincipalID || current.RuntimeID != mapping.RuntimeID || current.PolicyVersion != mapping.PolicyVersion || (delivery.DeliveryTargetID != "" && delivery.DeliveryTargetID != mapping.DeliveryTargetID) {
				g.uncertainDelivery(*delivery)
				return
			}
			if v, ok := g.channel(mapping.Channel).(deliveryTargetVerifier); ok && v != nil {
				if err := v.VerifyDeliveryTarget(delivery, user.ID); err != nil {
					g.uncertainDelivery(*delivery)
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
			g.uncertainDelivery(*delivery)
			return
		}
	}
	delivery.Attempts++
	ch := g.channel(delivery.Channel)
	if ch == nil {
		log.Printf("gateway delivery channel=%q job_id=%q delivery_id=%q status=unrouted", delivery.Channel, delivery.JobID, delivery.ID)
		g.uncertainDelivery(*delivery)
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
		g.uncertainDelivery(*delivery)
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
	// A routine failure notice earns its dedupe digest only once it reached
	// the owner: notices that die unconfirmed never suppress the next
	// identical failure.
	if delivery.JobID != "" && (strings.HasSuffix(delivery.ID, "-error") || strings.HasSuffix(delivery.ID, "-uncertain")) {
		if o, ok := g.occurrenceFor(delivery.JobID); ok && o.Job.Trigger == "cron" {
			_, _ = g.spool.RecordRoutineFailure(o.ScheduleID, routineFailureDigest(delivery.JobID, delivery.Text))
		}
	}
	// Watch-fail notices carry the occurrence id instead of a routine JobID.
	if delivery.JobID == "" && delivery.OccurrenceID != "" && strings.HasPrefix(delivery.ID, "watch-fail-") {
		if o, ok := g.occurrenceFor(delivery.OccurrenceID); ok && o.Job.Trigger == "cron" {
			_, _ = g.spool.RecordRoutineFailure(o.ScheduleID, routineFailureDigest(delivery.OccurrenceID, delivery.Text))
		}
	}
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
	audio, _, err := g.synthesizer.Synthesize(ctx, delivery.Lang, text)
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
