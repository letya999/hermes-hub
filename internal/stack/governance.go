package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
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
	decisions map[string]toolhub.PolicyDecision
	forceRO   map[string]bool
}

// governanceDocPath resolves the shared governance document: the
// HUB_TOOL_GOVERNANCE pin wins, then the space's own runtime dir (the infra
// space), then any sibling space's copy (secondary spaces reach the shared
// ToolHub policy), falling back to the space-local path as the seed target.
func governanceDocPath(spaceDir string) string {
	if pinned := os.Getenv("HUB_TOOL_GOVERNANCE"); pinned != "" {
		return pinned
	}
	own := filepath.Join(spaceDir, "runtime", "toolhub", "governance.json")
	if _, err := os.Stat(own); err == nil {
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
			for _, name := range names {
				candidate := filepath.Join(parent, name, "runtime", "toolhub", "governance.json")
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}
	}
	return own
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
			return toolhub.SectionOrgMCP
		}
		return toolhub.SectionUserMCP
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
	out := []string{toolhub.SectionHubTools}
	for _, name := range []string{"terminal", "file", "web", "skills", "todo", "cronjob", "messaging", "memory", "session_search"} {
		out = append(out, "native:"+name)
	}
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
func (s Settings) denyDefaultSelections(g *toolhub.Governance) []string {
	preserve := []string{}
	for name, entry := range s.Tools {
		if !entry.enabled() {
			continue
		}
		section, ok := g.Section(s.sectionForEntry(name, entry))
		if ok && section.Default == toolhub.RuleDeny && section.ID != toolhub.SectionUserMCP {
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
		decisions: map[string]toolhub.PolicyDecision{}, forceRO: map[string]bool{}}
	evaluate := func(sectionID string) toolhub.PolicyDecision {
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
		case toolhub.RuleAllow:
		case toolhub.RuleCapRead:
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
		if decision := evaluate(toolhub.SectionUserMCP); !decision.Allowed() {
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
		if decision := evaluate(toolhub.SectionOrgMCP); !decision.Allowed() {
			return fmt.Errorf("organization MCP catalog (%s) denied by tool governance (%s): %s", toolhub.SectionOrgMCP, decision.Via, decision.Reason)
		}
	}
	// Implicit platform emissions are recorded for render-time filtering.
	for _, sectionID := range implicitToolsetSections(*s) {
		evaluate(sectionID)
	}
	s.gov = view
	return nil
}

func loadGovernanceForSpace(path string, s *Settings) (*toolhub.Governance, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// First render seeds the shared document, preserving the space's own
		// already-selected deny-default sections as recorded migration allows.
		seed := toolhub.NewGovernance()
		preserve := s.denyDefaultSelections(seed)
		return toolhub.LoadOrSeedGovernance(path, s.User, preserve, time.Now())
	}
	return toolhub.LoadGovernance(path)
}

// govDecision answers for render consumers; a Settings that never passed
// through Read (tests, internal construction) carries no view and no
// filtering — the governance boundary is the load path.
func (s Settings) govDecision(sectionID string) (toolhub.PolicyDecision, bool) {
	if s.gov == nil {
		return toolhub.PolicyDecision{}, false
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
func (s Settings) toolPolicySnapshot() toolhub.ToolPolicySnapshot {
	snapshot := toolhub.ToolPolicySnapshot{Schema: 1, Principal: s.User}
	if s.gov == nil {
		return snapshot
	}
	snapshot.Revision = s.gov.revision
	if decision, ok := s.govDecision(toolhub.SectionHubTools); ok && !decision.Allowed() {
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
		case decision.Effect == toolhub.RuleCapRead || entry.Access == ToolAccessRO:
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
