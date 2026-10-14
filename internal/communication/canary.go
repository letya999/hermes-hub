package communication

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// canaryInterval reads HUB_CANARY_INTERVAL ("5m" or bare seconds); 0 =
// disabled — the canary is opt-in only.
func canaryInterval() time.Duration {
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
	canaryChatID = int64(-9000)
	canaryPrompt = "Call the file_list tool once on the `canary` directory, then reply with the entry count."
	canaryTool   = "file_list"
)

// canaryJobTimeout bounds a single verification round; a run that has not
// settled inside it counts as the timeout failure class.
var canaryJobTimeout = 90 * time.Second

// runCanary executes one synthetic end-to-end check and returns the failure
// class ("" = healthy).
func (g *Gateway) runCanary(ctx context.Context, now time.Time) string {
	user, ok := g.canaryUser()
	if !ok {
		return "no_owner"
	}
	if class := g.canarySurfaceCheck(ctx, user); class != "" {
		return class
	}
	if err := g.mockLinkClaim(canaryChatID, user.ID); err != nil {
		return "mock_link"
	}
	task, err := g.spool.ResolveTask(user, canaryChatID, 0, true)
	if err != nil {
		return "task_bind"
	}
	tick := now.UTC().Format("20060102T150405")
	jobID := "canary-" + tick
	if err := g.enqueueChannelJob(ctx, user, task, "mock", jobID, "canary:"+tick, canaryChatID, 0, canaryPrompt, canaryChatID, ""); err != nil {
		return "enqueue"
	}
	deadline := now.Add(canaryJobTimeout)
	for {
		mapping, found, err := g.spool.Mapping(jobID)
		if err == nil && found && terminalStatus(mapping.Status) {
			if mapping.Status != "completed" {
				return "run_" + mapping.Status
			}
			if class := g.canaryToolCheck(jobID); class != "" {
				return class
			}
			return g.canaryDeliveryCheck(jobID)
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return "timeout"
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// canaryToolCheck reads the job's durable stream receipts — a completed run
// with no tool.start means the tool surface is broken even though the model
// answered.
func (g *Gateway) canaryToolCheck(jobID string) string {
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "events"))
	if err != nil {
		return "events_unreadable"
	}
	calls := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, readErr := os.ReadFile(filepath.Join(g.spool.root, "events", entry.Name()))
		var receipt streamReceipt
		if readErr != nil || json.Unmarshal(b, &receipt) != nil {
			continue
		}
		if receipt.JobID == jobID && receipt.Event.LastEvent == "tool.start" {
			switch receipt.Event.ToolName {
			case canaryTool, "mcp__hub__file_list":
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

// canaryDeliveryCheck waits briefly for the response delivery to land in the
// mock outbox — the chain isn't proven until the answer is durably captured.
var canaryDeliveryGrace = 15 * time.Second

func (g *Gateway) canaryDeliveryCheck(jobID string) string {
	path := filepath.Join(g.mockRoot(), "mock-sent", spoolFileID("job-"+jobID+"-response")+".json")
	deadline := time.Now().Add(canaryDeliveryGrace)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return ""
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "delivery"
}

// canarySurfaceCheck compares the persisted readiness surface against the
// probe's required tool before spending a run: a manifest that no longer hands
// out file_list — or a ToolHub reason — means the tool surface is broken and
// the run outcome would only restate it. A missing surface (unsupervised,
// unreachable) is not a failure: the run itself stays the ground truth.
func (g *Gateway) canarySurfaceCheck(ctx context.Context, user User) string {
	if strings.TrimSpace(g.config.RuntimeURL) == "" || strings.TrimSpace(g.config.RuntimeAuth) == "" {
		return ""
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var surface *runtimeSurface
	if g.config.Supervised {
		surface = g.supervisedSurface(probeCtx, user)
	} else {
		surface = g.residentSurface(probeCtx)
	}
	if surface == nil {
		return ""
	}
	for _, missing := range surface.MissingTools {
		if missing == canaryTool {
			return "surface"
		}
	}
	return ""
}

// canaryUser is the configured owner context the canary runs under: the first
// enabled user, the same owner notices address.
func (g *Gateway) canaryUser() (User, bool) {
	for _, u := range g.config.Users {
		if u.Enabled {
			return u, true
		}
	}
	return User{}, false
}

type canaryState struct {
	LastClass string `json:"last_class,omitempty"`
}

// recordCanary persists the tick outcome and raises owner notices: one per
// new failure class, deterministic text, no model involved (SPEC-0053 R3.4).
func (g *Gateway) recordCanary(ctx context.Context, now time.Time, class string) {
	g.journalCanary(now, class)
	state := g.canaryState()
	owner, ok := g.canaryUser()
	if !ok {
		return
	}
	chatID := int64(0)
	if len(owner.TelegramIDs) > 0 {
		chatID = owner.TelegramIDs[0]
	}
	if class != "" && class != state.LastClass {
		_ = g.queueDelivery(ctx, "canary-"+class, chatID, 0,
			"Канареечная проверка не проходит: "+class+". Проверьте /status.")
		state.LastClass = class
	} else if class == "" && state.LastClass != "" {
		_ = g.queueDelivery(ctx, "canary-recovered", chatID, 0,
			"Канареечная проверка восстановилась после "+state.LastClass+".")
		state.LastClass = ""
	}
	g.saveCanaryState(state)
}

// journalCanary appends the tick outcome to canary.jsonl — durable history for
// `hubctl trace` and post-incident review, bounded so the file can't grow
// without limit. Truncation keeps the most recent entries.
const canaryJournalKeep = 1000

func (g *Gateway) journalCanary(now time.Time, class string) {
	line, err := json.Marshal(map[string]any{"ts": now.UTC().Format(time.RFC3339), "class": class})
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(g.spool.root, "canary.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
	path := filepath.Join(g.spool.root, "canary.jsonl")
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > 1<<20 {
		if b, readErr := os.ReadFile(path); readErr == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) > canaryJournalKeep {
				lines = lines[len(lines)-canaryJournalKeep:]
			}
			_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
		}
	}
}

func (g *Gateway) canaryStatePath() string {
	return filepath.Join(g.spool.root, "canary-state.json")
}

func (g *Gateway) canaryState() canaryState {
	b, err := os.ReadFile(g.canaryStatePath())
	var state canaryState
	if err == nil {
		_ = json.Unmarshal(b, &state)
	}
	return state
}

func (g *Gateway) saveCanaryState(state canaryState) {
	_ = atomicJSON(g.canaryStatePath(), state)
}

// canaryLoop runs the synthetic probe on its configured interval. Zero or
// negative means disabled — the caller may still spawn it, it just returns.
func (g *Gateway) canaryLoop(ctx context.Context, interval time.Duration) {
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
			g.recordCanary(ctx, now, g.runCanary(ctx, now))
		}
	}
}
