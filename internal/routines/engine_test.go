package routines

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

type stubChannel struct{}

func (stubChannel) ValidateOccurrence(commodel.RoutineOccurrence, identity.Envelope) error {
	return nil
}

func (stubChannel) RouteBlockedNotice(commodel.Schedule, *commodel.Delivery) {}

// The engine seam stands alone: a stub owner spool drives PutOccurrence and
// DispatchDueOccurrences end to end over a temp root.
func TestEngineDispatchSeam(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"occurrences", "schedules"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var enqueued []commodel.Job
	var mu sync.Mutex
	e := NewEngine(root, &mu, Deps{
		WriteJSONAtomic: func(path string, value any) error {
			b, err := json.Marshal(value)
			if err != nil {
				return err
			}
			return os.WriteFile(path, b, 0600)
		},
		ChannelFor: func(string) OccurrenceChannel { return stubChannel{} },
		LoadMapping: func(string) (commodel.JobMapping, error) {
			return commodel.JobMapping{}, os.ErrNotExist
		},
		EnqueueJob: func(j commodel.Job) (bool, error) {
			enqueued = append(enqueued, j)
			return true, nil
		},
	})
	job := commodel.Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Channel: "telegram_bot", ChatID: 11, Text: "scheduled work"}
	o := commodel.RoutineOccurrence{ScheduleID: "schedule-one", Revision: 1, DueAt: time.Unix(100, 0), Job: job}
	if err := e.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	due := time.Unix(200, 0)
	ok := func(commodel.Job) error { return nil }
	if err := e.DispatchDueOccurrences(due, ok); err != nil {
		t.Fatal(err)
	}
	if len(enqueued) != 1 || enqueued[0].Text != "scheduled work" || enqueued[0].IdempotencyKey == "" {
		t.Fatalf("dispatch: %+v", enqueued)
	}
	body, err := os.ReadFile(filepath.Join(root, "occurrences", OccurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var live commodel.RoutineOccurrence
	if err := json.Unmarshal(body, &live); err != nil {
		t.Fatal(err)
	}
	if live.State != "dispatched" {
		t.Fatalf("occurrence state %q", live.State)
	}
	// Dispatch again: the settled occurrence does not enqueue twice.
	if err := e.DispatchDueOccurrences(due, ok); err != nil {
		t.Fatal(err)
	}
	if len(enqueued) != 1 {
		t.Fatalf("re-dispatch duplicated the job: %d", len(enqueued))
	}
}
