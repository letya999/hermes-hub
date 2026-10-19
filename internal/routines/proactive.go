package routines

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// processProactive admits at most one occurrence per schedule and sixteen per
// pass. Two workers bound network pressure while preserving old work first.
func (e *Engine) ProcessProactive(ctx context.Context, state string, execute func(commodel.RoutineOccurrence)) error {
	e.mu.Lock()
	entries, err := os.ReadDir(filepath.Join(e.root, "occurrences"))
	if err != nil {
		e.mu.Unlock()
		return err
	}
	var pending []commodel.RoutineOccurrence
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(e.root, "occurrences", entry.Name()))
		if err != nil {
			e.mu.Unlock()
			return err
		}
		var o commodel.RoutineOccurrence
		if json.Unmarshal(body, &o) != nil {
			e.mu.Unlock()
			return errors.New("invalid occurrence")
		}
		if o.State == state {
			pending = append(pending, o)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].DueAt.Before(pending[j].DueAt) })
	if e.proactiveFlight == nil {
		e.proactiveFlight = map[string]bool{}
	}
	selected := pending[:0]
	for _, o := range pending {
		if e.proactiveFlight[o.ScheduleID] {
			continue
		}
		e.proactiveFlight[o.ScheduleID] = true
		selected = append(selected, o)
		if len(selected) == 16 {
			break
		}
	}
	e.mu.Unlock()
	var wg sync.WaitGroup
	slots := make(chan struct{}, 2)
	for _, o := range selected {
		if ctx.Err() != nil {
			e.mu.Lock()
			delete(e.proactiveFlight, o.ScheduleID)
			e.mu.Unlock()
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			e.mu.Lock()
			delete(e.proactiveFlight, o.ScheduleID)
			e.mu.Unlock()
			continue
		}
		if ctx.Err() != nil {
			<-slots
			e.mu.Lock()
			delete(e.proactiveFlight, o.ScheduleID)
			e.mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(o commodel.RoutineOccurrence) {
			defer wg.Done()
			defer func() { <-slots; e.mu.Lock(); delete(e.proactiveFlight, o.ScheduleID); e.mu.Unlock() }()
			execute(o)
		}(o)
	}
	wg.Wait()
	return ctx.Err()
}

// RunDueScripts persists output before any delivery, so recovery does not rerun
// an already completed script after a partial outbox handoff.
func (e *Engine) RunDueScripts(ctx context.Context, now time.Time, authorize func(commodel.Job) error, run func(context.Context, commodel.RoutineOccurrence) ([]string, error)) error {
	return e.ProcessProactive(ctx, "scripting", func(o commodel.RoutineOccurrence) {
		path := filepath.Join(e.root, "occurrences", occurrenceID(o)+".json")
		receipt := filepath.Join(e.root, "occurrences", occurrenceID(o)+".script-output")
		var texts []string
		e.mu.Lock()
		current := e.OccurrenceCurrentLocked(o) && authorize != nil && authorize(o.Job) == nil
		e.mu.Unlock()
		if !current {
			o.State = "cancelled"
			_ = e.atomicJSON(path, o)
			return
		}
		if body, err := os.ReadFile(receipt); err == nil {
			if json.Unmarshal(body, &texts) != nil {
				return
			}
		} else if os.IsNotExist(err) {
			var err error
			texts, err = run(ctx, o)
			if err != nil {
				o.State = "failed"
				_ = e.atomicJSON(path, o)
				return
			}
			if e.atomicJSON(receipt, texts) != nil {
				return
			}
		} else {
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if !e.OccurrenceCurrentLocked(o) || authorize(o.Job) != nil {
			o.State = "cancelled"
			_ = e.atomicJSON(path, o)
			return
		}
		for i, text := range texts {
			d, err := e.normalizeDelivery(commodel.Delivery{ID: "script-" + o.Job.ID + "-" + strconv.Itoa(i), OccurrenceID: o.Job.ID, Channel: o.Job.Channel, ConversationID: o.Job.ConversationID, DeliveryTargetID: o.Job.DeliveryTargetID, ChatID: o.Job.ChatID, TaskID: o.Job.TaskID, TopicID: o.Job.TopicID, SlackChannel: o.Job.SlackChannel, SlackThread: o.Job.SlackThread, Text: text, CreatedAt: now.UTC()})
			if err != nil || e.deps.EnqueueDelivery(d) != nil {
				return
			}
		}
		o.State = "dispatched"
		_ = e.atomicJSON(path, o)
	})
}
