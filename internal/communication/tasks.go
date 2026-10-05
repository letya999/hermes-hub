package communication

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Task is a durable owner/context-scoped conversation unit. The binding is
// (channel, verified peer, chat, topic) -> task -> conversation -> Hermes
// session: ConversationID feeds sessionIDFor, so each task owns a
// deterministic upstream session. The implicit "default" task keeps the
// legacy telegram-<chat> conversation and is materialized only on mutation.
const defaultTaskID = "default"

const (
	taskNameLimit  = 64
	taskStyleLimit = 1024
)

type Task struct {
	Schema         int       `json:"schema"`
	TaskID         string    `json:"task_id"`
	Name           string    `json:"name"`
	PrincipalID    string    `json:"principal_id"`
	ContextID      string    `json:"context_id"`
	Channel        string    `json:"channel"`
	Peer           string    `json:"peer"`
	ChatID         int64     `json:"chat_id"`
	TopicID        int64     `json:"topic_id,omitempty"`
	ConversationID string    `json:"conversation_id"`
	Style          string    `json:"style,omitempty"`
	StyleVersion   uint64    `json:"style_version,omitempty"`
	Archived       bool      `json:"archived,omitempty"`
	NameAuto       bool      `json:"name_auto,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func taskConversation(chatID int64, taskID string) string {
	base := "telegram-" + strconv.FormatInt(chatID, 10)
	if taskID == "" || taskID == defaultTaskID {
		return base
	}
	return base + "-" + taskID
}

func defaultTask(user User, chatID int64) Task {
	return Task{Schema: 1, TaskID: defaultTaskID, Name: "default", PrincipalID: user.ID, ContextID: user.ID, Channel: "telegram_bot", ChatID: chatID, ConversationID: taskConversation(chatID, defaultTaskID)}
}

// validTaskName bounds operator-chosen labels; they never reach a provider.
func validTaskName(name string) bool {
	if name == "" || len(name) > taskNameLimit || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r < ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// cleanTaskStyle bounds free-form style text: length cap and no control
// characters besides newline/tab. Style is presentation guidance; it is never
// parsed for authorization and never reaches a permission path.
func cleanTaskStyle(text string) (string, error) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" || len(text) > taskStyleLimit || !utf8.ValidString(text) {
		return "", errors.New("invalid style")
	}
	for _, r := range text {
		if (r < ' ' && r != '\n' && r != '\t') || r == 0x7f {
			return "", errors.New("invalid style")
		}
	}
	return text, nil
}

func (s *Spool) taskPath(principal, taskID string) string {
	h := sha256.Sum256([]byte(principal + "\x00" + taskID))
	return filepath.Join(s.root, "tasks", hex.EncodeToString(h[:])+".json")
}

func (s *Spool) currentTaskPath(principal string, chatID int64) string {
	h := sha256.Sum256([]byte(principal + "\x00" + strconv.FormatInt(chatID, 10)))
	return filepath.Join(s.root, "tasks", "current", hex.EncodeToString(h[:])+".json")
}

func (s *Spool) topicTaskPath(principal string, chatID, topicID int64) string {
	h := sha256.Sum256([]byte(principal + "\x00" + strconv.FormatInt(chatID, 10) + "\x00" + strconv.FormatInt(topicID, 10)))
	return filepath.Join(s.root, "tasks", "topic", hex.EncodeToString(h[:])+".json")
}

func (s *Spool) loadTaskLocked(principal, taskID string) (Task, error) {
	b, err := os.ReadFile(s.taskPath(principal, taskID))
	if err != nil {
		return Task{}, err
	}
	var task Task
	if err := json.Unmarshal(b, &task); err != nil || task.TaskID != taskID || task.PrincipalID != principal {
		return Task{}, errors.New("invalid task record")
	}
	return task, nil
}

func (s *Spool) writeTaskLocked(task Task) error {
	task.UpdatedAt = time.Now().UTC()
	return atomicJSON(s.taskPath(task.PrincipalID, task.TaskID), task)
}

func (s *Spool) currentTaskLocked(principal string, chatID int64) (string, error) {
	b, err := os.ReadFile(s.currentTaskPath(principal, chatID))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var pointer struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(b, &pointer); err != nil {
		return "", errors.New("invalid current task pointer")
	}
	return pointer.TaskID, nil
}

func (s *Spool) topicTaskLocked(principal string, chatID, topicID int64) (string, error) {
	b, err := os.ReadFile(s.topicTaskPath(principal, chatID, topicID))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var binding struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(b, &binding); err != nil {
		return "", errors.New("invalid topic binding")
	}
	return binding.TaskID, nil
}

// materializeTaskLocked persists a task (or the lazily-created default task)
// and installs its topic binding when present.
func (s *Spool) materializeTaskLocked(task Task) error {
	if err := s.writeTaskLocked(task); err != nil {
		return err
	}
	if task.TopicID != 0 {
		return atomicJSON(s.topicTaskPath(task.PrincipalID, task.ChatID, task.TopicID), struct {
			TaskID string `json:"task_id"`
		}{task.TaskID})
	}
	return nil
}

func newTaskID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "t-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "t-" + hex.EncodeToString(b)
}

// CreateTask makes a named task for a verified owner, binds an optional topic
// and optionally switches the chat's current pointer. Repeating the same name
// returns the existing live task so a replayed update cannot mint a second.
func (s *Spool) CreateTask(user User, chatID, topicID int64, name string, makeCurrent bool) (Task, bool, error) {
	name = strings.TrimSpace(name)
	auto := name == ""
	if auto {
		name = "сессия " + time.Now().UTC().Format("02.01 15:04")
	}
	if !validTaskName(name) {
		return Task{}, false, errors.New("invalid task name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.listTasksLocked(user.ID)
	if err != nil {
		return Task{}, false, err
	}
	for _, task := range existing {
		if !task.Archived && task.TaskID != defaultTaskID && task.Name == name {
			if makeCurrent {
				if err := s.setCurrentLocked(task); err != nil {
					return Task{}, false, err
				}
			}
			return task, false, nil
		}
	}
	now := time.Now().UTC()
	var taskID string
	for i := 0; i < 8; i++ {
		taskID = newTaskID()
		if _, err := s.loadTaskLocked(user.ID, taskID); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return Task{}, false, err
		}
	}
	if topicID != 0 {
		if bound, err := s.topicTaskLocked(user.ID, chatID, topicID); err != nil {
			return Task{}, false, err
		} else if bound != "" {
			if boundTask, err := s.loadTaskLocked(user.ID, bound); err == nil && !boundTask.Archived {
				return Task{}, false, errors.New("topic is already bound to task " + boundTask.TaskID)
			}
		}
	}
	task := Task{Schema: 1, TaskID: taskID, Name: name, PrincipalID: user.ID, ContextID: user.ID, Channel: "telegram_bot", Peer: "telegram-" + strconv.FormatInt(user.TelegramIDs[0], 10), ChatID: chatID, TopicID: topicID, ConversationID: taskConversation(chatID, taskID), NameAuto: auto, CreatedAt: now, UpdatedAt: now}
	if err := s.materializeTaskLocked(task); err != nil {
		return Task{}, false, err
	}
	if makeCurrent {
		if err := s.setCurrentLocked(task); err != nil {
			return Task{}, false, err
		}
	}
	return task, true, nil
}

func (s *Spool) setCurrentLocked(task Task) error {
	return atomicJSON(s.currentTaskPath(task.PrincipalID, task.ChatID), struct {
		TaskID string `json:"task_id"`
	}{task.TaskID})
}

// ResolveTask maps an inbound audience to its task. A message inside a
// Telegram direct-messages topic resolves to the bound task; adoptTopic turns
// a verified first post in an unbound topic into a new bound task. Root-DM
// messages follow the chat's current pointer, falling back to the default
// task.
func (s *Spool) ResolveTask(user User, chatID, topicID int64, adoptTopic bool) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if topicID != 0 {
		taskID, err := s.topicTaskLocked(user.ID, chatID, topicID)
		if err != nil {
			return Task{}, err
		}
		if taskID != "" {
			task, err := s.loadTaskLocked(user.ID, taskID)
			if err == nil && !task.Archived {
				return task, nil
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return Task{}, err
			}
		}
		if !adoptTopic {
			taskID, err := s.currentTaskLocked(user.ID, chatID)
			if err != nil {
				return Task{}, err
			}
			if taskID != "" {
				task, err := s.loadTaskLocked(user.ID, taskID)
				if err == nil && !task.Archived {
					return task, nil
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return Task{}, err
				}
			}
			return defaultTask(user, chatID), nil
		}
		now := time.Now().UTC()
		taskID = newTaskID()
		task := Task{Schema: 1, TaskID: taskID, Name: "topic-" + strconv.FormatInt(topicID, 10), PrincipalID: user.ID, ContextID: user.ID, Channel: "telegram_bot", Peer: "telegram-" + strconv.FormatInt(user.TelegramIDs[0], 10), ChatID: chatID, TopicID: topicID, ConversationID: taskConversation(chatID, taskID), CreatedAt: now, UpdatedAt: now}
		if err := s.materializeTaskLocked(task); err != nil {
			return Task{}, err
		}
		if err := s.setCurrentLocked(task); err != nil {
			return Task{}, err
		}
		return task, nil
	}
	taskID, err := s.currentTaskLocked(user.ID, chatID)
	if err != nil {
		return Task{}, err
	}
	if taskID != "" {
		task, err := s.loadTaskLocked(user.ID, taskID)
		if err == nil && !task.Archived {
			return task, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Task{}, err
		}
	}
	return defaultTask(user, chatID), nil
}

// TaskByID returns one owned task; "default" resolves the implicit task.
func (s *Spool) TaskByID(user User, chatID int64, taskID string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskByIDLocked(user, chatID, taskID)
}

func (s *Spool) taskByIDLocked(user User, chatID int64, taskID string) (Task, error) {
	if taskID == "" || taskID == defaultTaskID {
		task, err := s.loadTaskLocked(user.ID, defaultTaskID)
		if errors.Is(err, os.ErrNotExist) {
			return defaultTask(user, chatID), nil
		}
		if err != nil {
			return Task{}, err
		}
		if task.Archived {
			return defaultTask(user, chatID), nil
		}
		return task, nil
	}
	task, err := s.loadTaskLocked(user.ID, taskID)
	if err != nil {
		return Task{}, err
	}
	if task.Archived {
		return Task{}, errors.New("task is archived")
	}
	return task, nil
}

// findTaskLocked resolves a selector (task id or unique live name).
func (s *Spool) findTaskLocked(user User, chatID int64, selector string) (Task, error) {
	if selector == defaultTaskID || selector == "" {
		return s.taskByIDLocked(user, chatID, defaultTaskID)
	}
	if task, err := s.loadTaskLocked(user.ID, selector); err == nil && !task.Archived {
		return task, nil
	}
	tasks, err := s.listTasksLocked(user.ID)
	if err != nil {
		return Task{}, err
	}
	found := Task{}
	count := 0
	for _, task := range tasks {
		if !task.Archived && task.Name == selector {
			found, count = task, count+1
		}
	}
	if count == 1 {
		return found, nil
	}
	if count > 1 {
		return Task{}, errors.New("task name is ambiguous; use its id")
	}
	return Task{}, errors.New("task not found")
}

func (s *Spool) listTasksLocked(principal string) ([]Task, error) {
	// ponytail: per-owner linear scan is bounded by the single-host spool; add an index if task volume makes it measurable.
	entries, err := os.ReadDir(filepath.Join(s.root, "tasks"))
	if err != nil {
		return nil, err
	}
	tasks := []Task{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root, "tasks", entry.Name()))
		if err != nil {
			return nil, err
		}
		var task Task
		if json.Unmarshal(b, &task) != nil || task.TaskID == "" || task.PrincipalID != principal {
			continue
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
	return tasks, nil
}

func (s *Spool) ListTasks(user User, chatID int64) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.listTasksLocked(user.ID)
	if err != nil {
		return nil, err
	}
	out := []Task{}
	hasDefault := false
	for _, task := range tasks {
		if task.TaskID == defaultTaskID {
			hasDefault = true
		}
		if task.Archived {
			continue
		}
		out = append(out, task)
	}
	if !hasDefault {
		out = append([]Task{defaultTask(user, chatID)}, out...)
	}
	return out, nil
}

// UseTask switches the chat's current pointer to an existing live task.
func (s *Spool) UseTask(user User, chatID int64, selector string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.findTaskLocked(user, chatID, selector)
	if err != nil {
		return Task{}, err
	}
	return task, s.setCurrentLocked(task)
}

// taskByConversationLocked finds the owner's live task that owns a
// conversation id, so a continuation job can keep the originating task's
// topic binding. Unknown conversations resolve to the default task.
func (s *Spool) taskByConversationLocked(user User, chatID int64, conversationID string) Task {
	tasks, err := s.listTasksLocked(user.ID)
	if err == nil {
		for _, task := range tasks {
			if !task.Archived && task.ChatID == chatID && task.ConversationID == conversationID {
				return task
			}
		}
	}
	return defaultTask(user, chatID)
}

// TaskByConversation resolves the task owning a conversation id for callers
// that only carry the durable envelope (e.g. prepare-outcome continuations).
func (s *Spool) TaskByConversation(user User, chatID int64, conversationID string) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskByConversationLocked(user, chatID, conversationID)
}

// taskForMutationLocked loads the task the command resolved — a topic-bound
// task wins over the chat's current pointer — and refuses foreign-chat tasks.
func (s *Spool) taskForMutationLocked(user User, chatID int64, taskID string) (Task, error) {
	task, err := s.taskByIDLocked(user, chatID, taskID)
	if err != nil {
		return Task{}, err
	}
	if task.ChatID != chatID {
		return Task{}, errors.New("task belongs to a different chat")
	}
	return task, nil
}

// RenameTask renames a live task. ConversationID is unchanged, so the bound
// Hermes session and history survive.
func (s *Spool) RenameTask(user User, chatID int64, taskID, name string) (Task, error) {
	name = strings.TrimSpace(name)
	if !validTaskName(name) {
		return Task{}, errors.New("invalid task name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.taskForMutationLocked(user, chatID, taskID)
	if err != nil {
		return Task{}, err
	}
	task.Name = name
	task.NameAuto = false
	return task, s.materializeTaskLocked(task)
}

// ArchiveTask retires a live task: its topic binding and current pointer are
// released and the chat falls back to the default conversation. The task file
// keeps its session binding for audit; the same name can be reused later.
func (s *Spool) ArchiveTask(user User, chatID int64, taskID string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.taskForMutationLocked(user, chatID, taskID)
	if err != nil {
		return Task{}, err
	}
	if task.TaskID == defaultTaskID {
		return Task{}, errors.New("the default task cannot be archived")
	}
	task.Archived = true
	if err := s.writeTaskLocked(task); err != nil {
		return Task{}, err
	}
	if task.TopicID != 0 {
		if err := os.Remove(s.topicTaskPath(user.ID, chatID, task.TopicID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Task{}, err
		}
	}
	if current, err := s.currentTaskLocked(user.ID, chatID); err == nil && current == task.TaskID {
		if err := os.Remove(s.currentTaskPath(user.ID, chatID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Task{}, err
		}
	} else if err != nil {
		return Task{}, err
	}
	return task, nil
}

// DeleteTask removes a live or archived task record: the topic binding and
// current pointer are released and the chat falls back to the default task.
// The upstream Hermes session is left for audit; only the owner's registry
// entry and routing state are removed.
func (s *Spool) DeleteTask(user User, chatID int64, taskID string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.loadTaskLocked(user.ID, taskID)
	if err != nil {
		return Task{}, errors.New("task not found")
	}
	return s.deleteTaskLocked(user, chatID, task)
}

// DeleteTaskBySelector deletes a task addressed by id or unique name across
// both live and archived records, so an archived task can be removed without
// resurrecting it first.
func (s *Spool) DeleteTaskBySelector(user User, chatID int64, selector string) (Task, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return Task{}, errors.New("missing task selector")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if task, err := s.findTaskLocked(user, chatID, selector); err == nil {
		return s.deleteTaskLocked(user, chatID, task)
	}
	tasks, err := s.listTasksLocked(user.ID)
	if err != nil {
		return Task{}, err
	}
	found, count := Task{}, 0
	for _, task := range tasks {
		if task.Archived && (task.TaskID == selector || task.Name == selector) {
			found, count = task, count+1
		}
	}
	if count > 1 {
		return Task{}, errors.New("task name is ambiguous; use its id")
	}
	if count == 0 {
		return Task{}, errors.New("task not found")
	}
	return s.deleteTaskLocked(user, chatID, found)
}

// deleteTaskLocked removes one resolved task record; caller holds the lock.
func (s *Spool) deleteTaskLocked(user User, chatID int64, task Task) (Task, error) {
	if task.ChatID != chatID {
		return Task{}, errors.New("task belongs to a different chat")
	}
	if task.TaskID == defaultTaskID {
		return Task{}, errors.New("the default task cannot be deleted")
	}
	if task.TopicID != 0 {
		if err := os.Remove(s.topicTaskPath(user.ID, chatID, task.TopicID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Task{}, err
		}
	}
	if current, err := s.currentTaskLocked(user.ID, chatID); err == nil && current == task.TaskID {
		if err := os.Remove(s.currentTaskPath(user.ID, chatID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Task{}, err
		}
	} else if err != nil {
		return Task{}, err
	}
	if err := os.Remove(s.taskPath(user.ID, task.TaskID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Task{}, err
	}
	return task, nil
}

// ListArchivedTasks returns the owner's archived tasks, oldest first.
func (s *Spool) ListArchivedTasks(user User, chatID int64) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.listTasksLocked(user.ID)
	if err != nil {
		return nil, err
	}
	out := []Task{}
	for _, task := range tasks {
		if task.Archived {
			out = append(out, task)
		}
	}
	return out, nil
}

// AutoTitleTask renames an auto-named task once: explicit names and archived
// tasks are never touched. Returns false when the task does not qualify.
func (s *Spool) AutoTitleTask(principal, taskID, title string) bool {
	title = strings.TrimSpace(title)
	if !validTaskName(title) || taskID == "" || taskID == defaultTaskID {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.loadTaskLocked(principal, taskID)
	if err != nil || task.Archived || !task.NameAuto {
		return false
	}
	task.Name = title
	task.NameAuto = false
	return s.writeTaskLocked(task) == nil
}

// TaskNameAuto reports whether the task still carries its auto-generated
// placeholder name, i.e. it is eligible for session-title adoption.
func (s *Spool) TaskNameAuto(principal, taskID string) bool {
	if taskID == "" || taskID == defaultTaskID {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.loadTaskLocked(principal, taskID)
	return err == nil && task.NameAuto && !task.Archived
}

// SetTaskStyle writes or clears a task's presentation style, bumping the
// version so an admitted run snapshot can never be silently reinterpreted.
// The default task is materialized on first style.
func (s *Spool) SetTaskStyle(user User, chatID int64, taskID, style string) (Task, error) {
	if style != "" {
		cleaned, err := cleanTaskStyle(style)
		if err != nil {
			return Task{}, err
		}
		style = cleaned
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task, err := s.taskForMutationLocked(user, chatID, taskID)
	if err != nil {
		return Task{}, err
	}
	task.Style = style
	task.StyleVersion++
	return task, s.materializeTaskLocked(task)
}

// StyleForRun pins the admitted-run style snapshot on the durable mapping
// before the run is submitted: a queued or retried job reuses its snapshot
// while a later /style change applies to the next run only.
func (s *Spool) StyleForRun(job Job) (string, error) {
	if job.TaskID == "" {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, err := s.loadMappingLocked(job.ID)
	if err != nil {
		return "", err
	}
	if mapping.StyleSnapshotted {
		return mapping.Style, nil
	}
	style := ""
	version := uint64(0)
	if task, err := s.loadTaskLocked(job.PrincipalID, job.TaskID); err == nil {
		style, version = task.Style, task.StyleVersion
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	mapping.Style, mapping.StyleVersion, mapping.StyleSnapshotted = style, version, true
	if err := s.writeMappingLocked(mapping); err != nil {
		return "", err
	}
	return style, nil
}

func taskLabel(task Task) string {
	if task.TaskID == defaultTaskID {
		return "default (основной)"
	}
	return fmt.Sprintf("%s (%s)", task.Name, task.TaskID)
}
