// Compile-time checks for the v2 normative bodies (ADR-0093 D2, SPEC-0092
// R3-R7). The source vocabulary mirrors the emitted shape: layers write
// the normative structure directly, and a keyset-narrow class lets a
// ceiling layer bound which keys deeper layers may write under a map.
package passport

import (
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/catalog"
)

// classKeysetNarrow marks map-subtree leaves (registry key "map.*"): any
// leaf under <map>.<key>.<rest> is writable, but when a ceiling layer
// (platform or org) declares keys under <map>, deeper layers may only
// write inside that key set.
const classKeysetNarrow fieldClass = 4

// leafClass resolves the field class of a dotted leaf: exact key first,
// then the longest "<prefix>.*" wildcard — the match whose prefix is
// deepest wins, so "supervision.watchers.*" beats "supervision.*".
func leafClass(doc, path string) (fieldClass, string, bool) {
	rules := classes[doc]
	if c, ok := rules[path]; ok {
		return c, path, true
	}
	segs := strings.Split(path, ".")
	for i := len(segs) - 1; i > 0; i-- {
		prefix := strings.Join(segs[:i], ".")
		if c, ok := rules[prefix+".*"]; ok {
			return c, prefix, true
		}
	}
	return 0, "", false
}

// keysetBounds collects the key sets ceiling layers (platform and org)
// declare under each keyset-narrow map; an empty bound means the field is
// unbounded and any layer may write keys. The org ceiling narrows the
// platform bound: where it declares the path it must stay inside it and
// its (smaller) key set becomes the bound for deeper layers.
func keysetBounds(layers []Layer) (map[string]map[string]bool, error) {
	bounds := map[string]map[string]bool{}
	for i, l := range layers {
		if i != 0 && i != 2 {
			continue
		}
		orgDeclared := map[string]map[string]bool{}
		for doc, tree := range l.Docs {
			leaves := map[string]any{}
			flatten(tree, "", leaves)
			for p := range leaves {
				class, base, ok := leafClass(doc, p)
				if !ok || class != classKeysetNarrow {
					continue
				}
				key := keyAfter(base, p)
				if key == "" {
					continue
				}
				full := doc + "." + base
				if i == 2 {
					if bounds[full] != nil && !bounds[full][key] {
						return nil, fmt.Errorf("passport compile: org ceiling %q widens %s.%s beyond the platform ceiling", l.Name, doc, p)
					}
					if orgDeclared[full] == nil {
						orgDeclared[full] = map[string]bool{}
					}
					orgDeclared[full][key] = true
					continue
				}
				if bounds[full] == nil {
					bounds[full] = map[string]bool{}
				}
				bounds[full][key] = true
			}
		}
		for full, set := range orgDeclared {
			if bounds[full] != nil {
				bounds[full] = set
			}
		}
	}
	return bounds, nil
}

// keyAfter returns the map key a leaf writes under a bound base:
// "services.cliproxy-1.mode" with base "services" -> "cliproxy-1".
func keyAfter(base, path string) string {
	rest := strings.TrimPrefix(path, base+".")
	if rest == path || rest == "" {
		return ""
	}
	return strings.SplitN(rest, ".", 2)[0]
}

// ---- stack (R3) ----

// serviceModes maps the catalog `instances` class to the modes a service
// instance may take (ADR-0093 D6).
var serviceModes = map[string][]string{
	"per-runtime":      {"supervised"},
	"per-stack":        {"shared", "per-space"},
	"shared|dedicated": {"shared", "per-space"},
	"per-space":        {"per-space"},
	"per-space-env":    {"per-space"},
}

// intPort normalizes a YAML-decoded port value; non-numeric input is 0.
func intPort(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		p, _ := strconv.Atoi(strings.TrimSpace(n))
		return p
	}
	return 0
}

type instInfo struct {
	id   string
	mode string
	svc  catalog.Service
}

// resolveInstance maps an instance name to its catalog service: the exact
// name or the longest hyphen-prefix that is a cataloged service
// (cliproxy-google-1 -> cliproxy).
func resolveInstance(docs catalog.Docs, name string) (instInfo, bool) {
	for base := name; base != ""; {
		if svc, err := docs.Service(base); err == nil {
			return instInfo{id: base, svc: svc}, true
		}
		i := strings.LastIndexByte(base, '-')
		if i < 0 {
			break
		}
		base = base[:i]
	}
	return instInfo{}, false
}

// checkStack validates service instances, modes and derived dependencies,
// and returns instance resolution for cross-doc checks (models endpoints).
func checkStack(bodies map[string]map[string]any, docs catalog.Docs) (map[string]instInfo, error) {
	out := map[string]instInfo{}
	svcs, _ := bodies["stack"]["services"].(map[string]any)
	for name, raw := range svcs {
		spec, _ := raw.(map[string]any)
		mode := fmt.Sprint(spec["mode"])
		inst, ok := resolveInstance(docs, name)
		if !ok {
			return nil, fmt.Errorf("passport compile: stack.services.%s resolves to no catalog service", name)
		}
		allowed := serviceModes[inst.svc.Instances]
		if len(allowed) == 0 {
			allowed = []string{"per-space"}
		}
		if !slices.Contains(allowed, mode) {
			return nil, fmt.Errorf("passport compile: stack.services.%s mode %q not allowed for %s instances (%s)", name, mode, inst.svc.Instances, strings.Join(allowed, "|"))
		}
		inst.mode = mode
		out[name] = inst
	}
	// A space without hermes-runtime is simply unmanaged — the runtime's
	// presence, not a mode flag, is what makes a space managed.
	for name, inst := range out {
		for _, dep := range inst.svc.DependsOn {
			satisfied := false
			for _, other := range out {
				if other.id == dep {
					satisfied = true
					break
				}
			}
			if !satisfied {
				return nil, fmt.Errorf("passport compile: service %q runs without its dependency %q", name, dep)
			}
		}
	}
	return out, nil
}

// ---- workspace (R5) ----

func checkWorkspace(bodies map[string]map[string]any) error {
	ws := bodies["workspace"]
	readonly := map[string]bool{}
	for _, p := range asList(ws["read_only"]) {
		readonly[fmt.Sprint(p)] = true
	}
	for _, p := range asList(ws["writable"]) {
		s := fmt.Sprint(p)
		if clean := path.Clean(s); clean != s {
			return fmt.Errorf("passport compile: workspace.writable %q is not a canonical path", s)
		}
		// Longest-prefix resolution: a read_only entry at or above the
		// writable path makes it dead — the declaration contradicts itself
		// and is rejected. An ro descendant under a writable parent narrows
		// it legally: read_only wins inside.
		for ro := range readonly {
			if pathWithin(s, ro) {
				return fmt.Errorf("passport compile: workspace path %q is writable under read-only %s", s, ro)
			}
		}
	}
	for name, raw := range asMap(ws["mounts"]) {
		m, _ := raw.(map[string]any)
		if clean := path.Clean(name); clean != name || !strings.HasPrefix(name, "/shared/") {
			return fmt.Errorf("passport compile: workspace.mounts target %q must be a canonical path under /shared/", name)
		}
		mode := fmt.Sprint(m["mode"])
		if mode != "ro" && mode != "rw" {
			return fmt.Errorf("passport compile: workspace.mounts.%s mode %q must be ro or rw", name, mode)
		}
		src := fmt.Sprint(m["source"])
		if src == "" || strings.Contains(src, "..") || strings.HasPrefix(src, "/") {
			return fmt.Errorf("passport compile: workspace.mounts.%s has an unsafe source", name)
		}
		// Organization mounts resolve outside the user space — a writable
		// handle on them would let one space write every sibling's mirror.
		if strings.HasPrefix(src, "org:") && mode != "ro" {
			return fmt.Errorf("passport compile: workspace.mounts.%s mounts organization scope — mode must be read-only", name)
		}
	}
	return nil
}

// ---- config (R4) ----

// PortOwners binds each declared host port to the stack service that
// publishes it (SPEC-0092 R4.1): oauth-callback is the runtime's OAuth
// listener, browser-vnc the browser sidecar's noVNC, slack-events the
// shared gateway's Slack ingress. A port naming anything else is a
// compile error.
var PortOwners = map[string]string{
	"oauth-callback": "hermes-runtime",
	"browser-vnc":    "browser",
	"slack-events":   "communication-hub",
}

// channelVocabulary is the set of ingress transports config.channels may
// name; each is also a catalog feature the compile verifies. It mirrors
// the stack ingress adapters until the catalog grows a channel class.
var channelVocabulary = map[string]bool{"telegram": true, "slack_app": true}

func checkConfig(bodies map[string]map[string]any, instances map[string]instInfo, in Inputs) error {
	cfg := bodies["config"]
	claimed := map[string]string{}
	for name, p := range asMap(cfg["ports"]) {
		owner, known := PortOwners[name]
		if !known {
			return fmt.Errorf("passport compile: config.ports.%s is not a declared port name", name)
		}
		inst, ok := instances[owner]
		if !ok {
			return fmt.Errorf("passport compile: config.ports.%s declares a port for %s, which is not in the stack", name, owner)
		}
		if inst.mode == "shared" && !in.RendersInfra {
			return fmt.Errorf("passport compile: config.ports.%s is published by shared %s — only the infra space declares it", name, owner)
		}
		port := intPort(p)
		if port <= 0 || port > 65535 {
			return fmt.Errorf("passport compile: config.ports.%s must be a host port number, got %v", name, p)
		}
		portKey := fmt.Sprint(port)
		if prev, dup := claimed[portKey]; dup {
			return fmt.Errorf("passport compile: host port %s claimed by both %s and %s", portKey, prev, name)
		}
		if space, reserved := in.ReservedPorts[port]; reserved {
			return fmt.Errorf("passport compile: host port %d already published by space %q", port, space)
		}
		claimed[portKey] = name
	}
	for name, raw := range asMap(cfg["channels"]) {
		if !channelVocabulary[name] {
			return fmt.Errorf("passport compile: config.channels.%s is not a known channel", name)
		}
		if _, ok := instances["communication-hub"]; !ok {
			return fmt.Errorf("passport compile: config.channels.%s requires communication-hub in the stack", name)
		}
		ch, _ := raw.(map[string]any)
		for _, key := range []string{"bot", "allowed_users"} {
			if v, set := ch[key]; set {
				m, isMap := v.(map[string]any)
				if !isMap || fmt.Sprint(m["ref"]) == "" {
					return fmt.Errorf("passport compile: config.channels.%s.%s must be {ref: name} — literal credentials are forbidden", name, key)
				}
			}
		}
		if _, hasVoice := ch["voice"]; hasVoice {
			for _, need := range []string{"hub-stt", "hub-tts"} {
				if _, ok := instances[need]; !ok {
					return fmt.Errorf("passport compile: config.channels.%s voice requires %s running", name, need)
				}
			}
		}
	}
	return nil
}

// ---- models (R7) ----

var modelVias = []string{"cliproxy", "openai-compatible"}
var limitKinds = []string{"requests", "tokens", "provider_quota", "spend_usd"}
var reasoningLevels = []string{"none", "low", "medium", "high"}
var authKinds = []string{"oauth-session", "broker-key"}

func checkModels(bodies map[string]map[string]any, instances map[string]instInfo) error {
	m := bodies["models"]
	services := asMap(m["services"])
	routes := asMap(m["routes"])
	if def := fmt.Sprint(m["default"]); def != "" && def != "<nil>" {
		if _, ok := routes[def]; !ok {
			return fmt.Errorf("passport compile: models.default %q is not a route", def)
		}
	}
	for rname, raw := range routes {
		r, _ := raw.(map[string]any)
		for _, hop := range asList(r["ladder"]) {
			if _, ok := services[fmt.Sprint(hop)]; !ok {
				return fmt.Errorf("passport compile: models.routes.%s.ladder names unknown service %q", rname, hop)
			}
		}
	}
	for sname, raw := range services {
		s, _ := raw.(map[string]any)
		via := fmt.Sprint(s["via"])
		if !slices.Contains(modelVias, via) {
			return fmt.Errorf("passport compile: models.services.%s via %q must be %s", sname, via, strings.Join(modelVias, "|"))
		}
		auth, _ := s["auth"].(map[string]any)
		// auth.proxy_access is a Broker contract ref — a {kind, ref} map; a
		// literal string would put credential material in the passport.
		if raw, set := auth["proxy_access"]; set {
			if _, isMap := raw.(map[string]any); !isMap {
				return fmt.Errorf("passport compile: models.services.%s auth.proxy_access must be a {kind, ref} contract reference", sname)
			}
		}
		_, hasProxyAccess := auth["proxy_access"]
		if via == "cliproxy" {
			u, err := url.Parse(fmt.Sprint(s["endpoint"]))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
				return fmt.Errorf("passport compile: models.services.%s endpoint is not an http(s) URL", sname)
			}
			inst, ok := instances[u.Hostname()]
			if !ok || inst.id != "cliproxy" {
				return fmt.Errorf("passport compile: models.services.%s endpoint host %q is not a cliproxy stack instance", sname, u.Hostname())
			}
			if !hasProxyAccess {
				return fmt.Errorf("passport compile: models.services.%s via cliproxy requires auth.proxy_access", sname)
			}
		} else if hasProxyAccess {
			return fmt.Errorf("passport compile: models.services.%s auth.proxy_access only applies to cliproxy services", sname)
		}
		if up, ok := auth["upstream"].(map[string]any); ok {
			if kind := fmt.Sprint(up["kind"]); !slices.Contains(authKinds, kind) {
				return fmt.Errorf("passport compile: models.services.%s auth.upstream.kind %q must be %s", sname, kind, strings.Join(authKinds, "|"))
			}
		}
		for _, l := range asList(s["limits"]) {
			lm, _ := l.(map[string]any)
			if of := fmt.Sprint(lm["of"]); !slices.Contains(limitKinds, of) {
				return fmt.Errorf("passport compile: models.services.%s limit of %q must be %s", sname, of, strings.Join(limitKinds, "|"))
			}
			if per := fmt.Sprint(lm["per"]); per == "" || per == "<nil>" {
				return fmt.Errorf("passport compile: models.services.%s limit requires per", sname)
			}
		}
		for _, md := range asList(s["models"]) {
			mm, _ := md.(map[string]any)
			if rs, set := mm["reasoning"]; set && !slices.Contains(reasoningLevels, fmt.Sprint(rs)) {
				return fmt.Errorf("passport compile: models.services.%s reasoning %q must be %s", sname, rs, strings.Join(reasoningLevels, "|"))
			}
		}
	}
	return nil
}

// ---- tools (R6) ----

const coreBudget = 20

// checkTools validates the core/extended toolset maps against the catalog
// executor contracts, applies the core member budget (an overflowing
// toolset moves whole into extended) and pins discovery in core.
func checkTools(bodies map[string]map[string]any, docs catalog.Docs) error {
	t := bodies["tools"]
	core := asMap(t["core"])
	extended := asMap(t["extended"])
	if dup := intersectKeys(core, extended); len(dup) > 0 {
		return fmt.Errorf("passport compile: toolset %s listed in both core and extended", strings.Join(dup, ", "))
	}
	validate := func(name string, raw any) ([]string, error) {
		ts, err := ResolveToolset(docs, name)
		if err != nil {
			return nil, err
		}
		if ts.Section == "" {
			return nil, fmt.Errorf("passport compile: toolset %q is denied by construction", name)
		}
		spec, _ := raw.(map[string]any)
		via := fmt.Sprint(spec["via"])
		okVia := slices.Contains(ts.Via, via) ||
			(strings.HasPrefix(via, "mcp:") && slices.Contains(ts.Via, "mcp"))
		if !okVia {
			return nil, fmt.Errorf("passport compile: toolset %q via %q is outside its executor surface %v", name, via, ts.Via)
		}
		members := ts.Members
		if len(members) == 0 {
			members = []string{name}
		}
		memberSet := map[string]bool{}
		for _, m := range members {
			memberSet[m] = true
		}
		enforces := slices.Contains(ts.Via, "toolhub") || slices.Contains(ts.Via, "mcp")
		if !enforces {
			for _, k := range []string{"tools", "access", "scope", "rules"} {
				if _, set := spec[k]; set {
					return nil, fmt.Errorf("passport compile: toolset %q cannot enforce %q on a %q surface", name, k, via)
				}
			}
		}
		tools, _ := spec["tools"].(map[string]any)
		inc, hasInc := tools["include"].([]any)
		exc, hasExc := tools["exclude"].([]any)
		if hasInc && hasExc {
			return nil, fmt.Errorf("passport compile: toolset %q uses both include and exclude", name)
		}
		effective := map[string]bool{}
		for _, m := range members {
			effective[m] = true
		}
		if hasInc {
			effective = map[string]bool{}
			for _, m := range inc {
				s := fmt.Sprint(m)
				if !memberSet[s] {
					return nil, fmt.Errorf("passport compile: toolset %q include names unknown member %q", name, s)
				}
				effective[s] = true
			}
		}
		if hasExc {
			for _, m := range exc {
				s := fmt.Sprint(m)
				if !memberSet[s] {
					return nil, fmt.Errorf("passport compile: toolset %q exclude names unknown member %q", name, s)
				}
				delete(effective, s)
			}
		}
		if access, set := spec["access"]; set && fmt.Sprint(access) != "read" {
			return nil, fmt.Errorf("passport compile: toolset %q access %q is unsupported", name, access)
		}
		if scope, set := spec["scope"]; set {
			for k := range asMap(scope) {
				if !slices.Contains([]string{"paths", "hosts", "urls"}, k) {
					return nil, fmt.Errorf("passport compile: toolset %q scope %q is unsupported", name, k)
				}
			}
		}
		for rname := range asMap(spec["rules"]) {
			if !memberSet[rname] {
				return nil, fmt.Errorf("passport compile: toolset %q rule names unknown member %q", name, rname)
			}
		}
		out := make([]string, 0, len(effective))
		for m := range effective {
			out = append(out, m)
		}
		slices.Sort(out)
		return out, nil
	}
	type entry struct {
		name    string
		members []string
	}
	var coreEntries []entry
	for name, raw := range core {
		members, err := validate(name, raw)
		if err != nil {
			return err
		}
		coreEntries = append(coreEntries, entry{name, members})
	}
	for name, raw := range extended {
		if _, err := validate(name, raw); err != nil {
			return err
		}
	}
	if extended == nil {
		extended = map[string]any{}
	}
	if core == nil {
		core = map[string]any{}
	}
	slices.SortFunc(coreEntries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	count := 0
	for _, e := range coreEntries {
		if count+len(e.members) > coreBudget {
			extended[e.name] = core[e.name]
			delete(core, e.name)
			continue
		}
		count += len(e.members)
	}
	core["discovery"] = map[string]any{"via": "toolhub"}
	bodies["tools"] = map[string]any{"core": core, "extended": extended}
	return nil
}

// resolveToolset finds the catalog toolset a declared name refers to:
// exact first, then the hub: and native: namespaces. An ambiguous short
// name fails closed rather than binding the wrong executor.
// ResolveToolset resolves a passport toolset name to its catalog entry —
// full section id first, then the unambiguous hub:/native: short name.
func ResolveToolset(docs catalog.Docs, name string) (catalog.Toolset, error) {
	ts, err := docs.Toolset(name)
	if err == nil {
		return ts, nil
	}
	var found *catalog.Toolset
	var canon string
	for _, cand := range []string{"hub:" + name, "native:" + name} {
		if t, err := docs.Toolset(cand); err == nil {
			if found != nil {
				return catalog.Toolset{}, fmt.Errorf("passport compile: toolset %q is ambiguous (%s and %s)", name, canon, cand)
			}
			cp := t
			found, canon = &cp, cand
		}
	}
	if found == nil {
		return catalog.Toolset{}, fmt.Errorf("passport compile: tools: unknown toolset %q", name)
	}
	return *found, nil
}

// ---- completeness (anti-degradation) ----

// checkComplete refuses to publish a skeleton: a managed revision — one
// that runs hermes-runtime — must carry real content in every contract
// area. An unmanaged space may legitimately be minimal, so the guard only
// fires where an admitted runtime exists.
func checkComplete(bodies map[string]map[string]any, instances map[string]instInfo) error {
	if _, managed := instances["hermes-runtime"]; !managed {
		return nil
	}
	identity, _ := bodies["stack"]["identity"].(map[string]any)
	if user := fmt.Sprint(identity["user"]); user == "" || user == "<nil>" {
		return fmt.Errorf("passport compile: managed revision without stack.identity.user")
	}
	if len(bodies["config"]) == 0 {
		return fmt.Errorf("passport compile: managed revision has an empty config passport")
	}
	if _, hasRelay := instances["model-relay"]; hasRelay {
		if len(asMap(bodies["models"]["services"])) == 0 {
			return fmt.Errorf("passport compile: model-relay runs but models.services is empty")
		}
		if len(asMap(bodies["models"]["routes"])) == 0 {
			return fmt.Errorf("passport compile: model-relay runs but models.routes is empty")
		}
	}
	tools := bodies["tools"]
	nonDiscovery := 0
	for _, area := range []string{"core", "extended"} {
		for name := range asMap(tools[area]) {
			if name != "discovery" {
				nonDiscovery++
			}
		}
	}
	if nonDiscovery == 0 {
		return fmt.Errorf("passport compile: managed revision admits no toolsets beyond discovery")
	}
	return nil
}

// ---- shared helpers ----

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func intersectKeys(a, b map[string]any) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
