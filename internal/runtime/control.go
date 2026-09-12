package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"
)

// RunControl addresses an existing run; it never admits new work.
type RunControl struct {
	Reconcile bool `json:"reconcile,omitempty"`
	RunReference
	Action    string    `json:"action"`
	RequestID string    `json:"request_id,omitempty"`
	Choice    string    `json:"choice,omitempty"`
	Deadline  time.Time `json:"deadline,omitempty"`
}

func (s *runtimeHTTP) control(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	defer r.Body.Close()
	var request RunControl
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid control request")
		return
	}
	check := request.ExecuteRequest
	check.Text = "control"
	if validateExecuteRequest(check) != nil || request.SessionID != sessionIDFor(check) || request.RuntimeGeneration != os.Getenv("HUB_RUNTIME_GENERATION") || !validHermesRunID(request.RunID) || (request.Action != "cancel" && request.Action != "approve") {
		writeRuntimeError(w, http.StatusConflict, "run binding mismatch")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + env("HUB_HERMES_API_HOST", "127.0.0.1") + ":" + env("HUB_HERMES_API_PORT", "8642")
	auth := env("API_SERVER_KEY", os.Getenv("HUB_RUNTIME_AUTH"))
	var status struct {
		Status    string         `json:"status"`
		SessionID string         `json:"session_id"`
		Output    string         `json:"output"`
		Approval  nativeRunEvent `json:"approval"`
	}
	if hermesRequest(ctx, client, http.MethodGet, base+"/v1/runs/"+request.RunID, auth, nil, &status) != nil {
		writeRuntimeError(w, http.StatusServiceUnavailable, "run state unavailable")
		return
	}
	if status.SessionID != request.SessionID {
		writeRuntimeError(w, http.StatusConflict, "run session mismatch")
		return
	}
	known := ExecuteResponse{JobID: request.JobID, RunID: request.RunID, SessionID: request.SessionID, RuntimeGeneration: request.RuntimeGeneration, Status: status.Status, Text: status.Output}
	if request.Reconcile {
		known.Text = ""
		switch status.Status {
		case "completed", "failed", "cancelled", "interrupted":
			known.Text = status.Output
		}
		if status.Status == "waiting_for_approval" {
			known.ApprovalID = status.Approval.RequestID
			known.ApprovalChoices = append([]string(nil), status.Approval.Choices...)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(known)
		return
	}
	if request.Action == "cancel" {
		switch status.Status {
		case "completed", "failed", "cancelled", "interrupted":
			// The authoritative terminal state already satisfies cancellation.
		default:
			var err error
			known, err = stopAndConfirmHermesRun(ctx, client, base, auth, known)
			if err != nil {
				known.Status = "uncertain"
			}
		}
	} else {
		allowed := false
		for _, choice := range status.Approval.Choices {
			if choice == request.Choice {
				allowed = true
			}
		}
		if status.Status != "waiting_for_approval" || request.RequestID == "" || request.RequestID != status.Approval.RequestID || !allowed || request.Deadline.IsZero() || !time.Now().Before(request.Deadline) {
			writeRuntimeError(w, http.StatusConflict, "approval is stale or invalid")
			return
		}
		if hermesRequest(ctx, client, http.MethodPost, base+"/v1/runs/"+request.RunID+"/approval", auth, map[string]string{"request_id": request.RequestID, "choice": request.Choice}, nil) != nil {
			known.Status = "uncertain"
		} else {
			known.Status = "running"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(known)
}
