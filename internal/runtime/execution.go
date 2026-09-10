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
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/jobcontract"
)

type Job = jobcontract.Job

var ErrUncertain = errors.New("runtime execution outcome is uncertain")

type Binding struct {
	UserID           string
	OrganizationID   string
	UserHome         string
	OrganizationHome string
	Features         []string
	Env              map[string]string
	OrgActions       map[string]bool
}

func (b Binding) scopeID() string {
	return "user:" + b.UserID
}

type Executor interface {
	Run(context.Context, Job) (string, error)
}

type HermesRunner struct {
	Command string
	Timeout time.Duration
	Binding Binding
}

func (r HermesRunner) Run(ctx context.Context, job Job) (string, error) {
	if job.Text == "" {
		return "", errors.New("empty Hermes prompt")
	}
	if r.Command == "" {
		r.Command = "hermes"
	}
	if r.Timeout <= 0 {
		r.Timeout = 120 * time.Second
	}
	if r.Binding.UserHome == "" || r.Binding.UserID == "" {
		return "", errors.New("runtime user binding is required")
	}
	home := r.Binding.UserHome
	if strings.HasPrefix(job.ScopeID, "organization:") {
		home = r.Binding.OrganizationHome
	}
	if home == "" {
		return "", errors.New("runtime scope home is not configured")
	}
	workspace := filepath.Join(home, "workspace")
	stateDir := filepath.Join(home, "connections")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return "", err
	}
	hermesHome := filepath.Join(home, "hermes")
	if config := os.Getenv("HUB_RUNTIME_CONFIG"); config != "" {
		if err := copyFile(config, filepath.Join(hermesHome, "config.yaml")); err != nil {
			return "", err
		}
	}
	jobCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(jobCtx, r.Command, "-z", job.Text)
	cmd.Dir = workspace
	cmd.Env = processEnv(r.Binding.Env)
	cmd.Env = append(cmd.Env,
		"HERMES_HOME="+hermesHome,
		"HOME="+filepath.Join(home, "connections", "home"),
		"HUB_STATE="+stateDir,
		"HUB_WORKSPACE="+workspace,
		"HUB_USER_ID="+r.Binding.UserID,
		"HUB_ORGANIZATION_ID="+job.OrganizationID,
		"HUB_SCOPE_ID="+job.ScopeID,
		"HUB_FEATURES="+strings.Join(r.Binding.Features, ","),
		"HUB_DEFER_RUNTIME_RESTART=true",
	)
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, io.Discard
	if err := cmd.Run(); err != nil {
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			return "", errors.New("hermes timed out")
		}
		if errors.Is(jobCtx.Err(), context.Canceled) {
			return "", ErrUncertain
		}
		return "", errors.New("hermes invocation failed")
	}
	response := strings.TrimSpace(stdout.String())
	if response == "" {
		return "", errors.New("hermes returned an empty response")
	}
	return response, nil
}

func copyFile(source, destination string) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	return os.WriteFile(destination, body, 0600)
}

var envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func processEnv(userEnv map[string]string) []string {
	keys := []string{"PATH", "Path", "SystemRoot", "TEMP", "TMP", "LANG", "LC_ALL", "TZ", "HUB_ORG_ACTIONS", "HUB_SELF_ENV_KEYS", "HUB_PROTECTED_ENV_KEYS"}
	env := []string{}
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	selected := make([]string, 0, len(userEnv))
	for key := range userEnv {
		selected = append(selected, key)
	}
	slices.Sort(selected)
	for _, key := range selected {
		if envNamePattern.MatchString(key) && key != "TELEGRAM_BOT_TOKEN" && key != "TELEGRAM_ALLOWED_USERS" {
			env = append(env, key+"="+userEnv[key])
		}
	}
	return env
}

func ProcessEnv(userEnv map[string]string) []string { return processEnv(userEnv) }

type runtimeRecord struct {
	hash   string
	status string
	result string
}

type Server struct {
	Binding Binding
	Token   string
	Exec    Executor
	mu      sync.Mutex
	records map[string]runtimeRecord
	running map[string]context.CancelFunc
}

func NewServer(binding Binding, token string, executor Executor) *Server {
	if executor == nil {
		executor = HermesRunner{Binding: binding}
	}
	return &Server{Binding: binding, Token: token, Exec: executor, records: map[string]runtimeRecord{}, running: map[string]context.CancelFunc{}}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token == "" || r.Header.Get("Authorization") != "Bearer "+s.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/jobs/") {
			s.cancel(strings.TrimPrefix(r.URL.Path, "/v1/jobs/"))
			writeJSON(w, http.StatusAccepted, jobcontract.ExecutionResponse{Version: jobcontract.Version, Status: "cancellation_requested"})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/jobs" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 128*1024+1))
		if err != nil || len(body) > 128*1024 {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		var request jobcontract.ExecutionRequest
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Version != jobcontract.Version {
			http.Error(w, "invalid execution request", http.StatusBadRequest)
			return
		}
		if err := s.validate(request.Job); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		response, status := s.execute(r.Context(), request.Job)
		writeJSON(w, status, response)
	})
}

func (s *Server) validate(job Job) error {
	userScope := job.ScopeID == s.Binding.scopeID()
	orgScope := s.Binding.OrganizationID != "" && job.ScopeID == "organization:"+s.Binding.OrganizationID
	if job.ID == "" || job.IdempotencyKey == "" || job.Text == "" || job.UserID != s.Binding.UserID || job.OrganizationID != s.Binding.OrganizationID || job.ActorID != job.UserID || (!userScope && !orgScope) {
		return errors.New("job identity does not match runtime binding")
	}
	if orgScope && job.OrganizationAction != "" && !s.Binding.OrgActions[job.OrganizationAction] {
		return errors.New("organization action is not allowed")
	}
	return nil
}

func (s *Server) execute(ctx context.Context, job Job) (jobcontract.ExecutionResponse, int) {
	hashBytes := sha256.Sum256([]byte(job.IdempotencyKey + "\x00" + job.ID + "\x00" + job.ScopeID + "\x00" + job.Text))
	hash := hex.EncodeToString(hashBytes[:])
	s.mu.Lock()
	if previous, ok := s.records[job.IdempotencyKey]; ok {
		if previous.hash != hash {
			s.mu.Unlock()
			return jobcontract.ExecutionResponse{Version: jobcontract.Version, Status: "rejected", Error: "idempotency key reused for another job"}, http.StatusConflict
		}
		if previous.status == "succeeded" {
			s.mu.Unlock()
			return jobcontract.ExecutionResponse{Version: jobcontract.Version, Status: previous.status, Result: previous.result}, http.StatusOK
		}
		if previous.status == "running" {
			s.mu.Unlock()
			return jobcontract.ExecutionResponse{Version: jobcontract.Version, Status: "running"}, http.StatusConflict
		}
		if previous.status == "uncertain" {
			s.mu.Unlock()
			return jobcontract.ExecutionResponse{Version: jobcontract.Version, Status: "uncertain", Error: ErrUncertain.Error()}, http.StatusAccepted
		}
		if previous.status == "failed" {
			s.mu.Unlock()
			return jobcontract.ExecutionResponse{Version: jobcontract.Version, Status: "failed", Error: "previous execution failed"}, http.StatusBadGateway
		}
	}
	jobCtx, cancel := context.WithCancel(ctx)
	s.running[job.ID] = cancel
	s.records[job.IdempotencyKey] = runtimeRecord{hash: hash, status: "running"}
	s.mu.Unlock()
	result, err := s.Exec.Run(jobCtx, job)
	s.mu.Lock()
	delete(s.running, job.ID)
	if err != nil {
		status := "failed"
		code := http.StatusBadGateway
		if errors.Is(err, ErrUncertain) || errors.Is(jobCtx.Err(), context.Canceled) {
			status = "uncertain"
			code = http.StatusAccepted
		}
		s.records[job.IdempotencyKey] = runtimeRecord{hash: hash, status: status}
		s.mu.Unlock()
		return jobcontract.ExecutionResponse{Version: jobcontract.Version, Status: status, Error: err.Error()}, code
	}
	s.records[job.IdempotencyKey] = runtimeRecord{hash: hash, status: "succeeded", result: result}
	s.mu.Unlock()
	return jobcontract.ExecutionResponse{Version: jobcontract.Version, Status: "succeeded", Result: result}, http.StatusOK
}

func (s *Server) cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel := s.running[id]; cancel != nil {
		cancel()
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type HTTPClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func NewHTTPClient(baseURL, token string) *HTTPClient {
	return &HTTPClient{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, Client: &http.Client{Timeout: 130 * time.Second}}
}

func (c *HTTPClient) Run(ctx context.Context, job Job) (string, error) {
	if c == nil || c.BaseURL == "" || c.Token == "" {
		return "", errors.New("runtime HTTP client is not configured")
	}
	body, err := json.Marshal(jobcontract.ExecutionRequest{Version: jobcontract.Version, Job: job})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/jobs", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ErrUncertain
		}
		return "", err
	}
	defer response.Body.Close()
	var result jobcontract.ExecutionResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 128*1024)).Decode(&result); err != nil {
		return "", err
	}
	if result.Status == "succeeded" && response.StatusCode == http.StatusOK {
		return result.Result, nil
	}
	if result.Status == "uncertain" || response.StatusCode == http.StatusAccepted {
		return "", ErrUncertain
	}
	if result.Error == "" {
		result.Error = fmt.Sprintf("runtime HTTP %d", response.StatusCode)
	}
	return "", errors.New(result.Error)
}
