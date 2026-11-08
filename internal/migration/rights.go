package migration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"gopkg.in/yaml.v3"
)

// DeriveSpaceRights rewrites the space's managed rights state from the
// published grants revision (SPEC-0092 R2.1): capability profiles get the
// flattened allow as their Rights ceiling, and the governance ledger gains
// platform-issued grants for exactly those rows — grants the revision
// dropped are revoked in place. ToolHub records are always derived here,
// never authored by hand. Spaces without a toolhub store have no managed
// principal and skip derivation entirely.
//
// Secondary spaces share the infra space's control plane: profiles and the
// rights ledger live in the infra sibling's store + governance document —
// the same files the shared ToolHub serves — so derivation resolves through
// InfraSpaceDir, exactly like the governance read path.
func DeriveSpaceRights(dir, user, environment string, rev passport.Revision, docs catalog.Docs, now time.Time) error {
	root := filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub")
	storePath := filepath.Join(root, "store.json")
	if _, err := os.Stat(filepath.Join(root, "store.db")); err == nil {
		// SPEC-0090 R5.1: the SQLite substrate is authoritative once the
		// verified store.db exists and the legacy store.json was retired.
		if _, err := os.Stat(storePath); os.IsNotExist(err) {
			storePath = filepath.Join(root, "store.db")
		}
	}
	store, err := toolhub.Load(storePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil // unmanaged space: no principal to bind
	}
	if err != nil {
		return fmt.Errorf("rights derivation: %w", err)
	}
	allow, err := rev.GrantAllow()
	if err != nil {
		return fmt.Errorf("rights derivation: %w", err)
	}
	settings, err := stack.ReadEffectiveRevision(dir, environment, rev.ID)
	if err != nil {
		return fmt.Errorf("rights derivation: %w", err)
	}
	bound, _ := rev.Value("tools", "binding.profile_id")
	profileID, _ := bound.(string)
	if err := store.RequireActiveBoundProfile(user, environment, profileID, settings.CapabilityGeneration); err != nil {
		return fmt.Errorf("rights derivation: %w", err)
	}
	gov := toolhub.NewGovernanceStore(toolhub.GovernanceStorePath(storePath))
	selections, core, err := selectedHubTools(store, rev.Docs["tools"], docs)
	if err != nil {
		return fmt.Errorf("tools derivation: %w", err)
	}
	connectorSelections, connectorCore, err := selectedConnectorTools(store, rev.Docs["tools"], docs, profileID)
	if err != nil {
		return fmt.Errorf("tools derivation: %w", err)
	}
	selections, core = append(selections, connectorSelections...), append(core, connectorCore...)
	return toolhub.DerivePublishedRightsAndSelections(store, gov, docs.Rights, user, allow, environment, profileID, stack.PolicyVersion(settings), rev.ID, agenttools.HubToolsDefinitionID, selections, core, now)
}

// RestoreSpaceRights restores the bound profile after a failed apply. It
// intersects the previous passport allow with live grants so a concurrent
// revocation stays revoked. It leaves policy versions and bindings untouched.
func RestoreSpaceRights(dir, user, environment, profileID, revisionID string, allow map[string][]string, toolsBody []byte, docs catalog.Docs, now time.Time) error {
	if profileID == "" {
		return fmt.Errorf("rights restore: bound profile is required")
	}
	root := filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub")
	storePath := filepath.Join(root, "store.json")
	if _, err := os.Stat(filepath.Join(root, "store.db")); err == nil {
		if _, err := os.Stat(storePath); os.IsNotExist(err) {
			storePath = filepath.Join(root, "store.db")
		}
	}
	store, err := toolhub.Load(storePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("rights restore: %w", err)
	}
	gov := toolhub.NewGovernanceStore(toolhub.GovernanceStorePath(storePath))
	issued, err := toolhub.IssuedRightCeiling(gov, docs.Rights, user, now)
	if err != nil {
		return fmt.Errorf("rights restore: %w", err)
	}
	restored := make(map[string][]string, len(allow))
	for capability, operations := range allow {
		for _, operation := range operations {
			if slices.Contains(issued[capability], operation) {
				restored[capability] = append(restored[capability], operation)
			}
		}
	}
	selections, core, err := selectedHubTools(store, toolsBody, docs)
	if err != nil {
		return fmt.Errorf("rights restore: tools derivation: %w", err)
	}
	connectorSelections, connectorCore, err := selectedConnectorTools(store, toolsBody, docs, profileID)
	if err != nil {
		return fmt.Errorf("rights restore: tools derivation: %w", err)
	}
	selections, core = append(selections, connectorSelections...), append(core, connectorCore...)
	return toolhub.DerivePublishedRightsAndSelections(store, gov, docs.Rights, user, restored, environment, profileID, "", revisionID, agenttools.HubToolsDefinitionID, selections, core, now)
}

// SetSpaceProfileActive gates a rejected or superseded binding without
// changing any issued grant or another principal/environment's profile.
func SetSpaceProfileActive(dir, user, environment, profileID string, active bool, now time.Time) error {
	root := filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub")
	storePath := filepath.Join(root, "store.json")
	if _, err := os.Stat(filepath.Join(root, "store.db")); err == nil {
		if _, err := os.Stat(storePath); errors.Is(err, os.ErrNotExist) {
			storePath = filepath.Join(root, "store.db")
		}
	}
	store, err := toolhub.Load(storePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return store.SetBoundProfileActive(user, environment, profileID, active, now)
}

// ValidateSelectedConnectors resolves declared connector tools before the
// publisher moves the active pointer. A missing binding or tool is an error,
// never a reason to retain an old profile selection.
func ValidateSelectedConnectors(dir string, toolsBody []byte, docs catalog.Docs) error {
	var authored passportview.Tools
	if err := yaml.Unmarshal(toolsBody, &authored); err != nil {
		return err
	}
	selected := false
	for _, entries := range []map[string]passportview.Toolset{authored.Core, authored.Extended} {
		for _, entry := range entries {
			selected = selected || entry.Via == "mcp" || strings.HasPrefix(entry.Via, "mcp:")
		}
	}
	if !selected {
		return nil
	}
	root := filepath.Join(stack.InfraSpaceDir(dir), "runtime", "toolhub")
	storePath := filepath.Join(root, "store.json")
	if _, err := os.Stat(filepath.Join(root, "store.db")); err == nil {
		if _, err := os.Stat(storePath); errors.Is(err, os.ErrNotExist) {
			storePath = filepath.Join(root, "store.db")
		}
	}
	store, err := toolhub.Load(storePath)
	if err != nil {
		return fmt.Errorf("connector selection store: %w", err)
	}
	if authored.Binding == nil {
		return fmt.Errorf("connector selection requires tools.binding")
	}
	_, _, err = selectedConnectorTools(store, toolsBody, docs, authored.Binding.ProfileID)
	return err
}

func selectedConnectorTools(store *toolhub.Store, body []byte, docs catalog.Docs, profileID string) ([]api.CapabilitySelection, []string, error) {
	var authored passportview.Tools
	if err := yaml.Unmarshal(body, &authored); err != nil {
		return nil, nil, err
	}
	var selections []api.CapabilitySelection
	var core []string
	var bindings []api.ToolBinding
	for layer, entries := range []map[string]passportview.Toolset{authored.Core, authored.Extended} {
		names := make([]string, 0, len(entries))
		for name := range entries {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			entry := entries[name]
			if entry.Via != "mcp" && !strings.HasPrefix(entry.Via, "mcp:") {
				continue
			}
			toolset, err := passport.ResolveToolset(docs, name)
			if err != nil || toolset.Kind != "connector" {
				return nil, nil, fmt.Errorf("connector toolset %q is unavailable", name)
			}
			if bindings == nil {
				bindings, err = store.BindingsForProfile(profileID)
				if err != nil {
					return nil, nil, err
				}
			}
			definitionID := name
			if strings.HasPrefix(entry.Via, "mcp:") {
				definitionID = strings.TrimPrefix(entry.Via, "mcp:")
			}
			var chosen *api.ToolBinding
			for i := range bindings {
				binding := &bindings[i]
				if binding.DefinitionID != definitionID {
					continue
				}
				if chosen != nil && chosen.ConnectionID != binding.ConnectionID {
					return nil, nil, fmt.Errorf("connector %q has ambiguous bindings", name)
				}
				if chosen == nil || binding.ProjectionRevision > chosen.ProjectionRevision {
					chosen = binding
				}
			}
			if chosen == nil {
				return nil, nil, fmt.Errorf("connector %q has no active binding", name)
			}
			definition, err := store.Definition(chosen.DefinitionID, chosen.DefinitionVersion)
			if err != nil {
				return nil, nil, err
			}
			if definition.Transport != api.RemoteMCP && definition.Transport != api.ContainerMCP {
				return nil, nil, fmt.Errorf("connector %q has unsupported transport", name)
			}
			available := map[string]api.ToolSpec{}
			for _, tool := range definition.ProjectedToolSpecs(*chosen) {
				available[tool.Name] = tool
			}
			if entry.Tools != nil {
				for _, member := range append(slices.Clone(entry.Tools.Include), entry.Tools.Exclude...) {
					if _, ok := available[member]; !ok {
						return nil, nil, fmt.Errorf("connector %q selects unknown tool %q", name, member)
					}
				}
			}
			names := make([]string, 0, len(available))
			for member := range available {
				names = append(names, member)
			}
			slices.Sort(names)
			for _, member := range names {
				tool := available[member]
				if tool.Audience != "" && tool.Audience != api.AudienceModel || entry.Access == "read" && tool.Effect != api.ReadEffect ||
					entry.Tools != nil && (entry.Tools.Include != nil && !slices.Contains(entry.Tools.Include, member) || slices.Contains(entry.Tools.Exclude, member)) {
					continue
				}
				if tool.CapabilityID == "" || len(tool.Uses) == 0 && !api.RemoteExecutor(tool.Executor) {
					return nil, nil, fmt.Errorf("connector %q tool %q has no capability contract", name, member)
				}
				projected := api.ProjectedToolName(definition.DefinitionID, definition.Version, member)
				selections = append(selections, api.CapabilitySelection{CapabilityID: tool.CapabilityID, DefinitionID: definition.DefinitionID,
					DefinitionVersion: definition.Version, ImplementationDigest: api.DefinitionDigest(definition), ToolName: member, Name: projected, ConnectionID: chosen.ConnectionID})
				if layer == 0 {
					core = append(core, projected)
				}
			}
		}
	}
	return selections, core, nil
}

// selectedHubTools resolves the compiler-validated first-party families from
// the pinned catalog. Selection only chooses an implementation; profile
// policy, published rights and the live ledger still decide admission.
func selectedHubTools(store *toolhub.Store, body []byte, docs catalog.Docs) ([]api.CapabilitySelection, []string, error) {
	var authored passportview.Tools
	if err := yaml.Unmarshal(body, &authored); err != nil {
		return nil, nil, err
	}
	if len(authored.Core) == 0 && len(authored.Extended) == 0 {
		return nil, nil, nil
	}
	definition := agenttools.HubToolsDefinition()
	registered, err := store.Definition(definition.DefinitionID, definition.Version)
	if err != nil {
		if errors.Is(err, api.ErrNotFound) {
			for name, entry := range authored.Core {
				if name != "platform" && entry.Via == "toolhub" {
					return nil, nil, fmt.Errorf("selected first-party definition unavailable: %w", err)
				}
			}
			for name, entry := range authored.Extended {
				if name != "platform" && entry.Via == "toolhub" {
					return nil, nil, fmt.Errorf("selected first-party definition unavailable: %w", err)
				}
			}
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if api.DefinitionDigest(registered) != api.DefinitionDigest(definition) {
		return nil, nil, fmt.Errorf("first-party definition differs from pinned catalog")
	}
	byName := make(map[string]api.ToolSpec, len(registered.Tools))
	for _, tool := range registered.Tools {
		byName[tool.Name] = tool
	}
	selected := map[string]api.CapabilitySelection{}
	coreSet := map[string]bool{}
	for _, layer := range []struct {
		entries map[string]passportview.Toolset
		core    bool
	}{{authored.Core, true}, {authored.Extended, false}} {
		for name, entry := range layer.entries {
			if entry.Via != "toolhub" || name == "platform" {
				continue
			}
			toolset, err := passport.ResolveToolset(docs, name)
			if err != nil {
				return nil, nil, err
			}
			members := toolset.Members
			if entry.Tools != nil && entry.Tools.Include != nil {
				members = entry.Tools.Include
			}
			if _, err := selectedPathScopes(entry.Scope, nil); err != nil {
				return nil, nil, fmt.Errorf("toolset %q: %w", name, err)
			}
			for member, rule := range entry.Rules {
				if !slices.Contains(toolset.Members, member) {
					return nil, nil, fmt.Errorf("toolset %q has unknown rule %q", name, member)
				}
				if _, err := selectedPathScopes(nil, rule.Scope); err != nil {
					return nil, nil, fmt.Errorf("toolset %q rule %q: %w", name, member, err)
				}
			}
			for _, member := range members {
				if entry.Tools != nil && slices.Contains(entry.Tools.Exclude, member) {
					continue
				}
				tool, ok := byName[member]
				if !ok || tool.Audience != api.AudienceModel || (entry.Access == "read" && tool.Effect != api.ReadEffect) {
					continue
				}
				paths, err := selectedPathScopes(entry.Scope, entry.Rules[member].Scope)
				if err != nil {
					return nil, nil, fmt.Errorf("toolset %q tool %q: %w", name, member, err)
				}
				if len(paths) > 0 {
					if tool.Executor != "handler:files" || len(tool.Uses) == 0 {
						return nil, nil, fmt.Errorf("toolset %q tool %q cannot enforce path scope", name, member)
					}
					for _, use := range tool.Uses {
						if use.PathArgument == "" {
							return nil, nil, fmt.Errorf("toolset %q tool %q cannot enforce path scope", name, member)
						}
					}
				}
				selection := api.CapabilitySelection{CapabilityID: tool.CapabilityID, DefinitionID: definition.DefinitionID,
					DefinitionVersion: definition.Version, ImplementationDigest: api.DefinitionDigest(registered), ToolName: member, Name: member, PathScopes: paths}
				if previous, exists := selected[member]; exists && len(previous.PathScopes) > 0 && len(paths) > 0 {
					selection.PathScopes, err = intersectSelectedPaths(previous.PathScopes, paths)
					if err != nil {
						return nil, nil, fmt.Errorf("tool %q: %w", member, err)
					}
				} else if exists && len(previous.PathScopes) > 0 {
					selection.PathScopes = previous.PathScopes
				}
				selected[member] = selection
				if layer.core {
					coreSet[member] = true
				}
			}
		}
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	out, core := make([]api.CapabilitySelection, 0, len(names)), []string{}
	for _, name := range names {
		out = append(out, selected[name])
		if coreSet[name] {
			core = append(core, name)
		}
	}
	return out, core, nil
}

func selectedPathScopes(parent, child *passportview.Scope) ([]string, error) {
	paths := func(scope *passportview.Scope) ([]string, error) {
		if scope == nil {
			return nil, nil
		}
		if len(scope.Hosts) > 0 || len(scope.URLs) > 0 || len(scope.Paths) == 0 {
			return nil, fmt.Errorf("unsupported scope")
		}
		for _, value := range scope.Paths {
			if !api.ValidSelectionPath(value) {
				return nil, fmt.Errorf("invalid path scope")
			}
			if !slices.Contains([]string{"/workspace", "/archive", "/organization"}, value) &&
				!strings.HasPrefix(value, "/workspace/") && !strings.HasPrefix(value, "/archive/") && !strings.HasPrefix(value, "/organization/") {
				return nil, fmt.Errorf("unsupported path root")
			}
		}
		return scope.Paths, nil
	}
	outer, err := paths(parent)
	if err != nil {
		return nil, err
	}
	inner, err := paths(child)
	if err != nil {
		return nil, err
	}
	if len(outer) == 0 {
		return inner, nil
	}
	if len(inner) == 0 {
		return outer, nil
	}
	return intersectSelectedPaths(outer, inner)
}

func intersectSelectedPaths(left, right []string) ([]string, error) {
	var out []string
	for _, a := range left {
		for _, b := range right {
			if a == b || strings.HasPrefix(a, b+"/") {
				out = append(out, a)
			} else if strings.HasPrefix(b, a+"/") {
				out = append(out, b)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("disjoint path scopes")
	}
	sort.Strings(out)
	return slices.Compact(out), nil
}
