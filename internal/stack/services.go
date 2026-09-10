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

// ServiceMCPConfig returns the upstream MCP definition for a self-service
// connector. GitLab is CLI-only and therefore intentionally has no MCP entry.
func ServiceMCPConfig(name string) (string, M, bool, error) {
	if !selfServiceNames[name] {
		return "", nil, false, fmt.Errorf("service %q is host-managed", name)
	}
	google := Settings{GoogleEmail: "${GOOGLE_EMAIL}", OAuthPort: 8000, Features: []string{"google"}}
	telegram := Settings{Features: []string{"telegram_user"}}
	var settings Settings
	switch name {
	case "google":
		settings = google
	case "google_write":
		settings = google
		settings.Features = append(settings.Features, "google_write")
	case "github", "slack", "atlassian":
		settings.Features = []string{name}
	case "telegram_user":
		settings = telegram
	case "telegram_write":
		settings = telegram
		settings.Features = append(settings.Features, "telegram_write")
	case "hh":
		settings.Features = []string{"workspace", "hh"}
	case "gitlab":
		return "", nil, true, nil
	default:
		return "", nil, false, fmt.Errorf("service %q is not available", name)
	}
	servers, ok := Config(settings)["mcp_servers"].(M)
	if !ok {
		return "", nil, false, fmt.Errorf("service %q has no MCP definition", name)
	}
	serverName := name
	if name == "google_write" || name == "telegram_write" {
		serverName = map[string]string{"google_write": "google", "telegram_write": "telegram_user"}[name]
	}
	if name == "hh" {
		serverName = "hub"
	}
	config, ok := servers[serverName].(M)
	if !ok {
		return "", nil, false, fmt.Errorf("service %q has no MCP definition", name)
	}
	return serverName, config, true, nil
}
