package toolhub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func (g *Gateway) execSession(ctx context.Context, auth identity.Envelope, tool api.ToolSpec, args map[string]any) (api.BackendResult, error) {
	fail := func(code string) (api.BackendResult, error) {
		return api.BackendResult{IsError: true, Structured: map[string]any{"error": code}}, nil
	}
	in := map[string]any{"action": strings.TrimPrefix(tool.Name, "session_")}
	switch tool.Name {
	case "session_list":
		in["cursor"], in["limit"] = api.ArgString(args, "cursor"), api.ArgInt(args, "limit")
	case "session_search":
		query := strings.TrimSpace(api.ArgString(args, "query"))
		if query == "" || len(query) > 256 {
			return fail("invalid_query")
		}
		in["query"], in["limit"] = query, api.ArgInt(args, "limit")
	case "session_read":
		in["offset"], in["limit"] = api.ArgInt(args, "offset"), api.ArgInt(args, "limit")
	case "session_create":
		title := strings.TrimSpace(api.ArgString(args, "title"))
		if title == "" || len(title) > 80 {
			return fail("invalid_title")
		}
		in["title"] = title
	case "session_edit":
		edit := api.ArgString(args, "action")
		if edit != "archive" && edit != "restore" {
			return fail("invalid_action")
		}
		in["edit_action"] = edit
	case "session_switch", "session_remove":
	default:
		return api.BackendResult{}, api.ErrInvalid
	}
	if tool.Name != "session_list" && tool.Name != "session_search" && tool.Name != "session_create" {
		id := api.ArgString(args, "id")
		if id == "" || len(id) > 80 {
			return fail("invalid_id")
		}
		in["id"] = id
	}
	var out map[string]any
	switch status := g.callCommunication(ctx, auth, http.MethodPost, "/v1/sessions", in, &out); status {
	case http.StatusOK:
	case http.StatusNotFound:
		return fail("not_found")
	case http.StatusBadRequest:
		return fail("invalid_request")
	case http.StatusUnauthorized, http.StatusForbidden:
		return fail("forbidden")
	default:
		return fail("unavailable")
	}
	encoded, _ := json.Marshal(out)
	return api.BackendResult{Structured: out, Text: string(encoded)}, nil
}
