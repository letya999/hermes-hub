package runtime

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxPromptBytes = 2 * 1024 * 1024

// Connector installation is an isolated build, not an ordinary chat turn.
// Keep the supervisor observing long enough for the ToolHub build ceiling;
// Hermes itself sends the user periodic heartbeats while it waits.
// The cap is the single run budget every hop shares (RunBudget).

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
	InboundID      string `json:"inbound_id,omitempty"`
	// Instructions is the hub-pinned presentation guidance for this admitted
	// run (ephemeral system prompt upstream). It is never a synthetic user
	// message and never carries authorization.
	Instructions string `json:"instructions,omitempty"`
}

// SelfEnvRequest is the private protected-form-to-runtime contract. It is
// never sent through Hermes jobs or exposed as a model tool.
type SelfEnvRequest struct {
	identity.Envelope
	OrganizationID string            `json:"organization_id"`
	UserID         string            `json:"user_id"`
	ActorID        string            `json:"actor_id"`
	ScopeID        string            `json:"scope_id"`
	Values         map[string]string `json:"values"`
}

type ExecuteResponse struct {
	Text              string   `json:"text"`
	JobID             string   `json:"job_id,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	RunID             string   `json:"run_id,omitempty"`
	RuntimeGeneration string   `json:"runtime_generation,omitempty"`
	Status            string   `json:"status,omitempty"`
	LastEvent         string   `json:"last_event,omitempty"`
	EventID           string   `json:"event_id,omitempty"`
	ToolName          string   `json:"tool_name,omitempty"`
	ApprovalID        string   `json:"approval_id,omitempty"`
	ApprovalChoices   []string `json:"approval_choices,omitempty"`
	// ApprovalSummary is the redacted human-readable context of the flagged
	// action (what is being approved), bounded at the runtime boundary.
	ApprovalSummary string        `json:"approval_summary,omitempty"`
	Artifacts       []ArtifactRef `json:"artifacts,omitempty"`
	// Voice carries the text the model asked to deliver as a voice message
	// (upstream VOICE: marker). The gateway synthesizes and sends it; the
	// audio itself is never persisted — the text transcript stays canonical.
	Voice string `json:"voice,omitempty"`
}

type runtimeHTTP struct{}

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
	case "/v1/health":
		s.health(w, r)
	case "/readyz":
		s.ready(w, r)
	case "/v1/execute":
		s.execute(w, r)
	case "/v1/readmit":
		s.readmit(w, r)
	case "/v1/observe":
		s.observe(w, r)
	case "/v1/control":
		s.control(w, r)
	case "/v1/compact":
		s.compact(w, r)
	case "/v1/goal":
		s.goal(w, r)
	case "/v1/restart":
		s.restart(w, r)
	case "/v1/restart-request":
		s.restartRequest(w, r)
	case "/v1/self-env":
		s.selfEnv(w, r)
	case "/v1/artifact":
		s.artifact(w, r)
	case "/v1/usage":
		s.usage(w, r)
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
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://"+env("HUB_HERMES_API_HOST", "127.0.0.1")+":"+env("HUB_HERMES_API_PORT", "8642")+"/health/detailed", nil)
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
	var health struct {
		Readiness struct {
			Status string `json:"status"`
			Checks struct {
				Gateway struct {
					Status string `json:"status"`
					State  string `json:"state"`
				} `json:"gateway"`
			} `json:"checks"`
		} `json:"readiness"`
		Platforms struct {
			API struct {
				State string `json:"state"`
			} `json:"api_server"`
		} `json:"platforms"`
	}
	// Discovery responds during startup; admission must wait for the gateway
	// and API platform to finish initializing. Non-critical degradation serves.
	if json.NewDecoder(http.MaxBytesReader(w, response.Body, 128*1024)).Decode(&health) != nil ||
		(health.Readiness.Status != "ok" && health.Readiness.Status != "degraded") ||
		health.Readiness.Checks.Gateway.Status != "ok" || health.Readiness.Checks.Gateway.State != "running" || health.Platforms.API.State != "connected" {
		writeRuntimeError(w, http.StatusServiceUnavailable, "Hermes task admission is not ready")
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

	if r.Header.Get("Accept") == RunStreamContentType {
		s.executeStream(w, r, request)
		return
	}
	response, err := s.executePersistent(r.Context(), request)
	response.RuntimeGeneration = os.Getenv("HUB_RUNTIME_GENERATION")
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

// readmit restores the original payload from the owning runtime's journal.
// Missing legacy receipts cannot authorize execution with an invented input.
func (s *runtimeHTTP) readmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	request, err := decodeExecuteRequest(w, r)
	if err != nil {
		writeRuntimeError(w, http.StatusBadRequest, err.Error())
		return
	}
	check := request
	check.Text = "readmit"
	if err := validateExecuteRequest(check); err != nil {
		writeRuntimeError(w, http.StatusConflict, err.Error())
		return
	}
	payload, err := admissionPayload(request, nil)
	if err != nil {
		writeRuntimeError(w, http.StatusConflict, "original admission unavailable")
		return
	}
	if r.Header.Get("Accept") == RunStreamContentType {
		s.writeStream(w, request, func(emit func(ExecuteResponse) error) (ExecuteResponse, error) {
			return s.postHermesRun(r.Context(), request, payload, emit)
		})
		return
	}
	response, err := s.postHermesRun(r.Context(), request, payload, nil)
	response.JobID = request.JobID
	response.RuntimeGeneration = os.Getenv("HUB_RUNTIME_GENERATION")
	if err != nil {
		writeRuntimeError(w, http.StatusInternalServerError, "runtime execution failed")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, response)
}

// executePersistent uses the pinned Hermes Gateway API inside this runtime.
// No provider or session state crosses the owning container.
func (s *runtimeHTTP) executePersistent(ctx context.Context, request ExecuteRequest) (ExecuteResponse, error) {
	return s.executePersistentEvents(ctx, request, nil)
}

func (s *runtimeHTTP) executePersistentEvents(ctx context.Context, request ExecuteRequest, emit func(ExecuteResponse) error) (ExecuteResponse, error) {
	if err := s.ensureBootstrap(ctx, request); err != nil {
		return ExecuteResponse{Status: "failed", LastEvent: "bootstrap.not_ready"}, err
	}
	first, sawTool := s.admitAndObserve(ctx, request, emit)
	if errors.Is(first.err, errAdmissionReceipt) || !retryColdWake(first.response, first.err, first.elapsed, sawTool) {
		return first.response, first.err
	}
	log.Printf("runtime retrying cold-start job=%s status=%s", request.JobID, first.response.Status)
	retry := request
	if retry.IdempotencyKey != "" {
		retry.IdempotencyKey += ":wake"
	}
	second, _ := s.admitAndObserve(ctx, retry, emit)
	return second.response, second.err
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
	allowedScope := request.ScopeID == "user:"+user || request.ScopeID == "organization:"+organization
	if os.Getenv("HUB_SCOPE_KIND") == "group" {
		allowedScope = request.ScopeID == "group:"+user
	}
	if !allowedScope {
		return errors.New("runtime scope mismatch")
	}
	if request.Channel == "" || request.Trigger == "" || request.IdempotencyKey == "" {
		return errors.New("job identity is incomplete")
	}
	if len(request.IdempotencyKey) > 256 || len(request.Text) == 0 || len(request.Text) > maxPromptBytes {
		return errors.New("invalid prompt")
	}
	if len(request.Instructions) > 4096 || strings.ContainsRune(request.Instructions, '\x00') {
		return errors.New("invalid instructions")
	}
	if request.InboundID != "" && request.Channel != "telegram_bot" {
		return errors.New("inbound file requires Telegram channel")
	}
	return nil
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

// restartRequest writes the deferred restart marker without signaling. The
// managed ToolHub cannot reach this state dir, so the supervisor relays
// projection changes here; the restart itself is applied by /v1/restart when
// the runtime is between runs.
func (s *runtimeHTTP) restartRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := os.WriteFile(filepath.Join(state, "restart.request"), nil, 0600); err != nil {
		writeRuntimeError(w, http.StatusInternalServerError, "restart marker unavailable")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]bool{"scheduled": true})
}

func (s *runtimeHTTP) selfEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if os.Getenv("HUB_CAPABILITY_MODE") == "managed" {
		writeRuntimeError(w, http.StatusForbidden, "self-managed environment disabled")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	defer clear(body)
	var request SelfEnvRequest
	if json.Unmarshal(body, &request) != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if err := validateSelfEnvRequest(request); err != nil {
		writeRuntimeError(w, http.StatusConflict, "invalid runtime scope")
		return
	}
	keys, err := envstore.UpdateValues(filepath.Join(state, envstore.FileName), request.Values, os.Getenv("HUB_SELF_ENV_KEYS"), os.Getenv("HUB_PROTECTED_ENV_KEYS"))
	if err != nil {
		writeRuntimeError(w, http.StatusBadRequest, "credential update rejected")
		return
	}
	if err := os.WriteFile(filepath.Join(state, "restart.request"), nil, 0600); err != nil {
		writeRuntimeError(w, http.StatusInternalServerError, "restart unavailable")
		return
	}
	writeRuntimeJSON(w, http.StatusOK, map[string]any{"updated": keys, "restart_scheduled": true})
	go func() {
		time.Sleep(250 * time.Millisecond)
		signalRuntimeProcess()
	}()
}

func validateSelfEnvRequest(request SelfEnvRequest) error {
	user := env("HUB_USER_ID", "me")
	organization := env("HUB_ORGANIZATION_ID", "personal")
	if request.OrganizationID != organization || request.UserID != user || request.ActorID != user || request.ScopeID != "user:"+user {
		return errors.New("runtime scope mismatch")
	}
	return request.Envelope.Validate(user, user, env("HUB_RUNTIME_ID", user), env("HUB_POLICY_VERSION", "policy-1"))
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
