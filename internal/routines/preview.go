package routines

import (
	"errors"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

type RoutinePreview struct {
	State    string      `json:"state"`
	Times    []time.Time `json:"times"`
	Timezone string      `json:"timezone"`
}

func PreviewSchedule(item commodel.Schedule, state string, count int, now time.Time) (RoutinePreview, error) {
	loc, err := time.LoadLocation(item.Timezone)
	if err != nil {
		return RoutinePreview{}, err
	}
	out := RoutinePreview{Times: []time.Time{}, Timezone: item.Timezone, State: state}
	if state == "completed" || state == "deleted" {
		return out, nil
	}
	next := item.NextDue
	if item.SnoozedUntil.After(next) {
		next = item.SnoozedUntil
	}
	for i := 0; i < count; i++ {
		if !next.After(now) {
			next, err = NextSchedule(item, loc, now)
			if errors.Is(err, ErrExhausted) {
				break
			}
			if err != nil {
				return out, err
			}
		}
		out.Times = append(out.Times, next)
		if strings.HasPrefix(strings.TrimSpace(item.Expression), "once:") {
			break
		}
		now, next = next, time.Time{}
	}
	return out, nil
}
