package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// connectionsCommand answers /connections: the operator-facing connection
// catalog plus the ToolHub tool inventory projected for this principal —
// builtin agent tools, bounded CLIs, containerized and remote MCP servers.
func (g *Gateway) connectionsCommand(user User, sender int64) string {
	answer := connectionList(user)
	if g.secrets == nil || g.secrets.Registry == nil {
		// Broker-approve mode keeps the ToolHub store inside the ToolHub
		// container; ask it over the control endpoint instead.
		if lines, ok := g.toolHubConnectorsHTTP(user); ok {
			return answer + lines
		}
		return answer + "\nКоннекторы ToolHub: реестр недоступен."
	}
	tools, err := g.secrets.Registry.ListProjectedTools(user.envelope(sender))
	if err != nil {
		return answer + "\nКоннекторы ToolHub: не удалось получить список."
	}
	if len(tools) == 0 {
		return answer + "\nКоннекторы ToolHub: нет."
	}
	kind := map[string]string{}
	for _, t := range tools {
		if _, seen := kind[t.DefinitionID]; seen {
			continue
		}
		label := string(t.Tool.Effect)
		if def, derr := g.secrets.Registry.Definition(t.DefinitionID, t.Version); derr == nil {
			label = string(def.Transport)
		}
		kind[t.DefinitionID] = label
	}
	lines := []string{"", "Коннекторы ToolHub:"}
	seen := map[string]bool{}
	for _, t := range tools {
		if seen[t.DefinitionID] {
			continue
		}
		seen[t.DefinitionID] = true
		var names []string
		for _, o := range tools {
			if o.DefinitionID == t.DefinitionID {
				names = append(names, o.Name)
			}
		}
		lines = append(lines, "- "+connectorLine(t.DefinitionID, kind[t.DefinitionID], names))
	}
	return answer + strings.Join(lines, "\n")
}

// connectorLine renders one ToolHub connector compactly: the hashed projected
// names are stripped back to their readable form and long inventories collapse
// to a count plus a few examples.
func connectorLine(definitionID, transport string, names []string) string {
	clean := make([]string, 0, len(names))
	for _, name := range names {
		clean = append(clean, connectorToolName(definitionID, name))
	}
	label := definitionID
	if transport != "" {
		label += " [" + transport + "]"
	}
	if len(clean) <= 6 {
		return label + " — " + strconv.Itoa(len(clean)) + " инструментов: " + strings.Join(clean, ", ")
	}
	return label + " — " + strconv.Itoa(len(clean)) + " инструментов: " + strings.Join(clean[:3], ", ") + ", …"
}

// connectorToolName strips the "hub-<definition>-" prefix and the trailing
// dedup hash that projection adds to every tool name.
func connectorToolName(definitionID, name string) string {
	name = strings.TrimPrefix(name, "hub-"+definitionID+"-")
	if i := strings.LastIndex(name, "-"); i > 0 && len(name)-i == 9 {
		if _, err := strconv.ParseUint(name[i+1:], 16, 32); err == nil {
			name = name[:i]
		}
	}
	return name
}

// toolHubConnectorsHTTP renders the ToolHub inventory through
// HUB_TOOLHUB_URL/v1/connectors when the local registry file is unreachable
// (broker-approve mode never mounts the store into the gateway). A sibling
// token from ControlTokensFile pins the caller's own principal; otherwise the
// supervisor/owner bearer goes with X-Hub-Principal, matching ToolHub's
// control-token contract. The header never substitutes for the token.
func (g *Gateway) toolHubConnectorsHTTP(user User) (string, bool) {
	base := strings.TrimRight(strings.TrimSpace(g.config.ToolHubURL), "/")
	if base == "" {
		return "", false
	}
	token, header := g.toolHubCredential(user.ID)
	if token == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/connectors", nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if header != "" {
		req.Header.Set("X-Hub-Principal", header)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var payload struct {
		Connectors []struct {
			DefinitionID string   `json:"definition_id"`
			Transport    string   `json:"transport"`
			Tools        []string `json:"tools"`
		} `json:"connectors"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", false
	}
	if len(payload.Connectors) == 0 {
		return "\nКоннекторы ToolHub: нет.", true
	}
	lines := []string{"", "Коннекторы ToolHub:"}
	for _, c := range payload.Connectors {
		label := c.Transport
		if label == "" {
			label = "mcp"
		}
		lines = append(lines, "- "+connectorLine(c.DefinitionID, label, c.Tools))
	}
	return strings.Join(lines, "\n"), true
}

// toolHubCredential picks the bearer for a /v1/connectors call. A sibling
// token enrolled in ControlTokensFile pins exactly this principal. Without
// one, only supervised mode falls back to RuntimeAuth — there it is the
// supervisor bearer, which ToolHub accepts as a control token selecting any
// enrolled principal via X-Hub-Principal. In unsupervised mode RuntimeAuth is
// the owner's own token, which pins the owner regardless of the header, so
// pairing it with a sibling's principal would silently render the wrong
// principal's inventory.
func (g *Gateway) toolHubCredential(userID string) (token, principalHeader string) {
	if path := strings.TrimSpace(g.config.ControlTokensFile); path != "" {
		if body, err := os.ReadFile(path); err == nil {
			var entries map[string]identity.Envelope
			if json.Unmarshal(body, &entries) == nil {
				for candidate, envelope := range entries {
					if envelope.PrincipalID == userID && len(candidate) >= 32 {
						return candidate, ""
					}
				}
			}
		}
	}
	if g.config.Supervised {
		if auth := strings.TrimSpace(g.config.RuntimeAuth); auth != "" {
			return auth, userID
		}
	}
	return "", ""
}

// toolHubJobDone notifies ToolHub that a job reached a terminal status so
// task-tier bounded-cli cells keyed by that job are released. Best-effort:
// the caller already treats the job as done and the controller's own ladder
// still reaps cells on idle TTL.
func (g *Gateway) toolHubJobDone(jobID, principalID string) {
	base := strings.TrimRight(strings.TrimSpace(g.config.ToolHubURL), "/")
	if base == "" {
		return
	}
	token, header := g.toolHubCredential(principalID)
	if token == "" {
		return
	}
	body, err := json.Marshal(map[string]string{"job_id": jobID})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/cli-jobs/release", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if header != "" {
		req.Header.Set("X-Hub-Principal", header)
	}
	if jobID != "" {
		req.Header.Set("X-Hub-Job-ID", jobID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

func connectionList(user User) string {
	lines := []string{"Подключения:"}
	for _, service := range stack.ServiceCatalog() {
		if contains(user.PolicyDisabled, service.Name) {
			lines = append(lines, service.Name+" — policy-disabled")
			continue
		}
		if !contains(user.Features, service.Name) {
			if service.SelfService {
				lines = append(lines, service.Name+" — доступен для включения; env: "+strings.Join(service.Requires, ", "))
			} else {
				lines = append(lines, service.Name+" — host-managed")
			}
			continue
		}
		missing := []string{}
		for _, key := range service.Requires {
			if !user.ConfiguredEnv[key] {
				missing = append(missing, key)
			}
		}
		if len(missing) == 0 {
			lines = append(lines, service.Name+" — готово")
		} else {
			lines = append(lines, service.Name+" — нужны настройки: "+strings.Join(missing, ", "))
		}
	}
	return strings.Join(lines, "\n")
}
