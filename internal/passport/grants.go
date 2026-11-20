package passport

import (
	"fmt"
	"maps"
	"sort"
)

// LayeredGrants folds each layer's grants document into the capability
// list Compile narrows and derives the trusted ledger the effective grants
// snapshot projects from. A layer's grants.allow may carry the authored
// {capability: [operations]} map — declaring both the capability and its
// operations — or a bare capability list that keeps the operations a layer
// above declared. The deepest declaration of a capability's operations
// wins; every declaration must stay inside the operations a layer above
// declared, and the deepest (agent) layer may not introduce a capability
// the layers above never declared — lower layers only narrow.
//
// The returned layers carry grants.allow as the sorted capability list the
// classNarrow merge expects; the caller's trees are left untouched.
func LayeredGrants(layers []Layer) ([]Layer, map[string][]string, error) {
	out := make([]Layer, len(layers))
	declaredOps := map[string][]string{}
	mergedCaps := []string{}
	for i, l := range layers {
		out[i] = l
		raw, declared := l.Docs["grants"]["allow"]
		if !declared || raw == nil {
			continue
		}
		caps := []string{}
		switch allow := raw.(type) {
		case []any:
			for _, cap := range allow {
				s, ok := cap.(string)
				if !ok || s == "" {
					return nil, nil, fmt.Errorf("passport compile: layer %q grants.allow entries must be capability names", l.Name)
				}
				caps = append(caps, s)
			}
		case map[string]any:
			parsed, opsMap, err := parseGrantsOps(allow, fmt.Sprintf("passport compile: layer %q grants.allow.", l.Name))
			if err != nil {
				return nil, nil, err
			}
			for _, cap := range parsed {
				ops := opsMap[cap]
				if above, hasAbove := declaredOps[cap]; hasAbove {
					for _, op := range ops {
						if !containsString(above, op) {
							return nil, nil, fmt.Errorf("passport compile: layer %q grants.allow.%s.%s widens beyond the layer above", l.Name, cap, op)
						}
					}
				} else if i == len(layers)-1 {
					return nil, nil, fmt.Errorf("passport compile: agent layer %q grants.allow.%s adds a capability beyond the user layer", l.Name, cap)
				}
				declaredOps[cap] = ops
				caps = append(caps, cap)
			}
		default:
			return nil, nil, fmt.Errorf("passport compile: layer %q grants.allow must be a capability list or {capability: [operations]} map", l.Name)
		}
		sort.Strings(caps)
		mergedCaps = caps
		docs := maps.Clone(l.Docs)
		grant := maps.Clone(l.Docs["grants"])
		allow := make([]any, len(caps))
		for i, cap := range caps {
			allow[i] = cap
		}
		grant["allow"] = allow
		docs["grants"] = grant
		out[i].Docs = docs
	}
	ledger := map[string][]string{}
	for _, cap := range mergedCaps {
		if ops, ok := declaredOps[cap]; ok {
			ledger[cap] = ops
		}
	}
	return out, ledger, nil
}

func containsString(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}

// parseGrantsOps parses an authored grants.allow {capability: [operations]}
// map into the sorted capability list and the operations each capability
// declared; where is the full error prefix ending in "allow." — authored.go
// passes the grants.yaml path, LayeredGrants the layer name. The bound each
// caller checks against differs (the trusted grant ceiling in authored.go,
// the ops declared by layers above in LayeredGrants) — only the parse is
// shared.
func parseGrantsOps(raw map[string]any, where string) ([]string, map[string][]string, error) {
	caps := make([]string, 0, len(raw))
	ops := make(map[string][]string, len(raw))
	for cap, opsRaw := range raw {
		list, ok := opsRaw.([]any)
		if !ok || len(list) == 0 {
			return nil, nil, fmt.Errorf("%s%s must be a nonempty list", where, cap)
		}
		seen := map[string]bool{}
		for _, opRaw := range list {
			op, ok := opRaw.(string)
			if !ok || op == "" || seen[op] {
				return nil, nil, fmt.Errorf("%s%s has an invalid or duplicate operation", where, cap)
			}
			seen[op] = true
			ops[cap] = append(ops[cap], op)
		}
		caps = append(caps, cap)
	}
	sort.Strings(caps)
	return caps, ops, nil
}
