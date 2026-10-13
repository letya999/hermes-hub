package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fileAgentCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, mode, name string) (status, reason string) {
	if !admittedHubTool(ctx, client, config, user, "file_write") || !admittedHubTool(ctx, client, config, user, "file_read") || !admittedHubTool(ctx, client, config, user, "artifact_remove") {
		return "BLOCKED", "owner file fixture and cleanup grants required; no agent job submitted"
	}
	// The tools daemon belongs to a live runtime. Wake it through the existing
	// mock pipeline, never a direct supervisor mutation or a second queue writer.
	if status, reason := mockServerCase(ctx, client, config, user, "text"); status != "PASS" {
		return status, reason
	}
	session, err := ownerHubSession(ctx, client, config, user)
	if err != nil {
		return "BLOCKED", "owner ToolHub session unavailable"
	}
	defer session.Close()
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "FAIL", "random file fixture identity unavailable"
	}
	marker := "E2E " + hex.EncodeToString(nonce[:8])
	secret := "E2E_PRIVATE_" + hex.EncodeToString(nonce[8:])
	directory := "artifacts/documents/e2e-" + hex.EncodeToString(nonce[:8])
	filename := "hidden-" + hex.EncodeToString(nonce[8:]) + ".txt"
	rel := directory + "/" + filename
	target := rel
	cleanupPaths := []string{rel}
	initial, err := hubToolObject(ctx, session, "file_write", map[string]any{"path": rel, "text": secret})
	if err != nil {
		return "FAIL", "own synthetic file preparation failed"
	}
	initialRevision, _ := initial["revision"].(string)
	if initialRevision == "" {
		return "FAIL", "file fixture revision absent; own artifact retained"
	}
	terminal := false
	defer func() {
		if !terminal {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, ownPath := range cleanupPaths {
			if _, err := hubToolObject(cleanup, session, "artifact_remove", map[string]any{"path": ownPath}); err != nil {
				status, reason = "FAIL", "own file fixture cleanup failed"
			}
		}
	}()
	prompt, expected := "", marker
	switch name {
	case "code_exec":
		target = directory + "/exports/result.txt"
		prompt = fmt.Sprintf("Use code_exec with path %s. Run this bounded shell command exactly: cat \"$INPUTS/%s\" > \"$OUTPUTS/result.txt\"; printf ' 42' >> \"$OUTPUTS/result.txt\". Return exactly %s only after exit_code zero and result.txt exported. Do not run any other command or change any other file.", directory, filename, marker)
	case "file_list":
		prompt = fmt.Sprintf("Use file_list only inside %s. Return exactly %s followed by a space and the basename of its single file. Do not read other directories.", directory, marker)
		expected = marker + " " + filename
	case "file_read":
		prompt = fmt.Sprintf("Use file_read only on %s. Return exactly %s followed by a space and its text, without quotes or explanation.", rel, marker)
		expected = marker + " " + secret
	case "file_search":
		prompt = fmt.Sprintf("Use file_search only inside %s, query E2E_PRIVATE_. Return exactly %s followed by a space and the basename of the matching file. Do not search other paths.", directory, marker)
		expected = marker + " " + filename
	case "file_write":
		prompt = fmt.Sprintf("Use file_read on %s to obtain its revision, then file_write to update that same file to exactly %s BETA with the obtained revision. Return exactly %s after success. Do not change other files.", rel, secret, marker)
	case "document_extract":
		prompt = fmt.Sprintf("Use document_extract only on %s. Return exactly %s followed by a space and its extracted text, without quotes or explanation.", rel, marker)
		expected = marker + " " + secret
	case "document_edit":
		prompt = fmt.Sprintf("Use document_edit only on %s, text exactly %s BETA. Return exactly %s after success. Do not change other files.", rel, secret, marker)
	case "document_create", "document_convert":
		ownName := "e2e-" + hex.EncodeToString(nonce[:8]) + "-output"
		format := "txt"
		prompt = fmt.Sprintf("Use document_create with name %s, format txt, text exactly %s. Return exactly %s after success.", ownName, secret, marker)
		if name == "document_convert" {
			format = "md"
			prompt = fmt.Sprintf("Use document_convert only on %s, name %s, format md. Return exactly %s after success.", rel, ownName, marker)
		}
		target = "artifacts/documents/" + ownName + "." + format
	case "artifact_remove":
		if !admittedHubTool(ctx, client, config, user, "file_list") {
			terminal = true
			return "BLOCKED", "owned directory listing grant required for deletion oracle"
		}
		prompt = fmt.Sprintf("Use artifact_remove only on %s. Return exactly %s after success. Do not remove other files.", rel, marker)
	default:
		terminal = true
		return "BLOCKED", "no file agent oracle for this operation"
	}
	run := mockPromptCase
	if mode == "telegram-server" {
		run = serverPromptCase
	}
	status, reason = run(ctx, client, config, user, "text", prompt, expected)
	if status != "PASS" {
		return status, reason + "; synthetic file retained; no automatic retry"
	}
	terminal = true
	if name == "artifact_remove" {
		listed, err := hubToolObject(ctx, session, "file_list", map[string]any{"path": directory})
		if err != nil {
			return "FAIL", "independent deletion directory read failed"
		}
		items, ok := listed["items"].([]any)
		if !ok {
			return "FAIL", "deletion oracle directory response is invalid"
		}
		for _, item := range items {
			file, ok := item.(map[string]any)
			if !ok || file["name"] == filename {
				return "FAIL", "agent claimed deletion but own file remains"
			}
		}
		cleanupPaths = nil
		return "PASS", reason + "; independent directory read proves own artifact absent"
	}
	actual, err := hubToolObject(ctx, session, "file_read", map[string]any{"path": target})
	if err != nil {
		return "FAIL", "independent file read failed"
	}
	if target != rel {
		cleanupPaths = append(cleanupPaths, target)
		seed, err := hubToolObject(ctx, session, "file_read", map[string]any{"path": rel})
		if err != nil || seed["text"] != secret {
			return "FAIL", "document operation changed its source fixture"
		}
	}
	want := secret
	if name == "file_write" || name == "document_edit" {
		want += " BETA"
	}
	if name == "code_exec" {
		want += " 42"
	}
	if actual["text"] != want {
		return "FAIL", "independent file content contradicts agent reply"
	}
	if name == "file_write" {
		if actual["revision"] == initialRevision {
			return "FAIL", "agent update did not advance revision"
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "file_write", Arguments: map[string]any{"path": rel, "text": "E2E_STALE_WRITE", "revision": initialRevision}})
		if err != nil || result == nil || !result.IsError {
			return "FAIL", "real ToolHub did not reject stale revision"
		}
		actual, err = hubToolObject(ctx, session, "file_read", map[string]any{"path": rel})
		if err != nil || actual["text"] != want {
			return "FAIL", "stale write changed synthetic file"
		}
	}
	reason += "; independent file content verified; own artifact removed"
	if name == "file_write" {
		reason += "; stale revision independently rejected through real ToolHub"
	}
	return "PASS", reason
}

func hubToolObject(ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) (map[string]any, error) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || result == nil || result.IsError {
		return nil, fmt.Errorf("synthetic %s call failed", path.Base(name))
	}
	data, err := json.Marshal(result.StructuredContent)
	var out map[string]any
	if err != nil || json.Unmarshal(data, &out) != nil || out == nil {
		return nil, fmt.Errorf("synthetic tool result is not an object")
	}
	return out, nil
}
