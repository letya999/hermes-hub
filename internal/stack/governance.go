package stack

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// This file applies the hub-owned tool-governance document (issue 139) to one
// space's settings at load time. The document lives beside the shared ToolHub
// registry — governance.json in the infra space's runtime/toolhub directory —
// and is evaluated with the space's own principal and organization. Outcomes:
//
//   - explicit wishes (a tools entry, an mcp: declaration) that policy denies
//     fail the whole read — the space cannot render a surface policy forbids;
//   - implicit surfaces (the unmanaged default toolset list, the hub executor
//     server) are filtered silently — they are platform emissions, not wishes;
//   - cap:read on an executability boundary (toolhub families, mcp-raw) forces
//     the read-only rendering those backends already honor; on a native
//     toolset it is an error because upstream grants toolsets whole;
//   - an absent document seeds the restrictive default posture, preserving —
//     as explicit recorded user-scope allows — only the deny-default sections
//     the space already selected by name. user_mcp is never preserved: it
//     stays denied until a host grant unlocks it.
//
// The loaded decision set rides on the Settings value so render-time
// consumers (Config, disabledToolsets, rawMCPServers, readOnlyCapabilities)
// enforce the same answers without re-reading the file.

type toolGovernance struct {
	path      string
	revision  uint64
	decisions map[string]api.PolicyDecision
	forceRO   map[string]bool
}

// governanceDocPath resolves the shared governance document: the
// HUB_TOOL_GOVERNANCE pin wins; the infra space always resolves its own
// runtime dir (the seed target and document authority); a secondary space
// reaches the infra sibling's copy first, then its own leftover doc, then
// any documented sibling, falling back to the space-local path as the seed
// target. Alphabetical sibling order never outranks the infra marker — a
// stale doc on a secondary space must not shadow the shared policy.
func governanceDocPath(spaceDir string) string {
	if pinned := os.Getenv("HUB_TOOL_GOVERNANCE"); pinned != "" {
		return pinned
	}
	own := filepath.Join(spaceDir, "runtime", "toolhub", "governance.json")
	if spaceRendersInfra(spaceDir) {
		return own
	}
	parent := filepath.Dir(spaceDir)
	if filepath.Base(parent) == "spaces" {
		if siblings, err := os.ReadDir(parent); err == nil {
			names := []string{}
			for _, sibling := range siblings {
				if sibling.IsDir() {
					names = append(names, sibling.Name())
				}
			}
			slices.Sort(names)
			fallback := ""
			for _, name := range names {
				if name == filepath.Base(spaceDir) {
					continue
				}
				dir := filepath.Join(parent, name)
				candidate := filepath.Join(dir, "runtime", "toolhub", "governance.json")
				if _, err := os.Stat(candidate); err != nil {
					continue
				}
				if spaceRendersInfra(dir) {
					return candidate
				}
				if fallback == "" {
					fallback = candidate
				}
			}
			if _, err := os.Stat(own); err == nil {
				return own
			}
			if fallback != "" {
				return fallback
			}
		}
	}
	return own
}

// InfraSpaceDir resolves which space owns the shared control plane for a
// spaces/ layout: the caller itself when it renders infra, else the first
// sibling that does, else the caller. Host-side callers (hubctl skill git)
// must present the infra-owner's credentials to the shared controller.
func InfraSpaceDir(dir string) string {
	if spaceRendersInfra(dir) {
		return dir
	}
	parent := filepath.Dir(dir)
	if filepath.Base(parent) != "spaces" {
		return dir
	}
	siblings, err := os.ReadDir(parent)
	if err != nil {
		return dir
	}
	names := []string{}
	for _, sibling := range siblings {
		if sibling.IsDir() {
			names = append(names, sibling.Name())
		}
	}
	slices.Sort(names)
	for _, name := range names {
		if name != filepath.Base(dir) && spaceRendersInfra(filepath.Join(parent, name)) {
			return filepath.Join(parent, name)
		}
	}
	return dir
}

// spaceRendersInfra peeks at a space's own files for the infra flag without
// running Read — full settings load would recurse into governance
// resolution. The flag lives in agent.yaml (split layout) or settings.yaml
// (legacy single file); absence means the space renders the shared control
// plane — Settings.RendersInfra semantics.
func spaceRendersInfra(dir string) bool {
	for _, name := range []string{"agent.yaml", "settings.yaml"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var probe struct {
			Infra *bool `yaml:"infra"`
		}
		if err := yaml.Unmarshal(body, &probe); err != nil {
			continue
		}
		return probe.Infra == nil || *probe.Infra
	}
	return false
}

// sectionForEntry maps an enabled tools entry to its governance section.
// mcp-raw targets a user-declared server on a personal space (user_mcp) or
// the merged organization catalog (org_mcp).
func (s Settings) sectionForEntry(name string, e ToolEntry) string {
	switch e.Via {
	case ToolViaNative:
		return "native:" + name
	case ToolViaToolHub:
		if capability, ok := toolhubCapabilityID[name]; ok {
			return "hub:" + capability
		}
		return "hub:" + name
	case ToolViaMCP:
		return "hub:" + name
	case ToolViaMCPRaw:
		if s.OrgScoped() {
			return api.SectionOrgMCP
		}
		return api.SectionUserMCP
	default:
		return ""
	}
}

// implicitSections lists the platform-emitted sections not selected by a
// tools entry: the unmanaged default toolset list and the hub executor
// server. Denied sections are filtered out at render; no error is raised
// because nothing was explicitly requested.
func implicitToolsetSections(s Settings) []string {
	if s.CapabilityMode == "managed" {
		return nil
	}
	// SPEC-0083 R3.2: the unmanaged native base list is gone — only the hub
	// tools server and the conditional guest browser are implicit emissions.
	out := []string{api.SectionHubTools}
	if s.toolEntry("browser").enabled() {
		// The anonymous-profile guest server rides the browser entry but is a
		// distinct emission surface with its own section.
		out = append(out, "hub:browser_guest")
	}
	return out
}

// denyDefaultSelections lists deny-default sections the space already
// selected by name — the only surfaces migration preserves. Implicit default
// emissions (terminal in the unmanaged base list) are not preserved: the
// restrictive default is the point, and the host re-admits them by rule or
// grant.
func (s Settings) denyDefaultSelections(g *api.Governance) []string {
	preserve := []string{}
	for name, entry := range s.Tools {
		if !entry.enabled() {
			continue
		}
		section, ok := g.Section(s.sectionForEntry(name, entry))
		if ok && section.Default == api.RuleDeny && section.ID != api.SectionUserMCP {
			preserve = append(preserve, section.ID)
		}
	}
	slices.Sort(preserve)
	return slices.Compact(preserve)
}

// applyGovernance loads the shared document for this space's principal and
// evaluates every section the space emits. Denied explicit wishes fail the
// read; the surviving decisions land on s.gov for the render consumers.
func applyGovernance(s *Settings) error {
	if s.User == "" || s.SpaceDir == "" {
		return nil
	}
	now := time.Now().UTC()
	path := governanceDocPath(s.SpaceDir)
	g, err := loadGovernanceForSpace(path, s)
	if err != nil {
		return fmt.Errorf("tool governance: %w", err)
	}
	view := &toolGovernance{path: path, revision: g.Revision,
		decisions: map[string]api.PolicyDecision{}, forceRO: map[string]bool{}}
	evaluate := func(sectionID string) api.PolicyDecision {
		if _, seen := view.decisions[sectionID]; seen {
			return view.decisions[sectionID]
		}
		attrs := map[string]string{"dynamic": "false"}
		if section, ok := g.Section(sectionID); ok {
			attrs = section.SectionAttrs()
		}
		decision := g.Evaluate(s.User, s.Organization, sectionID, attrs, now)
		view.decisions[sectionID] = decision
		return decision
	}
	// Explicit tools wishes — deny fails the read; cap:read narrows where the
	// backend can honor it.
	for name, entry := range s.Tools {
		if !entry.enabled() {
			continue
		}
		sectionID := s.sectionForEntry(name, entry)
		decision := evaluate(sectionID)
		switch decision.Effect {
		case api.RuleAllow:
		case api.RuleCapRead:
			switch entry.Via {
			case ToolViaToolHub:
				view.forceRO[name] = true
			case ToolViaMCPRaw:
				server := entry.Server
				if server == "" {
					server = name
				}
				if len(mcpRawMutationTools[server]) == 0 && len(entry.Except) == 0 {
					return fmt.Errorf("tools.%s: governance caps %s to read but server %q has no reviewed mutation table — set except: or accept denial", name, sectionID, server)
				}
				view.forceRO[name] = true
			default:
				return fmt.Errorf("tools.%s: governance caps %s to read but the %s backend cannot enforce read-only", name, sectionID, entry.Via)
			}
		default:
			return fmt.Errorf("tools.%s: %s denied by tool governance (%s): %s", name, sectionID, decision.Via, decision.Reason)
		}
	}
	// User-declared MCP servers are the user_mcp section: denied by default.
	if !s.OrgScoped() && len(s.MCP) > 0 {
		if decision := evaluate(api.SectionUserMCP); !decision.Allowed() {
			names := make([]string, 0, len(s.MCP))
			for name := range s.MCP {
				names = append(names, name)
			}
			slices.Sort(names)
			return fmt.Errorf("mcp: %s declared but user_mcp is denied by tool governance (%s); unlock needs a host grant — hubctl governance --kind grant", names[0], decision.Via)
		}
	}
	// The merged organization catalog is the org_mcp section.
	if s.OrgScoped() && len(s.MCP) > 0 {
		if decision := evaluate(api.SectionOrgMCP); !decision.Allowed() {
			return fmt.Errorf("organization MCP catalog (%s) denied by tool governance (%s): %s", api.SectionOrgMCP, decision.Via, decision.Reason)
		}
	}
	// Implicit platform emissions are recorded for render-time filtering.
	for _, sectionID := range implicitToolsetSections(*s) {
		evaluate(sectionID)
	}
	s.gov = view
	return nil
}

func loadGovernanceForSpace(path string, s *Settings) (*api.Governance, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// First render seeds the shared document, preserving the space's own
		// already-selected deny-default sections as recorded migration allows.
		seed := api.NewGovernance()
		preserve := s.denyDefaultSelections(seed)
		return api.LoadOrSeedGovernance(path, s.User, preserve, time.Now())
	}
	return api.LoadGovernance(path)
}

// govDecision answers for render consumers; a Settings that never passed
// through Read (tests, internal construction) carries no view and no
// filtering — the governance boundary is the load path.
func (s Settings) govDecision(sectionID string) (api.PolicyDecision, bool) {
	if s.gov == nil {
		return api.PolicyDecision{}, false
	}
	decision, ok := s.gov.decisions[sectionID]
	return decision, ok
}

// govAllows reports whether a section may render at all.
func (s Settings) govAllows(sectionID string) bool {
	decision, ok := s.govDecision(sectionID)
	return !ok || decision.Allowed()
}

// govForcedRO reports whether a tools entry is capped to read by governance.
func (s Settings) govForcedRO(name string) bool {
	return s.gov != nil && s.gov.forceRO[name]
}

// toolPolicySnapshot compiles the governance view into the executor contract.
// DenyAll follows the hub:tools decision: when the whole executor surface is
// denied the private exec channel refuses every call too.
func (s Settings) toolPolicySnapshot() api.ToolPolicySnapshot {
	snapshot := api.ToolPolicySnapshot{Schema: 1, Principal: s.User}
	if s.gov == nil {
		return snapshot
	}
	snapshot.Revision = s.gov.revision
	if decision, ok := s.govDecision(api.SectionHubTools); ok && !decision.Allowed() {
		snapshot.DenyAll = true
	}
	deny, readOnly := map[string]bool{}, map[string]bool{}
	for name, entry := range s.Tools {
		if entry.Via != ToolViaToolHub {
			continue
		}
		capability := name
		if mapped, ok := toolhubCapabilityID[name]; ok {
			capability = mapped
		}
		decision, _ := s.govDecision("hub:" + capability)
		switch {
		case !decision.Allowed():
			deny[capability] = true
		case decision.Effect == api.RuleCapRead || entry.Access == ToolAccessRO:
			readOnly[capability] = true
		}
	}
	for capability := range deny {
		snapshot.Deny = append(snapshot.Deny, capability)
	}
	for capability := range readOnly {
		if !deny[capability] {
			snapshot.ReadOnly = append(snapshot.ReadOnly, capability)
		}
	}
	slices.Sort(snapshot.Deny)
	slices.Sort(snapshot.ReadOnly)
	return snapshot
}

// MaterializeToolPolicySnapshot writes the executor policy beside the other
// generated files and returns its host path. The supervisor calls this at
// spawn so supervised runtimes mount the same read-only contract the compose
// path binds at /config/tool-policy.json.
func MaterializeToolPolicySnapshot(dir string, s Settings) (string, error) {
	if err := writeToolPolicySnapshot(dir, s); err != nil {
		return "", err
	}
	return filepath.Join(dir, "generated", "tool-policy."+s.Environment+".json"), nil
}

// writeToolPolicySnapshot materializes the executor policy next to the other
// generated files; compose mounts it read-only at /config/tool-policy.json.
func writeToolPolicySnapshot(dir string, s Settings) error {
	snapshot := s.toolPolicySnapshot()
	body, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "generated", "tool-policy."+s.Environment+".json")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tool-policy-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
