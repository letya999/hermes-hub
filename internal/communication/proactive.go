package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// processProactive admits at most one occurrence per schedule and sixteen per
// pass. Two workers bound network pressure while preserving old work first.
func (s *Spool) processProactive(ctx context.Context, state string, execute func(RoutineOccurrence)) error {
	s.mu.Lock()
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil {
		s.mu.Unlock()
		return err
	}
	var pending []RoutineOccurrence
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(s.root, "occurrences", e.Name()))
		if err != nil {
			s.mu.Unlock()
			return err
		}
		var o RoutineOccurrence
		if json.Unmarshal(body, &o) != nil {
			s.mu.Unlock()
			return errors.New("invalid occurrence")
		}
		if o.State == state {
			pending = append(pending, o)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].DueAt.Before(pending[j].DueAt) })
	if s.proactiveFlight == nil {
		s.proactiveFlight = map[string]bool{}
	}
	selected := pending[:0]
	for _, o := range pending {
		if s.proactiveFlight[o.ScheduleID] {
			continue
		}
		s.proactiveFlight[o.ScheduleID] = true
		selected = append(selected, o)
		if len(selected) == 16 {
			break
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	slots := make(chan struct{}, 2)
	for _, o := range selected {
		if ctx.Err() != nil {
			s.mu.Lock()
			delete(s.proactiveFlight, o.ScheduleID)
			s.mu.Unlock()
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			s.mu.Lock()
			delete(s.proactiveFlight, o.ScheduleID)
			s.mu.Unlock()
			continue
		}
		if ctx.Err() != nil {
			<-slots
			s.mu.Lock()
			delete(s.proactiveFlight, o.ScheduleID)
			s.mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(o RoutineOccurrence) {
			defer wg.Done()
			defer func() { <-slots; s.mu.Lock(); delete(s.proactiveFlight, o.ScheduleID); s.mu.Unlock() }()
			execute(o)
		}(o)
	}
	wg.Wait()
	return ctx.Err()
}

// runProbeScript runs a watcher probe script in the disposable runtime and
// requires exactly one canonical emitted value (SPEC-0057).
func (g *Gateway) runProbeScript(ctx context.Context, o RoutineOccurrence, src, input string, mountSpace bool) (string, error) {
	texts, err := g.postScript(ctx, hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: o.Job.Envelope, JobID: o.Job.ID, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: o.Job.Channel, Trigger: o.Job.Trigger, IdempotencyKey: o.Job.IdempotencyKey, Text: src}, DueAt: o.DueAt, Input: input, MountSpace: mountSpace})
	if err != nil {
		return "", err
	}
	if len(texts) != 1 {
		return "", errors.New("probe script must emit exactly one value")
	}
	return texts[0], nil
}

func (g *Gateway) runIsolatedScript(ctx context.Context, o RoutineOccurrence) ([]string, error) {
	return g.postScript(ctx, hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: o.Job.Envelope, JobID: o.Job.ID, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: o.Job.Channel, Trigger: o.Job.Trigger, IdempotencyKey: o.Job.IdempotencyKey, Text: o.Job.Text}, DueAt: o.DueAt})
}

func (g *Gateway) postScript(ctx context.Context, request hubruntime.ScriptRequest) ([]string, error) {
	if !g.config.Supervised || g.config.RuntimeURL == "" {
		return nil, errors.New("isolated script supervisor is unavailable")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/script", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("isolated script failed")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(data) > 128*1024 {
		return nil, errors.New("script response exceeds limit")
	}
	var texts []string
	if json.Unmarshal(data, &texts) != nil || len(texts) > scriptMaxEmits {
		return nil, errors.New("invalid script response")
	}
	for _, text := range texts {
		if len(text) > scriptMaxEmitBytes {
			return nil, errors.New("script emit exceeds limit")
		}
	}
	return texts, nil
}

// RunDueScripts persists output before any delivery, so recovery does not rerun
// an already completed script after a partial outbox handoff.
func (s *Spool) RunDueScripts(ctx context.Context, now time.Time, authorize func(Job) error, run func(context.Context, RoutineOccurrence) ([]string, error)) error {
	return s.processProactive(ctx, "scripting", func(o RoutineOccurrence) {
		path := filepath.Join(s.root, "occurrences", occurrenceID(o)+".json")
		receipt := filepath.Join(s.root, "occurrences", occurrenceID(o)+".script-output")
		var texts []string
		s.mu.Lock()
		current := s.occurrenceCurrentLocked(o) && authorize != nil && authorize(o.Job) == nil
		s.mu.Unlock()
		if !current {
			o.State = "cancelled"
			_ = atomicJSON(path, o)
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
				_ = atomicJSON(path, o)
				return
			}
			if atomicJSON(receipt, texts) != nil {
				return
			}
		} else {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.occurrenceCurrentLocked(o) || authorize(o.Job) != nil {
			o.State = "cancelled"
			_ = atomicJSON(path, o)
			return
		}
		for i, text := range texts {
			d, err := normalizeDelivery(Delivery{ID: "script-" + o.Job.ID + "-" + strconv.Itoa(i), OccurrenceID: o.Job.ID, Channel: o.Job.Channel, ConversationID: o.Job.ConversationID, DeliveryTargetID: o.Job.DeliveryTargetID, ChatID: o.Job.ChatID, TaskID: o.Job.TaskID, TopicID: o.Job.TopicID, SlackChannel: o.Job.SlackChannel, SlackThread: o.Job.SlackThread, Text: text, CreatedAt: now.UTC()})
			if err != nil || s.enqueueDeliveryLocked(d) != nil {
				return
			}
		}
		o.State = "dispatched"
		_ = atomicJSON(path, o)
	})
}
