package stack

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"os"
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

// toolManifest classifies every enabled tools entry: toolhub always projects;
// builtin MCP connectors project only under managed mode, where ToolHub fronts
// them (unmanaged render attaches a local server directly); native sets and
// user-declared MCP servers stay direct; off entries appear in neither list.
func toolManifest(s Settings) ToolManifest {
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
		if entry.Via == ToolViaToolHub || (s.CapabilityMode == "managed" && entry.Via == ToolViaMCP && mcpConnectors[name]) {
			manifest.Projected = append(manifest.Projected, name)
		} else {
			manifest.Direct = append(manifest.Direct, name)
		}
	}
	slices.Sort(manifest.Projected)
	slices.Sort(manifest.Direct)
	return manifest
}

// writeToolManifest materializes the manifest beside the other generated
// contracts so runtime readiness can load the expectation read-only.
func writeToolManifest(dir string, s Settings) error {
	body, err := json.MarshalIndent(toolManifest(s), "", "  ")
	if err != nil {
		return err
	}
	return atomic(filepath.Join(dir, "generated", "tool-manifest."+s.Environment+".json"), append(body, '\n'))
}

// readToolManifest loads the manifest written by the previous render; a
// missing file means the space predates manifests (nothing to migrate).
func readToolManifest(dir, environment string) (*ToolManifest, error) {
	body, err := os.ReadFile(filepath.Join(dir, "generated", "tool-manifest."+environment+".json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest ToolManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("tool manifest: %w", err)
	}
	return &manifest, nil
}

// migrateToolhubBindings rolls this space's active ToolHub bindings and
// capability profiles onto the policy version this render computed
// (SPEC-0053 R1.2): a rights-hash change must not orphan grants the operator
// never revoked. The migration scans live state rather than trusting the
// previous manifest's `from` — envelopes are minted at the rendered hash, so
// entities pinned at a version no manifest recorded still brick the
// projection (the CHG-0230 live outage). Migration runs at render time so the
// deploy that follows already finds matching state.
func migrateToolhubBindings(dir string, s Settings) error {
	manifest := toolManifest(s)
	storePath := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if _, err := os.Stat(storePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		return fmt.Errorf("toolhub store: %w", err)
	}
	report, err := toolhub.MigrateProjection(store, s.User, manifest.Context, s.User, manifest.PolicyVersion)
	if err != nil {
		return fmt.Errorf("toolhub policy-version migration -> %s: %w", manifest.PolicyVersion, err)
	}
	if report.BindingsMoved > 0 {
		fmt.Fprintf(os.Stderr, "hubctl: migrated %d toolhub binding(s) -> %s\n", report.BindingsMoved, manifest.PolicyVersion)
	}
	for _, id := range report.ProfilesBlocked {
		fmt.Fprintf(os.Stderr, "hubctl: capability profile %s pinned at a stale policy — republish it (--kind profile --confirm) or the projection stays denied\n", id)
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
