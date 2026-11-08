package communication

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type sessionControl struct {
	Action     string `json:"action"`
	EditAction string `json:"edit_action"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	Query      string `json:"query"`
	Cursor     string `json:"cursor"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

func (g *Gateway) handleSessions(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok || r.Method != http.MethodPost {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	user := g.user(caller.PrincipalID)
	if len(user.TelegramIDs) == 0 || caller.PrincipalID != user.ID {
		http.Error(w, "session owner unavailable", http.StatusConflict)
		return
	}
	var in sessionControl
	if decodeJSON(r, &in) != nil || len(in.Query) > 256 || len(in.ID) > 80 || len(in.Title) > 80 || in.Offset < 0 || in.Offset > 100000 || in.Limit < 0 || in.Limit > 100 {
		http.Error(w, "invalid session request", http.StatusBadRequest)
		return
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	chatID := user.TelegramIDs[0]
	if in.Action == "create" {
		if strings.TrimSpace(in.Title) == "" {
			http.Error(w, "session title required", http.StatusBadRequest)
			return
		}
		task, created, err := g.spool.CreateTask(user, chatID, 0, in.Title, true)
		if err != nil {
			http.Error(w, "session create failed", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]any{"session": g.sessionCard(user, task, true), "created": created})
		return
	}
	if in.Action == "list" || in.Action == "search" {
		if in.Action == "search" && strings.TrimSpace(in.Query) == "" {
			http.Error(w, "query required", http.StatusBadRequest)
			return
		}
		live, err := g.spool.ListTasks(user, chatID)
		if err != nil {
			http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
			return
		}
		archived, err := g.spool.ListArchivedTasks(user, chatID)
		if err != nil {
			http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
			return
		}
		tasks := append(live, archived...)
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
		current, err := g.spool.ResolveTask(user, chatID, 0, false)
		if err != nil {
			http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
			return
		}
		start := 0
		if in.Cursor != "" {
			start, err = strconv.Atoi(in.Cursor)
			if err != nil || start < 0 || start > len(tasks) {
				http.Error(w, "invalid cursor", http.StatusBadRequest)
				return
			}
		}
		items := make([]map[string]any, 0, in.Limit)
		for i := start; i < len(tasks); i++ {
			task := tasks[i]
			if task.PrincipalID != user.ID || task.ChatID != chatID || task.Channel != "telegram_bot" {
				continue
			}
			if in.Action == "search" && !strings.Contains(strings.ToLower(task.Name), strings.ToLower(in.Query)) {
				matched, err := g.spool.SessionResultContains(user.ID, task.ConversationID, in.Query)
				if err != nil {
					http.Error(w, "session search unavailable", http.StatusServiceUnavailable)
					return
				}
				if !matched {
					continue
				}
			}
			items = append(items, g.sessionCard(user, task, current.TaskID == task.TaskID))
			if len(items) == in.Limit {
				cursor := ""
				if i+1 < len(tasks) {
					cursor = strconv.Itoa(i + 1)
				}
				writeJSON(w, map[string]any{"items": items, "next_cursor": cursor, "search_scope": "title_and_retained_replies"})
				return
			}
		}
		writeJSON(w, map[string]any{"items": items, "next_cursor": "", "search_scope": "title_and_retained_replies"})
		return
	}
	if in.ID == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}
	if in.Action != "read" && in.Action != "switch" && in.Action != "edit" && in.Action != "remove" ||
		in.Action == "edit" && in.EditAction != "archive" && in.EditAction != "restore" {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}
	live, err := g.spool.ListTasks(user, chatID)
	if err != nil {
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	archived, err := g.spool.ListArchivedTasks(user, chatID)
	if err != nil {
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
		return
	}
	var task commodel.Task
	for _, candidate := range append(live, archived...) {
		if candidate.TaskID == in.ID && candidate.ChatID == chatID && candidate.PrincipalID == user.ID && candidate.Channel == "telegram_bot" {
			task = candidate
			break
		}
	}
	if task.TaskID == "" {
		http.NotFound(w, r)
		return
	}
	switch in.Action {
	case "read":
		mapping, found, err := g.spool.SessionFor(user.ID, user.ID, task.ConversationID)
		if err != nil {
			http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
			return
		}
		if !found || mapping.SessionID == "" {
			writeJSON(w, map[string]any{"session": g.sessionCard(user, task, false), "messages": []any{}, "status": "not_started"})
			return
		}
		if in.Limit > 50 {
			in.Limit = 50
		}
		messages, err := g.sessionMessages(r, caller, task, mapping.SessionID, in.Offset, in.Limit)
		if err != nil {
			http.Error(w, "session transcript unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]any{"session": g.sessionCard(user, task, false), "transcript": messages})
	case "switch":
		if task.Archived {
			http.Error(w, "session archived", http.StatusConflict)
			return
		}
		previous, err := g.spool.ResolveTask(user, chatID, 0, false)
		if err != nil {
			http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, err := g.spool.UseTask(user, chatID, task.TaskID); err != nil {
			http.Error(w, "session switch failed", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]string{"switched_to": task.TaskID, "resumed_from": previous.TaskID})
	case "edit":
		if task.TaskID == defaultTaskID {
			http.Error(w, "invalid session edit", http.StatusBadRequest)
			return
		}
		var updated commodel.Task
		if in.EditAction == "restore" && task.Archived {
			updated, _, err = g.spool.UnarchiveTask(user, chatID, task.TaskID)
		} else if in.EditAction == "archive" && !task.Archived {
			updated, err = g.spool.ArchiveTask(user, chatID, task.TaskID)
		} else {
			http.Error(w, "invalid session edit", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "session edit failed", http.StatusConflict)
			return
		}
		writeJSON(w, g.sessionCard(user, updated, false))
	case "remove":
		if task.TaskID == defaultTaskID {
			http.Error(w, "default session cannot be removed", http.StatusBadRequest)
			return
		}
		if _, err := g.spool.DeleteTask(user, chatID, task.TaskID); err != nil {
			http.Error(w, "session removal failed", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]any{"id": task.TaskID, "removed": true, "transcript_retained_for_audit": true})
	default:
		http.Error(w, "invalid action", http.StatusBadRequest)
	}
}

func (g *Gateway) sessionCard(user User, task commodel.Task, current bool) map[string]any {
	status := "active"
	if task.Archived {
		status = "archived"
	}
	card := map[string]any{"id": task.TaskID, "title": task.Name, "channel": task.Channel, "status": status, "current": current, "created_at": task.CreatedAt, "last_active": task.UpdatedAt}
	if mapping, found, err := g.spool.SessionFor(user.ID, user.ID, task.ConversationID); err == nil && found {
		card["session_id"] = mapping.SessionID
		if mapping.UpdatedAt.After(task.UpdatedAt) {
			card["last_active"] = mapping.UpdatedAt
		}
	}
	return card
}

func (g *Gateway) sessionMessages(r *http.Request, caller identity.Envelope, task commodel.Task, sessionID string, offset, limit int) (map[string]any, error) {
	request := hubruntime.SessionReadRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: caller, OrganizationID: g.config.OrganizationID, UserID: caller.PrincipalID, ActorID: caller.PrincipalID, ScopeID: "user:" + caller.PrincipalID}, SessionID: sessionID, Offset: offset, Limit: limit}
	request.ConversationID = task.ConversationID
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/session-read", bytes.NewReader(encoded)) // #nosec G704 -- configured supervisor or resident runtime
	if err != nil {
		return nil, err
	}
	upstream.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	upstream.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(upstream) // #nosec G704 -- configured supervisor or resident runtime
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, io.EOF
	}
	var out map[string]any
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&out) != nil || out["declared_session_id"] != sessionID {
		return nil, io.EOF
	}
	return out, nil
}

// SessionResultContains searches only retained model replies; prompts and
// archived upstream transcript rows are intentionally outside the spool.
func (s *Spool) SessionResultContains(principal, conversation, query string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.fsys.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return false, err
	}
	needle := strings.ToLower(query)
	for _, entry := range entries {
		if !strings.HasSuffix(entry, ".json") {
			continue
		}
		body, err := s.fsys.ReadFile(filepath.Join(s.root, "mappings", entry))
		if err != nil {
			return false, err
		}
		var row commodel.JobMapping
		if json.Unmarshal(body, &row) == nil && row.PrincipalID == principal && row.ConversationID == conversation && strings.Contains(strings.ToLower(row.Result), needle) {
			return true, nil
		}
	}
	return false, nil
}
