package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DeepWiki's official public endpoint needs no login. Restrict the installation
// to its read-only structure tool and public repositories; never send workspace data.
func mcpAgentCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, mode string) (status, reason string) {
	return installationAgentCase(ctx, client, config, user, mode, false)
}

func installationAgentCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, mode string, cli bool) (status, reason string) {
	if mode == "telegram-server" {
		if status, reason := mockServerCase(ctx, client, config, user, "text"); status != "PASS" {
			return status, reason + "; installation not submitted"
		}
	}
	for _, operation := range []string{"prepare_source", "status", "confirm", "enable", "disable", "revoke", "remove"} {
		if !admittedHubTool(ctx, client, config, user, operation) {
			return "BLOCKED", "owner ToolHub installation control operations unavailable"
		}
	}
	longClient := *client
	longClient.Timeout = 75 * time.Second // Public MCP execution is bounded at 60 seconds by ToolHub.
	session, err := ownerHubSession(ctx, &longClient, config, user)
	if err != nil {
		return "BLOCKED", "owner ToolHub session unavailable"
	}
	defer session.Close()
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "FAIL", "random installation identity unavailable"
	}
	name := "remote-e2e-dw-" + hex.EncodeToString(nonce)
	variable, prefix := "HUB_E2E_REMOTE_MCP_NAME", "remote-e2e-"
	if cli {
		variable, prefix = "HUB_E2E_CLI_NAME", "cli-e2e-"
		name = prefix + hex.EncodeToString(nonce)
	}
	if configured := os.Getenv(variable); configured != "" {
		if !identity.ValidID(configured) || !strings.HasPrefix(configured, prefix) || len(configured) > 40 {
			return "FAIL", "invalid operator-approved MCP fixture name"
		}
		name = configured
	}
	marker := "E2E " + hex.EncodeToString(nonce)
	run := mockPromptCase
	if mode == "telegram-server" {
		run = serverPromptCase
	}
	prompt := fmt.Sprintf("Acceptance test explicitly authorized by the owner. Use ToolHub prepare_source remote_url https://mcp.deepwiki.com/mcp, name %s, tools only [read_wiki_structure], request_key %s. It needs no credentials. Confirm its returned onboarding_id and nonce, then enable that onboarding. Do not touch existing integrations. Return exactly %s only after phase enabled. If denied, report the error instead of claiming success.", name, name, marker)
	if cli {
		prompt = fmt.Sprintf(`Acceptance test explicitly authorized by the owner. Call prepare_source with request_key %s and cli exactly {"name":%q,"command":"bash","args":["-c","uv tool install --python 3.13 ruff==0.11.13 >/dev/null 2>&1 && ruff --version"],"install_packages":true,"lifecycle":"binding","workspace_scope":"none","scope":"owner","egress":["pypi.org","files.pythonhosted.org","github.com","release-assets.githubusercontent.com"],"tools":[{"name":"version","description":"Install pinned Ruff in the private cell home and report its version","effect":"write"}]}. Confirm returned onboarding_id and nonce, then enable it. Do not touch existing integrations. Return exactly %s after phase enabled; report denial instead of claiming success.`, name, name, marker)
		prompt += " Omit source and binary entirely; do not supply nulls or empty strings. The operator already allowlisted bash and package installation; the literal argv runs only in a private sandbox cell. Invoke the actual prepare_source tool with this object; do not infer a denial from its description."
	}
	status, reason = run(ctx, client, config, user, "text", prompt, marker)
	// Read state even after a false claim, but never prepare on behalf of the
	// agent: doing that would manufacture the installation evidence.
	actual, err := hubToolObject(ctx, session, "status", map[string]any{})
	if err != nil || actual["definition_id"] != name {
		if status != "PASS" {
			return status, reason + "; independent installation absent; no automatic retry"
		}
		return "FAIL", "agent installation absent from independent owner ToolHub status; no automatic retry"
	}
	id, _ := actual["onboarding_id"].(string)
	if id == "" {
		return "FAIL", "installation identity absent"
	}
	revoked := false
	defer func() {
		if revoked {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := hubToolObject(cleanup, session, "revoke", map[string]any{"onboarding_id": id}); err != nil {
			status, reason = "FAIL", "own MCP installation revocation failed"
		}
	}()
	if status != "PASS" {
		return status, reason
	}
	if actual["phase"] != toolhub.PhaseEnabled || (!cli && actual["repository"] != "https://mcp.deepwiki.com/mcp") {
		return "FAIL", "independent installation phase or endpoint differs"
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return "FAIL", "installed MCP projection unavailable"
	}
	projected := toolhub.ProjectedToolName(name, "1.0.0", "read_wiki_structure")
	arguments := map[string]any{"repoName": "golang/go"}
	if cli {
		projected, arguments = toolhub.ProjectedToolName(name, "1.0.0", "version"), map[string]any{}
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name == projected {
			found = true
		}
	}
	if !found {
		return "FAIL", "enabled MCP tool absent from actual tools/list"
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: projected, Arguments: arguments})
	if err != nil {
		return "FAIL", fmt.Sprintf("installed public MCP call failed with RPC or transport error (%T)", err)
	}
	if result == nil || result.IsError || len(result.Content) == 0 {
		return "FAIL", "installed public MCP call returned a backend error or empty content"
	}
	answer, err := installationAnswer(result, cli)
	if err != nil {
		return "FAIL", err.Error()
	}
	invoke := fmt.Sprintf("Use the installed tool %s with repoName golang/go. Return the first top-level page title in its returned Markdown list, without the bullet or numeric prefix. Do not install or change anything.", projected)
	if cli {
		invoke = fmt.Sprintf("Use the installed tool %s with no arguments. Return its exact version output. Do not install other packages or change other integrations.", projected)
	}
	status, reason = run(ctx, client, config, user, "text", invoke+" Return exactly "+marker+" followed by one space and the result, no explanation.", marker+" "+answer)
	if status != "PASS" {
		return status, reason + "; installed tool agent invocation not verified"
	}
	for _, stage := range []struct{ operation, phase string }{{"disable", toolhub.PhaseDisabled}, {"enable", toolhub.PhaseEnabled}, {"revoke", toolhub.PhaseRevoked}, {"remove", toolhub.PhaseRemoved}} {
		prompt := fmt.Sprintf("Call ToolHub %s only with onboarding_id %s, the synthetic installation created for this acceptance test. Do not touch other integrations. Return exactly %s only after the operation succeeds; otherwise report the error.", stage.operation, id, marker)
		status, reason = run(ctx, client, config, user, "text", prompt, marker)
		if status != "PASS" {
			return status, reason + "; installation lifecycle stage " + stage.operation + " failed"
		}
		state, err := hubToolObject(ctx, session, "status", map[string]any{"onboarding_id": id})
		if err != nil || state["onboarding_id"] != id || state["definition_id"] != name || state["phase"] != stage.phase {
			return "FAIL", "independent installation lifecycle state differs after " + stage.operation
		}
		result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: projected, Arguments: arguments})
		if stage.phase == toolhub.PhaseEnabled {
			if err != nil || result == nil || result.IsError {
				return "FAIL", "reenabled installation remains unavailable"
			}
			if after, err := installationAnswer(result, cli); err != nil || after != answer {
				return "FAIL", "reenabled installation returned a different result"
			}
		} else if !revocationDenied(result, err, projected) {
			return "FAIL", "disabled/revoked/removed tool did not explicitly deny an open-session call"
		}
		if stage.phase == toolhub.PhaseRevoked || stage.phase == toolhub.PhaseRemoved {
			revoked = true
		}
	}
	if cli {
		return "PASS", reason + "; agent CLI install/invocation and disable/reenable/revoke/remove independently verified; pinned version and open-session denials checked"
	}
	return "PASS", reason + "; agent installation/invocation and disable/reenable/revoke/remove independently verified; public result and open-session denials checked"
}

func installationAnswer(result *mcp.CallToolResult, cli bool) (string, error) {
	output := ""
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			output += text.Text
		}
	}
	if cli {
		if strings.TrimSpace(output) != "ruff 0.11.13" {
			return "", errors.New("pinned CLI installation and execution not proven")
		}
		return "ruff 0.11.13", nil
	}
	if !strings.HasPrefix(output, "Available pages for golang/go:\n") {
		return "", errors.New("public MCP structure has an unexpected repository or format")
	}
	title := regexp.MustCompile(`(?m)^- [0-9]+ ([^\r\n]+)$`).FindStringSubmatch(output)
	if len(title) != 2 || strings.TrimSpace(title[1]) == "" {
		return "", errors.New("public MCP structure has no top-level page title")
	}
	return title[1], nil
}

func revocationDenied(result *mcp.CallToolResult, err error, tool string) bool {
	if err == nil {
		return result != nil && result.IsError
	}
	var rpc *jsonrpc.Error
	return errors.As(err, &rpc) && rpc.Code == jsonrpc.CodeInvalidParams && rpc.Message == fmt.Sprintf("unknown tool %q", tool)
}
