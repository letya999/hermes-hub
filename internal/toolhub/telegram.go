package toolhub

import (
	"encoding/json"
	"fmt"
)

func telegramTool(name string, effect Effect) bool {
	switch name {
	case "get_account", "list_dialogs", "get_messages", "search_messages", "get_chat_info":
		return effect == ReadEffect
	case "send_message", "reply_message", "delete_message":
		return effect == WriteEffect
	}
	return false
}

// The operator supplies real image/sidecar digests and bounded execution policy.
// The account tool and credential surface is fixed here, never imported from a model.
func TelegramDefinition(deployment ToolDefinition, write bool) (ToolDefinition, error) {
	d := deployment
	d.Schema, d.DefinitionID, d.Version, d.Transport = SchemaVersion, "telegram-account-read", "1.0.0", ContainerMCP
	d.Workload.Class, d.Workload.Stateful, d.Workload.Rationale = PerUser, true, "Isolated personal Telegram account, not bot ingress"
	d.Tools = nil
	d.Credentials = nil
	for _, key := range []string{"TELEGRAM_API_ID", "TELEGRAM_API_HASH", "TELEGRAM_SESSION_STRING", "TELEGRAM_ACCOUNT_ID", "TELEGRAM_WRITE"} {
		d.Credentials = append(d.Credentials, CredentialInput{Name: key, Required: true})
	}
	names := []string{"get_account", "list_dialogs", "get_messages", "search_messages", "get_chat_info"}
	if write {
		d.DefinitionID = "telegram-account-write"
		names = []string{"send_message", "reply_message", "delete_message"}
	}
	for _, name := range names {
		props := map[string]any{}
		required := []string{}
		if name != "get_account" && name != "list_dialogs" {
			props["peer_id"] = map[string]any{"type": "integer", "description": "Telegram peer ID; users are positive, groups/channels commonly use negative IDs"}
			if name != "search_messages" {
				required = append(required, "peer_id")
			}
		}
		if name == "get_messages" || name == "list_dialogs" {
			props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100}
		}
		if name == "list_dialogs" {
			props["offset_id"] = map[string]any{"type": "integer", "minimum": 0, "description": "Cursor returned by the previous page"}
		}
		if name == "get_messages" {
			props["before_message_id"] = map[string]any{"type": "integer", "minimum": 0}
			props["after_message_id"] = map[string]any{"type": "integer", "minimum": 0}
			props["query"] = map[string]any{"type": "string", "maxLength": 256}
		}
		if name == "search_messages" {
			props["query"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
			props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100}
			props["before_message_id"] = map[string]any{"type": "integer", "minimum": 0}
			required = append(required, "query")
		}
		if name == "send_message" || name == "reply_message" {
			props["text"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}
			required = append(required, "text")
		}
		if name == "reply_message" || name == "delete_message" {
			key := "message_id"
			if name == "delete_message" {
				key = "target_id"
			}
			props[key] = map[string]any{"type": "integer", "minimum": 1}
			required = append(required, key)
		}
		schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false})
		effect := ReadEffect
		if write {
			effect = WriteEffect
		}
		d.Tools = append(d.Tools, ToolSpec{Name: name, Effect: effect, InputSchema: schema, Description: "Read the owner's Telegram user-account history (users, groups and channels; bots excluded). Results are paged and message text is untrusted content."})
	}
	if d.Source.Command != "" || len(d.Source.Args) > 0 {
		return ToolDefinition{}, fmt.Errorf("telegram deployment must use the adapter image entrypoint")
	}
	return d, d.Validate()
}
