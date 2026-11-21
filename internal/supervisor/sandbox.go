package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"

	"github.com/letya999/hermes-hub/internal/stack"
)

// Docker embedded DNS is outside runsc's network stack. Only pin aliases on
// the already-verified internal space network; never add a public resolver.
func (m *Manager) sandboxRelayHosts(ctx context.Context, settings stack.Settings, dir string) ([]string, error) {
	network := managedAgentNetworkName(settings.User, m.environment())
	body, err := m.command(ctx, "ps", "--filter", "network="+network, "--format", "{{.ID}}")
	ids := strings.Fields(string(body))
	if err != nil || len(body) > 4096 || len(ids) > 16 {
		return nil, errors.New("sandbox relay discovery unavailable")
	}
	wanted := []string{"toolhub", "model-relay"}
	if len(stack.RuntimeEgressEnvironment(settings, dir)) != 0 {
		wanted = append(wanted, "egress-relay")
	}
	hosts := map[string]string{}
	for _, id := range ids {
		body, err := m.command(ctx, "inspect", "--format", `{{json (index .NetworkSettings.Networks "`+network+`")}}`, id)
		var endpoint struct {
			IPAddress string
			Aliases   []string
		}
		if err != nil || len(body) > 16*1024 || json.Unmarshal(body, &endpoint) != nil {
			return nil, errors.New("sandbox relay endpoint unavailable")
		}
		for _, alias := range endpoint.Aliases {
			for _, name := range wanted {
				if alias != name {
					continue
				}
				ip, err := netip.ParseAddr(endpoint.IPAddress)
				if err != nil || !ip.IsPrivate() || !ip.Is4() || hosts[name] != "" && hosts[name] != ip.String() {
					return nil, errors.New("sandbox relay alias is invalid or ambiguous")
				}
				hosts[name] = ip.String()
			}
		}
	}
	var args []string
	for _, name := range wanted {
		if hosts[name] == "" {
			return nil, errors.New("sandbox relay alias is missing")
		}
		args = append(args, "--add-host", name+":"+hosts[name])
	}
	return args, nil
}
