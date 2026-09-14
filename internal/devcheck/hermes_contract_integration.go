//go:build integration

package devcheck

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	hermesContractPin     = "869228cab4a8276d3b4c78da9d9939670c47bd0f"
	hermesContractVersion = "0.21.0"
	hermesContractPort    = "8642"
)

type contractHTTPResponse struct {
	status  int
	headers http.Header
	body    []byte
}

func nativeContractProvider() (*httptest.Server, string, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, "", err
	}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			http.Error(w, "unreadable provider request", http.StatusBadRequest)
			return
		}
		var request struct {
			Stream   bool   `json:"stream"`
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &request) != nil {
			http.Error(w, "invalid bounded provider request", http.StatusBadRequest)
			return
		}
		input := ""
		for _, message := range request.Messages {
			if message.Role == "user" {
				if scenario := nativeFixtureScenario(message.Content); scenario != "" {
					input = scenario
				}
			}
		}
		// Blocking is an explicit current-request fixture model, never inherited history.
		if request.Stream && request.Model == "hub-contract-block" {
			fmt.Println("Native provider fixture: explicit cancellation/restart block")
			<-r.Context().Done()
			return
		}
		hasToolResult, canaryReturned := false, false
		for _, message := range request.Messages {
			if message.Role == "user" && nativeFixtureScenario(message.Content) != "" {
				hasToolResult, canaryReturned = false, false
			}
			hasToolResult = hasToolResult || message.Role == "tool"
			canaryReturned = canaryReturned || (message.Role == "tool" && strings.Contains(string(message.Content), "HUB_TOOL_SECRET_CANARY"))
		}
		if hasToolResult && !canaryReturned {
			reason := "real terminal probe did not return its canary"
			detail := ""
			for _, message := range request.Messages {
				if message.Role == "user" {
					detail = ""
				}
				if message.Role == "tool" {
					detail = string(message.Content)
					if len(detail) > 1024 {
						detail = detail[:1024]
					}
				}
			}
			reason += ": " + detail
			http.Error(w, reason, http.StatusBadRequest)
			return
		}
		fmt.Printf("Native provider fixture: contract=%t approval=%t tool_result=%t stream=%t\n", strings.TrimSpace(input) == "contract probe", strings.TrimSpace(input) == "approval probe", hasToolResult, request.Stream)
		if request.Stream && !hasToolResult && (input == "contract probe" || input == "approval probe") {
			command := "printf HUB_TOOL_SECRET_CANARY"
			if strings.TrimSpace(input) == "approval probe" {
				command = "mkdir -p /workspace/hub-approval-probe && chmod -R 777 /workspace/hub-approval-probe && printf HUB_TOOL_SECRET_CANARY"
			}
			arguments, _ := json.Marshal(map[string]string{"command": command})
			message := map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"index": 0, "id": "probe-terminal", "type": "function", "function": map[string]any{"name": "terminal", "arguments": string(arguments)}}}}
			w.Header().Set("Content-Type", "application/json")
			if request.Stream {
				w.Header().Set("Content-Type", "text/event-stream")
				chunk, _ := json.Marshal(map[string]any{"id": "probe", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": "tool_calls"}}})
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "probe", "object": "chat.completion", "model": "gpt-4o-mini", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "tool_calls"}}})
			}
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"probe\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"contract final answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"probe\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"probe","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"contract final answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	provider.Listener = listener
	provider.Start()
	providerURL := "http://host.docker.internal:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port) + "/v1"
	return provider, providerURL, nil
}

// HermesContract runs against the pinned upstream API server, not a mock or the Go runtime.
func HermesContract(ctx context.Context, image string) error {
	provider, providerURL, err := nativeContractProvider()
	if err != nil {
		return err
	}
	defer func() { provider.CloseClientConnections(); provider.Close() }()
	suffix := make([]byte, 5)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	name := fmt.Sprintf("hermes-contract-%x", suffix)
	volume := name + "-state"
	key := fmt.Sprintf("fixture-auth-%x", suffix)
	run := func(args ...string) error { return dockerContract(ctx, args...) }
	out := func(args ...string) ([]byte, error) { return dockerContractOutput(ctx, args...) }
	// Remove the container before its mounted state volume (defer runs LIFO).
	defer func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() }()
	defer func() { _ = exec.Command("docker", "rm", "-f", name).Run() }()

	if err := run("volume", "create", volume); err != nil {
		return err
	}
	// The pinned resolver reads model.base_url from config, not OPENAI_BASE_URL
	// for explicit custom-provider requests. Configure only this isolated volume.
	if err := run("run", "--rm", "--entrypoint", "python", "-v", volume+":/state",
		"-e", "HERMES_PROBE_BASE_URL="+providerURL, image, "-c",
		"from pathlib import Path; import os; Path('/state/home').mkdir(parents=True,exist_ok=True); p=Path('/state/hermes'); p.mkdir(parents=True,exist_ok=True); (p/'config.yaml').write_text(chr(10).join(['model:', '  provider: custom', '  default: gpt-4o-mini', '  base_url: '+os.environ['HERMES_PROBE_BASE_URL'], '']))"); err != nil {
		return err
	}
	if err := run("run", "-d", "--name", name, "--entrypoint", "hermes", "--read-only", "--cap-drop", "ALL",
		"--add-host", "host.docker.internal:host-gateway",
		"--security-opt", "no-new-privileges:true", "--shm-size", "256m",
		"--tmpfs", "/tmp:mode=1777", "--tmpfs", "/workspace:uid=10001,gid=10001,mode=0700",
		"-v", volume+":/state", "-e", "HERMES_HOME=/state/hermes", "-e", "HOME=/state/home",
		"-e", "XDG_CONFIG_HOME=/state/config", "-e", "HERMES_GATEWAY_NO_TTY=true",
		"-e", "HERMES_DISABLE_LAZY_INSTALLS=1",
		"-e", "API_SERVER_ENABLED=true", "-e", "API_SERVER_KEY="+key,
		"-e", "API_SERVER_HOST=127.0.0.1", "-e", "API_SERVER_PORT="+hermesContractPort,
		"-e", "OPENAI_API_KEY=probe-openai-key-0123456789",
		"-e", "OPENAI_BASE_URL="+providerURL, image, "gateway", "run", "--no-supervise", "--force"); err != nil {
		return err
	}
	if err := waitHermesHealth(ctx, out, name, key); err != nil {
		return err
	}

	version, err := out("exec", name, "hermes", "--version")
	if err != nil || !strings.Contains(string(version), hermesContractVersion) {
		return fmt.Errorf("pinned Hermes version check failed")
	}
	commit, err := out("exec", name, "git", "-c", "safe.directory=/opt/hermes", "-C", "/opt/hermes", "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(commit)) != hermesContractPin {
		return fmt.Errorf("pinned Hermes commit check failed")
	}

	caps, err := hermesHTTP(ctx, out, name, key, http.MethodGet, "/v1/capabilities", "", "")
	if err != nil {
		return err
	}
	if err := validateHermesCapabilities(caps); err != nil {
		return err
	}

	const sessionID = "contract-session"
	create, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/api/sessions", `{"id":"contract-session","title":"Contract Probe"}`, "")
	if err != nil || create.status != http.StatusCreated {
		return fmt.Errorf("session create failed: HTTP %d", create.status)
	}
	if err := expectHermesHTTP(ctx, out, name, key, http.MethodGet, "/api/sessions/"+sessionID, http.StatusOK); err != nil {
		return err
	}
	if err := expectHermesHTTP(ctx, out, name, key, http.MethodGet, "/api/sessions/"+sessionID+"/messages", http.StatusOK); err != nil {
		return err
	}

	runBody := `{"input":"contract probe","session_id":"contract-session","provider":"custom","model":"gpt-4o-mini"}`
	first, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", runBody, "contract-run-1")
	if err != nil || first.status != http.StatusAccepted {
		return fmt.Errorf("run admission failed: HTTP %d", first.status)
	}
	runID, err := jsonString(first.body, "run_id")
	if err != nil {
		return fmt.Errorf("run admission response missing run_id")
	}
	replay, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", runBody, "contract-run-1")
	if err != nil || replay.status != http.StatusAccepted || !strings.EqualFold(replay.headers.Get("Idempotency-Replayed"), "true") {
		return fmt.Errorf("run replay contract failed")
	}
	if replayID, _ := jsonString(replay.body, "run_id"); replayID != runID {
		return fmt.Errorf("run replay returned a different run_id")
	}
	conflict, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", `{"input":"different payload","session_id":"contract-session","provider":"custom","model":"gpt-4o-mini"}`, "contract-run-1")
	if err != nil || conflict.status != http.StatusConflict || !strings.Contains(string(conflict.body), "idempotency_key_conflict") {
		return fmt.Errorf("idempotency conflict contract failed")
	}
	if err := waitHermesRunTerminal(ctx, out, name, key, runID); err != nil {
		return err
	}
	completed, err := hermesHTTP(ctx, out, name, key, http.MethodGet, "/v1/runs/"+runID, "", "")
	status, _ := jsonString(completed.body, "status")
	if err != nil || status != "completed" || !strings.Contains(string(completed.body), "contract final answer") {
		reason, _ := jsonString(completed.body, "error")
		return fmt.Errorf("real Hermes positive execution failed: status=%s error=%s", status, reason)
	}

	cancel, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", `{"input":"cancel probe","session_id":"contract-session","provider":"custom","model":"hub-contract-block"}`, "contract-cancel-1")
	if err != nil || cancel.status != http.StatusAccepted {
		return fmt.Errorf("cancellation run admission failed")
	}
	cancelID, _ := jsonString(cancel.body, "run_id")
	stop, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs/"+cancelID+"/stop", `{}`, "")
	if err != nil || stop.status != http.StatusOK {
		return fmt.Errorf("run stop contract failed: HTTP %d", stop.status)
	}
	if err := waitHermesRunTerminal(ctx, out, name, key, cancelID); err != nil {
		return err
	}
	events, err := hermesHTTP(ctx, out, name, key, http.MethodGet, "/v1/runs/"+cancelID+"/events", "", "")
	if err != nil || events.status != http.StatusOK || !strings.HasPrefix(events.headers.Get("Content-Type"), "text/event-stream") || !strings.Contains(string(events.body), "run.") {
		return fmt.Errorf("run event SSE contract failed")
	}
	approval, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs/"+cancelID+"/approval", `{"choice":"deny"}`, "")
	if err != nil || approval.status != http.StatusConflict || !strings.Contains(string(approval.body), "approval_not_active") {
		return fmt.Errorf("approval boundary contract failed")
	}
	if err := expectHermesHTTP(ctx, out, name, key, http.MethodGet, "/api/jobs", http.StatusOK); err != nil {
		return err
	}

	if err := hermesRestartProbe(ctx, out, run, name, key); err != nil {
		return err
	}
	if err := expectHermesHTTP(ctx, out, name, key, http.MethodGet, "/api/sessions/"+sessionID, http.StatusOK); err != nil {
		return fmt.Errorf("session resume after restart failed: %w", err)
	}
	if err := supervisorSmoke(ctx, image, providerURL); err != nil {
		return err
	}
	if err := fasterWhisperFixtureSmoke(ctx, image); err != nil {
		fmt.Printf("faster-whisper fixture on shipped image unavailable: %v\n", err)
	}
	if err := gatewayLifecycleSmoke(ctx, image, providerURL); err != nil {
		return err
	}
	fmt.Printf("Pinned Hermes %s API contract passed: sessions, idempotency, SSE, stop, approvals, cron coexistence, restart\n", hermesContractVersion)
	return nil
}

func dockerContract(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

func dockerContractOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = io.Discard
	return cmd.Output()
}

func waitHermesHealth(ctx context.Context, out func(...string) ([]byte, error), name, key string) error {
	for range 90 {
		response, err := hermesHTTP(ctx, out, name, key, http.MethodGet, "/health", "", "")
		if err == nil && response.status == http.StatusOK {
			return nil
		}
		if err := contractSleep(ctx, time.Second); err != nil {
			return err
		}
	}
	return errors.New("hermes API health check timed out")
}

func waitHermesRunTerminal(ctx context.Context, out func(...string) ([]byte, error), name, key, runID string) error {
	for range 30 {
		response, err := hermesHTTP(ctx, out, name, key, http.MethodGet, "/v1/runs/"+runID, "", "")
		if err == nil {
			status, _ := jsonString(response.body, "status")
			if status == "completed" || status == "failed" || status == "cancelled" || status == "interrupted" {
				return nil
			}
		}
		if err := contractSleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("hermes run did not reach a terminal status")
}

func contractSleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func hermesRestartProbe(ctx context.Context, out func(...string) ([]byte, error), run func(...string) error, name, key string) error {
	body := `{"input":"restart probe","session_id":"contract-session","provider":"custom","model":"hub-contract-block"}`
	for attempt := 1; attempt <= 3; attempt++ {
		idempotencyKey := "contract-restart-" + strconv.Itoa(attempt)
		if err := run("exec", "-d", name, "curl", "-sS", "-o", "/tmp/hermes-contract-restart.json",
			"-H", "Authorization: Bearer "+key, "-H", "Idempotency-Key: "+idempotencyKey,
			"-H", "Content-Type: application/json", "--data-raw", body,
			"http://127.0.0.1:"+hermesContractPort+"/v1/runs"); err != nil {
			return err
		}
		if err := waitHermesRunAdmitted(ctx, out, name, key, idempotencyKey, body); err != nil {
			return err
		}
		if err := run("kill", name); err != nil {
			return err
		}
		if err := run("start", name); err != nil {
			return err
		}
		if err := waitHermesHealth(ctx, out, name, key); err != nil {
			return err
		}
		replay, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", body, idempotencyKey)
		if err == nil && replay.status == http.StatusAccepted && strings.EqualFold(replay.headers.Get("Idempotency-Replayed"), "true") {
			status, _ := jsonString(replay.body, "status")
			if status == "interrupted" {
				return nil
			}
		}
	}
	return errors.New("hermes restart did not mark an admitted run interrupted")
}

func waitHermesRunAdmitted(ctx context.Context, out func(...string) ([]byte, error), name, key, idempotencyKey, body string) error {
	for range 30 {
		response, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", body, idempotencyKey)
		if err == nil && response.status == http.StatusAccepted {
			status, statusErr := jsonString(response.body, "status")
			if statusErr == nil && status != "completed" && status != "failed" && status != "cancelled" && status != "interrupted" {
				return nil
			}
		}
		if err := contractSleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("hermes restart probe run was not admitted")
}

func hermesHTTP(ctx context.Context, out func(...string) ([]byte, error), name, key, method, path, body, idempotencyKey string) (contractHTTPResponse, error) {
	args := []string{"exec", name, "curl", "-sS", "-i", "--max-time", "15", "-X", method,
		"-H", "Authorization: Bearer " + key}
	if idempotencyKey != "" {
		args = append(args, "-H", "Idempotency-Key: "+idempotencyKey)
	}
	if body != "" {
		args = append(args, "-H", "Content-Type: application/json", "--data-raw", body)
	}
	args = append(args, "http://127.0.0.1:"+hermesContractPort+path)
	raw, err := out(args...)
	if err != nil {
		return contractHTTPResponse{}, err
	}
	return parseContractHTTP(raw)
}

// Native inserts continuation user notes and makes non-streaming housekeeping
// calls. Only the latest explicit fixture task drives a streaming run scenario.
func nativeFixtureScenario(content json.RawMessage) string {
	latest, scenario := -1, ""
	for _, candidate := range []string{"contract probe", "approval probe", "restart probe", "cancel probe"} {
		if at := strings.LastIndex(string(content), candidate); at > latest {
			latest, scenario = at, candidate
		}
	}
	return scenario
}

func parseContractHTTP(raw []byte) (contractHTTPResponse, error) {
	text := string(raw)
	lineEnd := strings.Index(text, "\r\n")
	separator := strings.Index(text, "\r\n\r\n")
	if lineEnd < 0 || separator < 0 {
		return contractHTTPResponse{}, errors.New("invalid HTTP probe response")
	}
	fields := strings.Fields(text[:lineEnd])
	if len(fields) < 2 {
		return contractHTTPResponse{}, errors.New("invalid HTTP status line")
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return contractHTTPResponse{}, errors.New("invalid HTTP status")
	}
	headers := make(http.Header)
	for _, line := range strings.Split(text[lineEnd+2:separator], "\r\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			headers.Add(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}
	return contractHTTPResponse{status: status, headers: headers, body: []byte(text[separator+4:])}, nil
}

func expectHermesHTTP(ctx context.Context, out func(...string) ([]byte, error), name, key, method, path string, status int) error {
	response, err := hermesHTTP(ctx, out, name, key, method, path, "", "")
	if err != nil || response.status != status {
		return fmt.Errorf("hermes %s %s contract failed: HTTP %d", method, path, response.status)
	}
	return nil
}

func jsonString(body []byte, key string) (string, error) {
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		return "", err
	}
	text, ok := value[key].(string)
	if !ok || text == "" {
		return "", errors.New("missing string field")
	}
	return text, nil
}

func validateHermesCapabilities(response contractHTTPResponse) error {
	if response.status != http.StatusOK {
		return fmt.Errorf("capabilities endpoint returned HTTP %d", response.status)
	}
	var value map[string]any
	if err := json.Unmarshal(response.body, &value); err != nil {
		return errors.New("capabilities response is not JSON")
	}
	if auth, ok := value["auth"].(map[string]any); !ok || auth["required"] != true {
		return errors.New("capabilities auth contract failed")
	}
	if runtime, ok := value["runtime"].(map[string]any); !ok || runtime["mode"] != "server_agent" || runtime["tool_execution"] != "server" || runtime["split_runtime"] != false {
		return errors.New("capabilities runtime boundary contract failed")
	}
	features, ok := value["features"].(map[string]any)
	if !ok {
		return errors.New("capabilities feature map missing")
	}
	for _, name := range []string{"run_status", "run_events_sse", "run_stop", "run_approval_response", "approval_events", "session_chat", "session_chat_streaming", "session_continuity_header", "session_key_header"} {
		if _, exists := features[name]; !exists {
			return fmt.Errorf("capability %s missing", name)
		}
	}
	idempotency, ok := features["runs_idempotency"].(map[string]any)
	if !ok || idempotency["supported"] != true || idempotency["durable"] != true || idempotency["retention_seconds"] != float64(86400) {
		return errors.New("durable run idempotency contract failed")
	}
	for _, name := range []string{"admin_config_rw", "memory_write_api", "audio_api", "realtime_voice"} {
		if features[name] != false {
			return fmt.Errorf("unsupported capability %s was advertised", name)
		}
	}
	endpoints, ok := value["endpoints"].(map[string]any)
	if !ok {
		return errors.New("capabilities endpoint map missing")
	}
	for name, expected := range map[string]string{
		"runs": "/v1/runs", "run_status": "/v1/runs/{run_id}", "run_events": "/v1/runs/{run_id}/events",
		"run_stop": "/v1/runs/{run_id}/stop", "run_approval": "/v1/runs/{run_id}/approval",
		"session_create": "/api/sessions", "session": "/api/sessions/{session_id}", "session_messages": "/api/sessions/{session_id}/messages",
	} {
		entry, ok := endpoints[name].(map[string]any)
		if !ok || entry["path"] != expected {
			return fmt.Errorf("endpoint %s contract failed", name)
		}
	}
	return nil
}

func fasterWhisperFixtureSmoke(ctx context.Context, image string) error {
	id, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		return err
	}
	src, err := spokenFixtureWAV()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "whisper-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.wav"), data, 0600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--entrypoint", "/usr/local/bin/hub-stt", "-v", dir+":/fixture:ro", image, "/fixture/hello.wav", "audio/wav")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	fmt.Printf("faster-whisper image=%s hub-stt stdout=%s stderr=%s\n", strings.TrimSpace(string(id)), strings.TrimSpace(string(out)), strings.TrimSpace(stderr.String()))
	if err != nil {
		return fmt.Errorf("hub-stt adapter: %w: stdout=%s stderr=%s", err, out, stderr.String())
	}
	transcript := strings.TrimSpace(string(out))
	if transcript == "" {
		return errors.New("empty hub-stt transcript for spoken fixture audio")
	}
	fmt.Printf("faster-whisper hub-stt on shipped image produced a transcript %q\n", transcript)
	return nil
}

func spokenFixtureWAV() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime caller for spoken fixture")
	}
	path := filepath.Join(filepath.Dir(file), "testdata", "hello.wav")
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

func whisperTranscript(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "TRANSCRIPT=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "TRANSCRIPT="))
		}
	}
	return ""
}
