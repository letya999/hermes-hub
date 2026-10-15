package toolhub

import (
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
)

// GovernanceStore is the control-plane's path-bound accessor for the
// governance document. Reads are lock-free because savegovernance writes
// atomically; mutations take the flock so concurrent control ops and host
// edits serialize.
type GovernanceStore struct {
	path string
}

// NewGovernanceStore binds a path; empty disables governance (nil receiver
// reads as "legacy deployment without the document" — callers stay
// permissive only in that deliberately-unconfigured case).
func NewGovernanceStore(path string) *GovernanceStore {
	if path == "" {
		return nil
	}
	return &GovernanceStore{path: path}
}

// GovernanceStorePath derives the document location from the registry path:
// governance.json beside store.json — same durability class and mount.
func GovernanceStorePath(storePath string) string {
	if storePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(storePath), "governance.json")
}

// Load returns the document; an absent file yields the shipped in-memory
// default posture (user_mcp denied, external denied) rather than failing the
// whole plane, while a present-but-corrupt file is a hard error — callers
// deny closed on it.
func (g *GovernanceStore) Load() (*api.Governance, error) {
	if g == nil || g.path == "" {
		return nil, fmt.Errorf("%w: governance store", api.ErrInvalid)
	}
	doc, err := api.LoadGovernance(g.path)
	if errors.Is(err, os.ErrNotExist) {
		return api.NewGovernance(), nil
	}
	return doc, err
}

// Update serializes one mutation: load under flock (seeding on absence), run
// fn, persist. A failed fn leaves the file untouched.
func (g *GovernanceStore) Update(now time.Time, fn func(*api.Governance) error) error {
	if g == nil || g.path == "" {
		return fmt.Errorf("%w: governance store", api.ErrInvalid)
	}
	if err := os.MkdirAll(filepath.Dir(g.path), 0700); err != nil {
		return err
	}
	lock := flock.New(g.path + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	doc, err := api.LoadGovernance(g.path)
	if errors.Is(err, os.ErrNotExist) {
		doc = api.NewGovernance()
	} else if err != nil {
		return err // corrupt document: fail closed, never overwrite
	}
	if err := fn(doc); err != nil {
		return err
	}
	return api.SaveGovernanceLocked(g.path, doc)
}

// projectedToolAttrs derives the deterministic axes a dynamic ToolHub
// projection is evaluated under: who authored it (connection owner, prepared
// provenance or unreviewed artifact), transport, prepared/official markers
// and the concrete tool's declared effect.
func projectedToolAttrs(effective api.EffectiveBinding, tool api.ToolSpec) map[string]string {
	definition := effective.Definition
	owner := api.OwnerExternal
	reviewed := definition.Source.ReviewDigest != ""
	prepared := reviewed || definition.Source.RecipeDigest != "" || definition.Source.ToolContractDigest != ""
	switch {
	case definition.Transport == api.AgentTools || shippedCLI(definition):
		owner, reviewed, prepared = api.OwnerHub, true, true
	case effective.Connection != nil && effective.Connection.Owner.Type == api.PrincipalOwner:
		owner = api.OwnerUser
	case effective.Connection != nil && effective.Connection.Owner.Type == api.ContextOwner:
		owner = api.OwnerOrg
	case reviewed || prepared:
		owner = api.OwnerOrg
	}
	return map[string]string{
		"definition_id": definition.DefinitionID,
		"transport":     string(definition.Transport),
		"dynamic":       "true",
		"prepared":      fmt.Sprint(prepared),
		"official":      fmt.Sprint(reviewed),
		"owner":         owner,
		"effect":        string(tool.Effect),
	}
}

// Trust the reviewed release bytes, not a user-controlled id or transport.
func shippedCLI(definition api.ToolDefinition) bool {
	for _, shipped := range api.CLICatalogDefinitions() {
		if api.DefinitionDigest(definition) == api.DefinitionDigest(shipped) {
			return true
		}
	}
	return false
}

// governanceDecision evaluates the toolhub section for one projection. A
// governance load failure is deny — the caller converts it into the
// surface-appropriate refusal.
func (g *Gateway) governanceDecision(auth identity.Envelope, tool api.ToolSpec, effective api.EffectiveBinding) (api.PolicyDecision, error) {
	doc, err := g.Governance.Load()
	if err != nil {
		return api.PolicyDecision{Effect: api.RuleDeny, Via: "unknown", Reason: "policy unreadable"}, fmt.Errorf("%w: tool governance: %v", api.ErrUnauthorized, err)
	}
	return doc.Evaluate(auth.PrincipalID, auth.Organization, api.SectionToolHub, projectedToolAttrs(effective, tool), time.Now().UTC()), nil
}

// filterProjectedTools applies section+per-tool governance at list time:
// denied sections and cap:read-narrowed effects drop out of the projection.
// The store layer already proved ownership; this is the policy fence.
func (g *Gateway) filterProjectedTools(auth identity.Envelope, projected []api.ProjectedTool) ([]api.ProjectedTool, error) {
	if g.Governance == nil {
		return projected, nil
	}
	doc, err := g.Governance.Load()
	if err != nil {
		return nil, fmt.Errorf("%w: tool governance: %v", api.ErrUnauthorized, err)
	}
	out := make([]api.ProjectedTool, 0, len(projected))
	bindings := map[string]api.EffectiveBinding{}
	for _, tool := range projected {
		var effective api.EffectiveBinding
		var resolveErr error
		if auth.CapabilityProfile == "" {
			var ok bool
			effective, ok = bindings[tool.BindingID]
			if !ok {
				effective, resolveErr = g.Store.Resolve(auth, tool.BindingID)
				if resolveErr == nil {
					bindings[tool.BindingID] = effective
				}
			}
		} else {
			_, effective, resolveErr = g.Store.ResolveProjectedTool(auth, tool.Name)
		}
		if resolveErr != nil {
			continue // unresolved bindings can't be invoked anyway
		}
		decision := doc.Evaluate(auth.PrincipalID, auth.Organization, api.SectionToolHub, projectedToolAttrs(effective, tool.Tool), time.Now().UTC())
		if decision.AllowsToolEffect(string(tool.Tool.Effect)) {
			out = append(out, tool)
		}
	}
	return out, nil
}

// admitGoverned wraps an admission callback with call-time governance: the
// evaluation runs after ownership admission but before credential injection,
// so a mid-session denial cuts the call exactly like a binding revocation.
func (g *Gateway) admitGoverned(auth identity.Envelope, admit func(api.ProjectedTool, api.EffectiveBinding) error) func(api.ProjectedTool, api.EffectiveBinding) error {
	if g.Governance == nil {
		return admit
	}
	return func(projected api.ProjectedTool, effective api.EffectiveBinding) error {
		decision, err := g.governanceDecision(auth, projected.Tool, effective)
		if err != nil {
			return err
		}
		if !decision.AllowsToolEffect(string(projected.Tool.Effect)) {
			return fmt.Errorf("%w: %s denied by tool governance (%s)", api.ErrUnauthorized, projected.Name, decision.Via)
		}
		return admit(projected, effective)
	}
}
