package automation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestAutomationAdmissionEpochSurvivesLostAckAndResume(t *testing.T) {
	h := newHarness(t)
	auth := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	h.sup.epoch.Store(2)
	h.sup.dropAck.Store(true)
	host := h.host(t)
	item, err := host.CreateSchedule(context.Background(), auth, draft("epoch-routine", auth), "")
	if err != nil {
		t.Fatal(err)
	}
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	job := h.sup.onlyJob()
	if _, logical := h.sup.counts(); logical != 1 || job.OperationalEpoch != 2 {
		t.Fatalf("new admission: jobs=%d epoch=%d", logical, job.OperationalEpoch)
	}
	h.sup.epoch.Store(4)
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	host = h.host(t)
	if err := host.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), h.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	in, err := host.queue.intent("alice", job.IdempotencyKey)
	if err != nil || in.Job.OperationalEpoch != 2 || in.Acked || h.sup.epochRejects.Load() == 0 {
		t.Fatalf("old work was rebound across resume: epoch=%d ack=%v rejects=%d err=%v", in.Job.OperationalEpoch, in.Acked, h.sup.epochRejects.Load(), err)
	}
	if _, logical := h.sup.counts(); logical != 1 {
		t.Fatalf("stale work admitted after resume: %d", logical)
	}
}

func TestAutomationHoldPreventsOccurrenceAdmission(t *testing.T) {
	h := newHarness(t)
	auth := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	item, err := host.CreateSchedule(context.Background(), auth, draft("held-routine", auth), "")
	if err != nil {
		t.Fatal(err)
	}
	h.sup.holdActive.Store(true)
	h.now = item.NextDue.Add(time.Second)
	if err := host.Tick(context.Background(), h.now); err == nil {
		t.Fatal("active administrative hold was ignored")
	}
	files, err := os.ReadDir(filepath.Join(host.ownerRoot("alice"), "occurrences"))
	if err != nil || len(files) != 0 {
		t.Fatalf("held occurrence persisted: files=%d err=%v", len(files), err)
	}
	h.sup.holdActive.Store(false)
	h.sup.epoch.Store(2)
	if err := host.Tick(context.Background(), h.now); err != nil {
		t.Fatal(err)
	}
	var occurrence commodel.RoutineOccurrence
	files, _ = os.ReadDir(filepath.Join(host.ownerRoot("alice"), "occurrences"))
	if len(files) != 1 || loadJSON(filepath.Join(host.ownerRoot("alice"), "occurrences", files[0].Name()), &occurrence) != nil || occurrence.Job.OperationalEpoch != 2 {
		t.Fatal("new occurrence did not bind the resumed epoch")
	}
}

func TestStreamOccurrencePinsAdmissionEpoch(t *testing.T) {
	f := newEventFixture(t)
	f.h.sup.epoch.Store(2)
	f.create(t, f.streamSchedule("epoch-stream", "watch", "notify", &commodel.WatchProbe{Kind: "mcp_tool", Tool: "read_events", MinIntervalSeconds: 5}))
	if err := f.host.tickStreams(context.Background(), f.eng, "alice", f.h.now); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(f.host.ownerRoot("alice"), "occurrences")
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 1 {
		t.Fatalf("stream occurrence: files=%d err=%v", len(files), err)
	}
	path := filepath.Join(root, files[0].Name())
	var occurrence commodel.RoutineOccurrence
	if err := loadJSON(path, &occurrence); err != nil || occurrence.Job.OperationalEpoch != 2 {
		t.Fatalf("stream admission epoch=%d err=%v", occurrence.Job.OperationalEpoch, err)
	}
	f.h.sup.epoch.Store(4)
	if err := f.host.tickStreams(context.Background(), f.eng, "alice", f.h.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := loadJSON(path, &occurrence); err != nil || occurrence.Job.OperationalEpoch != 2 {
		t.Fatal("pending stream occurrence was rebound after resume")
	}
}
