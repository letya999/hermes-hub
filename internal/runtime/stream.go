package runtime

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

const RunStreamContentType = "application/x-ndjson"

type nativeRunEvent struct {
	Event     string   `json:"event"`
	RunID     string   `json:"run_id"`
	Timestamp float64  `json:"timestamp"`
	Tool      string   `json:"tool"`
	Error     bool     `json:"error"`
	RequestID string   `json:"request_id"`
	Choices   []string `json:"choices"`
}

func eventID(event nativeRunEvent) string {
	b, _ := json.Marshal(event)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ReadRunStream bounds each normalized frame and the total connection output.
// Synchronous consumption propagates backpressure rather than growing a queue.
func ReadRunStream(reader io.Reader, consume func(ExecuteResponse) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxPromptBytes+64*1024)
	total := 0
	for scanner.Scan() {
		total += len(scanner.Bytes())
		if total > 4*maxPromptBytes {
			return errors.New("runtime stream output limit exceeded")
		}
		var event ExecuteResponse
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return errors.New("invalid runtime stream frame")
		}
		if err := consume(event); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func nativeRunEvents(ctx context.Context, base, auth, runID string) <-chan nativeRunEvent {
	events := make(chan nativeRunEvent, 8)
	go func() {
		defer close(events)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/runs/"+runID+"/events", nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+auth)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			return
		}
		_ = readNativeEvents(response.Body, func(event nativeRunEvent) error {
			select {
			case events <- event:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return events
}

func readNativeEvents(reader io.Reader, consume func(nativeRunEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	var data strings.Builder
	total := 0
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line)
		if total > 8*maxPromptBytes {
			return errors.New("native stream output limit exceeded")
		}
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(line, "data:"))
			data.WriteByte('\n')
			if data.Len() > 64*1024 {
				return errors.New("native event limit exceeded")
			}
		}
		if line == "" && data.Len() > 0 {
			var event nativeRunEvent
			if err := json.Unmarshal([]byte(data.String()), &event); err != nil {
				return errors.New("invalid native event")
			}
			if err := consume(event); err != nil {
				return err
			}
			data.Reset()
		}
	}
	return scanner.Err()
}

func (s *runtimeHTTP) executeStream(w http.ResponseWriter, r *http.Request, request ExecuteRequest) {
	s.writeStream(w, request, func(emit func(ExecuteResponse) error) (ExecuteResponse, error) {
		return s.executePersistentEvents(r.Context(), request, emit)
	})
}

func (s *runtimeHTTP) writeStream(w http.ResponseWriter, request ExecuteRequest, run func(func(ExecuteResponse) error) (ExecuteResponse, error)) {
	w.Header().Set("Content-Type", RunStreamContentType)
	emit := func(event ExecuteResponse) error {
		event.JobID, event.RuntimeGeneration = request.JobID, os.Getenv("HUB_RUNTIME_GENERATION")
		if err := json.NewEncoder(w).Encode(event); err != nil {
			return err
		}
		return http.NewResponseController(w).Flush()
	}
	response, err := run(emit)
	if err != nil && response.Status == "" {
		response.Status = "uncertain"
	}
	if err != nil && response.Status == "completed" && strings.TrimSpace(response.Text) == "" {
		response.Status, response.LastEvent = "uncertain", "run.unknown"
	}
	response.EventID = "terminal:" + response.Status
	response.Text = strings.TrimSpace(response.Text)
	_ = emit(response)
}

type RunReference struct {
	ExecuteRequest
	RunID             string `json:"run_id"`
	SessionID         string `json:"session_id"`
	RuntimeGeneration string `json:"runtime_generation"`
}

func (s *runtimeHTTP) observe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	defer r.Body.Close()
	var ref RunReference
	if json.NewDecoder(r.Body).Decode(&ref) != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid run reference")
		return
	}
	check := ref.ExecuteRequest
	check.Text = "observe"
	if validateExecuteRequest(check) != nil || ref.SessionID != sessionIDFor(check) || ref.RuntimeGeneration != os.Getenv("HUB_RUNTIME_GENERATION") || !validHermesRunID(ref.RunID) {
		writeRuntimeError(w, http.StatusConflict, "run binding mismatch")
		return
	}
	known := ExecuteResponse{JobID: ref.JobID, SessionID: ref.SessionID, RunID: ref.RunID, RuntimeGeneration: ref.RuntimeGeneration, Status: "running", LastEvent: "run.admitted", EventID: "admitted"}
	s.writeStream(w, ref.ExecuteRequest, func(emit func(ExecuteResponse) error) (ExecuteResponse, error) {
		return s.observeHermesRun(r.Context(), known, emit)
	})
}

func validHermesRunID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}
