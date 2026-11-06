package routines

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// ExtractEvents preserves the source event identities and requeue arguments
// used by both routine owners while an event source changes hands.
func ExtractEvents(probe *commodel.WatchProbe, body []byte) ([]commodel.Event, error) {
	if probe == nil {
		return nil, errors.New("probe required")
	}
	if probe.Kind != "mcp_tool" {
		return []commodel.Event{{ID: WatchHash(string(body)), Text: BoundedWatchText(string(body))}}, nil
	}
	var envelope struct {
		Events   []json.RawMessage `json:"events"`
		Results  []json.RawMessage `json:"results"`
		Messages []json.RawMessage `json:"messages"`
		Event    *bool             `json:"event"`
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Event != nil && !*envelope.Event {
		return nil, nil
	} else if err == nil && envelope.Events != nil {
		items = envelope.Events
	} else if err == nil && envelope.Results != nil {
		items = envelope.Results
	} else if err == nil && envelope.Messages != nil {
		items = envelope.Messages
	} else {
		var array []json.RawMessage
		if err := json.Unmarshal(body, &array); err == nil {
			items = array
		} else {
			items = []json.RawMessage{json.RawMessage(body)}
		}
	}
	events := make([]commodel.Event, 0, len(items))
	for _, raw := range items {
		if len(raw) > 64*1024 {
			return nil, errors.New("event exceeds filter input limit")
		}
		var item map[string]any
		var id, text string
		var requeue map[string]any
		if json.Unmarshal(raw, &item) == nil {
			for _, key := range []string{"id", "event_id", "message_id"} {
				switch v := item[key].(type) {
				case string:
					if v != "" {
						id = v
					}
				case float64:
					id = strconv.FormatFloat(v, 'f', -1, 64)
				}
				if id != "" {
					break
				}
			}
			for _, key := range []string{"text", "title", "message"} {
				if v, ok := item[key].(string); ok && v != "" {
					text = v
					break
				}
			}
			if text == "" {
				var compact bytes.Buffer
				if json.Compact(&compact, raw) == nil {
					text = compact.String()
				}
			}
			if chatID, ok := item["chat_id"]; ok && chatID != nil {
				requeue = map[string]any{"chat_id": chatID}
			}
		} else {
			var scalar string
			if json.Unmarshal(raw, &scalar) == nil {
				text = scalar
			}
		}
		if id == "" {
			id = WatchHash(string(raw))
		}
		rawText := ""
		if item != nil {
			var compact bytes.Buffer
			if json.Compact(&compact, raw) == nil {
				rawText = compact.String()
			}
		}
		events = append(events, commodel.Event{ID: id, Text: BoundedWatchText(text), Raw: rawText, Requeue: requeue})
	}
	return events, nil
}
