package routines

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// concurrentDeps records cross-domain work the way the real owner does:
// every callback serializes itself, because after the lock split the engine
// calls owner ports from inside its own critical section.
type concurrentDeps struct {
	mu         sync.Mutex
	jobs       map[string]commodel.Job
	jobOrder   []string
	deliveries map[string]commodel.Delivery
	mappings   map[string]commodel.JobMapping
	signals    int
}

func newConcurrentDeps() *concurrentDeps {
	return &concurrentDeps{
		jobs:       make(map[string]commodel.Job),
		deliveries: make(map[string]commodel.Delivery),
		mappings:   make(map[string]commodel.JobMapping),
	}
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	encErr := json.NewEncoder(f).Encode(value)
	syncErr := f.Sync()
	closeErr := f.Close()
	switch {
	case encErr != nil:
		return encErr
	case syncErr != nil:
		return syncErr
	case closeErr != nil:
		return closeErr
	}
	return os.Rename(tmp, path)
}

func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names, nil
}

func (d *concurrentDeps) deps() Deps {
	return Deps{
		Hooks: DraftHooks{
			Channel: func(commodel.Schedule) error { return nil },
			Script:  func(string) error { return nil },
		},
		Storage: StoragePort{
			WriteJSONAtomic: writeJSONAtomic,
			WriteBytes: func(path string, body []byte) error {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					return err
				}
				return os.WriteFile(path, body, 0600)
			},
			MkdirAll: func(path string, _ bool) error { return os.MkdirAll(path, 0700) },
			Rename:   os.Rename,
			Remove:   os.Remove,
			ReadFile: os.ReadFile,
			ReadDir:  readDirNames,
			Lstat:    os.Lstat,
		},
		Dispatch: DispatchPort{
			EnqueueJob: func(j commodel.Job) (bool, error) {
				// The owner takes its own lock here; under the old shared
				// mutex the engine's critical section WAS this lock.
				d.mu.Lock()
				defer d.mu.Unlock()
				if _, ok := d.jobs[j.ID]; ok {
					return false, nil
				}
				d.jobs[j.ID] = j
				d.jobOrder = append(d.jobOrder, j.ID)
				d.mappings[j.IdempotencyKey] = commodel.JobMapping{IdempotencyKey: j.IdempotencyKey, JobID: j.ID}
				return true, nil
			},
			LoadMapping: func(key string) (commodel.JobMapping, error) {
				d.mu.Lock()
				defer d.mu.Unlock()
				m, ok := d.mappings[key]
				if !ok {
					return commodel.JobMapping{}, os.ErrNotExist
				}
				return m, nil
			},
			LoadTask: func(string, string) (commodel.Task, error) {
				return commodel.Task{}, os.ErrNotExist
			},
		},
		Delivery: DeliveryPort{
			EnqueueDelivery: func(dv commodel.Delivery) error {
				d.mu.Lock()
				defer d.mu.Unlock()
				if _, ok := d.deliveries[dv.ID]; ok {
					return nil
				}
				d.deliveries[dv.ID] = dv
				return nil
			},
			NormalizeDelivery: func(dv commodel.Delivery) (commodel.Delivery, error) { return dv, nil },
		},
		Signal:     func() { d.mu.Lock(); d.signals++; d.mu.Unlock() },
		ChannelFor: func(string) OccurrenceChannel { return stubChannel{} },
	}
}

func engineDirs(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{"occurrences", "schedules", "watchers", "events", "reminders", "assignments", "failures", "digests", "cancelled", "acquire"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
}

func mustCreateSchedule(t *testing.T, e *Engine, env identity.Envelope, id, kind string, at time.Time) commodel.Schedule {
	t.Helper()
	item, err := e.CreateScheduleAt(commodel.Schedule{
		ScheduleID: id,
		Envelope:   env,
		UserID:     env.PrincipalID,
		ActorID:    env.PrincipalID,
		ScopeID:    "user:" + env.PrincipalID,
		Channel:    "telegram_bot",
		ChatID:     11,
		Timezone:   "UTC",
		Expression: "every:1m",
		Input:      "work " + id,
		JobKind:    kind,
		Enabled:    true,
	}, env, "", nil, at)
	if err != nil {
		t.Fatalf("CreateScheduleAt(%s): %v", id, err)
	}
	return item
}

// The engine must not borrow the caller's lock: a mutex the engine was
// never constructed with may stay held while every engine operation runs.
// Under the pre-split contract these calls serialized on — and would
// deadlock against — the foreign lock.
func TestRoutineEngineIndependentLock(t *testing.T) {
	root := t.TempDir()
	engineDirs(t, root)
	d := newConcurrentDeps()
	e := NewEngine(root, d.deps())

	var foreign sync.Mutex
	foreign.Lock()
	defer foreign.Unlock()

	env := identity.TelegramEnvelope("alice", 11, "rt-1", "policy-1")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		item, err := e.CreateScheduleAt(commodel.Schedule{
			ScheduleID: "ind-1", Envelope: env, UserID: "alice", ActorID: "alice",
			ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC",
			Expression: "every:1m", Input: "work", JobKind: "agent", Enabled: true,
		}, env, "", nil, now)
		if err != nil {
			done <- fmt.Errorf("CreateScheduleAt: %w", err)
			return
		}
		tick := now.Add(2 * time.Minute)
		if err := e.TickSchedules(tick, "", nil); err != nil {
			done <- fmt.Errorf("TickSchedules: %w", err)
			return
		}
		if err := e.DispatchDueOccurrences(tick, func(commodel.Job) error { return nil }); err != nil {
			done <- fmt.Errorf("DispatchDueOccurrences: %w", err)
			return
		}
		if err := e.PauseSchedule(item.ScheduleID, env); err != nil {
			done <- fmt.Errorf("PauseSchedule: %w", err)
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("engine work blocked behind a foreign mutex")
	}
}

// Concurrent wake reservation + schedule mutation + tick: every public
// mutator must serialize on the engine lock, so racing
// reserve/settle/complete operations never tear a durable record — every
// distinct reservation must persist, and no record may lose an update.
// Storage reads are artificially widened so a missing lock shows up as a
// lost reservation instead of a flaky pass.
func TestRoutineConcurrentCompletionAndTick(t *testing.T) {
	root := t.TempDir()
	engineDirs(t, root)
	d := newConcurrentDeps()

	baseRead := d.deps().Storage.ReadFile
	var ownerMu sync.Mutex // stands in for the owner's own lock
	deps := d.deps()
	deps.Storage.ReadFile = func(path string) ([]byte, error) {
		body, err := baseRead(path)
		if strings.Contains(path, "assignments") {
			// Widen the read-modify-write window: without the engine lock
			// every racer snapshots the same pre-mutation record.
			time.Sleep(2 * time.Millisecond)
		}
		return body, err
	}
	// Re-entry: a dispatch dep holding the owner lock performs a lock-free
	// engine read — the only safe direction under the split.
	var e *Engine
	deps.Dispatch.EnqueueJob = func(j commodel.Job) (bool, error) {
		ownerMu.Lock()
		defer ownerMu.Unlock()
		if _, err := e.LoadSchedule(j.AssignmentID); err != nil && j.AssignmentID != "" && !os.IsNotExist(err) {
			return false, err
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if _, ok := d.jobs[j.ID]; ok {
			return false, nil
		}
		d.jobs[j.ID] = j
		d.jobOrder = append(d.jobOrder, j.ID)
		return true, nil
	}
	e = NewEngine(root, deps)

	env := identity.TelegramEnvelope("alice", 11, "rt-1", "policy-1")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	a := commodel.Assignment{AssignmentID: "wake-conc", Revision: 1, Envelope: env, State: "active", Plan: &commodel.AssignmentPlan{Revision: 1}, AppliedRev: 1, Grants: commodel.AssignmentGrants{MaxWakesPerDay: 64}, CreatedAt: now, UpdatedAt: now}
	if err := e.atomicJSON(e.AssignmentPath(a.AssignmentID), a); err != nil {
		t.Fatal(err)
	}
	mustCreateSchedule(t, e, env, "conc-1", "agent", now.Add(-2*time.Minute))

	// A durable probing occurrence for the staged-settle racer.
	probing := commodel.RoutineOccurrence{ScheduleID: "conc-1", DueAt: now, State: "probing", Job: commodel.Job{ID: "probe-job-1"}}
	if err := e.atomicJSON(filepath.Join(root, "occurrences", occurrenceID(probing)+".json"), probing); err != nil {
		t.Fatal(err)
	}

	const racers = 8
	var wg sync.WaitGroup
	errs := make(chan error, racers*8)
	for i := 0; i < racers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			op := fmt.Sprintf("op-%d", i)
			ok, err := e.ReserveAssignmentWake(a.AssignmentID, op, now, 1)
			if err != nil {
				errs <- err
				return
			}
			if !ok {
				errs <- fmt.Errorf("reservation %s rejected", op)
				return
			}
			if err := e.SettleAssignmentWake(a.AssignmentID, op, 1); err != nil {
				errs <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			at := now.Add(time.Duration(i) * time.Second)
			if err := e.TickSchedules(at, "", nil); err != nil {
				errs <- err
			}
			if err := e.DispatchDueOccurrences(at, func(commodel.Job) error { return nil }); err != nil {
				errs <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.PauseSchedule("conc-1", env); err != nil && !os.IsNotExist(err) {
				errs <- err
			}
			if _, err := e.UpdateSchedule("conc-1", env, "", fmt.Sprintf("input %d", i), nil); err != nil && !os.IsNotExist(err) {
				errs <- err
			}
			if _, err := e.MarkRoutineFailurePending("conc-1", fmt.Sprintf("digest-%d", i)); err != nil {
				errs <- err
			}
			if err := e.ClearRoutineFailure("conc-1"); err != nil {
				errs <- err
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			// RMW on the same schedule + occurrence records — races the
			// pause/update and settle paths.
			item := probing
			if err := e.ApplyProbeResult(ProbeResult{Occurrence: item}); err != nil {
				errs <- err
			}
			live, err := e.LoadSchedule("conc-1")
			if err != nil && !os.IsNotExist(err) {
				errs <- err
				return
			}
			if err == nil {
				if err := e.BlockSchedule(&live, fmt.Sprintf("race-%d", i), now); err != nil {
					errs <- err
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.LoadSchedule("conc-1"); err != nil && !os.IsNotExist(err) {
				errs <- err
			}
			if _, err := e.LoadWatchState("conc-1"); err != nil && !os.IsNotExist(err) {
				errs <- err
			}
			if _, err := e.TaskProbeValue("alice", "missing"); err != nil && !os.IsNotExist(err) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	// Every reservation must have durably persisted then settled — a lost
	// update leaves an orphan in WakeOperations or drops a charged wake.
	stored, err := e.loadAssignmentLocked(a.AssignmentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Checkpoint.WakeOperations) != 0 {
		t.Fatalf("unsettled reservations leaked: %v", stored.Checkpoint.WakeOperations)
	}
	if stored.Checkpoint.WakesToday != racers {
		t.Fatalf("lost wake charges: %d of %d", stored.Checkpoint.WakesToday, racers)
	}

	// Admitted jobs are unique by id — the engine's dedup fence survives
	// concurrent dispatch.
	d.mu.Lock()
	dupes := 0
	seen := map[string]bool{}
	for _, id := range d.jobOrder {
		if seen[id] {
			dupes++
		}
		seen[id] = true
	}
	d.mu.Unlock()
	if dupes != 0 {
		t.Fatalf("duplicate job admissions: %d", dupes)
	}
}

// A still-probing occurrence transitions exactly once: the first settle
// writes the caller's state, every later attempt reports the occurrence as
// no longer probing instead of overwriting the durable record.
func TestMutateProbingOccurrenceSettlesOnce(t *testing.T) {
	root := t.TempDir()
	engineDirs(t, root)
	d := newConcurrentDeps()
	e := NewEngine(root, d.deps())

	env := identity.TelegramEnvelope("alice", 11, "rt-1", "policy-1")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mustCreateSchedule(t, e, env, "probe-1", "agent", now)

	o := commodel.RoutineOccurrence{ScheduleID: "probe-1", DueAt: now, State: "probing", Job: commodel.Job{ID: "probe-job-9"}}
	path := filepath.Join(root, "occurrences", occurrenceID(o)+".json")
	if err := e.atomicJSON(path, o); err != nil {
		t.Fatal(err)
	}

	probing, err := e.MutateProbingOccurrence(o, func(live *commodel.RoutineOccurrence) error {
		live.State = "dispatched"
		return nil
	})
	if err != nil || !probing {
		t.Fatalf("first settle: probing=%v err=%v", probing, err)
	}
	probing, err = e.MutateProbingOccurrence(o, func(live *commodel.RoutineOccurrence) error {
		live.State = "failed"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if probing {
		t.Fatal("second settle overwrote a settled occurrence")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored commodel.RoutineOccurrence
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.State != "dispatched" {
		t.Fatalf("durable state = %q, want dispatched", stored.State)
	}
}

// An owner edit is one atomic revision bump under the engine lock: it clears
// the blocked marker, applies the patch and re-arms the next due — and a
// foreign principal never sees the record.
func TestEditRoutineOwnedRevisionBump(t *testing.T) {
	root := t.TempDir()
	engineDirs(t, root)
	d := newConcurrentDeps()
	e := NewEngine(root, d.deps())

	env := identity.TelegramEnvelope("alice", 11, "rt-1", "policy-1")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	mustCreateSchedule(t, e, env, "edit-1", "agent", now)

	live, err := e.LoadSchedule("edit-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BlockSchedule(&live, "test block", now); err != nil {
		t.Fatal(err)
	}

	patch := map[string]json.RawMessage{
		"input":             json.RawMessage(`"edited input"`),
		"expected_revision": json.RawMessage(`1`),
	}
	item, err := e.EditRoutine("edit-1", env, patch, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if item.Revision != 2 || item.Input != "edited input" || item.BlockedReason != "" {
		t.Fatalf("edit outcome: rev=%d input=%q blocked=%q", item.Revision, item.Input, item.BlockedReason)
	}

	if _, err := e.EditRoutine("edit-1", identity.TelegramEnvelope("mallory", 22, "rt-1", "policy-1"), patch, false, now); !errors.Is(err, ErrRoutineNotOwned) {
		t.Fatalf("foreign edit err=%v, want ErrRoutineNotOwned", err)
	}
}
