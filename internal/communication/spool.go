package communication

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/storagebudget"
	"github.com/letya999/hermes-hub/shared/locale"
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
		if err := s.writeJSON(filepath.Join(s.root, "callbacks", id+".json"), record); err != nil {
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
	b, err := s.fsys.ReadFile(filepath.Join(s.root, "callbacks", id+".json"))
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
	readOnly bool
	storage  *storagebudget.Counter
	root     string
	fsys     spoolFS
	mu       sync.Mutex
	secret   map[string]string
	// inFlight maps (principal, context, conversation) to the job currently
	// allowed to run; held until the job's mapping reaches a terminal status.
	// In-memory only, rebuilt from durable mappings on startup (ADR-0090,
	// amends ADR-0025).
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
	// pendingSettles lists claimed jobs whose settle write failed mid-handoff:
	// the sweep retries parking on the next tick instead of holding the
	// conversation lane until a restart. In-memory only — a restart's
	// recoverJobs covers the same files.
	pendingSettles map[string]bool
	// pendingDeliverySettles does the same for outbox deliveries whose
	// uncertain settle failed: the record sits in sending/ until the
	// sweep's retry moves it (a restart's recoverFiles covers the rest).
	pendingDeliverySettles map[string]bool
	// JobDone, when set, runs asynchronously at each terminal outcome so the
	// ToolHub cell controller can reap task-tier cells keyed by the job.
	JobDone func(jobID, principalID string)
	// onAccepted persists the accepted-turn outbox event before enqueue ack.
	onAccepted  func(job Job) error
	onCompleted func(job Job, outcome RunOutcome) error
	// refreshHolds, when set (supervised mode), fetches the supervisor's
	// hold projection at the public enqueue boundary — outside the spool
	// lock. holdsSnapshot reads the last projection under the lock: an
	// active hold suppresses the job, a settled hold stamps its operational
	// epoch so supervisor admission and delivery gating evaluate the epoch
	// the job was queued under — not a zero value that fails every fence
	// (SPEC-0053).
	refreshHolds  func(ctx context.Context) error
	holdsSnapshot func() []masterHold
	// jobFence, when wired by the owning gateway, re-authorizes a cron
	// routine job against its live owner occurrence before the worker
	// executes it — a schedule deleted or re-bound between dispatch and
	// claim retires the pending job instead of running stale work. Nil
	// (pure-spool use) keeps the legacy posture.
	jobFence func(job Job) (live bool, err error)
	// budget is the atomic reservation/settlement ledger (budget.json);
	// budgetBroken marks an unreadable ledger so admission fails closed.
	budget       *budgetLedger
	budgetBroken error
	limits       spoolLimits
	// langOf resolves the job owner's workspace language for user-facing
	// stream texts; nil falls back to the hub default.
	langOf      func(principalID string) locale.Lang
	archiveSync func(*os.File) error // Deterministic durability-fault seam; nil uses file.Sync.
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

func openSpool(root string, create bool) (result *Spool, err error) {
	s := &Spool{readOnly: !create, root: root, fsys: osFS{}, secret: map[string]string{}, inFlight: map[string]string{}, notify: make(chan struct{}, 1), budget: &budgetLedger{Reservations: map[string]reservation{}, Scopes: map[string]*scopeBudget{}}, limits: limitsFromEnv(), pendingSettles: map[string]bool{}, pendingDeliverySettles: map[string]bool{}}
	// A failed open must not leak the activated backend's database handle.
	defer func() {
		if err != nil {
			_ = s.fsys.Close()
		}
	}()
	if spoolActivated(root) {
		fsys, err := openSpoolDB(root, !create)
		if err != nil {
			return nil, err
		}
		s.fsys = fsys
	}
	limits, limitsErr := spoolStorageLimits(s.limits)
	if limitsErr != nil {
		return nil, limitsErr
	}
	if ledger, err := loadBudget(s.fsys, root); err != nil {
		s.budgetBroken = err
	} else {
		s.budget = ledger
	}
	if create {
		if body, err := readEventRecord(filepath.Join(root, "event-layout.json")); err == nil {
			var layout eventLayout
			if strictEventJSON(body, &layout) != nil || layout.Version != eventLayoutVersion {
				return nil, errors.New("unknown event layout")
			}
			if layout.Complete {
				for _, dir := range []string{"event-state", "run-events", "provider-dedup"} {
					info, err := os.Lstat(filepath.Join(root, dir))
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
						return nil, errors.New("initialized event directory missing or unsafe")
					}
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	for _, dir := range []string{"pending", "running", "done", "failed", "outbox/pending", "outbox/sending", "outbox/done", "outbox/failed", "outbox/superseded", "mappings", "mappings/by-key", "conversations", "event-state", "run-events", "provider-dedup", "tasks", "tasks/current", "tasks/topic", "callbacks", "inbound", "mock-sent", "mock-links", "tmp", "goals", "capture", "capture-acks"} {
		if !create {
			continue
		}
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return nil, err
		}
	}
	_, rootErr := os.Lstat(root)
	if create || !errors.Is(rootErr, os.ErrNotExist) {
		var storageErr error
		s.storage, storageErr = storagebudget.New(root, limits)
		if storageErr != nil {
			return nil, storageErr
		}
	}
	if create {
		for _, dir := range []string{"event-state", "run-events", "provider-dedup"} {
			info, err := os.Lstat(filepath.Join(root, dir))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("unsafe event directory")
			}
		}
		if err := s.migrateEventLayout(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Spool) recover() error {
	// A process can die after claiming work. Mapped jobs stay failed/uncertain so
	// a restart never silently replays an external run; legacy jobs remain retryable.
	if err := s.recoverJobs(); err != nil {
		return err
	}
	if err := s.recoverFiles(filepath.Join(s.root, "outbox", "sending"), filepath.Join(s.root, "outbox", "failed")); err != nil {
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
	if err := s.settleRecoveredAdmissions(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconcileBudgetLocked()
}

// settleRecoveredAdmissions finishes the corrupt-admission rebuild once the
// stream ledger has replayed. The replay may restore a run binding and
// rewind the status to a live event ("running", "waiting_for_approval") —
// the binding stays (it lets observation reattach), but the status goes
// back to uncertain: the outcome is still unknown and a live status in
// failed/ would wedge the job outside every observation path.
func (s *Spool) settleRecoveredAdmissions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.fsys.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		body, err := s.fsys.ReadFile(filepath.Join(s.root, "mappings", name))
		if err != nil {
			return err
		}
		var mapping JobMapping
		if err := json.Unmarshal(body, &mapping); err != nil || mapping.JobID == "" {
			continue // Unreadable entries stay for the operator; recovery must not brick on them.
		}
		if !mapping.CorruptAdmission || terminalStatus(mapping.Status) || mapping.Status == "uncertain" {
			continue
		}
		mapping.Status, mapping.LastKnownEvent = "uncertain", "run.unknown"
		if err := s.writeMappingLocked(mapping); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) recoverJobs() error {
	from, to := filepath.Join(s.root, "running"), filepath.Join(s.root, "pending")
	names, err := s.fsys.ReadDir(from)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		jobID := strings.TrimSuffix(name, ".json")
		mapping, loadErr := s.loadMappingLocked(jobID)
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			// Corrupt admission metadata must never make a claimed job
			// retryable — and must not brick the spool either: rebuild the
			// mapping from the running job record (the same source the
			// admission wrote it from) and park the job uncertain.
			_, loadErr = s.rebuildMappingFromJobLocked(filepath.Join(from, name))
			if loadErr != nil {
				if qerr := s.quarantineLocked(s.mappingPath(jobID), filepath.Join(from, name)); qerr != nil {
					return qerr
				}
				log.Printf("spool quarantined unrecoverable job record %s: %v", name, loadErr)
				continue
			}
			// A rebuilt mapping parks in failed/ like a bound run: admission
			// state is unverifiable, so the claim path must never re-run it.
			if err := s.renameOwned(filepath.Join(from, name), filepath.Join(s.root, "failed", name)); err != nil {
				return err
			}
			continue
		}
		if loadErr == nil {
			if err := s.parkClaimedLocked(mapping, "running", name); err != nil {
				return err
			}
			continue
		}
		if err := s.renameOwned(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			return err
		}
	}
	return nil
}

// rebuildMappingFromJobLocked rebuilds a claimed job's admission metadata
// from its running record — the same record admission wrote it from — and
// parks it uncertain for observation. It never requeues: a claimed job is
// never made retryable.
func (s *Spool) rebuildMappingFromJobLocked(path string) (JobMapping, error) {
	body, err := s.fsys.ReadFile(path)
	if err != nil {
		return JobMapping{}, err
	}
	var job Job
	if err := json.Unmarshal(body, &job); err != nil || job.ID == "" {
		return JobMapping{}, errors.New("invalid running job record")
	}
	mapping := mappingFromJob(job, job.CreatedAt.UTC())
	mapping.Status = "uncertain"
	mapping.LastKnownEvent = "run.unknown"
	mapping.UncertainSince = time.Now().UTC()
	mapping.CorruptAdmission = true
	if err := s.writeMappingLocked(mapping); err != nil {
		return JobMapping{}, err
	}
	return mapping, nil
}

// quarantineLocked moves unreadable spool records out of their hot
// directories so recovery continues past them; the files stay on disk for
// the operator under <dir>/quarantine/.
func (s *Spool) quarantineLocked(paths ...string) error {
	for _, path := range paths {
		if _, err := s.fsys.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		target := filepath.Join(filepath.Dir(path), "quarantine", filepath.Base(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := s.renameOwned(path, target); err != nil {
			return err
		}
	}
	return nil
}

// parkClaimedLocked gives a claimed job whose outcome is unknown the
// restart-recovery settlement. A durable terminal mapping moves to its
// terminal dir; otherwise the mapping turns uncertain with the observation
// window anchored and the file parks in pending/ (an admitted run can still
// be observed — the prompt is never resubmitted) or failed/ where
// RequeueObservations scans. The conversation lane stays held.
func (s *Spool) parkClaimedLocked(mapping JobMapping, fromDir, name string) error {
	if terminalStatus(mapping.Status) {
		dir := "failed"
		if mapping.Status == "completed" {
			dir = "done"
		}
		return s.renameOwned(filepath.Join(s.root, fromDir, name), filepath.Join(s.root, dir, name))
	}
	mapping.Status, mapping.LastKnownEvent, mapping.UpdatedAt = "uncertain", "run.unknown", time.Now().UTC()
	if mapping.UncertainSince.IsZero() {
		mapping.UncertainSince = mapping.UpdatedAt
	}
	if err := s.writeMappingLocked(mapping); err != nil {
		return err
	}
	destination := "failed"
	if mapping.RunID != "" && mapping.SessionID != "" {
		destination = "pending"
	}
	return s.renameOwned(filepath.Join(s.root, fromDir, name), filepath.Join(s.root, destination, name))
}

// ParkJobUncertain settles a claimed job the runner already reached but
// whose terminal write failed — the same settlement restart recovery gives
// an interrupted claim, so the observation loop, not a false "failed",
// owns the outcome.
func (s *Spool) ParkJobUncertain(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, err := s.loadMappingLocked(id)
	if err != nil {
		return err
	}
	return s.parkClaimedLocked(mapping, "running", spoolFileID(id)+".json")
}

// MarkSettleRetry registers a claimed job whose settle write failed; the
// periodic RetryPendingSettles parks it on the next tick instead of leaving
// the conversation lane held until a restart (the file still sits in
// running/, which recoverJobs would also cover).
func (s *Spool) MarkSettleRetry(jobID string) {
	s.mu.Lock()
	s.pendingSettles[jobID] = true
	s.mu.Unlock()
}

// RetryPendingSettles re-attempts parking for jobs whose settle write
// failed. A success — or a job already moved out of running/ — unmarks it;
// a persistent failure stays marked for the next tick.
func (s *Spool) RetryPendingSettles() int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.pendingSettles))
	for id := range s.pendingSettles {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	settled := 0
	for _, id := range ids {
		err := s.ParkJobUncertain(id)
		if err != nil {
			if _, stat := s.fsys.Lstat(filepath.Join(s.root, "running", spoolFileID(id)+".json")); !errors.Is(stat, os.ErrNotExist) {
				continue
			}
		}
		s.mu.Lock()
		delete(s.pendingSettles, id)
		s.mu.Unlock()
		settled++
	}
	return settled
}

// MarkDeliverySettleRetry registers an outbox delivery whose uncertain
// settle failed; the periodic RetryDeliverySettles moves it on the next
// tick instead of leaving the record claimed in sending/ until a restart.
func (s *Spool) MarkDeliverySettleRetry(id string) {
	s.mu.Lock()
	s.pendingDeliverySettles[id] = true
	s.mu.Unlock()
}

// RetryDeliverySettles re-attempts the sending/→failed/ move for marked
// deliveries. A success — or a record already gone — unmarks it; a
// persistent failure stays marked for the next tick.
func (s *Spool) RetryDeliverySettles() int {
	s.mu.Lock()
	ids := make([]string, 0, len(s.pendingDeliverySettles))
	for id := range s.pendingDeliverySettles {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	settled := 0
	for _, id := range ids {
		err := s.UncertainDelivery(id)
		if err != nil {
			if _, stat := s.fsys.Lstat(filepath.Join(s.root, "outbox", "sending", spoolFileID(id)+".json")); !errors.Is(stat, os.ErrNotExist) {
				continue
			}
		}
		s.mu.Lock()
		delete(s.pendingDeliverySettles, id)
		s.mu.Unlock()
		settled++
	}
	return settled
}

func (s *Spool) rebuildMappings() error {
	for _, dir := range []string{"pending", "running", "done", "failed"} {
		names, err := s.fsys.ReadDir(filepath.Join(s.root, dir))
		if err != nil {
			return err
		}
		for _, name := range names {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			body, err := s.fsys.ReadFile(filepath.Join(s.root, dir, name))
			if err != nil {
				return err
			}
			var job Job
			if json.Unmarshal(body, &job) != nil || job.IdempotencyKey == "" {
				continue
			}
			if _, err := s.fsys.Lstat(s.mappingPath(job.ID)); errors.Is(err, os.ErrNotExist) {
				mapping := mappingFromJob(job, job.CreatedAt.UTC())
				mapping.JobID = job.ID
				mapping.Status = map[string]string{"pending": "accepted", "running": "running", "done": "completed", "failed": "failed"}[dir]
				if err := s.writeMappingLocked(mapping); err != nil {
					return err
				}
			}
			if mapping, err := s.loadMappingLocked(job.ID); err == nil && mappingHoldsContext(mapping) {
				s.inFlight[jobContextKey(mapping.PrincipalID, mapping.ContextID, mapping.ConversationID)] = job.ID
			}
		}
	}
	return nil
}

// rebuildIdempotencyIndex backfills mappings/by-key for mappings written
// before the index existed (SPEC-0053 R2.3). Afterwards the index is the
// authoritative key→job pointer and the linear scan is a fallback only.
func (s *Spool) rebuildIdempotencyIndex() error {
	names, err := s.fsys.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := s.fsys.ReadFile(filepath.Join(s.root, "mappings", name))
		if err != nil {
			return err
		}
		var mapping JobMapping
		if json.Unmarshal(b, &mapping) != nil || strings.TrimSpace(mapping.IdempotencyKey) == "" {
			continue
		}
		index := s.idempotencyIndexPath(mapping.IdempotencyKey)
		if _, err := s.fsys.Lstat(index); err == nil {
			continue
		}
		if err := s.writeIdempotencyIndexLocked(mapping.IdempotencyKey, mapping.JobID); err != nil {
			return err
		}
	}
	return nil
}

// CompactLimits bound the hot terminal dirs and the compressed archive on top
// of the time-based retention: a burst inside the window still cannot grow
// the spool without bound.
type CompactLimits struct {
	MaxTerminalFiles int   // per terminal dir; overflow archives oldest-first
	ArchiveMaxBytes  int64 // total archive dir; oldest bundles evicted first
	MaxUsageEntries  int   // usage.jsonl lines; 0 = usageJournalMaxEntries
}

// Compact drops terminal spool history older than retention (SPEC-0053 R2.3):
// done records and aged terminal mappings plus their idempotency index
// entries. Non-terminal records and open approvals are never touched.
func (s *Spool) Compact(now time.Time, retention time.Duration, limits CompactLimits) error {
	cutoff := now.Add(-retention)
	s.mu.Lock()
	defer s.mu.Unlock()
	aged := func(path string) bool {
		info, err := s.fsys.Lstat(path)
		return err == nil && info.ModTime().Before(cutoff)
	}
	for _, dir := range []string{"done", filepath.Join("outbox", "done"), filepath.Join("outbox", "failed"), filepath.Join("outbox", "superseded")} {
		if err := s.compactDirLocked(dir, aged, nil, limits.MaxTerminalFiles, now); err != nil {
			return err
		}
	}
	if err := s.compactDirLocked("failed", aged, s.failedTerminalLocked, limits.MaxTerminalFiles, now); err != nil {
		return err
	}
	ackClosed := func(name string) bool {
		body, err := s.fsys.ReadFile(filepath.Join(s.root, "capture-acks", name))
		var eventID string
		if err != nil || json.Unmarshal(body, &eventID) != nil {
			return false
		}
		jobID, ok := strings.CutPrefix(eventID, "turn-")
		if !ok {
			jobID, ok = strings.CutPrefix(eventID, "reply-")
		}
		if !ok || jobID == "" {
			return false
		}
		for _, path := range []string{s.capturePath(eventID), filepath.Join(s.root, "pending", spoolFileID(jobID)+".json"), filepath.Join(s.root, "running", spoolFileID(jobID)+".json"), filepath.Join(s.root, "done", spoolFileID(jobID)+".json"), filepath.Join(s.root, "failed", spoolFileID(jobID)+".json")} {
			if _, err := s.fsys.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return false
			}
		}
		return true
	}
	if err := s.compactDirLocked("capture-acks", aged, ackClosed, limits.MaxTerminalFiles, now); err != nil {
		return err
	}
	live, err := s.liveDeliveryIDsLocked()
	if err != nil {
		return err
	}
	receiptClosed := func(name string) bool {
		b, err := s.fsys.ReadFile(filepath.Join(s.root, "run-events", name))
		var receipt streamReceipt
		if err != nil || json.Unmarshal(b, &receipt) != nil || receipt.JobID == "" || receipt.Position == 0 || receipt.Event.JobID != receipt.JobID || receipt.Event.EventID == "" {
			return false
		}
		if receipt.Delivery != nil && live[spoolFileID(receipt.Delivery.ID)] {
			return false
		}
		mapping, err := s.loadMappingLocked(receipt.JobID)
		return err == nil && terminalStatus(mapping.Status) && mapping.ApprovalState != "pending"
	}
	if err := s.compactDirLocked("run-events", aged, receiptClosed, limits.MaxTerminalFiles, now); err != nil {
		return err
	}
	if err := s.compactUsageLocked(now, retention, limits.MaxUsageEntries); err != nil {
		return err
	}
	mappingNames, err := s.fsys.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return err
	}
	for _, name := range mappingNames {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(s.root, "mappings", name)
		b, err := s.fsys.ReadFile(path)
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
			if _, err := s.fsys.Lstat(filepath.Join(s.root, dir, spoolFileID(mapping.JobID)+".json")); err == nil {
				live = true
			}
		}
		if live {
			continue
		}
		if err := s.archiveTerminalLocked("mappings", name, now); err != nil {
			return err
		}
		if strings.TrimSpace(mapping.IdempotencyKey) != "" {
			_ = s.removeOwned(s.idempotencyIndexPath(mapping.IdempotencyKey))
		}
	}
	return s.purgeArchivesLocked(now, retention, limits.ArchiveMaxBytes)
}

// purgeArchivesLocked drops gzip bundles older than retention and evicts the
// oldest first while the archive exceeds its byte cap.
func (s *Spool) purgeArchivesLocked(now time.Time, retention time.Duration, maxBytes int64) error {
	dir := filepath.Join(s.root, "archive")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type bundle struct {
		name string
		mod  time.Time
		size int64
	}
	var files []bundle
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl.gz") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, bundle{entry.Name(), info.ModTime(), info.Size()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	cutoff := now.Add(-retention)
	for _, f := range files {
		if !f.mod.Before(cutoff) && !(maxBytes > 0 && total > maxBytes) {
			break
		}
		if err := s.removeOwned(filepath.Join(dir, f.name)); err != nil {
			return err
		}
		total -= f.size
	}
	return nil
}

// usageJournalMaxEntries bounds the live metering journal when no explicit
// cap is configured.
const usageJournalMaxEntries = 1 << 16

// compactUsageLocked rewrites usage.jsonl keeping records inside retention
// plus the newest record per session — the delta baseline a still-open
// session needs. Evicted records go to a compressed archive bundle. Over the
// entry cap, oldest non-baselines drop first and oldest baselines last.
func (s *Spool) compactUsageLocked(now time.Time, retention time.Duration, maxEntries int) error {
	if err := s.repairUsageTailLocked(); err != nil {
		return err
	}
	entries, err := s.usageEntriesLocked()
	if err != nil {
		return err
	}
	cutoff := now.Add(-retention)
	baseline := map[int]bool{}
	lastForKey := map[string]int{}
	for i := range entries {
		e := &entries[i]
		if e.Version == 2 && e.SessionID != "" {
			lastForKey[e.ScopeID+"\x00"+e.ContextID+"\x00"+e.SessionID] = i
		}
	}
	for _, i := range lastForKey {
		baseline[i] = true
	}
	var keep, evicted []usageEntry
	keepBaseline := map[int]bool{}
	for i, e := range entries {
		if e.MeasuredAt.Before(cutoff) && !baseline[i] {
			evicted = append(evicted, e)
			continue
		}
		keepBaseline[len(keep)] = baseline[i]
		keep = append(keep, e)
	}
	max := maxEntries
	if max <= 0 {
		max = usageJournalMaxEntries
	}
	if need := len(keep) - max; need > 0 {
		var survivors []usageEntry
		for i, e := range keep {
			if need > 0 && !keepBaseline[i] {
				evicted = append(evicted, e)
				need--
				continue
			}
			survivors = append(survivors, e)
		}
		if len(survivors) > max {
			evicted = append(evicted, survivors[:len(survivors)-max]...)
			survivors = survivors[len(survivors)-max:]
		}
		keep = survivors
	}
	if len(evicted) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, e := range evicted {
		body, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(append(body, '\n'))
	}
	if err := s.appendArchiveLocked("usage-"+now.UTC().Format("20060102")+".jsonl.gz", buf.Bytes()); err != nil {
		return err
	}
	var retained bytes.Buffer
	for _, e := range keep {
		body, err := json.Marshal(e)
		if err != nil {
			return err
		}
		retained.Write(append(body, '\n'))
	}
	return s.writeBlob(usagePath(s.root), retained.Bytes(), true)
}

// failedTerminalLocked reports whether a record under failed/ has no path
// back to observation: its mapping is gone or terminal. Uncertain mappings
// keep the record hot for RequeueObservations.
func (s *Spool) failedTerminalLocked(name string) bool {
	b, err := s.fsys.ReadFile(filepath.Join(s.root, "failed", name))
	var job Job
	if err != nil || json.Unmarshal(b, &job) != nil {
		return false
	}
	mapping, err := s.loadMappingLocked(job.ID)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	return terminalStatus(mapping.Status)
}

// compactDirLocked archives entries older than the aged cutoff and, while the
// dir still exceeds max files, evicts oldest archivable entries first. guard
// filters which files may leave the hot dir at all; nil means all of them.
func (s *Spool) compactDirLocked(dir string, aged func(string) bool, guard func(string) bool, max int, now time.Time) error {
	names, err := s.fsys.ReadDir(filepath.Join(s.root, dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	total := 0
	type candidate struct {
		name string
		mod  time.Time
	}
	var rest []candidate
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if guard != nil && !guard(name) {
			continue
		}
		total++
		path := filepath.Join(s.root, dir, name)
		if aged(path) {
			if err := s.archiveTerminalLocked(dir, name, now); err != nil {
				return err
			}
			total--
			continue
		}
		info, err := s.fsys.Lstat(path)
		if err != nil {
			return err
		}
		rest = append(rest, candidate{name, info.ModTime()})
	}
	if max <= 0 || total <= max || len(rest) == 0 {
		return nil
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].mod.Before(rest[j].mod) })
	evict := total - max
	if evict > len(rest) {
		evict = len(rest)
	}
	for _, c := range rest[:evict] {
		if err := s.archiveTerminalLocked(dir, c.name, now); err != nil {
			return err
		}
	}
	return nil
}

// archiveTerminalLocked appends one record to the day's gzip bundle under
// archive/ then removes the original after the archive is durable and verified.
func (s *Spool) archiveTerminalLocked(dir, name string, now time.Time) error {
	src := filepath.Join(s.root, dir, name)
	info, err := s.fsys.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("unsafe terminal record")
	}
	body, err := s.fsys.ReadFile(src)
	if err != nil {
		return err
	}
	flat := strings.ReplaceAll(dir, string(filepath.Separator), "-")
	if err := s.appendArchiveLocked(flat+"-"+now.UTC().Format("20060102")+".jsonl.gz", append(body, '\n')); err != nil {
		return err
	}
	if err := s.removeOwned(src); err != nil {
		return err
	}
	return syncSpoolDirectory(filepath.Dir(src))
}

// appendArchiveLocked commits an immutable, content-addressed gzip chunk.
// Replaying a source after a crash reuses the same verified chunk.
func (s *Spool) appendArchiveLocked(name string, payload []byte) error {
	if filepath.Base(name) != name || strings.ContainsAny(name, "/\\") {
		return errors.New("invalid archive name")
	}
	dir := filepath.Join(s.root, "archive")
	if err := s.mkdirOwned(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe archive directory")
	}
	archiveRoot, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer archiveRoot.Close()
	sum := sha256.Sum256(payload)
	prefix := strings.TrimSuffix(name, ".jsonl.gz")
	if i := strings.LastIndex(prefix, "-"); i >= 0 {
		if _, err := time.Parse("20060102", prefix[i+1:]); err == nil {
			prefix = prefix[:i]
		}
	}
	bundle := prefix + "-" + hex.EncodeToString(sum[:]) + ".jsonl.gz"
	verify := func(f *os.File) error {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(gz, int64(len(payload))+1))
		closeErr := gz.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !bytes.Equal(data, payload) {
			return errors.New("archive chunk verification failed")
		}
		return nil
	}
	syncFile := func(f *os.File) error {
		if s.archiveSync != nil {
			return s.archiveSync(f)
		}
		return f.Sync()
	}
	if info, err := archiveRoot.Lstat(bundle); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("unsafe archive chunk")
		}
		f, err := archiveRoot.OpenFile(bundle, os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		verifyErr := verify(f)
		if verifyErr == nil {
			verifyErr = syncFile(f)
		}
		closeErr := f.Close()
		if verifyErr != nil {
			return verifyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return syncSpoolDirectory(dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	release, err := s.reserveStorage(int64(len(payload))+int64(len(payload))/100+1024, 1, true)
	if err != nil {
		return err
	}
	defer release()
	f, err := os.CreateTemp(dir, ".archive-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer s.removeOwned(temp)
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	gz := gzip.NewWriter(f)
	if _, err = gz.Write(payload); err != nil {
		_ = gz.Close()
		_ = f.Close()
		return err
	}
	if err = gz.Close(); err != nil {
		_ = f.Close()
		return err
	}
	if err = syncFile(f); err == nil {
		err = verify(f)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = archiveRoot.Rename(filepath.Base(temp), bundle); err != nil {
		return err
	}
	return syncSpoolDirectory(dir)
}

// liveDeliveryIDsLocked lists outbox records still pending or in flight; an
// event receipt stays hot while its delivery is not terminal.
func (s *Spool) liveDeliveryIDsLocked() (map[string]bool, error) {
	live := map[string]bool{}
	for _, dir := range []string{"outbox/pending", "outbox/sending"} {
		names, err := s.fsys.ReadDir(filepath.Join(s.root, dir))
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			live[strings.TrimSuffix(name, ".json")] = true
		}
	}
	return live, nil
}

func (s *Spool) recoverFiles(from, to string) error {
	names, err := s.fsys.ReadDir(from)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if err := s.renameOwned(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) Enqueue(job Job) (bool, error) {
	if s.refreshHolds != nil {
		// The hold projection is I/O — refresh at the boundary before the
		// lock so inner producers and other writers never stall on it.
		if err := s.refreshHolds(context.Background()); err != nil {
			return false, err
		}
	}
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
	if s.holdsSnapshot != nil {
		for _, hold := range s.holdsSnapshot() {
			if hold.PrincipalID != job.PrincipalID || hold.ContextID != job.ContextID {
				continue
			}
			if hold.Active {
				// Administrative hold: the enqueue is accepted but suppressed —
				// the same fence enqueueChannelJob applies before dispatch.
				return true, nil
			}
			job.OperationalEpoch = hold.Epoch
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
		if _, err := s.fsys.Lstat(filepath.Join(s.root, dir, spoolFileID(job.ID)+".json")); err == nil {
			if job.IdempotencyKey != "" {
				confirmed, err := s.jobAdmissionLocked(job)
				if err != nil {
					return false, err
				}
				if !confirmed {
					return false, errors.New("job admission unavailable")
				}
			}

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
	job.CaptureExpected = s.onAccepted != nil
	// Reconcile before reserving: unmapped completions and crash-window
	// orphans release their reservation here, so stale demand never blocks
	// fresh admission.
	if err := s.reconcileBudgetLocked(); err != nil {
		return false, err
	}
	// The ledger reserves before the job file exists: a crash between the two
	// leaves an open reservation reconcile releases — never unpriced work.
	if err := s.reserveLocked(job); err != nil {
		return false, err
	}
	if err := s.writeJSON(filepath.Join(s.root, "pending", spoolFileID(job.ID)+".json"), job); err != nil {
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
	if err := s.captureAcceptedLocked(job); err != nil {
		return false, err
	}
	return true, nil
}

// captureAcceptedLocked persists the outbox event under the spool lock;
// a failure rejects enqueue and startup recovery reconstructs from the job.
func (s *Spool) captureAcceptedLocked(job Job) error {
	if s.onAccepted == nil {
		return nil
	}
	return s.onAccepted(job)
}

func (s *Spool) findIdempotencyLocked(key string) (JobMapping, bool, error) {
	// Constant-time path (SPEC-0053 R2.3): the by-key index points to the
	// owning job; the entry is verified against the mapping it names.
	if b, err := s.fsys.ReadFile(s.idempotencyIndexPath(key)); err == nil {
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
	names, err := s.fsys.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return JobMapping{}, false, err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := s.fsys.ReadFile(filepath.Join(s.root, "mappings", name))
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
	names, err := s.fsys.ReadDir(filepath.Join(s.root, "pending"))
	if err != nil {
		return nil, err
	}
	type candidate struct {
		name   string
		job    Job
		parked time.Time
	}
	var candidates []candidate
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := s.fsys.Lstat(filepath.Join(s.root, "pending", name))
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			continue
		}
		b, err := s.fsys.ReadFile(filepath.Join(s.root, "pending", name))
		if err != nil {
			return nil, err
		}
		var job Job
		if err := json.Unmarshal(b, &job); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate{name, job, info.ModTime()})
	}
	var cursor struct {
		Scope string `json:"scope"`
	}
	cursorPath := filepath.Join(s.root, "admission-cursor.json")
	if body, err := s.fsys.ReadFile(cursorPath); err == nil {
		if err := json.Unmarshal(body, &cursor); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Rotate scopes durably before prioritizing interactive work within one scope.
	// A background job parked longer than agingAfter counts as foreground:
	// steady interactive traffic can never starve scheduled work forever.
	agingAfter := 5 * time.Minute
	now := time.Now().UTC()
	background := func(c candidate) bool {
		return c.job.Trigger == "cron" && now.Sub(c.parked) < agingAfter
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i].job, candidates[j].job
		ak, bk := jobFairKey(a.PrincipalID, a.ContextID), jobFairKey(b.PrincipalID, b.ContextID)
		if ak != bk {
			if (ak > cursor.Scope) != (bk > cursor.Scope) {
				return ak > cursor.Scope
			}
			return ak < bk
		}
		if background(candidates[i]) != background(candidates[j]) {
			return !background(candidates[i])
		}
		return candidates[i].name < candidates[j].name
	})
	for _, entry := range candidates {
		name := entry.name
		job := entry.job
		pending := filepath.Join(s.root, "pending", name)
		if job.Trigger == "cron" && strings.HasPrefix(job.ID, "routine-") && s.jobFence != nil {
			live, fenceErr := s.jobFence(job)
			if fenceErr != nil {
				// Owner unreachable: keep the job pending and retry on the
				// next claim rather than retiring possibly-live work.
				continue
			}
			if !live {
				if err := s.updateMappingLocked(job.ID, RunOutcome{Status: "cancelled", LastEvent: "routine.cancelled"}, "cancelled", true); err != nil {
					return nil, err
				}
				if err := s.renameOwned(pending, filepath.Join(s.root, "failed", name)); err != nil {
					return nil, err
				}
				continue
			}
		}
		// ponytail: lanes share workspace and one browser sidecar; concurrent
		// writes there are last-writer-wins (ADR-0090 addendum). Use per-session
		// workspace views if independent file writes become necessary.
		key := jobContextKey(job.PrincipalID, job.ContextID, job.ConversationID)
		if holder, busy := s.inFlight[key]; busy && holder != job.ID {
			continue // Serialization is per conversation lane; other lanes stay claimable.
		}
		fairKey := jobFairKey(job.PrincipalID, job.ContextID)
		if s.inFlight[key] != job.ID && s.limits.MaxParallelPerContext > 0 && s.contextLanesHeldLocked(fairKey) >= s.limits.MaxParallelPerContext {
			continue // Context at its session cap; the lane waits like a busy key (ADR-0090 R3).
		}
		to := filepath.Join(s.root, "running", name)
		cursor.Scope = fairKey
		if err := s.writeJSON(cursorPath, cursor); err != nil {
			return nil, err
		}
		if err := s.renameOwned(pending, to); err != nil {
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
			if err := s.renameOwned(to, filepath.Join(s.root, dir, name)); err != nil {
				return nil, err
			}
			continue
		}
		if mappingErr == nil && mapping.RunID != "" && mapping.SessionID != "" {
			if job.TextSHA256 == "" {
				sum := sha256.Sum256([]byte(job.Text))
				job.TextSHA256 = hex.EncodeToString(sum[:])
			}
			job.Text = ""
		} else if job.Sensitive {
			job.Text = s.secret[job.ID]
			if job.Text == "" {
				_ = s.renameOwned(to, filepath.Join(s.root, "failed", name))
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
	if err := s.renameOwned(filepath.Join(s.root, "running", spoolFileID(id)+".json"), filepath.Join(s.root, "done", spoolFileID(id)+".json")); err != nil {
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
		return s.renameOwned(filepath.Join(s.root, "running", fileID+".json"), filepath.Join(s.root, "done", fileID+".json"))
	}
	err := s.renameOwned(filepath.Join(s.root, "running", fileID+".json"), filepath.Join(s.root, "failed", fileID+".json"))
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
