package communication

import (
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

type RoutineDestination struct {
	TaskID         string `json:"task_id"`
	Name           string `json:"name"`
	Channel        string `json:"channel"`
	ChatID         int64  `json:"chat_id"`
	TopicID        int64  `json:"topic_id"`
	ConversationID string `json:"conversation_id"`
	State          string `json:"state"`
}

type RoutineDestinationPage struct {
	Items      []RoutineDestination `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

func (s *Spool) routineDestinationLocked(item Schedule) RoutineDestination {
	id := item.TaskID
	if id == "" {
		id = defaultTaskID
	}
	out := RoutineDestination{TaskID: id, Channel: item.Channel, ChatID: item.ChatID, TopicID: item.TopicID, ConversationID: taskConversation(item.ChatID, id), State: "unavailable"}
	if id == defaultTaskID {
		out.Name = "default"
		if item.TopicID == 0 {
			out.State = "active"
		}
		return out
	}
	task, err := s.loadTaskLocked(item.Envelope.PrincipalID, id)
	if err != nil || task.ContextID != item.Envelope.ContextID || task.ChatID != item.ChatID || task.TopicID != item.TopicID {
		return out
	}
	if task.TopicID != 0 {
		bound, err := s.topicTaskLocked(task.PrincipalID, task.ChatID, task.TopicID)
		if err != nil || bound != task.TaskID {
			return out
		}
	}
	out.Name = task.Name
	if task.Archived {
		out.State = "archived"
	} else {
		out.State = "active"
	}
	return out
}

func (s *Spool) ListRoutineDestinations(user User, caller identity.Envelope, after string, limit int, query string) (RoutineDestinationPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := RoutineDestinationPage{Items: []RoutineDestination{}}
	tasks, err := s.listTasksLocked(user.ID)
	if err != nil {
		return out, err
	}
	byID := map[string]Task{}
	for _, task := range tasks {
		byID[task.TaskID] = task
	}
	for _, chatID := range user.TelegramIDs {
		byID[defaultTaskID] = defaultTask(user, chatID)
		break
	}
	tasks = nil
	for _, task := range byID {
		draft := Schedule{ChatID: task.ChatID, DestinationTaskID: task.TaskID}
		if _, err := s.bindRoutineDestinationLocked(draft, caller, user); err != nil {
			continue
		}
		if task.TaskID <= after || !strings.Contains(strings.ToLower(task.Name+" "+task.TaskID), strings.ToLower(query)) {
			continue
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	for _, task := range tasks {
		if len(out.Items) == limit {
			out.NextCursor = out.Items[len(out.Items)-1].TaskID
			break
		}
		out.Items = append(out.Items, RoutineDestination{TaskID: task.TaskID, Name: task.Name, Channel: task.Channel, ChatID: task.ChatID, TopicID: task.TopicID, ConversationID: task.ConversationID, State: "active"})
	}
	return out, nil
}

// Caller holds the spool lock: task validation and persistence must share it.
func (s *Spool) bindRoutineDestinationLocked(in Schedule, caller identity.Envelope, user User) (Schedule, error) {
	target := in.DestinationTaskID
	var source JobMapping
	if in.OriginJobID != "" {
		var err error
		source, err = s.loadMappingLocked(in.OriginJobID)
		canonical := user.Envelope(source.ChatID)
		if err != nil || source.PrincipalID != caller.PrincipalID || source.ContextID != caller.ContextID || source.RuntimeID != caller.RuntimeID || source.PolicyVersion != caller.PolicyVersion || source.Channel != "telegram_bot" || source.UserID != user.ID || source.ActorID != user.ID || source.ScopeID != "user:"+user.ID || source.OrganizationID != in.OrganizationID || source.ExternalIdentityID != canonical.ExternalIdentityID || source.DeliveryTargetID != canonical.DeliveryTargetID || !slices.Contains(user.TelegramIDs, source.ChatID) {
			return Schedule{}, errors.New("routine origin unavailable in this context")
		}
	}
	fromOrigin := target == "" || target == "current"
	if fromOrigin {
		if in.OriginJobID == "" {
			return Schedule{}, errors.New("current destination requires origin_job_id")
		}
		target = source.TaskID
		if target == "" {
			target = defaultTaskID
		}
	}
	chatID := in.ChatID
	if fromOrigin {
		chatID = source.ChatID
	} else if target == defaultTaskID {
		for _, verified := range user.TelegramIDs {
			chatID = verified
			break
		}
	}
	var task Task
	var err error
	if target == defaultTaskID {
		task = defaultTask(user, chatID)
	} else {
		task, err = s.loadTaskLocked(user.ID, target)
	}
	if err != nil || task.Archived || task.PrincipalID != caller.PrincipalID || task.ContextID != caller.ContextID || task.Channel != "telegram_bot" || !slices.Contains(user.TelegramIDs, task.ChatID) || task.TopicID < 0 || (fromOrigin && (source.ChatID != task.ChatID || source.TopicID != task.TopicID)) {
		return Schedule{}, errors.New("routine destination unavailable in this context")
	}
	if task.TopicID != 0 {
		bound, err := s.topicTaskLocked(user.ID, task.ChatID, task.TopicID)
		if err != nil || bound != task.TaskID {
			return Schedule{}, errors.New("routine destination topic binding unavailable")
		}
	}
	in.TaskID, in.TopicID, in.ChatID, in.Channel = task.TaskID, task.TopicID, task.ChatID, task.Channel
	in.Envelope = user.Envelope(task.ChatID)
	return in, nil
}
