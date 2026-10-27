package routines

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
)

func newLocaleEngine(t *testing.T, delivered *[]commodel.Delivery) (*Engine, string) {
	root := t.TempDir()
	for _, dir := range []string{"occurrences", "schedules", "watchers", "digests"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	mu := &sync.Mutex{}
	e := NewEngine(root, mu, Deps{
		WriteJSONAtomic: func(path string, value any) error {
			b, err := json.Marshal(value)
			if err != nil {
				return err
			}
			return os.WriteFile(path, b, 0600)
		},
		ChannelFor:      func(string) OccurrenceChannel { return stubChannel{} },
		EnqueueJob:      func(j commodel.Job) (bool, error) { return true, nil },
		EnqueueDelivery: func(d commodel.Delivery) error { *delivered = append(*delivered, d); return nil },
		NormalizeDelivery: func(d commodel.Delivery) (commodel.Delivery, error) { return d, nil },
		Signal:          func() {},
		ListTasks:       func(string) ([]commodel.Task, error) { return nil, nil },
		LangOf:          func(string) locale.Lang { return locale.LangES },
	})
	return e, root
}

// Digest and brief footers plus the blocked-schedule notice render in the
// occurrence owner's language via Deps.LangOf.
func TestRoutineNoticesLocalized(t *testing.T) {
	for _, key := range []string{"routines.watch-no-success", "routines.watch-unavailable", "routines.no-changes", "routines.unavailable-count", "routines.blocked"} {
		if _, ok := locale.Lookup(locale.LangES, key); !ok {
			t.Errorf("catalog missing es %q", key)
		}
	}
	var delivered []commodel.Delivery
	e, _ := newLocaleEngine(t, &delivered)
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")

	// Watcher with a dead source owned by the same principal/context.
	watchOwner := commodel.Schedule{ScheduleID: "w-src", Revision: 1, Envelope: env, Channel: "telegram_bot", Input: "watch site", Probe: &commodel.WatchProbe{URL: "https://x"}}
	if err := e.atomicJSON(e.SchedulePath("w-src"), watchOwner); err != nil {
		t.Fatal(err)
	}
	st := WatchState{ScheduleID: "w-src", LastError: "boom"}
	if err := e.atomicJSON(filepath.Join(e.root, "watchers", "w-src.json"), st); err != nil {
		t.Fatal(err)
	}
	digest := commodel.Schedule{ScheduleID: "digest-one", Revision: 1, Envelope: env, Channel: "telegram_bot"}
	if err := e.atomicJSON(e.SchedulePath("digest-one"), digest); err != nil {
		t.Fatal(err)
	}
	ok := func(commodel.Job) error { return nil }
	now := time.Unix(300, 0)

	// First digest renders the source-unavailable line; the deduped second
	// renders the deterministic empty footer with the unavailable count.
	o := watchOccurrence("digest-one", "digest", 1, env, now)
	o.Job.Text = "digest"
	if err := e.dispatchDigestLocked(o, now, ok); err != nil {
		t.Fatal(err)
	}
	o2 := watchOccurrence("digest-one", "digest", 2, env, now.Add(time.Minute))
	o2.Job.ID = "digest-one-2"
	o2.Job.Text = "digest"
	if err := e.dispatchDigestLocked(o2, now.Add(time.Minute), ok); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 {
		t.Fatalf("deliveries=%d want 2", len(delivered))
	}
	if !strings.Contains(delivered[0].Text, locale.T(locale.LangES, "routines.watch-unavailable", "watch site", locale.T(locale.LangES, "routines.watch-no-success"))) &&
		!strings.Contains(delivered[0].Text, locale.T(locale.LangES, "routines.watch-no-success")) {
		t.Errorf("digest[0]=%q", delivered[0].Text)
	}
	if !strings.Contains(delivered[1].Text, locale.T(locale.LangES, "routines.no-changes")) ||
		!strings.Contains(delivered[1].Text, locale.T(locale.LangES, "routines.unavailable-count", 1)) {
		t.Errorf("digest[1]=%q", delivered[1].Text)
	}

	// Blocked-schedule notice.
	item := commodel.Schedule{ScheduleID: "bad-one", Envelope: env, Channel: "telegram_bot"}
	if err := e.BlockScheduleLocked(&item, "bad cron", now); err != nil {
		t.Fatal(err)
	}
	last := delivered[len(delivered)-1].Text
	if !strings.Contains(last, strings.Split(locale.T(locale.LangES, "routines.blocked", "bad-one", "bad cron"), "bad cron")[0]) {
		t.Errorf("blocked=%q", last)
	}
}
