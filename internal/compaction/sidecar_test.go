package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// stubSidecar swaps the process runner for a canned-response function.
func stubSidecar(t *testing.T, fn func(ctx context.Context, payload []byte) ([]byte, error)) {
	t.Helper()
	old := Runner
	Runner = fn
	t.Cleanup(func() { Runner = old })
}

func TestSidecarProbeReportsSymbols(t *testing.T) {
	stubSidecar(t, func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"ok":true,"supported":true,"missing":[],"symbols":["SessionDB","archive_and_compact"]}`), nil
	})
	probe, err := sidecarProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Supported || len(probe.Symbols) != 2 {
		t.Fatalf("probe: %+v", probe)
	}
	stubSidecar(t, func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"ok":true,"supported":false,"missing":["hermes_state.SessionDB"],"symbols":[]}`), nil
	})
	probe, err = sidecarProbe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if probe.Supported || len(probe.Missing) != 1 {
		t.Fatalf("missing symbols not reported: %+v", probe)
	}
}

func TestSidecarRefusesOversizedOutput(t *testing.T) {
	stubSidecar(t, func(context.Context, []byte) ([]byte, error) {
		return make([]byte, sidecarMaxOutput+1), nil
	})
	if _, _, err := sidecarEstimateTokens(context.Background(), "s1"); !errors.Is(err, errSidecarOversized) {
		t.Fatalf("oversized output accepted: %v", err)
	}
}

func TestSidecarRealProcessRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("python unavailable")
	}
	oldProg, oldArgs := sidecarProg, sidecarProgArgs
	sidecarProg, sidecarProgArgs = "python", []string{"-c",
		"import sys,json; req=json.loads(sys.stdin.readline()); print(json.dumps({'ok':True,'echo':req.get('op')}))"}
	t.Cleanup(func() { sidecarProg, sidecarProgArgs = oldProg, oldArgs })
	var out struct {
		Echo string `json:"echo"`
	}
	if err := sidecarCall(context.Background(), "probe", nil, &out); err != nil {
		t.Fatalf("real exec failed: %v", err)
	}
	if out.Echo != "probe" {
		t.Fatalf("echo=%q", out.Echo)
	}
}

func TestSidecarDeadlineKillsProcess(t *testing.T) {
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("python unavailable")
	}
	oldProg, oldArgs := sidecarProg, sidecarProgArgs
	sidecarProg, sidecarProgArgs = "python", []string{"-c", "import time; time.sleep(30)"}
	t.Cleanup(func() { sidecarProg, sidecarProgArgs = oldProg, oldArgs })
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, err := sidecarEstimateTokens(ctx, "s1"); err == nil {
		t.Fatal("deadline did not stop the sidecar")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("process was not killed at the deadline")
	}
}

func TestApplyRequiresLock(t *testing.T) {
	stubSidecar(t, func(context.Context, []byte) ([]byte, error) {
		return []byte(`{"ok":false,"error":"locked"}`), nil
	})
	_, err := sidecarApplyTranscript(context.Background(), "s1", "h1", []map[string]any{{"role": "user"}}, nil, 0)
	if !errors.Is(err, errCompactionLocked) {
		t.Fatalf("lock refusal mapped to %v", err)
	}
}

func TestSidecarErrorClassesStayBounded(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want error
	}{
		"unsupported":     {`{"ok":false,"error":"unsupported","missing":["x"]}`, errSidecarUnsupported},
		"session_missing": {`{"ok":false,"error":"session_missing"}`, errSessionMissing},
		"invalid_args":    {`{"ok":false,"error":"invalid_args"}`, errSidecarInvalidArgs},
		"internal":        {`{"ok":false,"error":"internal"}`, errSidecarInternal},
		"unknown-class":   {`{"ok":false,"error":"something_weird"}`, errSidecarInternal},
		"no-content-leak": {`{"ok":false,"error":"internal","detail":"DROP TABLE secrets"}`, errSidecarInternal},
		"transport-error": {"", errSidecarUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			stubSidecar(t, func(context.Context, []byte) ([]byte, error) {
				if tc.body == "" {
					return nil, errors.New("exec failed")
				}
				return []byte(tc.body), nil
			})
			_, err := sidecarScanCompactions(context.Background(), "s1")
			if !errors.Is(err, tc.want) {
				t.Fatalf("%q mapped to %v, want %v", tc.body, err, tc.want)
			}
		})
	}
}

func TestSidecarTranscriptOpsRoundTrip(t *testing.T) {
	var payloads []map[string]any
	stubSidecar(t, func(_ context.Context, payload []byte) ([]byte, error) {
		var req map[string]any
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		payloads = append(payloads, req)
		switch req["op"] {
		case "lease", "lock":
			return []byte(`{"ok":true,"acquired":true,"locked":true}`), nil
		case "unlease", "unlock":
			return []byte(`{"ok":true,"released":true}`), nil
		case "read_transcript":
			return []byte(`{"ok":true,"session_id":"s1","watermark":42,"messages":[{"role":"user","content":"hi"}]}`), nil
		case "estimate_tokens":
			return []byte(`{"ok":true,"tokens":1234,"message_count":9}`), nil
		default:
			return []byte(`{"ok":true,"applied":true,"active_count":7}`), nil
		}
	})
	locked, err := sidecarLock(context.Background(), "s1", "h1", 300)
	if err != nil || !locked {
		t.Fatalf("lock: %v %v", locked, err)
	}
	acquired, err := sidecarAcquireLease(context.Background(), "s1", "h1", 300)
	if err != nil || !acquired {
		t.Fatalf("lease: %v %v", acquired, err)
	}
	estTokens, estCount, err := sidecarEstimateTokens(context.Background(), "s1")
	if err != nil || estTokens != 1234 || estCount != 9 {
		t.Fatalf("estimate: %v %v %v", estTokens, estCount, err)
	}
	tx, err := sidecarReadTranscript(context.Background(), "s1")
	if err != nil || tx.Watermark != 42 || len(tx.Messages) != 1 {
		t.Fatalf("transcript: %+v %v", tx, err)
	}
	applied, err := sidecarApplyTranscript(context.Background(), "s1", "h1", []map[string]any{{"role": "user"}}, &tx.Watermark, 2)
	if err != nil || applied != 7 {
		t.Fatalf("apply: %d %v", applied, err)
	}
	if err := sidecarReleaseLease(context.Background(), "s1", "h1"); err != nil {
		t.Fatalf("unlease: %v", err)
	}
	// Every op must carry its session id — a request that drops it silently
	// acts on an empty key. Mutating ops additionally carry the lock/lease
	// holder; observational ops (read/estimate/scan) do not.
	mutating := map[string]bool{
		"lease": true, "unlease": true, "lock": true,
		"unlock": true, "apply_transcript": true,
	}
	for _, req := range payloads {
		if req["session_id"] != "s1" {
			t.Fatalf("request dropped session_id: %v", req)
		}
		if mutating[req["op"].(string)] && req["holder"] != "h1" {
			t.Fatalf("mutating request dropped holder: %v", req)
		}
	}
}
