package communication

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/shared/locale"
)

// slowObservationInterval paces re-observation of an uncertain job once its
// budget notice is durable: the supervisor settles such runs through its own
// reconcile and expiry sweeps, and the gateway must still accept that result.
const slowObservationInterval = 5 * time.Minute

// RequeueObservations retries observation for uncertain jobs. A durably
// admitted run re-observes by run identity; an admission never confirmed
// (no run binding) is only requeued when reconcileAdmissionUnknown is set —
// that path re-observes through the supervisor's /v1/resume, which replays a
// settled outcome or reports the admission still unknown. Replaying the input
// directly could repeat an action, so the plain-runtime path keeps the old
// run-binding requirement.
func (s *Spool) RequeueObservations(now time.Time, reconcileAdmissionUnknown bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.fsys.ReadDir(filepath.Join(s.root, "failed"))
	if err != nil {
		return err
	}
	for _, name := range entries {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		from := filepath.Join(s.root, "failed", name)
		b, err := s.fsys.ReadFile(from)
		var job Job
		if err != nil || json.Unmarshal(b, &job) != nil {
			return errors.New("invalid recovery job")
		}
		mapping, err := s.loadMappingLocked(job.ID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if mapping.Status != "uncertain" || now.Before(mapping.NextObservationAt) {
			continue
		}
		admitted := mapping.RunID != "" && mapping.SessionID != "" && mapping.RuntimeGeneration != ""
		if mapping.Fingerprint != jobFingerprint(job) || mapping.JobID != job.ID || name != spoolFileID(job.ID)+".json" {
			return errors.New("recovery job binding mismatch")
		}
		// Once the budget notice is durable, further observation cannot
		// notify again and must not consume a worker on the fast backoff.
		// It slows down instead of stopping: an admitted run keeps its
		// conversation lane until the supervisor's terminal result lands.
		pastBudget := !now.Before(mapping.UncertainSince.Add(hubruntime.RunBudget()))
		noticed := false
		if pastBudget {
			for _, dir := range []string{"pending", "sending", "done", "failed"} {
				_, err := s.fsys.Lstat(filepath.Join(s.root, "outbox", dir, spoolFileID("job-"+job.ID+"-uncertain")+".json"))
				if err == nil {
					noticed = true
					break
				}
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
		// The owner was told and the supervisor still has not settled: from
		// here only the operator's `job resolve` frees the lane, so say so
		// once instead of silently observing forever. A rebuilt
		// corrupt-admission job alerts on budget alone — no uncertain
		// notice was ever written for it, and nothing re-parks it.
		if !mapping.OperatorAlerted && (noticed || (mapping.CorruptAdmission && pastBudget)) {
			mapping.OperatorAlerted = true
			if err = s.writeMappingLocked(mapping); err != nil {
				return err
			}
			log.Printf("gateway job_id=%q status=uncertain_stuck action='hubctl job resolve %s'", job.ID, job.ID)
		}
		if !admitted && !reconcileAdmissionUnknown {
			// Without a supervisor there is no idempotent rebind to observe;
			// an unadmitted job is never re-run, it waits for `job resolve`.
			continue
		}
		if noticed && now.Before(mapping.UncertainSince.Add(hubruntime.RunBudget()+slowObservationInterval)) {
			continue
		}
		if mapping.ObservationAttempts < 6 {
			mapping.ObservationAttempts++
		}
		delay := 5 * time.Second
		for attempt := uint64(1); attempt < mapping.ObservationAttempts; attempt++ {
			delay *= 2
			if delay >= 30*time.Second {
				delay = 30 * time.Second
				break
			}
		}
		if noticed {
			delay = slowObservationInterval
		}
		mapping.NextObservationAt = now.Add(delay)
		if err = s.writeMappingLocked(mapping); err != nil {
			return err
		}
		if err = s.renameOwned(from, filepath.Join(s.root, "pending", name)); err != nil {
			return err
		}
	}
	return nil
}

var (
	errResolveNotUncertain = errors.New("job is not uncertain")
	errResolveObserving    = errors.New("job is being observed")
)

// ResolveUncertainJob is the operator's way out of an uncertain job the
// supervisor can no longer settle: it records the operator-named terminal
// status through the ordinary terminal handoff, which releases the
// conversation lane. Only a parked job (failed/) is resolved; one claimed or
// requeued for observation is refused so the operator retries after it.
// notice builds the owner-facing delivery for the loaded job — the operator
// verdict is not silent.
func (s *Spool) ResolveUncertainJob(jobID, status string, notice func(Job) *Delivery) error {
	if status != "failed" && status != "cancelled" && status != "interrupted" {
		return errors.New("invalid resolve status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, err := s.loadMappingLocked(jobID)
	if err != nil {
		return err
	}
	if mapping.Status != "uncertain" {
		return errResolveNotUncertain
	}
	body, err := s.readEventRecordLocked(filepath.Join(s.root, "failed", spoolFileID(jobID)+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return errResolveObserving
	}
	if err != nil {
		return err
	}
	var job Job
	if strictEventJSON(body, &job) != nil || job.ID != jobID {
		return errors.New("invalid uncertain job record")
	}
	var delivery *Delivery
	if notice != nil {
		delivery = notice(job)
	}
	if err := s.recordTerminalOutcomeLocked(job, RunOutcome{Status: status, LastEvent: "run." + status}, delivery); err != nil {
		return err
	}
	s.signal()
	return nil
}

// serveJobResolve is the service-only operator boundary for
// ResolveUncertainJob (hubctl job resolve).
func (g *Gateway) serveJobResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origin forbidden", http.StatusForbidden)
		return
	}
	if !g.serviceControlTokenAccepted(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || d.Decode(new(any)) != io.EOF || body.JobID == "" || len(body.JobID) > 512 {
		http.Error(w, "invalid resolve request", http.StatusBadRequest)
		return
	}
	if body.Status == "" {
		body.Status = "interrupted"
	}
	if body.Status != "failed" && body.Status != "cancelled" && body.Status != "interrupted" {
		http.Error(w, "invalid resolve status", http.StatusBadRequest)
		return
	}
	err := g.spool.ResolveUncertainJob(body.JobID, body.Status, func(job Job) *Delivery {
		lang := locale.LangOf(g.user(job.UserID).WorkspaceDir)
		key := "job-" + job.ID + "-resolved"
		return &Delivery{ID: key, IdempotencyKey: key, JobID: job.ID,
			OwnerPrincipalID: job.PrincipalID, OwnerContextID: job.ContextID,
			Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID,
			ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID,
			SlackChannel: job.SlackChannel, SlackThread: job.SlackThread,
			Text:      locale.T(lang, "job.resolved", failureMessage(lang, body.Status)),
			CreatedAt: g.now().UTC()}
	})
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "job not found", http.StatusNotFound)
		return
	case errors.Is(err, errResolveNotUncertain), errors.Is(err, errResolveObserving):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	default:
		http.Error(w, "resolve failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"job_id": body.JobID, "status": body.Status})
}
