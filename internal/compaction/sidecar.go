package compaction

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
)

var (
	errSidecarUnavailable = errors.New("sidecar unavailable")
	errSidecarUnsupported = errors.New("upstream sidecar unsupported")
	errSidecarInvalidArgs = errors.New("sidecar invalid args")
	errSidecarInternal    = errors.New("sidecar internal error")
	errSidecarOversized   = errors.New("sidecar output oversized")
	errSessionMissing     = errors.New("session missing")
	errCompactionLocked   = errors.New("compaction locked")
)

// Runner is the process boundary: one JSON request in, one JSON response
// out, the process exits. The ctx deadline kills the child. Exported so
// sibling packages stub it in tests — the bounded error mapping happens
// inside sidecarCall, so a fake Runner cannot smuggle error detail.
var Runner = func(ctx context.Context, payload []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, sidecarProg, sidecarProgArgs...)
	cmd.Stdin = bytes.NewReader(payload)
	return cmd.Output()
}

var sidecarProg = "hub-sidecar"
var sidecarProgArgs []string

// sidecarMaxOutput bounds one response — a hard stop, never a truncation
// that could parse as a shorter valid transcript.
const sidecarMaxOutput = 32 << 20

// sidecarEstimateTokens returns the transcript token estimate and active
// message count — the sweep threshold's measured/estimated input.
func sidecarEstimateTokens(ctx context.Context, sessionID string) (tokens, messages int, err error) {
	var out struct {
		Tokens       int `json:"tokens"`
		MessageCount int `json:"message_count"`
	}
	if err := sidecarCall(ctx, "estimate_tokens", map[string]any{"session_id": sessionID}, &out); err != nil {
		return 0, 0, err
	}
	return out.Tokens, out.MessageCount, nil
}

// EstimateTokens is the exported transcript estimate for status surfaces and
// the sweep gate: token count plus active message count, or a bounded error.
func EstimateTokens(ctx context.Context, sessionID string) (tokens int64, messages int64, err error) {
	n, count, err := sidecarEstimateTokens(ctx, sessionID)
	return int64(n), int64(count), err
}

// ActiveSession resolves the session holding an unexpired turn lease — the
// verifiably-running conversation a ToolHub call belongs to. Empty means no
// lease is held.
func ActiveSession(ctx context.Context) (string, error) {
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := sidecarCall(ctx, "active_session", nil, &out); err != nil {
		return "", err
	}
	return out.SessionID, nil
}

// sidecarLock takes the upstream compression lock — the single-writer gate
// inside the container that serializes compaction against in-flight runs.
func sidecarLock(ctx context.Context, sessionID, holder string, ttlSeconds int) (bool, error) {
	var out struct {
		Locked bool `json:"locked"`
	}
	err := sidecarCall(ctx, "lock", map[string]any{
		"session_id": sessionID, "holder": holder, "ttl_seconds": ttlSeconds}, &out)
	return out.Locked, err
}

// sidecarUnlock releases the upstream compression lock. Always called on the
// exit path — a leaked lock wedges every later compaction for its TTL.
func sidecarUnlock(ctx context.Context, sessionID, holder string) error {
	return sidecarCall(ctx, "unlock", map[string]any{"session_id": sessionID, "holder": holder}, nil)
}

// sidecarApplyTranscript commits a compacted message set through upstream's
// atomic archive_and_compact. The holder must own the compression lock — a
// stolen or expired lease fails in-txn as "locked" (errCompactionLocked),
// never a clobbered commit.
func sidecarApplyTranscript(ctx context.Context, sessionID, holder string, messages []map[string]any, watermark *int, tailCount int) (int, error) {
	var out struct {
		ActiveCount int `json:"active_count"`
	}
	err := sidecarCall(ctx, "apply_transcript", map[string]any{
		"session_id": sessionID, "holder": holder, "messages": messages,
		"watermark": watermark, "tail_count": tailCount}, &out)
	if err != nil {
		return 0, err
	}
	return out.ActiveCount, nil
}

// sidecarCompactionScan is the metadata-only compaction report for one
// session: rotation lineage plus the in-place summary-row count. Nothing in
// this struct carries message content.
type sidecarCompactionScan struct {
	Lineage       []string `json:"lineage"`
	Tip           string   `json:"tip"`
	Rotations     int      `json:"rotations"`
	InPlace       int      `json:"in_place"`
	LastSummaryAt *float64 `json:"last_summary_at"`
}

type sidecarTranscript struct {
	SessionID string           `json:"session_id"`
	Watermark int              `json:"watermark"`
	Messages  []map[string]any `json:"messages"`
}

// sidecarAcquireLease takes the upstream cross-process turn lease — the
// turn-boundary gate every compaction path must pass before mutating.
func sidecarAcquireLease(ctx context.Context, sessionID, holder string, ttlSeconds int) (bool, error) {
	var out struct {
		Acquired bool `json:"acquired"`
	}
	err := sidecarCall(ctx, "lease", map[string]any{
		"session_id": sessionID, "holder": holder, "ttl_seconds": ttlSeconds}, &out)
	return out.Acquired, err
}

func sidecarReleaseLease(ctx context.Context, sessionID, holder string) error {
	return sidecarCall(ctx, "unlease", map[string]any{"session_id": sessionID, "holder": holder}, nil)
}

// sidecarReadTranscript loads the active message set plus the commit watermark
// captured at read time — rows above it arrived concurrently and upstream
// re-sequences them verbatim on apply.
func sidecarReadTranscript(ctx context.Context, sessionID string) (*sidecarTranscript, error) {
	var out sidecarTranscript
	if err := sidecarCall(ctx, "read_transcript", map[string]any{"session_id": sessionID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func sidecarScanCompactions(ctx context.Context, sessionID string) (*sidecarCompactionScan, error) {
	var out sidecarCompactionScan
	if err := sidecarCall(ctx, "scan_compactions", map[string]any{"session_id": sessionID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ScanCompactions is the metadata-only carrier scan other packages consume:
// rotation lineage plus the in-place summary-row count for one session.
func ScanCompactions(ctx context.Context, sessionID string) (*ScanResult, error) {
	scan, err := sidecarScanCompactions(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &ScanResult{
		Lineage: scan.Lineage, Tip: scan.Tip, Rotations: scan.Rotations,
		InPlace: scan.InPlace, LastSummaryAt: scan.LastSummaryAt,
	}, nil
}

// sidecarCall marshals one op and maps wire error classes to sentinels.
// Unknown classes collapse to internal — the vocabulary never forwards
// upstream detail strings into Go errors.
func sidecarCall(ctx context.Context, op string, args map[string]any, out any) error {
	req := map[string]any{"op": op}
	for key, value := range args {
		req[key] = value
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return errSidecarInvalidArgs
	}
	body, err := Runner(ctx, payload)
	if err != nil {
		return errSidecarUnavailable
	}
	if len(body) > sidecarMaxOutput {
		return errSidecarOversized
	}
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return errSidecarInternal
	}
	if !envelope.OK {
		switch envelope.Error {
		case "unsupported":
			return errSidecarUnsupported
		case "invalid_args":
			return errSidecarInvalidArgs
		case "session_missing":
			return errSessionMissing
		case "locked":
			return errCompactionLocked
		default:
			return errSidecarInternal
		}
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

type sidecarProbeResult struct {
	Supported bool     `json:"supported"`
	Missing   []string `json:"missing"`
	Symbols   []string `json:"symbols"`
}

func sidecarProbe(ctx context.Context) (*sidecarProbeResult, error) {
	payload, err := json.Marshal(map[string]any{"op": "probe"})
	if err != nil {
		return nil, errSidecarInvalidArgs
	}
	body, err := Runner(ctx, payload)
	if err != nil {
		return nil, err
	}
	var out sidecarProbeResult
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, errSidecarInternal
	}
	return &out, nil
}
