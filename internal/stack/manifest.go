package stack

import (
	"encoding/json"
	"path/filepath"
	"slices"
)

// ToolManifest is the render-time expectation for the runtime tool surface
// (SPEC-0053 R1.4, ADR-0036 D6): `projected` names capabilities ToolHub must
// serve, `direct` the toolsets Hermes attaches without hub mediation. Runtime
// readiness compares the live surface against this list and reports the delta.
type ToolManifest struct {
	Schema        int      `json:"schema"`
	Context       string   `json:"context"`
	PolicyVersion string   `json:"policy_version"`
	Projected     []string `json:"projected"`
	Direct        []string `json:"direct"`
}

// ExpectedToolManifest classifies every enabled tools entry: toolhub always projects;
// builtin MCP connectors project only under managed mode, where ToolHub fronts
// them (unmanaged render attaches a local server directly); native sets and
// user-declared MCP servers stay direct; off entries appear in neither list.
func ExpectedToolManifest(s Settings) ToolManifest {
	contextID := s.User
	if s.OrgScoped() {
		contextID = s.Organization
		if contextID == "" {
			contextID = "personal"
		}
	}
	manifest := ToolManifest{Schema: 1, Context: contextID, PolicyVersion: policyVersion(s), Projected: []string{}, Direct: []string{}}
	for name, entry := range s.Tools {
		if !entry.enabled() {
			continue
		}
		if entry.Via == ToolViaToolHub || (entry.Via == ToolViaMCP && mcpConnectors[name]) {
			manifest.Projected = append(manifest.Projected, name)
		} else {
			manifest.Direct = append(manifest.Direct, name)
		}
	}
	slices.Sort(manifest.Projected)
	slices.Sort(manifest.Direct)
	return manifest
}

// MaterializeToolManifest is the claim-time entry the spawn spec builder
// uses: the manifest lands inside the runtime's own /config view, never in
// the shared generated root (S7d).
func MaterializeToolManifest(viewDir string, s Settings) error {
	body, err := json.MarshalIndent(ExpectedToolManifest(s), "", "  ")
	if err != nil {
		return err
	}
	return atomicMode(filepath.Join(viewDir, "tool-manifest.json"), append(body, '\n'), 0640)
}

// writeToolManifest materializes the manifest beside the other generated
// contracts so runtime readiness can load the expectation read-only.
func writeToolManifest(dir string, s Settings) error {
	body, err := json.MarshalIndent(ExpectedToolManifest(s), "", "  ")
	if err != nil {
		return err
	}
	if err := atomic(filepath.Join(dir, "generated", "tool-manifest."+s.Environment+".json"), append(body, '\n')); err != nil {
		return err
	}
	// With a published passport, /config is a single read-only directory
	// bind — the manifest rides inside the view (SPEC-0092 R5.2).
	if view := filepath.Join(dir, "generated", "config-view", s.Environment); dirExists(view) {
		return atomicMode(filepath.Join(view, "tool-manifest.json"), append(body, '\n'), 0640)
	}
	return nil
}

func cloneMap(source M) M {
	result := M{}
	for key, value := range source {
		result[key] = value
	}
	return result
}
