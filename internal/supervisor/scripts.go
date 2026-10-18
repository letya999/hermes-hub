package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// scriptHTTP isolates arbitrary Starlark allocations from the control plane.
// The existing core image contains the helper; no workspace, credentials or
// network are available to this disposable container.
func (m *Manager) scriptHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var request hubruntime.ScriptRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 384*1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || len(request.Text) == 0 || len(request.Text) > 16*1024 || len(request.Input) > 64*1024 || request.DueAt.IsZero() || request.Trigger != "cron" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid script request"})
		return
	}
	if len(request.Needs) > 4 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid script request"})
		return
	}
	for _, need := range request.Needs {
		if len(need) > 64*1024 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid script request"})
			return
		}
	}
	binding, err := m.bindingFor(request.ExecuteRequest)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "script owner unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-ctx.Done():
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "script capacity unavailable"})
		return
	}
	image, err := devScriptImage(binding.ContextRoot, binding.UserID, m.cfg.Environment, m.cfg.Image)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "dev script image unavailable"})
		return
	}
	name := "hermes-script-" + binding.PrincipalID + "-" + rand.Text()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = m.command(cleanup, "rm", "-f", name)
	}()
	args := []string{"run", "--rm", "--name", name, "--network", "none", "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", "16", "--memory", "128m", "--memory-swap", "128m", "--cpus", "0.5", "--log-driver", "none", "--label", "hermes-hub.owner=" + m.ownerID(), "--label", "hermes-hub.role=script"}
	if request.MountSpace {
		watchDir := filepath.Join(m.cfg.SpacesRoot, binding.ContextID, "watch")
		if err := os.MkdirAll(watchDir, 0750); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "watch directory unavailable"})
			return
		}
		args = append(args, "--mount", "type=bind,src="+watchDir+",dst=/watch,readonly")
	}
	args = append(args, "--entrypoint", "communication-hub", image, "--script", base64.StdEncoding.EncodeToString([]byte(request.Text)), request.DueAt.UTC().Format(time.RFC3339Nano), base64.StdEncoding.EncodeToString([]byte(request.Input)), map[bool]string{true: "/watch", false: "-"}[request.MountSpace])
	for _, need := range request.Needs {
		args = append(args, base64.StdEncoding.EncodeToString([]byte(need)))
	}
	out, err := m.command(ctx, args...)
	var emitted []string
	if err != nil || len(out) > 128*1024 || json.Unmarshal(out, &emitted) != nil || len(emitted) > 4 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "isolated script failed"})
		return
	}
	for _, text := range emitted {
		if len(text) > 4096 {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "script output exceeds limit"})
			return
		}
	}
	writeJSON(w, http.StatusOK, emitted)
}
