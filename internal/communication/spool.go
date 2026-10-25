package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/routines"
)

// approvalButtonsLocked persists one durable callback record per choice and
// returns the inline-keyboard row. Callback data stays under Telegram's
// 64-byte limit ("a:" + 24 hex chars); the record survives restarts so a tap
// resolves long after the message was sent. s.mu must be held.
func (s *Spool) approvalButtonsLocked(jobID, requestID string, choices []string) ([]DeliveryButton, error) {
	buttons := make([]DeliveryButton, 0, len(choices))
	for _, choice := range choices {
		digest := sha256.Sum256([]byte(jobID + "\x00" + requestID + "\x00" + choice))
		id := hex.EncodeToString(digest[0:12])
		record := approvalCallback{JobID: jobID, RequestID: requestID, Choice: choice}
		if err := atomicJSON(filepath.Join(s.root, "callbacks", id+".json"), record); err != nil {
			return nil, err
		}
		buttons = append(buttons, DeliveryButton{Text: choice, Data: "a:" + id})
	}
	return buttons, nil
}

type approvalCallback struct {
	JobID     string `json:"job_id"`
	RequestID string `json:"request_id"`
	Choice    string `json:"choice"`
}

// ApprovalCallback resolves inline-keyboard callback data to the persisted
// (job, request, choice) tuple. Unknown or malformed data is rejected.
func (s *Spool) ApprovalCallback(data string) (jobID, requestID, choice string, err error) {
	id := strings.TrimPrefix(data, "a:")
	if id == data || len(id) != 24 {
		return "", "", "", errors.New("unknown callback")
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", "", "", errors.New("unknown callback")
		}
	}
	b, err := os.ReadFile(filepath.Join(s.root, "callbacks", id+".json"))
	if err != nil {
		return "", "", "", errors.New("unknown callback")
	}
	var record approvalCallback
	if err := json.Unmarshal(b, &record); err != nil || record.JobID == "" || record.RequestID == "" || record.Choice == "" {
		return "", "", "", errors.New("unknown callback")
	}
	return record.JobID, record.RequestID, record.Choice, nil
}

// Spool is a tiny durable queue for one host. Renames are its state machine.
type Spool struct {
	root   string
	mu     sync.Mutex
	secret map[string]string
	// inFlight maps (principal, context) to the job currently allowed to run;
	// held until the job's mapping reaches a terminal status. In-memory only,
	// rebuilt from durable mappings on startup (ADR-0025).
	inFlight        map[string]string
	proactiveFlight map[string]bool
	activeMappings  map[string]bool // Lazily rebuilt; only this spool's writer changes mappings.
	// notify coalesces wakeups for the worker/control/delivery loops so work
	// starts on enqueue or completion instead of a poll tick (SPEC-0053 R2.3).
	notify chan struct{}
	// claimPolls counts ClaimJob/ClaimDelivery scans; the spec target is an
	// idle control plane — roughly one fallback poll per second, not ten.
	claimPolls   atomic.Int64
	deliverPolls atomic.Int64
	// JobDone, when set, runs asynchronously at each terminal outcome so the
	// ToolHub cell controller can reap task-tier cells keyed by the job.
	JobDone func(jobID, principalID string)
	// probeScript, when set, runs a watcher script probe inside the isolated
	// script runtime: (occurrence, source, fetched input, mount-space flag,
	// prefetched need results). Nil fails the probe — a script probe without
	// the supervisor never runs in-process.
	probeScript func(ctx context.Context, o RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error)
	// rt is the routines engine (internal/routines) sharing this spool's
	// mutex and root; it owns the schedules/occurrences/watchers/digests
	// subtrees and borrows the rest of the spool through its Deps seam.
	rt *routines.Engine
}

// Notify exposes the coalesced wake channel. Buffered with capacity one: a
// pending signal means "look again", duplicates carry no extra information.
func (s *Spool) Notify() <-chan struct{} { return s.notify }

func (s *Spool) signal() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func NewSpool(root string) (*Spool, error) {
	unlock, err := LockSpool(root)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s, err := OpenSpool(root)
	if err != nil {
		return nil, err
	}
	if err := s.recover(); err != nil {
		return nil, err
	}
	return s, nil
}

// LockSpool fences the gateway lifetime and offline writers on one host.
func LockSpool(root string) (func(), error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(root, ".writer.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		_ = lock.Close()
		return nil, errors.New("spool has an active writer; stop the gateway before offline apply")
	}
	return func() { _ = lock.Close() }, nil
}

// OpenSpool opens queue directories without classifying in-progress work.
// Offline mutations must hold LockSpool until every write completes.
func OpenSpool(root string) (*Spool, error) {
	return openSpool(root, true)
}

// InspectSpool constructs query state without creating or repairing files.
// Callers must acquire LockSpool and reopen before any mutation.
func InspectSpool(root string) (*Spool, error) {
	return openSpool(root, false)
}

func openSpool(root string, create bool) (*Spool, error) {
	s := &Spool{root: root, secret: map[string]string{}, inFlight: map[string]string{}, notify: make(chan struct{}, 1)}
	for _, dir := range []string{"pending", "running", "done", "failed", "outbox/pending", "outbox/sending", "outbox/done", "outbox/failed", "mappings", "mappings/by-key", "conversations", "events", "occurrences", "schedules", "tasks", "tasks/current", "tasks/topic", "callbacks", "inbound", "watchers", "digests", "mock-sent", "mock-links", "tmp", "goals"} {
		if !create {
			continue
		}
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return nil, err
		}
	}
	s.rt = routines.NewEngine(root, &s.mu, routines.Deps{
		Hooks:             scheduleDraftHooks,
		EnqueueJob:        s.enqueueLocked,
		EnqueueDelivery:   s.enqueueDeliveryLocked,
		LoadMapping:       s.loadMappingLocked,
		LoadTask:          s.loadTaskLocked,
		ListTasks:         s.listTasksLocked,
		TopicTask:         s.topicTaskLocked,
		WriteDeliveryBlob: s.WriteDeliveryBlob,
		NormalizeDelivery: normalizeDelivery,
		WriteJSONAtomic:   atomicJSON,
		Signal:            s.signal,
		ChannelFor:        func(id string) routines.OccurrenceChannel { return channelFor(nil, id) },
		ProbeScript:       func() routines.ProbeScriptFunc { return s.probeScript },
		SettleEvents:      s.settleEvents,
	})
	return s, nil
}

func (s *Spool) recover() error {
	// A process can die after claiming work. Mapped jobs stay failed/uncertain so
	// a restart never silently replays an external run; legacy jobs remain retryable.
	if err := s.recoverJobs(); err != nil {
		return err
	}
	if err := recoverFiles(filepath.Join(s.root, "outbox", "sending"), filepath.Join(s.root, "outbox", "failed")); err != nil {
		return err
	}
	if err := s.rebuildMappings(); err != nil {
		return err
	}
	if err := s.rebuildIdempotencyIndex(); err != nil {
		return err
	}
	if err := s.recoverStreamEvents(); err != nil {
		return err
	}
	return nil
}

func (s *Spool) recoverJobs() error {
	from, to := filepath.Join(s.root, "running"), filepath.Join(s.root, "pending")
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := entry.Name()
		jobID := strings.TrimSuffix(name, ".json")
		mapping, loadErr := s.loadMappingLocked(jobID)
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			return loadErr // Corrupt admission metadata must never make a claimed job retryable.
		}
		if loadErr == nil {
			if terminalStatus(mapping.Status) {
				dir := "failed"
				if mapping.Status == "completed" {
					dir = "done"
				}
				if err := os.Rename(filepath.Join(from, name), filepath.Join(s.root, dir, name)); err != nil {
					return err
				}
				continue
			}
			mapping.Status, mapping.LastKnownEvent, mapping.UpdatedAt = "uncertain", "run.unknown", time.Now().UTC()
			if err := s.writeMappingLocked(mapping); err != nil {
				return err
			}
			destination := "failed"
			if mapping.RunID != "" && mapping.SessionID != "" {
				destination = "pending"
			} // Observe the admitted run; never resubmit its prompt.
			if err := os.Rename(filepath.Join(from, name), filepath.Join(s.root, destination, name)); err != nil {
				return err
			}
			continue
		}
		if err := os.Rename(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) rebuildMappings() error {
	for _, dir := range []string{"pending", "running", "done", "failed"} {
		entries, err := os.ReadDir(filepath.Join(s.root, dir))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(s.root, dir, entry.Name()))
			if err != nil {
				return err
			}
			var job Job
			if json.Unmarshal(body, &job) != nil || job.IdempotencyKey == "" {
				continue
			}
			if _, err := os.Stat(s.mappingPath(job.ID)); errors.Is(err, os.ErrNotExist) {
				mapping := mappingFromJob(job, job.CreatedAt.UTC())
				mapping.JobID = job.ID
				mapping.Status = map[string]string{"pending": "accepted", "running": "running", "done": "completed", "failed": "failed"}[dir]
				if err := s.writeMappingLocked(mapping); err != nil {
					return err
				}
			}
			if mapping, err := s.loadMappingLocked(job.ID); err == nil && mappingHoldsContext(mapping) {
				s.inFlight[jobContextKey(mapping.PrincipalID, mapping.ContextID)] = job.ID
			}
		}
	}
	return nil
}

// rebuildIdempotencyIndex backfills mappings/by-key for mappings written
// before the index existed (SPEC-0053 R2.3). Afterwards the index is the
// authoritative key→job pointer and the linear scan is a fallback only.
func (s *Spool) rebuildIdempotencyIndex() error {
	entries, err := os.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root, "mappings", entry.Name()))
		if err != nil {
			return err
		}
		var mapping JobMapping
		if json.Unmarshal(b, &mapping) != nil || strings.TrimSpace(mapping.IdempotencyKey) == "" {
			continue
		}
		index := s.idempotencyIndexPath(mapping.IdempotencyKey)
		if _, err := os.Stat(index); err == nil {
			continue
		}
		if err := s.writeIdempotencyIndexLocked(mapping.IdempotencyKey, mapping.JobID); err != nil {
			return err
		}
	}
	return nil
}

// Compact drops terminal spool history older than retention (SPEC-0053 R2.3):
// done records and aged terminal mappings plus their idempotency index
// entries. Non-terminal records and open approvals are never touched.
func (s *Spool) Compact(now time.Time, retention time.Duration) error {
	cutoff := now.Add(-retention)
	s.mu.Lock()
	defer s.mu.Unlock()
	aged := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.ModTime().Before(cutoff)
	}
	if err := s.compactAgedLocked(filepath.Join(s.root, "done"), aged); err != nil {
		return err
	}
	if err := s.compactReceiptsLocked(aged); err != nil {
		return err
	}
	if err := s.compactOccurrencesLocked(now, retention); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.root, "mappings", entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var mapping JobMapping
		if json.Unmarshal(b, &mapping) != nil || mapping.JobID == "" || !terminalStatus(mapping.Status) || mapping.ApprovalState == "pending" || !aged(path) {
			continue
		}
		// A job file that still exists is live state regardless of mapping age.
		live := false
		for _, dir := range []string{"pending", "running"} {
			if _, err := os.Stat(filepath.Join(s.root, dir, spoolFileID(mapping.JobID)+".json")); err == nil {
				live = true
			}
		}
		if live {
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if strings.TrimSpace(mapping.IdempotencyKey) != "" {
			_ = os.Remove(s.idempotencyIndexPath(mapping.IdempotencyKey))
		}
	}
	return nil
}

// compactOccurrencesLocked moves settled, silent detail out of the hot scan.
// Detail stays in an archive for 48 hours; a small receipt keeps the normal
// spool deduplication horizon. Active and dispatched work stays untouched.
func (s *Spool) compactOccurrencesLocked(now time.Time, retention time.Duration) error {
	outcomes, err := s.routineDeliveryStatesLocked()
	if err != nil {
		return err
	}
	dir := filepath.Join(s.root, "occurrences")
	archiveDir := filepath.Join(dir, "archive")
	receiptDir := filepath.Join(dir, "receipts")
	activeDelivery := map[string]bool{}
	for _, outboxDir := range []string{"pending", "sending"} {
		items, err := os.ReadDir(filepath.Join(s.root, "outbox", outboxDir))
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.IsDir() || !strings.HasSuffix(item.Name(), ".json") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(s.root, "outbox", outboxDir, item.Name()))
			if err != nil {
				return err
			}
			var delivery Delivery
			if json.Unmarshal(body, &delivery) != nil {
				return errors.New("invalid live delivery")
			}
			if delivery.OccurrenceID != "" {
				activeDelivery[delivery.OccurrenceID] = true
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var o RoutineOccurrence
		if json.Unmarshal(body, &o) != nil || entry.Name() != occurrenceID(o)+".json" {
			continue
		}
		switch o.State {
		case "no_change", "missed", "blocked", "cancelled", "capped", "skipped", "failed":
		case "dispatched":
			if activeDelivery[o.Job.ID] {
				continue
			}
			live := false
			for _, jobDir := range []string{"pending", "running"} {
				_, statErr := os.Stat(filepath.Join(s.root, jobDir, spoolFileID(o.Job.ID)+".json"))
				if statErr == nil {
					live = true
				}
				if statErr != nil && !os.IsNotExist(statErr) {
					return statErr
				}
			}
			if live {
				continue
			}
			mapping, mappingErr := s.loadMappingLocked(o.Job.ID)
			if mappingErr == nil && !terminalStatus(mapping.Status) {
				continue
			}
			if mappingErr != nil && !os.IsNotExist(mappingErr) {
				continue
			}
		default:
			continue
		}
		if err := os.MkdirAll(receiptDir, 0700); err != nil {
			return err
		}
		if err := os.MkdirAll(archiveDir, 0700); err != nil {
			return err
		}
		receiptPath := filepath.Join(receiptDir, entry.Name())
		if err := atomicJSON(receiptPath, routineHistoryReceipt{ID: o.Job.ID, ScheduleID: o.ScheduleID, Envelope: o.Job.Envelope, Run: s.routineRunLocked(o, outcomes)}); err != nil {
			return err
		}
		if err := os.Chtimes(receiptPath, info.ModTime(), info.ModTime()); err != nil {
			return err
		}
		if err := os.Rename(path, filepath.Join(archiveDir, entry.Name())); err != nil {
			return err
		}
	}
	archives, err := os.ReadDir(archiveDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range archives {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(archiveDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(now.Add(-48 * time.Hour)) {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	receipts, err := os.ReadDir(receiptDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range receipts {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(receiptDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(now.Add(-retention)) {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Spool) compactAgedLocked(dir string, aged func(string) bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if aged(path) {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

// compactReceiptsLocked drops aged event receipts except those whose delivery
// record is still pending or in flight — those are live state, not history.
func (s *Spool) compactReceiptsLocked(aged func(string) bool) error {
	live := map[string]bool{}
	for _, dir := range []string{"outbox/pending", "outbox/sending"} {
		entries, err := os.ReadDir(filepath.Join(s.root, dir))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				live[strings.TrimSuffix(entry.Name(), ".json")] = true
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "events"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.root, "events", entry.Name())
		if !aged(path) {
			continue
		}
		if b, err := os.ReadFile(path); err == nil {
			var receipt streamReceipt
			if json.Unmarshal(b, &receipt) == nil && receipt.Delivery != nil && live[spoolFileID(receipt.Delivery.ID)] {
				continue
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func recoverFiles(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err := os.Rename(filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) Enqueue(job Job) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueueLocked(job)
}
func (s *Spool) enqueueLocked(job Job) (bool, error) {
	// Task binding is resolved at admission from the durable record: the
	// caller-visible envelope stays canonical, the conversation (and so the
	// deterministic Hermes session) is bound only to a task the owner holds.
	if job.TaskID != "" && job.TaskID != defaultTaskID {
		task, err := s.loadTaskLocked(job.PrincipalID, job.TaskID)
		if err != nil {
			return false, errors.New("task binding unavailable")
		}
		if task.ChatID != job.ChatID {
			return false, errors.New("task binding audience mismatch")
		}
		if job.Trigger != "cron" {
			job.Envelope.ConversationID = task.ConversationID
		}
	}
	fingerprint := jobFingerprint(job)
	if strings.TrimSpace(job.IdempotencyKey) != "" {
		if mapping, found, err := s.findIdempotencyLocked(job.IdempotencyKey); err != nil {
			return false, err
		} else if found {
			if mapping.Fingerprint != jobFingerprint(job) {
				return false, errors.New("idempotency key already used with different payload")
			}
			return false, nil
		}
	}
	for _, dir := range []string{"pending", "running", "done", "failed"} {
		if _, err := os.Stat(filepath.Join(s.root, dir, spoolFileID(job.ID)+".json")); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if job.Sensitive {
		s.secret[job.ID] = job.Text
		h := sha256.Sum256([]byte(job.Text))
		job.TextSHA256 = hex.EncodeToString(h[:])
		job.Text = ""
	}
	if err := atomicJSON(filepath.Join(s.root, "pending", spoolFileID(job.ID)+".json"), job); err != nil {
		return false, err
	}
	if strings.TrimSpace(job.IdempotencyKey) != "" {
		mapping := mappingFromJob(job, job.CreatedAt.UTC())
		mapping.Fingerprint = fingerprint
		if err := s.writeMappingLocked(mapping); err != nil {
			return false, err
		}
	}
	s.signal()
	return true, nil
}

func (s *Spool) findIdempotencyLocked(key string) (JobMapping, bool, error) {
	// Constant-time path (SPEC-0053 R2.3): the by-key index points to the
	// owning job; the entry is verified against the mapping it names.
	if b, err := os.ReadFile(s.idempotencyIndexPath(key)); err == nil {
		var index struct {
			JobID string `json:"job_id"`
		}
		if json.Unmarshal(b, &index) == nil && index.JobID != "" {
			if mapping, loadErr := s.loadMappingLocked(index.JobID); loadErr == nil && mapping.IdempotencyKey == key {
				return mapping, true, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return JobMapping{}, false, err
	}
	// Legacy path: linear scan kept for entries the index has not seen; a hit
	// self-heals the index so the scan happens at most once per key.
	entries, err := os.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return JobMapping{}, false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root, "mappings", entry.Name()))
		if err != nil {
			return JobMapping{}, false, err
		}
		var mapping JobMapping
		if err := json.Unmarshal(b, &mapping); err != nil || mapping.JobID == "" {
			return JobMapping{}, false, errors.New("invalid job mapping")
		}
		if mapping.IdempotencyKey == key {
			_ = s.writeIdempotencyIndexLocked(key, mapping.JobID)
			return mapping, true, nil
		}
	}
	return JobMapping{}, false, nil
}

// ClaimPolls reports how many pending-job scans ran; it bounds the fallback
// poll rate for an idle control plane (SPEC-0053 R2.3).
func (s *Spool) ClaimPolls() int64 { return s.claimPolls.Load() }

func (s *Spool) ClaimJob() (*Job, error) {
	s.claimPolls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight == nil {
		s.inFlight = map[string]string{}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "pending"))
	if err != nil {
		return nil, err
	}
	type candidate struct {
		name string
		job  Job
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root, "pending", entry.Name()))
		if err != nil {
			return nil, err
		}
		var job Job
		if err := json.Unmarshal(b, &job); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate{entry.Name(), job})
	}
	var cursor struct {
		Scope string `json:"scope"`
	}
	cursorPath := filepath.Join(s.root, "admission-cursor.json")
	if body, err := os.ReadFile(cursorPath); err == nil {
		if err := json.Unmarshal(body, &cursor); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Rotate scopes durably before prioritizing interactive work within one scope.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i].job, candidates[j].job
		ak, bk := jobContextKey(a.PrincipalID, a.ContextID), jobContextKey(b.PrincipalID, b.ContextID)
		if ak != bk {
			if (ak > cursor.Scope) != (bk > cursor.Scope) {
				return ak > cursor.Scope
			}
			return ak < bk
		}
		if (a.Trigger == "cron") != (b.Trigger == "cron") {
			return a.Trigger != "cron"
		}
		return candidates[i].name < candidates[j].name
	})
	for _, entry := range candidates {
		name := entry.name
		job := entry.job
		pending := filepath.Join(s.root, "pending", name)
		if job.Trigger == "cron" && strings.HasPrefix(job.ID, "routine-") {
			body, err := os.ReadFile(filepath.Join(s.root, "occurrences", spoolFileID(job.ID)+".json"))
			var o RoutineOccurrence
			if err != nil || json.Unmarshal(body, &o) != nil || !s.occurrenceCurrentLocked(o) {
				if err := s.updateMappingLocked(job.ID, RunOutcome{Status: "cancelled", LastEvent: "routine.cancelled"}, "cancelled", true); err != nil {
					return nil, err
				}
				if err := os.Rename(pending, filepath.Join(s.root, "failed", name)); err != nil {
					return nil, err
				}
				continue
			}
		}
		key := jobContextKey(job.PrincipalID, job.ContextID)
		if holder, busy := s.inFlight[key]; busy && holder != job.ID {
			continue // Context serialization is per key; other contexts stay claimable.
		}
		to := filepath.Join(s.root, "running", name)
		cursor.Scope = key
		if err := atomicJSON(cursorPath, cursor); err != nil {
			return nil, err
		}
		if err := os.Rename(pending, to); err != nil {
			return nil, err
		}
		mapping, mappingErr := s.loadMappingLocked(job.ID)
		if mappingErr != nil && job.IdempotencyKey != "" {
			// Routing and cancellation state must be readable before executing work.
			return nil, mappingErr
		}
		if mappingErr == nil && terminalStatus(mapping.Status) {
			dir := "failed"
			if mapping.Status == "completed" {
				dir = "done"
			}
			if err := os.Rename(to, filepath.Join(s.root, dir, name)); err != nil {
				return nil, err
			}
			continue
		}
		if mappingErr == nil && mapping.RunID != "" && mapping.SessionID != "" {
			job.Text = ""
		} else if job.Sensitive {
			job.Text = s.secret[job.ID]
			if job.Text == "" {
				_ = os.Rename(to, filepath.Join(s.root, "failed", name))
				return nil, errors.New("sensitive job payload unavailable after restart")
			}
		}
		if strings.TrimSpace(job.IdempotencyKey) != "" {
			if err := s.updateMappingLocked(job.ID, RunOutcome{JobID: job.ID, Status: "running", LastEvent: "job.claimed"}, "running", false); err != nil {
				return nil, err
			}
		}
		s.inFlight[key] = job.ID
		return &job, nil
	}
	return nil, nil
}

func (s *Spool) CompleteJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseInFlightLocked(id)
	if err := os.Rename(filepath.Join(s.root, "running", spoolFileID(id)+".json"), filepath.Join(s.root, "done", spoolFileID(id)+".json")); err != nil {
		return err
	}
	if _, err := s.loadMappingLocked(id); err == nil {
		if err := s.updateMappingLocked(id, RunOutcome{Status: "completed", LastEvent: "job.completed"}, "completed", true); err != nil {
			return err
		}
	}
	// Completion frees the in-flight context: wake a worker for queued work.
	s.signal()
	return nil
}

func (s *Spool) FailJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secret, id)
	fileID := spoolFileID(id)
	if mapping, loadErr := s.loadMappingLocked(id); loadErr == nil && mapping.Status == "completed" {
		s.releaseInFlightLocked(id)
		return os.Rename(filepath.Join(s.root, "running", fileID+".json"), filepath.Join(s.root, "done", fileID+".json"))
	}
	err := os.Rename(filepath.Join(s.root, "running", fileID+".json"), filepath.Join(s.root, "failed", fileID+".json"))
	if mapping, loadErr := s.loadMappingLocked(id); loadErr == nil {
		// terminal outcomes release via updateMappingLocked; uncertain holds.
		status := "failed"
		if mapping.Status == "uncertain" || mapping.Status == "cancelled" || mapping.Status == "interrupted" {
			status = "uncertain"
			if mapping.Status != "uncertain" {
				status = mapping.Status
			}
		}
		if updateErr := s.updateMappingLocked(id, RunOutcome{Status: status, LastEvent: "job." + status}, status, status != "uncertain"); err == nil {
			err = updateErr
		}
	} else {
		s.releaseInFlightLocked(id)
	}
	return err
}
