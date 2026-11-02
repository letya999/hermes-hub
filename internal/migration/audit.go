package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"gopkg.in/yaml.v3"
)

// SpaceAudit is the operator inventory of one managed space — read only from
// the published revision, never from editable sources (R11.2). Provenance is
// the compiled source-version map so a denied surface can be traced back to
// the layer that bounded it.
type SpaceAudit struct {
	Revision    string              `json:"revision"`
	Services    []string            `json:"services"`
	Routes      map[string]string   `json:"routes"`
	Workspace   WorkspaceView       `json:"workspace"`
	Toolsets    ToolsetView         `json:"toolsets"`
	Grants      map[string][]string `json:"grants"`
	DenyReasons []string            `json:"deny_reasons"`
	Provenance  map[string]string   `json:"provenance"`
}

type WorkspaceView struct {
	Writable []string `json:"writable"`
	ReadOnly []string `json:"read_only"`
	Tree     string   `json:"tree"`
}

type ToolsetView struct {
	Core      []string `json:"core"`
	Extended  []string `json:"extended"`
	Discovery bool     `json:"discovery"`
}

// auditDocument is one v2 passport file: a short header and top-level
// contract keys (no body wrapper).
type auditDocument = map[string]any

// AuditPassport answers what the space effectively runs, mounts, may call and
// was denied — the operator/user inventory command (S11.2). Unknown doc
// shapes fail closed rather than report a partial inventory as complete.
func AuditPassport(dir string) (SpaceAudit, error) {
	var audit SpaceAudit
	revID, err := passport.Current(dir)
	if err != nil {
		return audit, fmt.Errorf("space-audit: %w", err)
	}
	audit.Revision = revID
	revDir := filepath.Join(dir, "generated", "passports", revID)
	load := func(name string) (auditDocument, error) {
		var d auditDocument
		b, err := os.ReadFile(filepath.Join(revDir, name)) // #nosec G304 -- revision dir is published contract
		if err != nil {
			return d, err
		}
		if err := yaml.Unmarshal(b, &d); err != nil {
			return d, fmt.Errorf("space-audit: unreadable %s", name)
		}
		return d, nil
	}
	stackDoc, err := load("stack.yaml")
	if err != nil {
		return audit, err
	}
	modelsDoc, err := load("models.yaml")
	if err != nil {
		return audit, err
	}
	wsDoc, err := load("workspace.yaml")
	if err != nil {
		return audit, err
	}
	toolsDoc, err := load("tools.yaml")
	if err != nil {
		return audit, err
	}
	grantsDoc, err := load("grants.yaml")
	if err != nil {
		return audit, err
	}

	audit.Services = mapKeys(stackDoc["services"])
	if s := mapKeys(modelsDoc["services"]); len(s) > 0 {
		audit.Routes = map[string]string{}
		for _, svc := range s {
			audit.Routes[svc] = "model-service"
		}
		if def, ok := modelsDoc["default"].(string); ok && def != "" {
			audit.Routes["default"] = def
		}
	}
	audit.Workspace = WorkspaceView{
		Writable: stringsList(wsDoc["writable"]),
		ReadOnly: stringsList(wsDoc["read_only"]),
	}
	if tree, ok := wsDoc["tree"].(string); ok {
		audit.Workspace.Tree = tree
	}
	core := mapKeys(toolsDoc["core"])
	extended := mapKeys(toolsDoc["extended"])
	audit.Toolsets = ToolsetView{Core: core, Extended: extended}
	if coreMap, ok := toolsDoc["core"].(map[string]any); ok {
		if disc, ok := coreMap["discovery"].(map[string]any); ok {
			audit.Toolsets.Discovery = fmt.Sprint(disc["via"]) != "" && fmt.Sprint(disc["via"]) != "off"
		}
	}
	if allow, ok := grantsDoc["allow"].(map[string]any); ok {
		audit.Grants = map[string][]string{}
		for cap, ops := range allow {
			audit.Grants[cap] = stringsList(ops)
		}
	}

	// Deny reasons: every cataloged toolset the passport did not admit, with
	// the reason operators actually need — raw MCP is default-deny by
	// construction, the rest are simply not admitted by any layer.
	docs, err := catalog.Generate()
	if err != nil {
		return audit, err
	}
	admitted := map[string]bool{}
	for _, sec := range core {
		admitted[sec] = true
	}
	for _, sec := range extended {
		admitted[sec] = true
	}
	var names []string
	for name := range docs.Toolsets.Toolsets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if admitted[name] {
			continue
		}
		reason := "denied: not admitted by any passport layer"
		if name == "user_mcp" || name == "org_mcp" {
			reason = "denied: raw MCP requires a reviewed grant (default-deny)"
		}
		audit.DenyReasons = append(audit.DenyReasons, name+": "+reason)
	}

	// Provenance lives in the revision's side file — the passports carry
	// only the short envelope (ADR-0093 D2).
	var prov passport.Provenance
	if pb, err := os.ReadFile(filepath.Join(revDir, "provenance.yaml")); err == nil {
		if err := yaml.Unmarshal(pb, &prov); err != nil {
			return audit, fmt.Errorf("space-audit: unreadable provenance.yaml")
		}
	}
	audit.Provenance = map[string]string{}
	for src, v := range prov.Sources {
		audit.Provenance[src] = fmt.Sprintf("v%d", v)
	}
	for name, rel := range prov.Catalogs {
		audit.Provenance["catalog:"+name] = rel
	}
	return audit, nil
}

func mapKeys(v any) []string {
	m, _ := v.(map[string]any)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stringsList(v any) []string {
	var out []string
	switch list := v.(type) {
	case []any:
		for _, e := range list {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = list
	}
	return out
}
