// Diff compares two effective revisions leaf by leaf and binds each
// changed leaf to its registered consumer and effect class (SPEC-0092
// R1.1). Values under ref/auth leaves never print resolved material.
package passport

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Change is one differing leaf: the concrete schema path, the rendered
// before/after values (ref/auth leaves masked as <ref:name>) and the
// effect class the consumer registry assigns to the leaf.
type Change struct {
	Path     string
	Before   string
	After    string
	Effect   Effect
	Consumer string
}

// Rank orders effect classes from cheapest to most disruptive; the
// reconcile plan picks the highest class per consumer.
func (e Effect) Rank() int {
	switch e {
	case EffectLive:
		return 1
	case EffectRespawn:
		return 2
	case EffectRecreate:
		return 3
	case EffectRebuild:
		return 4
	}
	return 0
}

type flatLeaf struct {
	wild    string
	raw     string
	display string
}

// Diff returns the sorted leaf-level changes between prev and next.
// Empty means the bodies are identical (header churn is not a change).
func Diff(prev, next Effective) []Change {
	p, n := map[string]flatLeaf{}, map[string]flatLeaf{}
	for _, name := range Names {
		var pb, nb any
		switch name {
		case "stack":
			pb, nb = prev.Stack, next.Stack
		case "config":
			pb, nb = prev.Config, next.Config
		case "models":
			pb, nb = prev.Models, next.Models
		case "workspace":
			pb, nb = prev.Workspace, next.Workspace
		case "tools":
			pb, nb = prev.Tools, next.Tools
		case "grants":
			pb, nb = prev.Grants, next.Grants
		}
		flattenLeaves(reflect.ValueOf(pb), name, name, p)
		flattenLeaves(reflect.ValueOf(nb), name, name, n)
	}
	var out []Change
	for path, pl := range p {
		nl, ok := n[path]
		if !ok || nl.raw != pl.raw {
			out = append(out, mkChange(path, pl.display, nl.display))
		}
	}
	for path, nl := range n {
		if _, ok := p[path]; !ok {
			out = append(out, mkChange(path, "", nl.display))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// flatten walks the deepest leaves only: structs recurse by field, maps
// recurse per key (concrete key, wildcard "*"), scalars and slices are
// atomic leaves. This mirrors schemaLeaves plus per-key granularity so a
// diff can name the exact port, service or channel that changed.
func flattenLeaves(v reflect.Value, concrete, wild string, out map[string]flatLeaf) {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			out[concrete] = flatLeaf{wild: wild, raw: "<none>", display: "<none>"}
			return
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			out[concrete] = flatLeaf{wild: wild, raw: rawValue(v), display: display(v, wild)}
			return
		}
		for i := 0; i < v.NumField(); i++ {
			key := yamlKey(v.Type().Field(i))
			if key == "" {
				continue
			}
			flattenLeaves(v.Field(i), concrete+"."+key, wild+"."+key, out)
		}
	case reflect.Map:
		if v.IsNil() {
			out[concrete] = flatLeaf{wild: wild, raw: "<none>", display: "<none>"}
			return
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface())
		})
		for _, k := range keys {
			flattenLeaves(v.MapIndex(k), concrete+"."+fmt.Sprint(k.Interface()), wild+".*", out)
		}
	default:
		out[concrete] = flatLeaf{wild: wild, raw: rawValue(v), display: display(v, wild)}
	}
}

// rawValue is the comparison key for a leaf; it never leaves Diff.
func rawValue(v reflect.Value) string {
	return fmt.Sprintf("%#v", v.Interface())
}

// display renders a leaf; secret-bearing paths (any *.ref leaf or
// anything under .auth.) mask to <ref:name> using the subject segment —
// the ref identifier never appears in operator output.
func display(v reflect.Value, wild string) string {
	if secretPath(wild) {
		seg := strings.Split(wild, ".")
		if len(seg) >= 2 {
			return "<ref:" + seg[len(seg)-2] + ">"
		}
		return "<ref>"
	}
	switch v.Kind() {
	case reflect.Slice:
		parts := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			parts = append(parts, fmt.Sprint(v.Index(i).Interface()))
		}
		return "[" + strings.Join(parts, " ") + "]"
	}
	return fmt.Sprint(v.Interface())
}

func secretPath(wild string) bool {
	return strings.HasSuffix(wild, ".ref") || strings.Contains(wild, ".auth.")
}

// mkChange resolves the consumer for a leaf's wildcard path. A leaf the
// registry does not reach directly falls back to its nearest registered
// ancestor (a per-key map leaf like config.ports.<name> resolves to the
// map leaf config.ports); S0.2 guarantees an ancestor always exists.
func mkChange(path, before, after string) Change {
	wild := wildPath(path)
	eff, name := EffectRebuild, "unconsumed:"+path
	for w := wild; w != ""; {
		if c, ok := Consumers[w]; ok {
			eff, name = c.Effect, c.Name
			break
		}
		i := strings.LastIndexByte(w, '.')
		if i < 0 {
			break
		}
		w = w[:i]
	}
	return Change{Path: path, Before: before, After: after, Effect: eff, Consumer: name}
}

// wildPath replaces concrete map-key segments with "*": the flatten walk
// recorded each leaf's wildcard alongside; derive it back by checking
// which ancestors the registry knows — map positions are the segments
// where Consumers carries "<prefix>.*" entries.
func wildPath(path string) string {
	seg := strings.Split(path, ".")
	for i := 1; i < len(seg); i++ {
		prefix := strings.Join(seg[:i], ".")
		// A segment is a map key when the registry has a wildcard entry
		// under its parent (e.g. stack.services.*.mode) or treats the
		// parent itself as a map leaf (config.ports.<name>).
		if hasWildBelow(prefix) || isMapLeaf(prefix) {
			seg[i] = "*"
		}
	}
	return strings.Join(seg, ".")
}

func hasWildBelow(prefix string) bool {
	for k := range Consumers {
		if strings.HasPrefix(k, prefix+".*.") {
			return true
		}
	}
	return false
}

func isMapLeaf(prefix string) bool {
	if _, ok := Consumers[prefix]; !ok {
		return false
	}
	for k := range Consumers {
		if strings.HasPrefix(k, prefix+".") && k != prefix {
			return false // struct child entries exist -> not a leaf map
		}
	}
	return true
}
