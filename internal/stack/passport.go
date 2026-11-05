package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/media"
	passportview "github.com/letya999/hermes-hub/internal/passportview"
)

// The published passport revision is the imperative authority for managed
// spaces. The legacy overlay below exists only for spaces without a published
// revision while the six-file cutover is in progress.

// ErrRevisionUnloadable marks the fail-closed class: a published-revision
// pointer exists but the revision cannot be decoded.
var ErrRevisionUnloadable = errors.New("published revision does not load")

func (s *Settings) applyPassport(dir string) error {
	eff, err := passportview.LoadEffective(dir)
	if err != nil {
		if passportRevision(dir) {
			return fmt.Errorf("%w: %w", ErrRevisionUnloadable, err)
		}
		return nil // no published revision — legacy files remain the authority
	}
	return s.applyEffective(eff)
}

// applyEffective carries one loaded revision's fields onto the settings the
// extension files supplied.
func (s *Settings) applyEffective(eff passportview.Effective) error {
	if s.Schema == 0 {
		s.Schema = 3 // a published revision implies the v3 vocabulary
	}
	if user := eff.Stack.Identity.User; user != "" {
		s.User = user
	}
	if org := eff.Stack.Identity.Organization; org != "" {
		s.Organization = org
	}
	if kind := eff.Stack.Identity.Kind; kind != "" {
		s.ScopeKind = kind
	}
	if eff.Stack.Identity.Infra != nil {
		s.Infra = eff.Stack.Identity.Infra
	}
	s.PassportConfig = &eff.Config
	if tz := eff.Config.Runtime.Timezone; tz != "" {
		s.Timezone = tz
	}
	if engine := eff.Config.Runtime.Context.Compaction; engine != "" {
		s.Context.Compact.Engine = engine
	}
	if len(eff.Config.Runtime.Context.Engines) > 0 {
		s.Context.Compact.Engines = slices.Clone(eff.Config.Runtime.Context.Engines)
	}
	if goals := eff.Config.Runtime.Goals; goals != (passportview.RuntimeGoals{}) {
		s.Goals = ContextGoals{Engine: goals.Engine, Judge: goals.Judge, MaxTurns: goals.MaxTurns}
	}
	if len(eff.Config.Runtime.Compression) > 0 {
		s.Compression = eff.Config.Runtime.Compression
	}
	if aux := eff.Config.Runtime.Auxiliary; aux != (passportview.RuntimeAuxiliary{}) {
		s.SummaryModel = aux.SummaryModel
		s.JudgeModel = aux.JudgeModel
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
	if p := eff.Config.Ports["knowledge-ui"]; p != 0 {
		s.UIPort = p
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
	// memory.backend is the imperative memory switch: honcho turns on the
	// Honcho contract; its URL stays a host-level extension. Under managed
	// mode s.Honcho is forbidden, so a managed space declaring honcho fails
	// validation honestly rather than silently diverging.
	if backend := eff.Config.Memory.Backend; backend != "" {
		s.Honcho = backend == "honcho"
	}
	s.MemoryRollupTime = eff.Config.Memory.NightlyRollup
	s.MemoryLeafRetentionDays = eff.Config.Memory.LeafRetentionDays
	s.MemoryLeafFoldBytes = eff.Config.Memory.LeafFoldBytes
	if p := eff.Config.Memory.Provider; p != nil {
		s.MemoryProvider = p.Adapter
		s.MemoryProviderURL = p.URL
		s.MemoryProviderTokenEnv = p.TokenEnv
	}
	if h := eff.Config.Hosting; h != nil {
		s.Hosting = HostingSettings{Slots: h.Slots, Image: h.Image, Runtime: h.Runtime}
	}
	if g := eff.Config.ImageGen; g != (media.ImageGen{}) {
		s.ImageGen = g
	}
	if def := eff.Models.Default; def != "" {
		s.Model = def
	}
	s.ModelURL = "http://model-relay:8318/v1"
	// The workspace boundary is imperative too: the mount plan and file
	// admission read the published writable/read_only/mounts verbatim.
	s.PassportWorkspace = &eff.Workspace
	// The sandbox bootstrap contract is owner-authored too: the goal text and
	// the retry counter ride the workspace passport into the runtime env.
	s.Workspace.Goal = eff.Workspace.Goal
	s.Workspace.BootstrapRevision = eff.Workspace.BootstrapRevision
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
	// Declared raw-MCP upstreams: the connectors map is the managed home of
	// the retired workspace mcp: surface — user_mcp/org_mcp toolset
	// admission plus the same governance section decide whether it renders.
	if len(eff.Tools.Connectors) > 0 {
		s.MCP = make(map[string]MCPServer, len(eff.Tools.Connectors))
		for name, c := range eff.Tools.Connectors {
			server := MCPServer{URL: c.URL, Command: c.Command, Args: c.Args, Env: c.Env, Headers: c.Headers, Auth: c.Auth, Timeout: c.Timeout}
			if c.Tools != nil {
				server.Tools = &MCPTools{Include: c.Tools.Include, Exclude: c.Tools.Exclude}
			}
			s.MCP[name] = server
		}
		s.PassportConnectors = true
	}
	if eff.Tools.Binding != nil {
		s.CapabilityProfileID = eff.Tools.Binding.ProfileID
		s.CapabilityGeneration = eff.Tools.Binding.Generation
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
	denied := map[string]bool{}
	for _, area := range []map[string]passportview.Toolset{t.Core, t.Extended} {
		for name, entry := range area {
			if entry.Via == "off" {
				denied[name] = true
			}
		}
	}
	for _, area := range []map[string]passportview.Toolset{t.Core, t.Extended} {
		for name, ts := range area {
			// The pinned platform family is not an operator toolset; the
			// legacy discovery pin it replaced is skipped the same way for
			// passports published before r5.
			if name == "platform" || name == "discovery" {
				continue
			}
			entry := ToolEntry{Via: ToolViaToolHub}
			name = strings.TrimPrefix(strings.TrimPrefix(name, "hub:"), "native:")
			switch {
			case ts.Via == "off":
				// An explicit deny projects as `via: off` — org narrowing
				// and self-deny consumers read it verbatim.
				entry.Via = ToolViaOff
			case ts.Via == "native":
				entry.Via = ToolViaNative
			case strings.HasPrefix(ts.Via, "mcp:") && mcpConnectors[name]:
				// Reviewed connector surfaces carry their own server name —
				// no s.MCP definition is needed to rebind them.
				entry.Via = ToolViaMCP
				entry.Server = strings.TrimPrefix(ts.Via, "mcp:")
			case ts.Via == "mcp-raw":
				// Uncurated raw surfaces (user_mcp/org_mcp): the toolset
				// admits the declared connectors map as its member set —
				// one mcp-raw selection per declared server so the
				// selector/governance paths keep naming each.
				for server := range t.Connectors {
					if !denied[server] {
						out[server] = ToolEntry{Via: ToolViaMCPRaw, Server: server}
					}
				}
				continue
			case ts.Via == "mcp" || strings.HasPrefix(ts.Via, "mcp:"):
				// Unreviewed connector entries bind to server definitions
				// the reviewed surface does not carry.
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
			out[name] = entry
		}
	}
	return out
}

// passportRegistryLayer turns the published models doc into the registry
// layer LoadModelRegistry merges (SPEC-0092 R7.3): every service becomes a
// provider hop with its own endpoint and credential contract — per-instance
// cliproxy included — each route's ladder names services and carries their
// cataloged model list, limits and reasoning support through. The hop's
// credential is the service's client key: proxy_access for cliproxy, the
// broker-key upstream for openai-compatible; oauth-session material lives
// inside the cliproxy instance and never reaches the relay. Switching is
// governed by the models.switch right at the relay PEP — the file carries
// no switch map.
func passportRegistryLayer(dir string) (modelRegistryYAML, error) {
	var layer modelRegistryYAML
	eff, err := passportview.LoadEffective(dir)
	if err != nil {
		return layer, fmt.Errorf("%w: %w", ErrRevisionUnloadable, err)
	}
	owner := eff.Stack.Identity.User
	var grantMap map[string]map[string]map[string]string
	if raw, err := os.ReadFile(filepath.Join(dir, "generated", "model-grants.json")); err == nil {
		_ = json.Unmarshal(raw, &grantMap)
	}
	layer.Providers = map[string]ModelProvider{}
	layer.Models = map[string]ModelEntry{}
	for name, svc := range eff.Models.Services {
		p := ModelProvider{URL: svc.Endpoint, Via: svc.Via}
		if svc.Auth.Upstream != nil {
			p.UpstreamKind = svc.Auth.Upstream.Kind
		}
		var keyRef *passportview.AuthRef
		if svc.Via == "cliproxy" {
			p.AllowPrivate = true
			keyRef = svc.Auth.ProxyAccess
		} else if svc.Auth.Upstream != nil && svc.Auth.Upstream.Kind == "broker-key" {
			keyRef = svc.Auth.Upstream
		}
		if keyRef != nil && keyRef.Kind == "broker-key" && owner != "" {
			// The hop credential resolves to the relay-bound grant id; a
			// service whose key was never enrolled for the relay keeps the
			// ref and fails closed at acquire time.
			grant := grantMap[name][keyRef.Ref]["relay"]
			if grant == "" {
				grant = keyRef.Ref
			}
			p.BrokerGrants = map[string]string{owner: grant}
			p.CredentialEnv = "MODEL_API_KEY"
		}
		layer.Providers[name] = p
	}
	for name, route := range eff.Models.Routes {
		entry := ModelEntry{}
		for _, hop := range route.Ladder {
			ref := ModelHopRef{Provider: hop}
			if svc, ok := eff.Models.Services[hop]; ok {
				ids := make([]string, 0, len(svc.Models))
				reasoning := false
				for _, m := range svc.Models {
					ids = append(ids, m.ID)
					if m.Reasoning != "" && m.Reasoning != "none" {
						reasoning = true
					}
				}
				ref.Models = ids
				ref.Reasoning = reasoning
				// cliproxy is personal by construction (broker-key
				// proxy_access); stale revisions predate that auth contract
				// and re-migration must still read them, so the hop does not
				// redeclare sharing — admission rides on the provider grant.
				if svc.Sharing != "" && svc.Via != "cliproxy" {
					ref.Sharing = svc.Sharing
				}
				for _, l := range svc.Limits {
					ref.Limits = append(ref.Limits, ModelLimit{
						Of:     l.Of,
						Amount: int64(l.Amount),
						Unit:   l.Unit,
						Per:    l.Per,
						Clock:  l.Clock,
						Scope:  svc.Sharing,
					})
				}
			}
			entry.Ladder = append(entry.Ladder, ref)
		}
		layer.Models[name] = entry
	}
	return layer, nil
}

// passportRevision reports whether the space carries a published-revision
// pointer — the boundary where legacy config files stop being the
// authority. The gate is the marker's presence, not its decodability: a
// malformed or dangling pointer still claims managed status, and the load
// path then fails closed rather than downgrading to legacy reads.
func passportRevision(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "generated", "passports", "current"))
	return err == nil && !info.IsDir()
}
