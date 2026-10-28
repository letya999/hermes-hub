package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/compaction"
)

var signalRuntimeProcess = func() {
	if process, err := os.FindProcess(os.Getpid()); err == nil {
		_ = process.Signal(os.Interrupt)
	}
}

type wakeAttempt struct {
	response ExecuteResponse
	err      error
	elapsed  time.Duration
}

// retryColdWake repeats one chat turn that died before any tool call while
// the runtime was still waking. A finished answer, a long failure, or a run
// that already called a tool is left alone.
func retryColdWake(response ExecuteResponse, err error, elapsed time.Duration, sawTool bool) bool {
	if err == nil || sawTool || strings.TrimSpace(response.Text) != "" || elapsed > 20*time.Second {
		return false
	}
	return response.Status == "failed" || response.Status == ""
}

func (s *runtimeHTTP) admitAndObserve(ctx context.Context, request ExecuteRequest, emit func(ExecuteResponse) error) (wakeAttempt, bool) {
	started := time.Now()
	sawTool := false
	wrapped := emit
	if emit != nil {
		wrapped = func(event ExecuteResponse) error {
			if event.LastEvent == "tool.start" || event.LastEvent == "tool.end" {
				sawTool = true
			}
			return emit(event)
		}
	}
	response, err := s.admitHermesRun(ctx, request, wrapped)
	return wakeAttempt{response: response, err: err, elapsed: time.Since(started)}, sawTool
}

func (s *runtimeHTTP) admitHermesRun(ctx context.Context, request ExecuteRequest, emit func(ExecuteResponse) error) (ExecuteResponse, error) {
	input := request.Text
	if request.InboundID != "" {
		rel, err := fetchInbound(ctx, request.InboundID)
		if err != nil {
			return ExecuteResponse{}, err
		}
		input += "\nSaved incoming file: " + rel + ". Read or extract this file before answering."
	}
	sessionID := sessionIDFor(request)
	if notice := compaction.TakeNotice(env("HUB_STATE", "/state"), sessionID); notice != nil {
		input += "\n[hub: context compacted (" + notice.Trigger + ") at " + notice.At.UTC().Format(time.RFC3339) + "; earlier turns were folded into a summary]"
	}
	payload := hermesRunBody{Input: input, SessionID: sessionID, Instructions: request.Instructions}
	// Only supervisor-owned jobs support metadata-only admission recovery.
	if request.JobID != "" {
		var err error
		payload, err = admissionPayload(request, &payload)
		if err != nil {
			return ExecuteResponse{}, fmt.Errorf("%w: %w", errAdmissionReceipt, err)
		}
	}
	return s.postHermesRun(ctx, request, payload, emit)
}

func (s *runtimeHTTP) postHermesRun(ctx context.Context, request ExecuteRequest, payload hermesRunBody, emit func(ExecuteResponse) error) (ExecuteResponse, error) {
	base := "http://" + env("HUB_HERMES_API_HOST", "127.0.0.1") + ":" + env("HUB_HERMES_API_PORT", "8642")
	auth := env("API_SERVER_KEY", os.Getenv("HUB_RUNTIME_AUTH"))
	client := &http.Client{Timeout: 10 * time.Second}
	sessionID := payload.SessionID
	if err := hermesRequest(ctx, client, http.MethodPost, base+"/api/sessions", auth, map[string]any{"id": sessionID}, nil); err != nil && !errors.Is(err, errSessionExists) {
		return ExecuteResponse{}, err
	}
	var admission struct {
		RunID string `json:"run_id"`
	}
	runStart := time.Now()
	if err := hermesRequest(ctx, client, http.MethodPost, base+"/v1/runs", auth, payload, &admission, request.IdempotencyKey); err != nil {
		return ExecuteResponse{}, err
	}
	if admission.RunID == "" {
		return ExecuteResponse{}, errors.New("hermes returned no run ID")
	}
	if originalStart := runStartFor(admission.RunID); !originalStart.IsZero() {
		runStart = originalStart
	} else {
		markRunStart(admission.RunID)
	}
	known := ExecuteResponse{JobID: request.JobID, SessionID: sessionID, RunID: admission.RunID, RuntimeGeneration: os.Getenv("HUB_RUNTIME_GENERATION"), Status: "running", LastEvent: "run.admitted", EventID: "admitted"}
	response, err := s.observeHermesRunWindow(ctx, known, emit, runStart)
	s.observeCompactions(context.Background(), response.SessionID)
	// The sweep compacts between jobs — async so a real archive_and_compact
	// can never delay the completed response the caller is waiting on.
	go s.sweepAfterJob(context.Background(), response.SessionID) // #nosec G118 -- detached sweep intentionally outlives the request context
	return response, err
}

// sweepSummarize is the production summarizer the sweep calls; a package
// variable so tests can replace the relay call.
var sweepSummarize = compaction.RelaySummarize

// sweepAfterJob runs the between-jobs compaction policy for a finished job's
// session. Disabled declarations return immediately; every other outcome
// (commit, refusal, error) is the journal's business, never the caller's.
func (s *runtimeHTTP) sweepAfterJob(ctx context.Context, sessionID string) {
	if sessionID == "" {
		return
	}
	stateDir := env("HUB_STATE", "/state")
	mc := compaction.ManagedContextFromEnv()
	gate := mc.SweepGate()
	model := mc.SummaryModel
	if model == "" {
		model = os.Getenv("HUB_MANAGED_MODEL_ID")
	}
	// A queued manual compact owns the boundary first — it was asked for
	// explicitly, and a commit makes the scheduled sweep unnecessary.
	if pending := compaction.TakePending(stateDir, sessionID); pending != nil {
		result, err := compaction.Compact(ctx, compaction.CompactOptions{
			StateDir: stateDir, SessionID: sessionID, Holder: compactHolder,
			Trigger: compaction.TriggerManual, Engine: pending.Engine, ResolvedModel: model,
			Threshold: gate.Threshold, TailCount: sweepTailCount,
			Args: pending.Args, Now: time.Now().UTC, Summarize: compactSummarize,
		})
		if err == nil && result.Applied {
			return
		}
	}
	if !gate.Enabled || !mc.HubCompactGranted() {
		return
	}
	_, _ = compaction.Sweep(ctx, compaction.SweepOptions{
		StateDir: stateDir, SessionID: sessionID,
		ResolvedModel: model, Gate: gate, Now: time.Now().UTC,
		Summarize: sweepSummarize, TailCount: sweepTailCount,
	})
}

// sweepTailCount is the verbatim tail each compaction keeps: recent turns
// stay lossless while the folded prefix carries the summary.
const sweepTailCount = 8

// observeCompactions journals the in-place compactions a finished job's
// transcript proves happened — upstream's in-loop compression leaves no
// hub-side event otherwise. Best-effort: a failed scan or journal append
// must never turn a completed job into an error.
func (s *runtimeHTTP) observeCompactions(ctx context.Context, sessionID string) {
	if sessionID == "" {
		return
	}
	scan, err := compaction.ScanCompactions(ctx, sessionID)
	if err != nil {
		return
	}
	j, err := compaction.Open(env("HUB_STATE", "/state"))
	if err != nil {
		return
	}
	_, _ = j.Observe(sessionID, *scan)
}

func (s *runtimeHTTP) observeHermesRun(ctx context.Context, known ExecuteResponse, emit func(ExecuteResponse) error) (ExecuteResponse, error) {
	// A recovered observation never knew the run's start; the durable marker
	// recorded at admission restores the artifact-scan window so files the
	// interrupted run produced still deliver.
	return s.observeHermesRunWindow(ctx, known, emit, runStartFor(known.RunID))
}

func (s *runtimeHTTP) observeHermesRunWindow(ctx context.Context, known ExecuteResponse, emit func(ExecuteResponse) error, runStart time.Time) (ExecuteResponse, error) {
	base := "http://" + env("HUB_HERMES_API_HOST", "127.0.0.1") + ":" + env("HUB_HERMES_API_PORT", "8642")
	auth := env("API_SERVER_KEY", os.Getenv("HUB_RUNTIME_AUTH"))
	client := &http.Client{Timeout: 10 * time.Second}
	sessionID := known.SessionID
	admission := struct{ RunID string }{known.RunID}
	var events <-chan nativeRunEvent
	if emit != nil {
		if err := emit(known); err != nil {
			known.Status, known.LastEvent = "uncertain", "run.unknown"
			return known, err
		}
		streamCtx, closeStream := context.WithCancel(ctx)
		defer closeStream()
		events = nativeRunEvents(streamCtx, base, auth, admission.RunID)
	}
	lastApproval := ""
	// Stall watchdog: a run whose Hermes-side update timestamp stops moving
	// for stallTimeout is hung (e.g. a severed upstream model stream) — stop
	// it instead of polling until the hard deadline.
	stallTimeout := durationEnv("HUB_RUN_STALL_TIMEOUT", 15*time.Minute)
	lastProgress, lastRunUpdate := time.Now(), float64(0)
	deadline := time.Now().Add(RunBudget())
	// Stream-first: while the SSE channel is open, durable status is a slow
	// fallback polled every 2s; after the stream drops, tighten to 1s so a
	// terminal event still surfaces promptly.
	poll := time.NewTimer(0)
	defer poll.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-poll.C:
			var status struct {
				Status    string         `json:"status"`
				Output    string         `json:"output"`
				Error     string         `json:"error"`
				Approval  nativeRunEvent `json:"approval"`
				SessionID string         `json:"session_id"`
				UpdatedAt float64        `json:"updated_at"`
			}
			if err := hermesRequest(ctx, client, http.MethodGet, base+"/v1/runs/"+admission.RunID, auth, nil, &status); err != nil {
				known.Status, known.LastEvent = "uncertain", "run.unknown"
				return known, err
			}
			interval := 2 * time.Second
			if events == nil {
				interval = time.Second
			}
			poll.Reset(interval)
			if status.UpdatedAt > lastRunUpdate {
				lastRunUpdate, lastProgress = status.UpdatedAt, time.Now()
			} else if stallTimeout > 0 && time.Since(lastProgress) > stallTimeout {
				stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				result, _ := stopAndConfirmHermesRun(stopCtx, client, base, auth, known)
				cancel()
				return result, fmt.Errorf("hermes run stalled (no update for %s)", stallTimeout)
			}
			if status.SessionID != "" && status.SessionID != sessionID {
				known.Status = "uncertain"
				return known, errors.New("run session mismatch")
			}
			switch status.Status {
			case "waiting_for_approval":
				if emit != nil && status.Approval.RequestID != "" && status.Approval.RequestID != lastApproval {
					update := known
					update.Status, update.LastEvent, update.EventID = "waiting_for_approval", "approval.request", "approval:"+status.Approval.RequestID
					update.ApprovalID, update.ApprovalChoices = status.Approval.RequestID, status.Approval.Choices
					update.ApprovalSummary = approvalSummaryText(status.Approval)
					if len(update.ApprovalID) > 256 {
						return known, errors.New("invalid approval identity")
					}
					if err := emit(update); err != nil {
						known.Status = "uncertain"
						return known, err
					}
					lastApproval = update.ApprovalID
				}
			case "completed":
				text, mediaRefs := extractMediaArtifacts(status.Output)
				text, voice := extractVoiceLines(text)
				if strings.TrimSpace(text) != "" || len(mediaRefs) > 0 || voice != "" {
					final := ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: "completed", LastEvent: "run.completed", Text: text, Voice: voice}
					final.Artifacts = mergeArtifacts(scanArtifacts(runStart), mediaRefs)
					return final, nil
				}
				// Session history can contain a reply from an earlier run. Without
				// run-correlated output it cannot safely stand in for this final.
				known.Status, known.LastEvent = "uncertain", "run.unknown"
				return known, errors.New("completed run returned no final output")
			case "failed", "cancelled", "interrupted":
				if status.Error != "" {
					return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: status.Status, LastEvent: "run." + status.Status}, errors.New(status.Error)
				}
				return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: status.Status, LastEvent: "run." + status.Status}, fmt.Errorf("hermes run %s", status.Status)
			}
		case event, ok := <-events:
			if !ok {
				events = nil // Pinned Hermes destroys SSE transport on disconnect; poll durable status.
				poll.Reset(time.Second)
				continue
			}
			lastProgress = time.Now() // A live event stream is progress by itself.
			if event.RunID != admission.RunID {
				known.Status, known.LastEvent = "uncertain", "run.unknown"
				return known, errors.New("hermes stream returned another run")
			}
			update := known
			update.LastEvent, update.EventID = event.Event, eventID(event)
			switch event.Event {
			case "approval.request":
				if event.RequestID == "" || len(event.RequestID) > 256 {
					return known, errors.New("invalid approval identity")
				}
				update.Status, update.ApprovalID, update.ApprovalChoices = "waiting_for_approval", event.RequestID, event.Choices
				update.ApprovalSummary = approvalSummaryText(event)
				if lastApproval == event.RequestID {
					continue
				}
				update.EventID, lastApproval = "approval:"+event.RequestID, event.RequestID
			case "tool.start", "tool.started", "tool.end", "tool.completed", "run.started":
				if event.Event == "tool.started" {
					update.LastEvent = "tool.start"
				} else if event.Event == "tool.completed" {
					update.LastEvent = "tool.end"
				}
				update.Text = toolProgressText(event)
				update.ToolName = event.Tool
			default:
				continue // Never project deltas, arguments, prompts or traces into chat.
			}
			if err := emit(update); err != nil {
				known.Status, known.LastEvent = "uncertain", "run.unknown"
				return known, err
			}
		case <-ctx.Done():
			if emit != nil {
				known.Status, known.LastEvent = "uncertain", "run.unknown"
				return known, ctx.Err()
			}
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			result, _ := stopAndConfirmHermesRun(stopCtx, client, base, auth, known)
			cancel()
			return result, ctx.Err()
		}
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	result, _ := stopAndConfirmHermesRun(stopCtx, client, base, auth, known)
	cancel()
	return result, errors.New("hermes run timed out")
}

// approvalSummaryText renders what the approval actually gates; upstream
// redacts secrets in description/command before they reach the stream. One
// bounded line so the prompt stays readable and cannot smuggle layout.
func approvalSummaryText(event nativeRunEvent) string {
	desc := strings.Join(strings.Fields(event.Description), " ")
	cmd := strings.Join(strings.Fields(event.Command), " ")
	text := desc
	if cmd != "" {
		text = strings.TrimSpace(cmd + " — " + desc)
	}
	if r := []rune(text); len(r) > 300 {
		text = string(r[:299]) + "…"
	}
	return text
}

func toolProgressText(event nativeRunEvent) string {
	if event.Error {
		return "Шаг завершился ошибкой; проверяю причину."
	}
	tool := strings.ReplaceAll(event.Tool, "__", "_")
	completed := event.Event == "tool.end" || event.Event == "tool.completed"
	switch {
	case strings.HasSuffix(tool, "toolhub_prepare_source"):
		if completed {
			switch toolResultPhase(event) {
			case "preparing":
				return "MCP: шаг 1/4 принят — сборка ещё идёт в фоне. Итог придёт отдельным сообщением."
			case "failed":
				return "MCP: подготовка не завершилась. Смотрю причину."
			case "awaiting-credentials", "awaiting-confirm":
				return "MCP: шаг 1/4 вернул ответ. Дальше статус и защищённая форма, если она нужна."
			default:
				return "MCP: шаг 1/4 вернул ответ. Если фаза ещё preparing, сборка продолжается в фоне."
			}
		}
		return "MCP: шаг 1/4 — проверяю репозиторий и собираю изолированный образ. Обычно 2–15 минут."
	case strings.HasSuffix(tool, "toolhub_required_credentials"):
		if completed {
			return "MCP: шаг 2/4 завершён — безопасная ссылка авторизации готова."
		}
		return "MCP: шаг 2/4 — готовлю безопасную авторизацию."
	case strings.HasSuffix(tool, "toolhub_confirm"):
		if completed {
			return "MCP: шаг 3/4 завершён — учётные данные подтверждены."
		}
		return "MCP: шаг 3/4 — подтверждаю сохранённые учётные данные."
	case strings.HasSuffix(tool, "toolhub_enable"):
		if completed {
			return "MCP: шаг 4/4 завершён — инструменты подключены без рестарта."
		}
		return "MCP: шаг 4/4 — запускаю workload и подключаю инструменты без рестарта."
	case strings.HasSuffix(tool, "toolhub_invoke"):
		return "MCP подключён — проверяю реальный вызов провайдера."
	case completed:
		return "Шаг завершён, продолжаю."
	default:
		return ""
	}
}

func toolResultPhase(event nativeRunEvent) string {
	parts := []string{event.Output, event.Text, string(event.Result)}
	for _, part := range parts {
		part = strings.ReplaceAll(part, " ", "")
		for _, phase := range []string{"awaiting-credentials", "awaiting-confirm", "preparing", "failed", "enabled"} {
			if strings.Contains(part, `"phase":"`+phase+`"`) {
				return phase
			}
		}
	}
	return ""
}

func stopHermesRun(ctx context.Context, client *http.Client, base, auth, runID string) error {
	return hermesRequest(ctx, client, http.MethodPost, base+"/v1/runs/"+runID+"/stop", auth, map[string]any{}, nil)
}

func stopAndConfirmHermesRun(ctx context.Context, client *http.Client, base, auth string, known ExecuteResponse) (ExecuteResponse, error) {
	known.Status, known.LastEvent = "uncertain", "run.unknown"
	if err := stopHermesRun(ctx, client, base, auth, known.RunID); err != nil {
		return known, err
	}
	for {
		var status struct {
			Status    string `json:"status"`
			Output    string `json:"output"`
			SessionID string `json:"session_id"`
		}
		if err := hermesRequest(ctx, client, http.MethodGet, base+"/v1/runs/"+known.RunID, auth, nil, &status); err != nil {
			return known, err
		}
		if status.SessionID != "" && status.SessionID != known.SessionID {
			return known, errors.New("run session mismatch")
		}
		switch status.Status {
		case "completed", "failed", "cancelled", "interrupted":
			known.Status, known.LastEvent, known.Text = status.Status, "run."+status.Status, status.Output
			return known, nil
		}
		select {
		case <-ctx.Done():
			return known, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func sessionIDFor(request ExecuteRequest) string {
	sum := sha256.Sum256([]byte(request.ContextID + "\x00" + request.ConversationID + "\x00" + sessionRevision()))
	return "hub-" + hex.EncodeToString(sum[:12])
}

func sessionRevision() string {
	if revision := strings.TrimSpace(os.Getenv("HUB_SESSION_REVISION")); revision != "" {
		return revision
	}
	// The revision tracks what the agent reads: the effective SOUL (persona
	// plus channel contract) at $HERMES_HOME when mounted, else the persona
	// source legacy mounts provide. A contract change rotates sessions once.
	body, _ := os.ReadFile(filepath.Join(os.Getenv("HERMES_HOME"), "SOUL.md")) // #nosec G703 -- session config path
	if len(body) == 0 {
		body, _ = os.ReadFile("/config/SOUL.md")
	}
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

var errSessionExists = errors.New("hermes session already exists")

func hermesRequest(ctx context.Context, client *http.Client, method, endpoint, auth string, body any, target any, idempotency ...string) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader) // #nosec G704 -- internal hermes endpoint
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	if len(idempotency) > 0 && idempotency[0] != "" {
		req.Header.Set("Idempotency-Key", idempotency[0])
	}
	response, err := client.Do(req) // #nosec G704 -- internal hermes endpoint
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict && method == http.MethodPost && strings.HasSuffix(endpoint, "/api/sessions") {
		return errSessionExists
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("hermes API returned HTTP %d", response.StatusCode)
	}
	if target == nil {
		return nil
	}
	limited := io.LimitReader(response.Body, 2*1024*1024)
	if err := json.NewDecoder(limited).Decode(target); err != nil {
		return fmt.Errorf("invalid hermes API response: %w", err)
	}
	return nil
}
