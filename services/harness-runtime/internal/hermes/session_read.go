package hermes

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

func (s *runtimeHTTP) sessionRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var request SessionReadRequest
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&request) != nil || !ValidSessionReadRequest(request) || request.Envelope.Validate(os.Getenv("HUB_PRINCIPAL_ID"), os.Getenv("HUB_CONTEXT_ID"), os.Getenv("HUB_RUNTIME_ID"), os.Getenv("HUB_POLICY_VERSION")) != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid session request")
		return
	}
	base := "http://" + env("HUB_HERMES_API_HOST", "127.0.0.1") + ":" + env("HUB_HERMES_API_PORT", "8642")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var upstream struct {
		SessionID string `json:"session_id"`
		Data      []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"data"`
	}
	path := "/api/sessions/" + url.PathEscape(request.SessionID) + "/messages?limit=" + strconv.Itoa(request.Limit) + "&offset=" + strconv.Itoa(request.Offset)
	if err := hermesRequest(r.Context(), client, http.MethodGet, base+path, env("API_SERVER_KEY", os.Getenv("HUB_RUNTIME_AUTH")), nil, &upstream); err != nil || upstream.SessionID != request.SessionID {
		writeRuntimeError(w, http.StatusServiceUnavailable, "session unavailable")
		return
	}
	messages := make([]map[string]string, 0, len(upstream.Data))
	for _, row := range upstream.Data {
		if row.Role != "user" && row.Role != "assistant" {
			continue
		}
		content, ok := row.Content.(string)
		if !ok {
			continue
		}
		if len(content) > 16<<10 {
			content = content[:16<<10]
		}
		messages = append(messages, map[string]string{"role": row.Role, "content": content})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"declared_session_id": request.SessionID, "session_id": upstream.SessionID, "messages": messages, "offset": request.Offset, "limit": request.Limit, "more": len(upstream.Data) == request.Limit})
}
