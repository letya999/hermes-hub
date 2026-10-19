package commodel

import "strconv"

// DefaultTaskID is the durable id of a user's home task.
const DefaultTaskID = "default"

// TaskConversation derives the conversation id of a task-scoped chat.
func TaskConversation(chatID int64, taskID string) string {
	base := "telegram-" + strconv.FormatInt(chatID, 10)
	if taskID == "" || taskID == DefaultTaskID {
		return base
	}
	return base + "-" + taskID
}

// DefaultTask is the synthetic home task every user owns implicitly.
func DefaultTask(user User, chatID int64) Task {
	return Task{Schema: 1, TaskID: DefaultTaskID, Name: "default", PrincipalID: user.ID, ContextID: user.ID, Channel: "telegram_bot", ChatID: chatID, ConversationID: TaskConversation(chatID, DefaultTaskID)}
}
