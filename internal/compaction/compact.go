package compaction

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// CompactArgs are the parsed /compact arguments — the bounded vocabulary the
// channel command and the tool share.
type CompactArgs struct {
	Here    int    `json:"here,omitempty"`
	Focus   string `json:"focus,omitempty"`
	Prune   bool   `json:"prune,omitempty"`
	Preview bool   `json:"preview,omitempty"`
}

// CompactResult is the allowlisted response projection: metadata only, never
// summary or transcript text.
type CompactResult struct {
	Applied        bool   `json:"applied"`
	Method         string `json:"method,omitempty"`
	WouldMethod    string `json:"would_method,omitempty"`
	TokensBefore   *int64 `json:"tokens_before_est"`
	TokensAfter    *int64 `json:"tokens_after_est"`
	MessagesBefore int    `json:"messages_before"`
	MessagesAfter  int    `json:"messages_after"`
	RetainedTail   int    `json:"retained_tail"`
	FailureClass   string `json:"failure_class,omitempty"`
}

// CompactOptions carries the resolved engine request: session, holder, args
// and the summarizer seam.
type CompactOptions struct {
	StateDir      string
	SessionID     string
	Holder        string
	Trigger       string
	Engine        string
	ResolvedModel string
	Threshold     int64
	TailCount     int
	Args          CompactArgs
	Now           func() time.Time
	Summarize     func(ctx context.Context, model string, messages []map[string]any, focus string) (string, error)
}

// Compact runs the manual compaction pipeline for one session.
func Compact(ctx context.Context, o CompactOptions) (CompactResult, error) {
	before, _, err := EstimateTokens(ctx, o.SessionID)
	if errors.Is(err, errSessionMissing) {
		return CompactResult{FailureClass: "session_missing"}, nil
	}
	if err != nil {
		return CompactResult{}, err
	}
	result := CompactResult{TokensBefore: &before}
	if o.Args.Preview {
		return o.preview(ctx, &result, before)
	}
	acquired, err := sidecarAcquireLease(ctx, o.SessionID, o.Holder, compactLeaseTTL)
	if err != nil {
		return CompactResult{}, err
	}
	if !acquired {
		result.FailureClass = RefusalLeaseHeld
		return result, nil
	}
	defer func() { _ = sidecarReleaseLease(context.Background(), o.SessionID, o.Holder) }()
	locked, err := sidecarLock(ctx, o.SessionID, o.Holder, compactLockTTL)
	if err != nil {
		return CompactResult{}, err
	}
	if !locked {
		result.FailureClass = "locked"
		return result, nil
	}
	defer func() { _ = sidecarUnlock(context.Background(), o.SessionID, o.Holder) }()
	if o.Engine == EngineNative {
		return o.compactNative(ctx, &result, &before)
	}
	transcript, err := sidecarReadTranscript(ctx, o.SessionID)
	if err != nil {
		return CompactResult{}, err
	}
	result.MessagesBefore = len(transcript.Messages)
	head, tail := splitHeadTail(transcript.Messages, o.Args.Here, o.TailCount)
	result.RetainedTail = len(tail)
	if len(head) == 0 {
		result.FailureClass = "nothing_to_compress"
		return result, nil
	}
	pruned, prunedCount := pruneToolOutputs(head)
	if o.Args.Prune {
		if prunedCount == 0 {
			result.FailureClass = "nothing_to_prune"
			return result, nil
		}
		candidate := append(pruned, tail...)
		if _, err := sidecarApplyTranscript(ctx, o.SessionID, o.Holder, candidate, &transcript.Watermark, len(tail)); err != nil {
			return CompactResult{}, err
		}
		result.Applied = true
		result.Method = MethodPruneOnly
		result.MessagesAfter = len(candidate)
		if journal, err := Open(o.StateDir); err == nil {
			_ = journal.Append(Event{At: o.Now(), SessionID: o.SessionID, Trigger: o.Trigger,
				Engine: o.Engine, Method: MethodPruneOnly, TokensBefore: &before})
		}
		return result, nil
	}
	bare := o.Args.Here == 0 && o.Args.Focus == ""
	if bare && before > o.Threshold {
		candidate := append(pruned, tail...)
		if roughTokens(candidate) <= o.Threshold {
			if _, err := sidecarApplyTranscript(ctx, o.SessionID, o.Holder, candidate, &transcript.Watermark, len(tail)); err != nil {
				return CompactResult{}, err
			}
			result.Applied = true
			result.Method = MethodPruneOnly
			result.MessagesAfter = len(candidate)
			if journal, err := Open(o.StateDir); err == nil {
				_ = journal.Append(Event{At: o.Now(), SessionID: o.SessionID, Trigger: o.Trigger,
					Engine: o.Engine, Method: MethodPruneOnly, TokensBefore: &before})
			}
			return result, nil
		}
	}
	state := loadSessionSweep(o.StateDir, o.SessionID)
	summary := ""
	method := MethodSummary
	if h := state.Held; h != nil && h.Model == o.ResolvedModel && h.Watermark == transcript.Watermark {
		summary = h.Summary
		method = MethodPrecomputed
	} else {
		summary, err = o.Summarize(ctx, o.ResolvedModel, pruned, o.Args.Focus)
		if err != nil {
			result.FailureClass = "summarize_failed"
			o.journalFailure(&result, &before)
			return result, nil
		}
	}
	carrier := map[string]any{
		"role":                "user",
		"content":             summaryCarrierPrefix + summary + "\n" + summaryEndMarker,
		"_compressed_summary": true,
		"timestamp":           float64(o.Now().Unix()),
	}
	messages := mergedTail(carrier, tail)
	if roughTokens(messages) >= roughTokens(transcript.Messages) && len(tail) > 1 {
		// Shrink-and-retry: a shorter retained tail may let the summary fit
		// before the lossy fallback is reached.
		shrunk := tail[len(tail)/2:]
		if candidate := mergedTail(carrier, shrunk); roughTokens(candidate) < roughTokens(transcript.Messages) {
			messages = candidate
			tail = shrunk
			result.RetainedTail = len(shrunk)
			method = MethodShrinkRetry
		}
	}
	if method != MethodShrinkRetry && roughTokens(messages) >= roughTokens(transcript.Messages) {
		// The summary would grow the transcript — fall back to the honest
		// bounded-loss stage: drop the head behind a checkpoint marker.
		marker := map[string]any{
			"role":                "user",
			"content":             "[hub: context checkpoint — " + strconv.Itoa(len(head)) + " earlier turn(s) dropped without summary]",
			"_compressed_summary": true,
			"timestamp":           float64(o.Now().Unix()),
		}
		dropped := mergedTail(marker, tail)
		if roughTokens(dropped) >= roughTokens(transcript.Messages) {
			result.FailureClass = "would_grow"
			o.journalFailure(&result, &before)
			return result, nil
		}
		messages = dropped
		method = MethodDropCheckpoint
	}
	if _, err := sidecarApplyTranscript(ctx, o.SessionID, o.Holder, messages, &transcript.Watermark, len(tail)); err != nil {
		result.FailureClass = "apply_failed"
		o.journalFailure(&result, &before)
		return result, nil
	}
	result.Applied = true
	result.Method = method
	result.MessagesAfter = len(messages)
	after := roughTokens(messages)
	result.TokensAfter = &after
	if journal, err := Open(o.StateDir); err == nil {
		_ = journal.Append(Event{At: o.Now(), SessionID: o.SessionID, Trigger: o.Trigger,
			Engine: o.Engine, Method: result.Method, TokensBefore: &before, TokensAfter: &after})
	}
	state.Held = nil
	_ = saveSessionSweep(o.StateDir, o.SessionID, state)
	return result, nil
}

// compactNative delegates the compress itself to upstream through the
// sidecar — the hub still owns the turn boundary and the journal.
func (o CompactOptions) compactNative(ctx context.Context, result *CompactResult, before *int64) (CompactResult, error) {
	out, err := sidecarNativeCompress(ctx, o.SessionID, o.Holder, o.Args)
	if errors.Is(err, errSidecarUnsupported) {
		result.FailureClass = RefusalUnsupported
		return *result, nil
	}
	if err != nil {
		return CompactResult{}, err
	}
	result.MessagesBefore = out.MessagesBefore
	if !out.Applied {
		result.FailureClass = out.FailureClass
		if result.FailureClass == "" {
			result.FailureClass = "nothing_to_compress"
		}
		return *result, nil
	}
	result.Applied = true
	result.Method = MethodArchiveCompct
	result.MessagesAfter = out.ActiveCount
	result.RetainedTail = out.RetainedTail
	if out.TokensAfter > 0 {
		result.TokensAfter = &out.TokensAfter
	}
	if journal, err := Open(o.StateDir); err == nil {
		_ = journal.Append(Event{At: o.Now(), SessionID: o.SessionID, Trigger: o.Trigger,
			Engine: EngineNative, Method: result.Method,
			TokensBefore: before, TokensAfter: result.TokensAfter})
	}
	return *result, nil
}

// nativeResult is the sidecar's allowlisted report — counts and booleans,
// never message content.
type nativeResult struct {
	Applied        bool   `json:"applied"`
	ActiveCount    int    `json:"active_count"`
	MessagesBefore int    `json:"messages_before"`
	RetainedTail   int    `json:"retained_tail"`
	TokensAfter    int64  `json:"tokens_after_est"`
	FailureClass   string `json:"failure_class"`
}

func sidecarNativeCompress(ctx context.Context, sessionID, holder string, args CompactArgs) (*nativeResult, error) {
	var out nativeResult
	err := sidecarCall(ctx, "native_compress", map[string]any{
		"session_id": sessionID, "holder": holder,
		"keep_last": args.Here, "focus": args.Focus,
		"prune": args.Prune, "preview": args.Preview}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// splitHeadTail picks the verbatim tail: `here N` keeps the last N user
// exchanges (upstream /compress here semantics — the boundary lands on the
// Nth user turn from the end); a bare compact keeps the configured tail.
func splitHeadTail(messages []map[string]any, keepExchanges, defaultTail int) (head, tail []map[string]any) {
	if keepExchanges > 0 {
		seen := 0
		for i := len(messages) - 1; i >= 0; i-- {
			if role, _ := messages[i]["role"].(string); role == "user" {
				seen++
				if seen == keepExchanges {
					return messages[:i], messages[i:]
				}
			}
		}
		// Fewer exchanges than asked — degenerate split falls back to the
		// default tail like upstream's full-compression fallback.
	}
	if defaultTail > 0 && len(messages) > defaultTail {
		return messages[:len(messages)-defaultTail], messages[len(messages)-defaultTail:]
	}
	return nil, messages
}

// mergedTail returns the carrier plus verbatim tail copies. A tail that
// opens with a user row folds its text into the carrier — the merged-handoff
// shape upstream itself emits — so the seam never holds two user rows.
func mergedTail(carrier map[string]any, tail []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tail)+1)
	start := 0
	if len(tail) > 0 {
		if role, _ := tail[0]["role"].(string); role == "user" {
			if text, _ := tail[0]["content"].(string); text != "" {
				if content, _ := carrier["content"].(string); content != "" {
					carrier["content"] = content + "\n\n" + text
					start = 1
				}
			}
		}
	}
	out = append(out, carrier)
	for _, m := range tail[start:] {
		copy := make(map[string]any, len(m))
		for k, v := range m {
			if k != "_row_id" {
				copy[k] = v
			}
		}
		out = append(out, copy)
	}
	return out
}

// preview runs the dry-run pipeline: transcript read, split, and the stage
// check — minus lease, lock, summarizer spend and apply. Its report names
// the stage that would fire.
func (o CompactOptions) preview(ctx context.Context, result *CompactResult, before int64) (CompactResult, error) {
	if o.Engine == EngineNative {
		out, err := sidecarNativeCompress(ctx, o.SessionID, o.Holder, o.Args)
		if errors.Is(err, errSidecarUnsupported) {
			result.FailureClass = RefusalUnsupported
			return *result, nil
		}
		if err != nil {
			return CompactResult{}, err
		}
		result.WouldMethod = MethodArchiveCompct
		result.MessagesBefore = out.MessagesBefore
		result.RetainedTail = out.RetainedTail
		return *result, nil
	}
	transcript, err := sidecarReadTranscript(ctx, o.SessionID)
	if err != nil {
		return CompactResult{}, err
	}
	result.MessagesBefore = len(transcript.Messages)
	head := transcript.Messages
	tail := transcript.Messages
	if o.TailCount > 0 && len(tail) > o.TailCount {
		head = tail[:len(tail)-o.TailCount]
		tail = tail[len(tail)-o.TailCount:]
	}
	result.RetainedTail = len(tail)
	result.WouldMethod = MethodSummary
	pruned, _ := pruneToolOutputs(head)
	if o.Args.Prune {
		result.WouldMethod = MethodPruneOnly
	} else if o.Args.Here == 0 && o.Args.Focus == "" && before > o.Threshold && roughTokens(append(pruned, tail...)) <= o.Threshold {
		result.WouldMethod = MethodPruneOnly
	}
	return *result, nil
}

// pruneToolOutputs replaces bulky tool-result bodies in the head with a
// bounded stub. Pairing survives — the stub stays in the same slot — and
// the marker is metadata about size, never the content itself.
func pruneToolOutputs(head []map[string]any) ([]map[string]any, int) {
	out := make([]map[string]any, 0, len(head))
	pruned := 0
	for _, m := range head {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		if (role == "tool" || role == "function") && len(content) > pruneMinChars {
			pruned++
			content = "[tool output pruned: " + strconv.Itoa(len(content)) + " chars]"
		}
		copy := make(map[string]any, len(m))
		for k, v := range m {
			if k != "_row_id" {
				copy[k] = v
			}
		}
		copy["content"] = content
		out = append(out, copy)
	}
	return out, pruned
}

// pruneMinChars is the body size under which a tool output stays verbatim —
// stubbing a small result loses more signal than it saves.
const pruneMinChars = 4000

// roughTokens is the client-side fit guard: the same chars/4 floor the
// sidecar applies so the prune stage's post-prune estimate is honest.
func roughTokens(messages []map[string]any) int64 {
	var total int64
	for _, m := range messages {
		if text, _ := m["content"].(string); text != "" {
			total += int64(len(text)) / 4
		}
	}
	return total
}

// journalFailure records a mid-flight refusal — the attempt that reached the
// mutation window but never committed leaves bounded evidence.
func (o CompactOptions) journalFailure(result *CompactResult, before *int64) {
	if journal, err := Open(o.StateDir); err == nil {
		_ = journal.Append(Event{At: o.Now(), SessionID: o.SessionID, Trigger: o.Trigger,
			Engine: o.Engine, Method: MethodSummary,
			TokensBefore: before, FailureClass: result.FailureClass})
	}
}

// compactLeaseTTL bounds the manual path's turn lease the same way the
// sweep's does — a wedged engine cannot wedge the session.
const compactLeaseTTL = 180

// compactLockTTL bounds the compression lock inside the lease window.
const compactLockTTL = 120
