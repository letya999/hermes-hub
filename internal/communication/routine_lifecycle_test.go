package communication

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRoutineIntervalPersistsAnchorAndCompletesAtEnd(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbounded", true: "bounded"}[bounded], func(t *testing.T) {
			s, o, _ := occurrenceFixture(t)
			now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
			in := Schedule{ScheduleID: "interval-restart", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "every:15m", Input: "fixture", JobKind: "reminder"}
			if bounded {
				in.Timing = &RoutineTiming{EndAt: now.Add(30 * time.Minute)}
			}
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("optional timing must not panic: %v", r)
				}
			}()
			got, err := s.createScheduleAt(in, in.Envelope, "", nil, now)
			if err != nil {
				t.Fatal(err)
			}
			if !got.IntervalAnchor.Equal(now) {
				t.Errorf("anchor not persisted: %s", got.IntervalAnchor)
			}
			restart, err := NewSpool(s.root)
			if err != nil {
				t.Fatal(err)
			}
			if err := restart.TickSchedules(now.Add(15*time.Minute), "", nil); err != nil {
				t.Fatalf("last slot must settle: %v", err)
			}
			items, err := restart.ListSchedules(in.Envelope)
			if err != nil || len(items) != 1 {
				t.Fatalf("read: %v %v", items, err)
			}
			if bounded {
				if items[0].Enabled || !items[0].NextDue.IsZero() {
					t.Fatalf("exhausted interval not completed: %+v", items[0])
				}
				return
			}
			next, err := nextSchedule(items[0], time.UTC, now.Add(22*time.Minute))
			if err != nil || !next.Equal(now.Add(30*time.Minute)) {
				t.Fatalf("restart cadence drift: %s %v", next, err)
			}
			_, err = nextSchedule(Schedule{Expression: "every:15m", Timing: &RoutineTiming{EndAt: now}}, time.UTC, now)
			if !errors.Is(err, errRoutineExhausted) {
				t.Fatal("exhaustion must be explicit")
			}
		})
	}
}

func TestRoutineTimingRejectsUnsafeConfiguration(t *testing.T) {
	now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, expression string
		timing           *RoutineTiming
		delivery         string
	}{
		{"too fast", "every:1s", &RoutineTiming{}, ""},
		{"zero", "every:0s", &RoutineTiming{}, ""},
		{"negative", "every:-1m", &RoutineTiming{}, ""},
		{"too long", "every:9000h", &RoutineTiming{}, ""},
		{"fractional", "every:60.5s", &RoutineTiming{}, ""},
		{"equal window", "every:1m", &RoutineTiming{Windows: []RoutineWindow{{"14:00", "14:00"}}}, ""},
		{"bad window", "every:1m", &RoutineTiming{Windows: []RoutineWindow{{"25:00", "16:00"}}}, ""},
		{"many windows", "every:1m", &RoutineTiming{Windows: make([]RoutineWindow, 9)}, ""},
		{"many times", "every:1m", &RoutineTiming{At: make([]string, 25)}, ""},
		{"bad time", "every:1m", &RoutineTiming{At: []string{"25:00"}}, ""},
		{"duplicate time", "every:1m", &RoutineTiming{At: []string{"17:00", "17:00"}}, ""},
		{"duplicate window", "every:1m", &RoutineTiming{Windows: []RoutineWindow{{"14:00", "16:00"}, {"14:00", "16:00"}}}, ""},
		{"cron timing", "0 10 * * *", &RoutineTiming{At: []string{"17:00"}}, ""},
		{"stream timing", "every:1m", &RoutineTiming{At: []string{"17:00"}}, "stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Zero and negative durations must be rejected before any division.
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("unsafe timing panicked: %v", r)
				}
			}()
			_, err := nextSchedule(Schedule{Expression: tc.expression, Timing: tc.timing, Delivery: tc.delivery}, time.UTC, now)
			if err == nil {
				t.Fatal("unsafe timing accepted")
			}
		})
	}
}

func TestRoutineIntervalCalendarSlots(t *testing.T) {
	for _, tc := range []struct{ name, zone, from, config, want string }{
		{"anchored", "UTC", "2100-01-01T10:07:00Z", `{"expression":"every:15m","interval_anchor":"2100-01-01T10:00:00Z","timing":{}}`, "2100-01-01T10:15:00Z"},
		{"overnight opening weekday", "UTC", "2100-01-02T00:05:00Z", `{"expression":"every:30m","timing":{"weekdays":[5],"windows":[{"from":"23:00","to":"01:00"}]}}`, "2100-01-02T00:30:00Z"},
		{"next selected day", "UTC", "2100-01-02T01:00:00Z", `{"expression":"every:30m","timing":{"weekdays":[5],"windows":[{"from":"23:00","to":"01:00"}]}}`, "2100-01-08T23:00:00Z"},
		{"start inclusive", "UTC", "2100-01-01T10:00:00Z", `{"expression":"every:15m","interval_anchor":"2100-01-01T10:00:00Z","timing":{"start_at":"2100-01-01T11:00:00Z","end_at":"2100-01-01T12:00:00Z"}}`, "2100-01-01T11:00:00Z"},
		{"end exclusive", "UTC", "2100-01-01T11:45:00Z", `{"expression":"every:15m","interval_anchor":"2100-01-01T10:00:00Z","timing":{"end_at":"2100-01-01T12:00:00Z"}}`, ""},
		{"invalid weekday", "UTC", "2100-01-01T10:00:00Z", `{"expression":"every:15m","timing":{"weekdays":[7]}}`, ""},
		{"duplicate weekday", "UTC", "2100-01-01T10:00:00Z", `{"expression":"every:15m","timing":{"weekdays":[5,5]}}`, ""},
		{"invalid bounds", "UTC", "2100-01-01T10:00:00Z", `{"expression":"every:15m","timing":{"start_at":"2100-01-02T10:00:00Z","end_at":"2100-01-01T10:00:00Z"}}`, ""},
		{"DST gap", "America/New_York", "2026-03-08T06:59:00Z", `{"expression":"every:30m","timing":{"windows":[{"from":"02:00","to":"04:00"}]}}`, "2026-03-08T07:00:00Z"},
		{"DST fold extra", "America/New_York", "2026-11-01T05:45:00Z", `{"expression":"every:2h","timing":{"windows":[{"from":"12:00","to":"13:00"}],"at":["01:30"]}}`, "2026-11-01T06:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("calendar evaluation panicked: %v", r)
				}
			}()
			var in Schedule
			if err := json.Unmarshal([]byte(tc.config), &in); err != nil {
				t.Fatal(err)
			}
			from, err := time.Parse(time.RFC3339, tc.from)
			if err != nil {
				t.Fatal(err)
			}
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			got, err := nextSchedule(in, loc, from)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("invalid/exhausted calendar returned %s", got)
				}
				return
			}
			if err != nil || got.Format(time.RFC3339) != tc.want {
				t.Fatalf("slot = %s %v, want %s", got, err, tc.want)
			}
		})
	}
}

func TestRoutineIntervalWindowCadence(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	var in Schedule
	if err := json.Unmarshal([]byte(`{"schedule_id":"window-test","timezone":"Europe/Moscow","expression":"every:15m","input":"fixture","job_kind":"reminder","channel":"telegram_bot","chat_id":11,"timing":{"windows":[{"from":"14:00","to":"16:00"}],"at":["17:00"]}}`), &in); err != nil {
		t.Fatal(err)
	}
	in.Envelope = c.Users[0].envelope(11)
	in.UserID, in.ActorID, in.ScopeID = "alice", "alice", "user:alice"
	got, err := g.spool.createScheduleAt(in, in.Envelope, "", nil, now)
	if err != nil {
		t.Fatalf("interval window creation: %v", err)
	}
	want := now.Add(time.Hour)
	if !got.NextDue.Equal(want) {
		t.Fatalf("first window slot = %s, want %s", got.NextDue, want)
	}
	for i := 0; i < 9; i++ {
		due := want.Add(time.Duration(i) * 15 * time.Minute)
		if i == 8 {
			due = want.Add(3 * time.Hour)
		}
		items, err := g.spool.ListSchedules(in.Envelope)
		if err != nil || len(items) != 1 || !items[0].NextDue.Equal(due) {
			t.Fatalf("slot %d: %+v %v; want %s", i, items, err, due)
		}
		if err := g.spool.TickSchedules(due, "", nil); err != nil {
			t.Fatal(err)
		}
		if err := g.spool.DispatchDueOccurrences(due, func(Job) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}
