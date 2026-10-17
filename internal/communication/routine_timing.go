package communication

import (
	"errors"
	"strings"
	"time"
)

type RoutineTiming struct {
	Windows  []RoutineWindow `json:"windows,omitempty"`
	At       []string        `json:"at,omitempty"`
	Weekdays []int           `json:"weekdays,omitempty"`
	StartAt  time.Time       `json:"start_at,omitempty"`
	EndAt    time.Time       `json:"end_at,omitempty"`
}

var errRoutineExhausted = errors.New("routine has no future slot within its bounds")

type RoutineWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func nextSchedule(in Schedule, loc *time.Location, from time.Time) (time.Time, error) {
	if in.Timing != nil {
		if !strings.HasPrefix(strings.TrimSpace(in.Expression), "every:") || in.Delivery == "stream" {
			return time.Time{}, errors.New("timing requires a polled interval routine")
		}
		if len(in.Timing.Windows) > 8 || len(in.Timing.At) > 24 {
			return time.Time{}, errors.New("timing exceeds bounds")
		}
		windows := map[RoutineWindow]bool{}
		if !in.Timing.EndAt.IsZero() && !in.Timing.StartAt.IsZero() && !in.Timing.EndAt.After(in.Timing.StartAt) {
			return time.Time{}, errors.New("end_at must follow start_at")
		}
		seen := map[int]bool{}
		for _, w := range in.Timing.Weekdays {
			if w < 0 || w > 6 || seen[w] {
				return time.Time{}, errors.New("invalid or duplicate weekday")
			}
			seen[w] = true
		}
		for _, w := range in.Timing.Windows {
			if w.From == w.To || windows[w] {
				return time.Time{}, errors.New("invalid or duplicate window")
			}
			windows[w] = true
		}
		times := map[string]bool{}
		for _, v := range in.Timing.At {
			if times[v] {
				return time.Time{}, errors.New("duplicate extra time")
			}
			times[v] = true
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(in.Expression), "every:") {
		return nextExpression(in.Expression, loc, from)
	}
	d, err := time.ParseDuration(strings.TrimPrefix(strings.TrimSpace(in.Expression), "every:"))
	if err != nil {
		return time.Time{}, err
	}
	if d < time.Minute || d > 365*24*time.Hour || d%time.Second != 0 {
		return time.Time{}, errors.New("interval must be integral seconds from 1m through 8760h")
	}
	p := in.Timing
	if p == nil {
		p = &RoutineTiming{}
	}
	if !p.StartAt.IsZero() && from.Before(p.StartAt) {
		from = p.StartAt.Add(-time.Nanosecond)
	}
	allowed := func(t time.Time) bool {
		if len(p.Weekdays) == 0 {
			return true
		}
		for _, w := range p.Weekdays {
			if int(t.Weekday()) == w {
				return true
			}
		}
		return false
	}
	local := from.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -1)
	var best time.Time
	take := func(t time.Time) {
		if t.After(from) && (p.StartAt.IsZero() || !t.Before(p.StartAt)) && (p.EndAt.IsZero() || t.Before(p.EndAt)) && (best.IsZero() || t.Before(best)) {
			best = t.UTC()
		}
	}
	if len(p.Windows) == 0 {
		t := in.IntervalAnchor
		if t.IsZero() {
			t = from
		}
		if !t.After(from) {
			t = t.Add((from.Sub(t)/d + 1) * d)
		}
		for n := 0; n < 576001 && t.Sub(from) <= 401*24*time.Hour; n++ {
			if !p.EndAt.IsZero() && !t.Before(p.EndAt) {
				break
			}
			if allowed(t.In(loc)) {
				take(t)
				break
			}
			t = t.Add(d)
		}
	}
	for n := 0; n < 403; n++ {
		if (!best.IsZero() && day.After(best)) || (!p.EndAt.IsZero() && day.After(p.EndAt)) {
			break
		}
		if !allowed(day) {
			day = day.AddDate(0, 0, 1)
			continue
		}
		for _, w := range p.Windows {
			a, err := time.Parse("15:04", w.From)
			if err != nil {
				return time.Time{}, err
			}
			b, err := time.Parse("15:04", w.To)
			if err != nil {
				return time.Time{}, err
			}
			start := time.Date(day.Year(), day.Month(), day.Day(), a.Hour(), a.Minute(), 0, 0, loc)
			end := time.Date(day.Year(), day.Month(), day.Day(), b.Hour(), b.Minute(), 0, 0, loc)
			if b.Before(a) {
				endDay := day.AddDate(0, 0, 1)
				end = time.Date(endDay.Year(), endDay.Month(), endDay.Day(), b.Hour(), b.Minute(), 0, 0, loc)
			}
			t := start
			if !t.After(from) {
				t = t.Add((from.Sub(t)/d + 1) * d)
			}
			if t.Before(end) {
				take(t)
			}
		}
		for _, v := range p.At {
			clock, err := time.Parse("15:04", v)
			if err != nil {
				return time.Time{}, err
			}
			t := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
			if t.Format("15:04") == v {
				take(t)
			}
			for _, delta := range []time.Duration{-2 * time.Hour, -time.Hour, -30 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour} {
				other := t.Add(delta)
				if other.In(loc).Format("2006-01-02 15:04") == day.Format("2006-01-02")+" "+v {
					take(other)
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	if best.IsZero() {
		return best, errRoutineExhausted
	}
	return best, nil
}
