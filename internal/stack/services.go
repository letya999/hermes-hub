package stack

import (
	"fmt"
	"slices"
)

// ServiceInfo describes a connector that can be inspected from the owner chat.
// SelfService is deliberately narrower than Features: browser, gateway and
// host-bridge changes still belong in the host configuration.
type ServiceInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Requires    []string `json:"requires,omitempty"`
	Depends     []string `json:"depends_on,omitempty"`
	SelfService bool     `json:"self_service"`
}

var selfServiceNames = map[string]bool{
	"google":         true,
	"google_write":   true,
	"gitlab":         true,
	"github":         true,
	"slack":          true,
	"atlassian":      true,
	"telegram_user":  true,
	"telegram_write": true,
	"hh":             true,
}

func ServiceCatalog() []ServiceInfo {
	result := make([]ServiceInfo, 0, len(Features))
	for _, feature := range Features {
		requires := append([]string(nil), feature.Requires...)
		if feature.Name == "google" {
			requires = append(requires, "GOOGLE_EMAIL")
		}
		slices.Sort(requires)
		result = append(result, ServiceInfo{
			Name:        feature.Name,
			Description: feature.Scope,
			Requires:    requires,
			Depends:     serviceDependencies(feature.Name),
			SelfService: selfServiceNames[feature.Name],
		})
	}
	return result
}

func serviceDependencies(name string) []string {
	switch name {
	case "browser_act":
		return []string{"browser"}
	case "google_write":
		return []string{"google"}
	case "telegram_write":
		return []string{"telegram_user"}
	default:
		return nil
	}
}

func ServiceInfoByName(name string) (ServiceInfo, bool) {
	for _, service := range ServiceCatalog() {
		if service.Name == name {
			return service, true
		}
	}
	return ServiceInfo{}, false
}

// ServiceMCPConfig returns the MCP definition a self-service connector writes
// into the Hermes config. Upstream connectors (google, github, slack,
// atlassian, telegram_user and their write variants) are served through
// ToolHub and intentionally return no direct entry, like CLI-only GitLab.
// Only the platform-internal hub tool server is written for hh/workspace.
func ServiceMCPConfig(name string) (string, M, bool, error) {
	if !selfServiceNames[name] {
		return "", nil, false, fmt.Errorf("service %q is host-managed", name)
	}
	switch name {
	case "hh":
		servers, ok := Config(Settings{Features: []string{"workspace", "hh"}})["mcp_servers"].(M)
		if !ok {
			return "", nil, false, fmt.Errorf("service %q has no MCP definition", name)
		}
		config, ok := servers["hub"].(M)
		if !ok {
			return "", nil, false, fmt.Errorf("service %q has no MCP definition", name)
		}
		return "hub", config, true, nil
	case "google", "google_write", "github", "slack", "atlassian", "telegram_user", "telegram_write", "gitlab":
		return "", nil, true, nil
	default:
		return "", nil, false, fmt.Errorf("service %q is not available", name)
	}
}
