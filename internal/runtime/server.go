package runtime

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

const maxPromptBytes = 2 * 1024 * 1024

// ExecuteRequest is the private communication-hub to runtime contract.
type ExecuteRequest struct {
	identity.Envelope
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
	Text string `json:"text"`
}

type runtimeHTTP struct {
	mu      sync.Mutex
	results map[string]cachedResult
}

var executeHermes = runHermes
var hermesExecutable = "hermes"
var signalRuntimeProcess = func() {
	if process, err := os.FindProcess(os.Getpid()); err == nil {
		_ = process.Signal(os.Interrupt)
	}
}

type cachedResult struct {
	fingerprint string
	response    ExecuteResponse
}

func runtimeHandler() http.Handler {
	return &runtimeHTTP{results: map[string]cachedResult{}}
}

func (s *runtimeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet {
			writeRuntimeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.WriteHeader(http.StatusOK)
	case "/v1/execute":
		s.execute(w, r)
	case "/v1/restart":
		s.restart(w, r)
	default:
		writeRuntimeError(w, http.StatusNotFound, "not found")
	}
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

	// ponytail: one runtime owns one Telegram worker; add per-scope workers only if throughput matters.
	s.mu.Lock()
	defer s.mu.Unlock()
	fingerprint := requestFingerprint(request)
	if previous, ok := s.results[request.IdempotencyKey]; ok {
		if previous.fingerprint != fingerprint {
			writeRuntimeError(w, http.StatusConflict, "idempotency key already used")
			return
		}
		writeRuntimeJSON(w, http.StatusOK, previous.response)
		return
	}

	text, err := executeHermes(r.Context(), request.Text)
	if err != nil {
		writeRuntimeError(w, http.StatusInternalServerError, "runtime execution failed")
		return
	}
	response := ExecuteResponse{Text: text}
	s.results[request.IdempotencyKey] = cachedResult{fingerprint: fingerprint, response: response}
	if len(s.results) > 128 {
		for key := range s.results {
			delete(s.results, key)
			break
		}
	}
	writeRuntimeJSON(w, http.StatusOK, response)
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
	if err := request.Envelope.Validate(user, user, env("HUB_RUNTIME_ID", user), env("HUB_POLICY_VERSION", "policy-1")); err != nil {
		return err
	}
	if request.UserID != user || request.ActorID != user {
		return errors.New("runtime user mismatch")
	}
	if request.OrganizationID != organization || request.ScopeID != "user:"+user {
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

func requestFingerprint(request ExecuteRequest) string {
	body, _ := json.Marshal(request)
	hash := sha256.Sum256(body)
	return fmt.Sprintf("%x", hash[:])
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
