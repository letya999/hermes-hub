package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

type sampleEvent struct {
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
	Raw  string `json:"raw,omitempty"`
}

type sampleSource struct {
	Source   string            `json:"source"`
	Events   []sampleEvent     `json:"events,omitempty"`
	Error    string            `json:"error,omitempty"`
	Verdicts map[string]string `json:"verdicts,omitempty"`
}

type watchSample struct {
	ScheduleID string         `json:"schedule_id"`
	Sources    []sampleSource `json:"sources"`
}

// SampleWatch inspects source shape without reading or changing durable cursor, seen set, or delivery state.
func (h *Host) SampleWatch(ctx context.Context, auth identity.Envelope, id, override string) (watchSample, error) {
	schedule, err := h.LoadSchedule(ctx, auth, id)
	if err != nil {
		return watchSample{}, err
	}
	if schedule.JobKind != "event" || schedule.Probe == nil {
		return watchSample{}, routines.ErrRoutineNotOwned
	}
	probe := schedule.Probe
	if strings.Contains(probe.Tool, "settled") {
		return watchSample{}, errors.New("automation: consuming sample source")
	}
	for _, src := range probe.Sources {
		if strings.Contains(src.Tool, "settled") {
			return watchSample{}, errors.New("automation: consuming sample source")
		}
	}
	job := commodel.Job{Envelope: schedule.Envelope, OrganizationID: schedule.OrganizationID, UserID: schedule.UserID, ActorID: schedule.ActorID, ScopeID: schedule.ScopeID, Channel: schedule.Channel, ChatID: schedule.ChatID, TaskID: schedule.TaskID, TopicID: schedule.TopicID, SlackChannel: schedule.SlackChannel, SlackThread: schedule.SlackThread, Text: schedule.Input, Trigger: "cron", CreatedAt: h.now().UTC(), Probe: true}
	job.ID = "sample-" + id
	job.Envelope.ConversationID = routines.RoutineConversation(id)
	if err := h.prepareJob(&job); err != nil {
		return watchSample{}, err
	}
	if err := h.cfg.AuthorizeChannel(ctx, id, job); err != nil {
		return watchSample{}, errUnauthorized
	}
	filter := probe.Filter
	if override != "" {
		filter = override
	}
	occurrence := commodel.RoutineOccurrence{ScheduleID: id, DueAt: job.CreatedAt, Job: job}
	out := watchSample{ScheduleID: id, Sources: make([]sampleSource, max(1, len(probe.Sources)))}
	var workers sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i := range out.Sources {
		if len(probe.Sources) != 0 {
			out.Sources[i].Source = probe.Sources[i].Name
			if out.Sources[i].Source == "" {
				out.Sources[i].Source = fmt.Sprintf("s%d", i)
			}
		}
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				out.Sources[i].Error = routines.BoundedWatchText(ctx.Err().Error())
				return
			}
			call := *probe
			call.Args = make(map[string]any, len(probe.Args)+1)
			for key, value := range probe.Args {
				call.Args[key] = value
			}
			if len(probe.Sources) != 0 {
				src := probe.Sources[i]
				if src.Tool != "" {
					call.Tool = src.Tool
				}
				for key, value := range src.Args {
					call.Args[key] = value
				}
			}
			call.Sources = nil
			if call.CursorArg != "" {
				call.Args[call.CursorArg] = "0"
			}
			cap := float64(10) / float64(min(max(1, len(probe.Sources)), 8))
			if strings.Contains(call.Tool, "wait") || strings.Contains(call.Tool, "new_message") {
				if v, ok := call.Args["timeout"].(float64); !ok || v > cap {
					call.Args["timeout"] = int(cap)
				}
			}
			callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			var body []byte
			var err error
			switch call.Kind {
			case "mcp_tool":
				if h.cfg.ToolCall == nil {
					err = errors.New("automation: tool probe unavailable")
				} else {
					body, err = callEventSource(callCtx, h.cfg.ToolCall, auth.PrincipalID, &call)
				}
			case "http":
				body, err = h.cfg.Fetch(callCtx, call.URL)
			default:
				err = errors.New("automation: sample probe unavailable")
			}
			if err != nil {
				out.Sources[i].Error = routines.BoundedWatchText(err.Error())
				return
			}
			if len(body) > 4<<20 {
				out.Sources[i].Error = "source response exceeds limit"
				return
			}
			events, err := routines.ExtractEvents(&call, body)
			if err != nil {
				out.Sources[i].Error = routines.BoundedWatchText(err.Error())
				return
			}
			for _, event := range events[:min(len(events), 4)] {
				out.Sources[i].Events = append(out.Sources[i].Events, sampleEvent{ID: event.ID, Text: event.Text, Raw: event.Raw})
				if filter == "" {
					continue
				}
				input := event.Raw
				if input == "" {
					input = event.Text
				}
				verdict, err := h.cfg.Supervisor.ProbeScript(callCtx, occurrence, filter, input, false, nil)
				if err != nil {
					out.Sources[i].Error = routines.BoundedWatchText(err.Error())
					break
				}
				verdict = strings.TrimSpace(verdict)
				if verdict != "keep" && verdict != "drop" && verdict != "urgent" {
					out.Sources[i].Error = "invalid event filter verdict"
					break
				}
				if out.Sources[i].Verdicts == nil {
					out.Sources[i].Verdicts = map[string]string{}
				}
				out.Sources[i].Verdicts[event.ID] = verdict
			}
		}(i)
	}
	workers.Wait()
	return out, nil
}
