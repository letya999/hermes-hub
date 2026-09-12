package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

const maxPromptBytes = 2 * 1024 * 1024

// ExecuteRequest is the private communication-hub to runtime contract.
type ExecuteRequest struct {
	identity.Envelope
	JobID          string `json:"job_id"`
	OrganizationID string `json:"organization_id"`
	UserID         string `json:"user_id"`
	ActorID        string `json:"actor_id"`
	ScopeID        string `json:"scope_id"`
	Channel        string `json:"channel"`
	Trigger        string `json:"trigger"`
	IdempotencyKey string `json:"idempotency_key"`
	Text           string `json:"text"`
}

type ExecuteResponse struct {
	Text              string `json:"text"`
	JobID             string `json:"job_id,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	RunID             string `json:"run_id,omitempty"`
	RuntimeGeneration string `json:"runtime_generation,omitempty"`
	Status            string `json:"status,omitempty"`
	LastEvent         string `json:"last_event,omitempty"`
}

type runtimeHTTP struct{}

var executeHermes = runHermes
var hermesExecutable = "hermes"
var signalRuntimeProcess = func() {
	if process, err := os.FindProcess(os.Getpid()); err == nil {
		_ = process.Signal(os.Interrupt)
	}
}

func runtimeHandler() http.Handler {
	return &runtimeHTTP{}
}

func (s *runtimeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet {
			writeRuntimeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.WriteHeader(http.StatusOK)
	case "/readyz":
		s.ready(w, r)
	case "/v1/execute":
		s.execute(w, r)
	case "/v1/restart":
		s.restart(w, r)
	default:
		writeRuntimeError(w, http.StatusNotFound, "not found")
	}
}

func (s *runtimeHTTP) ready(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	client := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://"+env("HUB_HERMES_API_HOST", "127.0.0.1")+":"+env("HUB_HERMES_API_PORT", "8642")+"/v1/capabilities", nil)
	if err != nil {
		writeRuntimeError(w, http.StatusServiceUnavailable, "Hermes API unavailable")
		return
	}
	request.Header.Set("Authorization", "Bearer "+env("API_SERVER_KEY", os.Getenv("HUB_RUNTIME_AUTH")))
	response, err := client.Do(request)
	if err != nil {
		writeRuntimeError(w, http.StatusServiceUnavailable, "Hermes API unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeRuntimeError(w, http.StatusServiceUnavailable, "Hermes API is not ready")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *runtimeHTTP) authorized(r *http.Request) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	expected := os.Getenv("HUB_RUNTIME_AUTH")
	return expected != "" && len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func (s *runtimeHTTP) execute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	request, err := decodeExecuteRequest(w, r)
	if err != nil {
		writeRuntimeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateExecuteRequest(request); err != nil {
		writeRuntimeError(w, http.StatusConflict, err.Error())
		return
	}

	var response ExecuteResponse
	response.RuntimeGeneration = os.Getenv("HUB_RUNTIME_GENERATION")
	if os.Getenv("HUB_PERSISTENT_HERMES") == "true" {
		response, err = s.executePersistent(r.Context(), request)
	} else {
		response.Text, err = executeHermes(r.Context(), request.Text)
		response.Status = "completed"
		response.LastEvent = "run.completed"
	}
	if err != nil {
		if response.Status != "" {
			response.JobID = request.JobID
			writeRuntimeJSON(w, http.StatusInternalServerError, map[string]any{"error": "runtime execution failed", "job_id": response.JobID, "session_id": response.SessionID, "run_id": response.RunID, "runtime_generation": response.RuntimeGeneration, "status": response.Status, "last_event": response.LastEvent})
		} else {
			writeRuntimeError(w, http.StatusInternalServerError, "runtime execution failed")
		}
		return
	}
	response.JobID = request.JobID
	writeRuntimeJSON(w, http.StatusOK, response)
}

// executePersistent uses the pinned Hermes Gateway API inside this runtime.
// The one-shot hermes -z path remains available when the flag is unset for
// rollback deployments; no provider or session state crosses the container.
func (s *runtimeHTTP) executePersistent(ctx context.Context, request ExecuteRequest) (ExecuteResponse, error) {
	base := "http://" + env("HUB_HERMES_API_HOST", "127.0.0.1") + ":" + env("HUB_HERMES_API_PORT", "8642")
	auth := env("API_SERVER_KEY", os.Getenv("HUB_RUNTIME_AUTH"))
	client := &http.Client{Timeout: 10 * time.Second}
	sessionID := sessionIDFor(request)
	if err := hermesRequest(ctx, client, http.MethodPost, base+"/api/sessions", auth, map[string]any{"id": sessionID, "title": "Hermes Hub"}, nil); err != nil && !errors.Is(err, errSessionExists) {
		return ExecuteResponse{}, err
	}
	var admission struct {
		RunID string `json:"run_id"`
	}
	if err := hermesRequest(ctx, client, http.MethodPost, base+"/v1/runs", auth, map[string]any{"input": request.Text, "session_id": sessionID}, &admission, request.IdempotencyKey); err != nil {
		return ExecuteResponse{}, err
	}
	if admission.RunID == "" {
		return ExecuteResponse{}, errors.New("hermes returned no run ID")
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var status struct {
			Status string `json:"status"`
			Output string `json:"output"`
			Error  string `json:"error"`
		}
		if err := hermesRequest(ctx, client, http.MethodGet, base+"/v1/runs/"+admission.RunID, auth, nil, &status); err != nil {
			return ExecuteResponse{}, err
		}
		switch status.Status {
		case "completed":
			if strings.TrimSpace(status.Output) != "" {
				return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: "completed", LastEvent: "run.completed", Text: strings.TrimSpace(status.Output)}, nil
			}
			text, err := s.persistentSessionReply(ctx, client, base, auth, sessionID)
			return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: "completed", LastEvent: "run.completed", Text: text}, err
		case "failed", "cancelled", "interrupted":
			if status.Error != "" {
				return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: status.Status, LastEvent: "run." + status.Status}, errors.New(status.Error)
			}
			return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: status.Status, LastEvent: "run." + status.Status}, fmt.Errorf("hermes run %s", status.Status)
		}
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			stopErr := stopHermesRun(stopCtx, client, base, auth, admission.RunID)
			cancel()
			if stopErr == nil {
				return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: "cancelled", LastEvent: "run.cancelled"}, ctx.Err()
			}
			return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: "uncertain", LastEvent: "run.unknown"}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	stopErr := stopHermesRun(stopCtx, client, base, auth, admission.RunID)
	cancel()
	if stopErr == nil {
		return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: "cancelled", LastEvent: "run.cancelled"}, errors.New("hermes run timed out")
	}
	return ExecuteResponse{SessionID: sessionID, RunID: admission.RunID, Status: "uncertain", LastEvent: "run.unknown"}, errors.New("hermes run timed out")
}

func stopHermesRun(ctx context.Context, client *http.Client, base, auth, runID string) error {
	return hermesRequest(ctx, client, http.MethodPost, base+"/v1/runs/"+runID+"/stop", auth, nil, nil)
}

func sessionIDFor(request ExecuteRequest) string {
	sum := sha256.Sum256([]byte(request.ContextID + "\x00" + request.ConversationID))
	return "hub-" + hex.EncodeToString(sum[:12])
}

var errSessionExists = errors.New("hermes session already exists")

func (s *runtimeHTTP) persistentSessionReply(ctx context.Context, client *http.Client, base, auth, sessionID string) (string, error) {
	var payload struct {
		Data []struct{ Role, Content string } `json:"data"`
	}
	if err := hermesRequest(ctx, client, http.MethodGet, base+"/api/sessions/"+sessionID+"/messages", auth, nil, &payload); err != nil {
		return "", err
	}
	for i := len(payload.Data) - 1; i >= 0; i-- {
		if payload.Data[i].Role == "assistant" && strings.TrimSpace(payload.Data[i].Content) != "" {
			return strings.TrimSpace(payload.Data[i].Content), nil
		}
	}
	return "", errors.New("hermes returned an empty response")
}

func hermesRequest(ctx context.Context, client *http.Client, method, endpoint, auth string, body any, target any, idempotency ...string) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
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
	response, err := client.Do(req)
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

func decodeExecuteRequest(w http.ResponseWriter, r *http.Request) (ExecuteRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPromptBytes+64*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return ExecuteRequest{}, errors.New("request body too large")
	}
	var request ExecuteRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return ExecuteRequest{}, errors.New("invalid request")
	}
	return request, nil
}

func validateExecuteRequest(request ExecuteRequest) error {
	user := env("HUB_USER_ID", "me")
	organization := env("HUB_ORGANIZATION_ID", "personal")
	expectedContext := user
	if strings.HasPrefix(request.ScopeID, "organization:") {
		expectedContext = strings.TrimPrefix(request.ScopeID, "organization:")
	}
	if err := request.Envelope.Validate(user, expectedContext, env("HUB_RUNTIME_ID", user), env("HUB_POLICY_VERSION", "policy-1")); err != nil {
		return err
	}
	if request.UserID != user || request.ActorID != user {
		return errors.New("runtime user mismatch")
	}
	if request.OrganizationID != organization {
		return errors.New("runtime scope mismatch")
	}
	if request.ScopeID != "user:"+user && request.ScopeID != "organization:"+organization {
		return errors.New("runtime scope mismatch")
	}
	if request.Channel == "" || request.Trigger == "" || request.IdempotencyKey == "" {
		return errors.New("job identity is incomplete")
	}
	if len(request.IdempotencyKey) > 256 || len(request.Text) == 0 || len(request.Text) > maxPromptBytes {
		return errors.New("invalid prompt")
	}
	return nil
}

func runHermes(ctx context.Context, prompt string) (string, error) {
	jobCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(jobCtx, hermesExecutable, "-z", prompt)
	cmd.Dir = workspace
	cmd.Env = hermesEnvironment()
	output, err := cmd.Output()
	if err != nil {
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			return "", errors.New("hermes timed out")
		}
		return "", err
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return "", errors.New("hermes returned an empty response")
	}
	return text, nil
}

func hermesEnvironment() []string {
	result := make([]string, 0)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "TELEGRAM_BOT_TOKEN" || key == "TELEGRAM_ALLOWED_USERS" || key == "HUB_RUNTIME_AUTH" || key == "HUB_RUNTIME_LISTEN" {
			continue
		}
		result = append(result, key+"="+value)
	}
	result = append(result, "HUB_DEFER_RUNTIME_RESTART=true")
	return result
}

func (s *runtimeHTTP) restart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if _, err := os.Stat(filepath.Join(state, "restart.request")); errors.Is(err, os.ErrNotExist) {
		writeRuntimeJSON(w, http.StatusOK, map[string]bool{"scheduled": false})
		return
	} else if err != nil {
		writeRuntimeError(w, http.StatusInternalServerError, "restart state unavailable")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]bool{"scheduled": true})
	go func() {
		time.Sleep(250 * time.Millisecond)
		signalRuntimeProcess()
	}()
}

func shutdownRuntimeServer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func writeRuntimeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeRuntimeError(w http.ResponseWriter, status int, message string) {
	writeRuntimeJSON(w, status, map[string]string{"error": fmt.Sprint(message)})
}
