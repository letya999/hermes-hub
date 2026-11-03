package stack

import (
	"slices"
	"strings"

	passportview "github.com/letya999/hermes-hub/internal/passportview"
)

// The published passport revision is the imperative authority (SPEC-0092):
// where it speaks — identity, runtime config, model routes, workspace
// boundaries, the toolset surface — it overrides the legacy editable files.
// Fields the six-doc contract does not govern (image_gen, web backends,
// compression, capability wiring) keep coming from agent.yaml, so a managed
// space may slim its legacy files to exactly those keys.
//
// applyPassport is an overlay, not a replace: a space without a published
// revision keeps the legacy authority unchanged.

func (s *Settings) applyPassport(dir string) error {
	eff, err := passportview.LoadEffective(dir)
	if err != nil {
		return nil // no published revision — legacy files remain the authority
	}
	if s.Schema == 0 {
		s.Schema = 3 // a published revision implies the v3 vocabulary
	}
	if user := eff.Stack.Identity.User; user != "" {
		s.User = user
	}
	if org := eff.Stack.Identity.Organization; org != "" {
		s.Organization = org
	}
	if tz := eff.Config.Runtime.Timezone; tz != "" {
		s.Timezone = tz
	}
	if p := eff.Config.Ports["oauth-callback"]; p != 0 {
		s.OAuthPort = p
	}
	if p := eff.Config.Ports["browser-vnc"]; p != 0 {
		s.BrowserPort = p
	}
	if p := eff.Config.Ports["slack-events"]; p != 0 {
		s.SlackEventsPort = p
	}
	// Channels are the ingress transports; a named channel is its own ingress.
	if len(eff.Config.Channels) > 0 {
		ingress := make([]string, 0, len(eff.Config.Channels))
		for name := range eff.Config.Channels {
			ingress = append(ingress, name)
		}
		slices.Sort(ingress)
		s.Ingress = ingress
	}
	// memory.backend is informational under managed mode: the memory toolset
	// comes through tools, and s.Memory/s.Honcho remain the legacy-extension
	// flags managed validation forbids.
	if def := eff.Models.Default; def != "" {
		s.Model = def
	}
	if _, ok := eff.Stack.Services["model-relay"]; ok {
		s.ModelURL = "http://model-relay:8318/v1"
	}
	// Workspace mounts: the imperative mount contract, source and mode intact.
	if len(eff.Workspace.Mounts) > 0 {
		mounts := make([]Mount, 0, len(eff.Workspace.Mounts))
		for target, m := range eff.Workspace.Mounts {
			mode := m.Mode
			if mode == "" {
				mode = "rw"
			}
			mounts = append(mounts, Mount{From: m.Source, To: target, Mode: mode})
		}
		slices.SortFunc(mounts, func(a, b Mount) int { return strings.Compare(a.To, b.To) })
		s.Workspace.Mounts = mounts
	}
	// The services map becomes the imperative topology (R3.2): the compose
	// plan renders exactly the declared per-space instances. A per-space
	// cliproxy instance still selects the legacy dedicated emission branch;
	// shared stays on the infra stack.
	s.PassportServices = map[string]string{}
	for instance, spec := range eff.Stack.Services {
		s.PassportServices[instance] = spec.Mode
	}
	if !s.RendersInfra() {
		for instance, mode := range s.PassportServices {
			if serviceKind(instance) == "cliproxy" {
				if mode == "per-space" {
					s.Cliproxy = "dedicated"
				} else {
					s.Cliproxy = "shared"
				}
			}
		}
	}
	// The toolset surface rebuilds the tools map: core and extended both
	// admit, the platform family is pinned and never a legacy entry.
	if tools := passportTools(eff.Tools); tools != nil {
		s.Tools = tools
	}
	return nil
}

// passportTools projects the tools doc back to the tools map each compose
// consumer reads: a toolset name is its entry, via picks the backend, and
// selectors/access/scope fold into the entry fields.
func passportTools(t passportview.Tools) map[string]ToolEntry {
	if len(t.Core)+len(t.Extended) == 0 {
		return nil
	}
	out := map[string]ToolEntry{}
	for _, area := range []map[string]passportview.Toolset{t.Core, t.Extended} {
		for name, ts := range area {
			// The pinned platform family is not an operator toolset; the
			// legacy discovery pin it replaced is skipped the same way for
			// passports published before r5.
			if name == "platform" || name == "discovery" {
				continue
			}
			entry := ToolEntry{Via: ToolViaToolHub}
			switch {
			case ts.Via == "native":
				entry.Via = ToolViaNative
			case ts.Via == "mcp" || ts.Via == "mcp-raw" || strings.HasPrefix(ts.Via, "mcp:"):
				// Raw-MCP and connector entries bind to server definitions
				// (s.MCP) the passport contract does not carry — one toolset
				// cannot rebuild per-server entries, so the slim operator
				// file keeps them as extensions.
				continue
			}
			if ts.Access == "read" {
				entry.Access = ToolAccessRO
			}
			if ts.Tools != nil {
				entry.Only = ts.Tools.Include
				entry.Except = ts.Tools.Exclude
			}
			if ts.Scope != nil && len(ts.Scope.Paths) > 0 {
				// Passport scope uses absolute container paths; the Settings
				// entry keeps the legacy relative-prefix vocabulary.
				paths := make([]string, 0, len(ts.Scope.Paths))
				for _, p := range ts.Scope.Paths {
					paths = append(paths, strings.TrimPrefix(p, "/"))
				}
				entry.Paths = paths
			}
			out[strings.TrimPrefix(strings.TrimPrefix(name, "hub:"), "native:")] = entry
		}
	}
	return out
}

// passportRegistryLayer turns the published models doc into the registry
// layer LoadModelRegistry merges — providers the file registry does not
// declare stay builtin (cliproxy), openai-compatible services carry their
// endpoint, and the ladder's model ids come off each service's cataloged
// list. The grants doc answers the switch policy: models.switch allowed
// means catalog, denied means none — the RBAC right is the switch map.
func passportRegistryLayer(dir string) modelRegistryYAML {
	var layer modelRegistryYAML
	eff, err := passportview.LoadEffective(dir)
	if err != nil {
		return layer
	}
	layer.Providers = map[string]ModelProvider{}
	layer.Models = map[string]ModelEntry{}
	for name, svc := range eff.Models.Services {
		if svc.Via != "openai-compatible" {
			continue // cliproxy stays the builtin provider
		}
		p := ModelProvider{URL: svc.Endpoint}
		if svc.Auth.Upstream != nil && svc.Auth.Upstream.Kind == "broker-key" {
			p.CredentialEnv = svc.Auth.Upstream.Ref
		}
		layer.Providers[name] = p
	}
	for name, route := range eff.Models.Routes {
		entry := ModelEntry{}
		for _, hop := range route.Ladder {
			ref := ModelHopRef{Provider: hop}
			if svc, ok := eff.Models.Services[hop]; ok {
				ids := make([]string, 0, len(svc.Models))
				for _, m := range svc.Models {
					ids = append(ids, m.ID)
				}
				ref.Models = ids
				// cliproxy is personal by construction (broker-key
				// proxy_access); the hop does not redeclare it, and the
				// registry validator has no cliproxy provider entry to
				// check broker_grants against.
				if svc.Sharing != "" && svc.Via != "cliproxy" {
					ref.Sharing = svc.Sharing
				}
			}
			entry.Ladder = append(entry.Ladder, ref)
		}
		layer.Models[name] = entry
	}
	if user := eff.Stack.Identity.User; user != "" && eff.Grants.Allow != nil {
		level := "none"
		if slices.Contains(eff.Grants.Allow["models"], "switch") {
			level = "catalog"
		}
		layer.Switch = map[string]string{user: level}
	}
	return layer
}

// passportRevision reports whether the space carries a published revision —
// the boundary where legacy config files stop being the authority.
func passportRevision(dir string) bool {
	_, err := passportview.Current(dir)
	return err == nil
}
