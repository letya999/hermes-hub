package routines

import (
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// RoutineDestination is one routable task target for a routine.
type RoutineDestination struct {
	TaskID         string `json:"task_id"`
	Name           string `json:"name"`
	Channel        string `json:"channel"`
	ChatID         int64  `json:"chat_id"`
	TopicID        int64  `json:"topic_id"`
	ConversationID string `json:"conversation_id"`
	State          string `json:"state"`
}

// RoutineDestinationPage is one page of the destination listing.
type RoutineDestinationPage struct {
	Items      []RoutineDestination `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// RoutineDestinationLocked resolves the durable destination view of one
// schedule. The engine lock must be held.
func (e *Engine) RoutineDestinationLocked(item commodel.Schedule) RoutineDestination {
	id := item.TaskID
	if id == "" {
		id = commodel.DefaultTaskID
	}
	out := RoutineDestination{TaskID: id, Channel: item.Channel, ChatID: item.ChatID, TopicID: item.TopicID, ConversationID: commodel.TaskConversation(item.ChatID, id), State: "unavailable"}
	if id == commodel.DefaultTaskID {
		out.Name = "default"
		if item.TopicID == 0 {
			out.State = "active"
		}
		return out
	}
	task, err := e.deps.LoadTask(item.Envelope.PrincipalID, id)
	if err != nil || task.ContextID != item.Envelope.ContextID || task.ChatID != item.ChatID || task.TopicID != item.TopicID {
		return out
	}
	if task.TopicID != 0 {
		bound, err := e.deps.TopicTask(task.PrincipalID, task.ChatID, task.TopicID)
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

// ListRoutineDestinations pages the caller's routable tasks.
func (e *Engine) ListRoutineDestinations(user commodel.User, caller identity.Envelope, after string, limit int, query string) (RoutineDestinationPage, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := RoutineDestinationPage{Items: []RoutineDestination{}}
	tasks, err := e.deps.ListTasks(user.ID)
	if err != nil {
		return out, err
	}
	byID := map[string]commodel.Task{}
	for _, task := range tasks {
		byID[task.TaskID] = task
	}
	for _, chatID := range user.TelegramIDs {
		byID[commodel.DefaultTaskID] = commodel.DefaultTask(user, chatID)
		break
	}
	tasks = nil
	for _, task := range byID {
		draft := commodel.Schedule{ChatID: task.ChatID, DestinationTaskID: task.TaskID}
		if _, err := e.BindRoutineDestinationLocked(draft, caller, user); err != nil {
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

// BindRoutineDestinationLocked pins a routine draft's destination task to
// the caller's verified bindings. The engine lock must be held.
func (e *Engine) BindRoutineDestinationLocked(in commodel.Schedule, caller identity.Envelope, user commodel.User) (commodel.Schedule, error) {
	target := in.DestinationTaskID
	var source commodel.JobMapping
	if in.OriginJobID != "" {
		var err error
		source, err = e.deps.LoadMapping(in.OriginJobID)
		canonical := user.Envelope(source.ChatID)
		if err != nil || source.PrincipalID != caller.PrincipalID || source.ContextID != caller.ContextID || source.RuntimeID != caller.RuntimeID || source.PolicyVersion != caller.PolicyVersion || source.Channel != "telegram_bot" || source.UserID != user.ID || source.ActorID != user.ID || source.ScopeID != "user:"+user.ID || source.OrganizationID != in.OrganizationID || source.ExternalIdentityID != canonical.ExternalIdentityID || source.DeliveryTargetID != canonical.DeliveryTargetID || !slices.Contains(user.TelegramIDs, source.ChatID) {
			return commodel.Schedule{}, errors.New("routine origin unavailable in this context")
		}
	}
	fromOrigin := target == "" || target == "current"
	if fromOrigin {
		if in.OriginJobID == "" {
			return commodel.Schedule{}, errors.New("current destination requires origin_job_id")
		}
		target = source.TaskID
		if target == "" {
			target = commodel.DefaultTaskID
		}
	}
	chatID := in.ChatID
	if fromOrigin {
		chatID = source.ChatID
	} else if target == commodel.DefaultTaskID {
		for _, verified := range user.TelegramIDs {
			chatID = verified
			break
		}
	}
	var task commodel.Task
	var err error
	if target == commodel.DefaultTaskID {
		task = commodel.DefaultTask(user, chatID)
	} else {
		task, err = e.deps.LoadTask(user.ID, target)
	}
	if err != nil || task.Archived || task.PrincipalID != caller.PrincipalID || task.ContextID != caller.ContextID || task.Channel != "telegram_bot" || !slices.Contains(user.TelegramIDs, task.ChatID) || task.TopicID < 0 || (fromOrigin && (source.ChatID != task.ChatID || source.TopicID != task.TopicID)) {
		return commodel.Schedule{}, errors.New("routine destination unavailable in this context")
	}
	if task.TopicID != 0 {
		bound, err := e.deps.TopicTask(user.ID, task.ChatID, task.TopicID)
		if err != nil || bound != task.TaskID {
			return commodel.Schedule{}, errors.New("routine destination topic binding unavailable")
		}
	}
	in.TaskID, in.TopicID, in.ChatID, in.Channel = task.TaskID, task.TopicID, task.ChatID, task.Channel
	in.Envelope = user.Envelope(task.ChatID)
	return in, nil
}
