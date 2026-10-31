package commops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

// Interval reads HUB_CANARY_INTERVAL ("5m" or bare seconds); 0 =
// disabled — the canary is opt-in only.
func Interval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("HUB_CANARY_INTERVAL"))
	if raw == "" {
		return 0
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// The synthetic canary (SPEC-0053 R3.4) exercises the real execution chain on
// a fixed prompt that provokes exactly one safe read tool call. Its chat is a
// dedicated mock conversation — the prompt is a compile-time constant no user
// can reach or alter.
const (
	chatID = int64(-9000)
	prompt = "Call the file_list tool once on the `canary` directory, then reply with the entry count."
	tool   = "file_list"
)

// terminal reports whether a mapping status is a settled outcome.
func terminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "interrupted"
}

// Run executes one synthetic end-to-end check and returns the failure
// class ("" = healthy).
func (e *Engine) Run(ctx context.Context, now time.Time) string {
	owner, ok := e.Deps.Owner()
	if !ok {
		return "no_owner"
	}
	if class := e.surfaceCheck(ctx, owner); class != "" {
		return class
	}
	if err := e.Deps.MockLink(chatID, owner.ID); err != nil {
		return "mock_link"
	}
	task, err := e.Deps.BindTask(owner, chatID)
	if err != nil {
		return "task_bind"
	}
	tick := now.UTC().Format("20060102T150405")
	jobID := "canary-" + tick
	if err := e.Deps.Enqueue(ctx, owner, task, chatID, jobID, "canary:"+tick, prompt); err != nil {
		return "enqueue"
	}
	deadline := now.Add(e.Deps.JobTimeout())
	for {
		status, found, err := e.Deps.MappingStatus(jobID)
		if err == nil && found && terminal(status) {
			if status != "completed" {
				return "run_" + status
			}
			if class := e.ToolCheck(jobID); class != "" {
				return class
			}
			return e.deliveryCheck(jobID)
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return "timeout"
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// toolReceipt decodes only the fields the canary asserts on — the durable
// stream receipt file format is the contract, not the full record.
type toolReceipt struct {
	JobID string `json:"job_id"`
	Event struct {
		LastEvent string `json:"last_event"`
		ToolName  string `json:"tool_name"`
	} `json:"event"`
}

// ToolCheck reads the job's durable stream receipts — a completed run
// with no tool.start means the tool surface is broken even though the model
// answered.
func (e *Engine) ToolCheck(jobID string) string {
	entries, err := os.ReadDir(filepath.Join(e.Deps.SpoolRoot, "events"))
	if err != nil {
		return "events_unreadable"
	}
	calls := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, readErr := os.ReadFile(filepath.Join(e.Deps.SpoolRoot, "events", entry.Name()))
		var receipt toolReceipt
		if readErr != nil || json.Unmarshal(b, &receipt) != nil {
			continue
		}
		if receipt.JobID == jobID && receipt.Event.LastEvent == "tool.start" {
			switch receipt.Event.ToolName {
			case tool, "mcp__hub__file_list":
				calls++
			case "tool_describe", "tool_call": // Hermes wraps MCP discovery and invocation.
			default:
				return "unexpected_tool"
			}
		}
	}
	// Exactly one read-tool call — not zero, not "contains one".
	if calls != 1 {
		return "missing_tool_call"
	}
	return ""
}

// deliveryCheck waits briefly for the response delivery to land in the
// mock outbox — the chain isn't proven until the answer is durably captured.
func (e *Engine) deliveryCheck(jobID string) string {
	deliveryID := "job-" + jobID + "-response"
	deadline := time.Now().Add(e.Deps.DeliveryGrace())
	for time.Now().Before(deadline) {
		if e.Deps.MockDelivered(deliveryID) {
			return ""
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "delivery"
}

// surfaceCheck compares the persisted readiness surface against the
// probe's required tool before spending a run: a manifest that no longer hands
// out file_list — or a ToolHub reason — means the tool surface is broken and
// the run outcome would only restate it. A missing surface (unsupervised,
// unreachable) is not a failure: the run itself stays the ground truth.
func (e *Engine) surfaceCheck(ctx context.Context, owner Owner) string {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, missing := range e.Deps.SurfaceMissingTools(probeCtx, owner) {
		if missing == tool {
			return "surface"
		}
	}
	return ""
}

type state struct {
	LastClass string `json:"last_class,omitempty"`
}

// Record persists the tick outcome and raises owner notices: one per
// new failure class, deterministic text, no model involved (SPEC-0053 R3.4).
func (e *Engine) Record(ctx context.Context, now time.Time, class string) {
	e.journal(now, class)
	st := e.loadState()
	owner, ok := e.Deps.Owner()
	if !ok {
		return
	}
	home := int64(0)
	if len(owner.TelegramIDs) > 0 {
		home = owner.TelegramIDs[0]
	}
	lang := owner.Lang
	if lang == "" {
		lang = locale.Fallback()
	}
	if class != "" && class != st.LastClass {
		e.Deps.NotifyOwner(ctx, "canary-"+class, home,
			locale.T(lang, "ops.canary-failing", class))
		st.LastClass = class
	} else if class == "" && st.LastClass != "" {
		e.Deps.NotifyOwner(ctx, "canary-recovered", home,
			locale.T(lang, "ops.canary-recovered", st.LastClass))
		st.LastClass = ""
	}
	e.saveState(st)
}

// journal appends the tick outcome to canary.jsonl — durable history for
// `hubctl trace` and post-incident review, bounded so the file can't grow
// without limit. Truncation keeps the most recent entries.
const journalKeep = 1000

func (e *Engine) journal(now time.Time, class string) {
	line, err := json.Marshal(map[string]any{"ts": now.UTC().Format(time.RFC3339), "class": class})
	if err != nil {
		return
	}
	path := filepath.Join(e.Deps.SpoolRoot, "canary.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 1<<20 {
		if b, readErr := os.ReadFile(path); readErr == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) > journalKeep {
				lines = lines[len(lines)-journalKeep:]
			}
			_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600) // #nosec G703 -- canary journal under the engine root
		}
	}
}

func (e *Engine) statePath() string {
	return filepath.Join(e.Deps.SpoolRoot, "canary-state.json")
}

func (e *Engine) loadState() state {
	b, err := os.ReadFile(e.statePath())
	var st state
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (e *Engine) saveState(st state) {
	_ = e.Deps.WriteJSONAtomic(e.statePath(), st)
}

// Loop runs the synthetic probe on its configured interval. Zero or
// negative means disabled — the caller may still spawn it, it just returns.
func (e *Engine) Loop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			e.Record(ctx, now, e.Run(ctx, now))
		}
	}
}
