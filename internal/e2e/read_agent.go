package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/communication"
	"net/http"
	"sort"
	"strings"
)

func readAgentCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, mode, name string) (string, string) {
	if !admittedHubTool(ctx, client, config, user, name) {
		return "BLOCKED", "owner ToolHub does not admit this read tool"
	}
	if status, reason := mockServerCase(ctx, client, config, user, "text"); status != "PASS" {
		return status, reason
	}
	session, err := ownerHubSession(ctx, client, config, user)
	if err != nil {
		return "BLOCKED", "owner ToolHub session unavailable"
	}
	defer session.Close()
	arguments := map[string]any{}
	if name == "discover" {
		arguments["query"] = "filesystem"
	}
	if name == "required_credentials" {
		installation, err := hubToolObject(ctx, session, "status", map[string]any{})
		id, _ := installation["onboarding_id"].(string)
		definition, _ := installation["definition_id"].(string)
		if err != nil || id == "" || (!strings.HasPrefix(definition, "cli-e2e-") && !strings.HasPrefix(definition, "remote-e2e-")) {
			return "BLOCKED", "own credential-free installation required"
		}
		arguments["onboarding_id"] = id
	}
	actual, err := hubToolObject(ctx, session, name, arguments)
	if err != nil || ((name == "settings_get" || name == "service_catalog") && actual["secret_values_included"] != false) {
		return "FAIL", "independent read response unavailable or secret-free contract absent"
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "FAIL", "random read identity unavailable"
	}
	marker := "E2E " + hex.EncodeToString(nonce)
	value, prompt := "", ""
	switch name {
	case "discover":
		candidates, ok := actual["candidates"].([]any)
		if !ok || len(candidates) == 0 {
			return "FAIL", "independent reviewed discovery candidates unavailable"
		}
		value, prompt = fmt.Sprint(len(candidates)), "Use ToolHub discover with query filesystem. Count the entries in candidates. Do not prepare, install or connect anything."
	case "service_catalog":
		services, ok := actual["services"].([]any)
		if !ok {
			return "FAIL", "independent service catalog is invalid"
		}
		value = fmt.Sprint(len(services))
		prompt = "Use service_catalog. Count every entry in its services array."
	case "settings_get":
		raw, ok := actual["manageable_keys"].([]any)
		if !ok {
			return "FAIL", "independent manageable settings list is invalid"
		}
		keys := []string{}
		for _, item := range raw {
			key, ok := item.(string)
			if !ok {
				return "FAIL", "invalid manageable key"
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		data, _ := json.Marshal(keys)
		value = string(data)
		prompt = "Use settings_get with no key. Sort manageable_keys alphabetically and serialize it as a compact JSON array."
	case "status":
		phase, ok := actual["phase"].(string)
		id, valid := actual["onboarding_id"].(string)
		definition, _ := actual["definition_id"].(string)
		if !ok || !valid || phase == "" || id == "" || (!strings.HasPrefix(definition, "cli-e2e-") && !strings.HasPrefix(definition, "remote-e2e-")) {
			return "BLOCKED", "own installation status required"
		}
		value, prompt = phase+" "+id, "Use ToolHub status with no arguments. Return its phase, one space, then onboarding_id."
	case "required_credentials":
		credentials, ok := actual["credentials"].([]any)
		if !ok || len(credentials) != 0 {
			return "BLOCKED", "credential-free own installation required; no account requested"
		}
		value = "0"
		prompt = fmt.Sprintf("Use ToolHub required_credentials only with onboarding_id %s. Count the entries in credentials. Do not request, rotate or return credentials.", arguments["onboarding_id"])
	case "diagnostics":
		workloads, ok := actual["workloads"].([]any)
		if !ok {
			return "FAIL", "own diagnostics workload list unavailable"
		}
		value, prompt = fmt.Sprint(len(workloads)), "Use ToolHub diagnostics with no arguments. Count the entries in workloads. Do not return logs or identity fields."
	default:
		return "BLOCKED", "no read oracle for this tool"
	}
	prompt += " Return exactly " + marker + " followed by one space and the result, no explanation. Do not mutate anything."
	run := mockPromptCase
	if mode == "telegram-server" {
		run = serverPromptCase
	}
	status, reason := run(ctx, client, config, user, "text", prompt, marker+" "+value)
	return status, reason + "; agent answer compared with independent owner-bound read"
}
