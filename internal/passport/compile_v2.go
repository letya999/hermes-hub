// Compile-time checks for the v2 normative bodies (ADR-0093 D2, SPEC-0092
// R3-R7). The source vocabulary mirrors the emitted shape: layers write
// the normative structure directly, and a keyset-narrow class lets a
// ceiling layer bound which keys deeper layers may write under a map.
package passport

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/exposure"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/selfsettings"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
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
	if raw, ok := ws["goal"]; ok {
		if _, ok := raw.(string); !ok {
			return fmt.Errorf("passport compile: workspace.goal must be a string")
		}
	}
	if raw, ok := ws["bootstrap_revision"]; ok {
		if v, ok := raw.(int); !ok || v < 0 {
			return fmt.Errorf("passport compile: workspace.bootstrap_revision must be a non-negative integer")
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
	// knowledge-ui is the loopback Knowledge editor edge (SPEC-0085 R10.1):
	// open-knowledge is feature-rendered, so its "instance" is the tools
	// doc's knowledge_ui marker rather than a declared stack service.
	"knowledge-ui": "open-knowledge",
	// identity-hub publishes the bundled IdP's external port (the port the
	// iam issuer points at); absent it derives the dev default.
	"identity-hub": "identity-hub",
}

// channelVocabulary is the set of ingress transports config.channels may
// name; each is also a catalog feature the compile verifies. It mirrors
// the stack ingress adapters until the catalog grows a channel class.
var channelVocabulary = map[string]bool{"telegram": true, "slack_app": true}

// envRefPattern names a secrets variable — the provider token_ref vocabulary
// never carries literal credentials.
var envRefPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func checkConfig(bodies map[string]map[string]any, instances map[string]instInfo, in Inputs) error {
	cfg := bodies["config"]
	memory := asMap(cfg["memory"])
	if enabled, set := memory["enabled"]; set {
		if _, ok := enabled.(bool); !ok {
			return fmt.Errorf("passport compile: config.memory.enabled must be a boolean")
		}
	}
	if raw, set := memory["nightly_rollup"]; set {
		value, ok := raw.(string)
		parsed, err := time.Parse("15:04", value)
		if !ok || err != nil || parsed.Format("15:04") != value {
			return fmt.Errorf("passport compile: config.memory.nightly_rollup must be HH:MM")
		}
	}
	if raw, set := memory["leaf_retention_days"]; set {
		value, ok := raw.(int)
		if !ok || value < 0 || value == 1 {
			return fmt.Errorf("passport compile: config.memory.leaf_retention_days must be 0 or >=2")
		}
	}
	if raw, set := memory["leaf_fold_bytes"]; set {
		value, ok := raw.(int)
		if !ok || value < 0 {
			return fmt.Errorf("passport compile: config.memory.leaf_fold_bytes must be >=0")
		}
	}
	if raw, set := memory["provider"]; set {
		provider, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("passport compile: config.memory.provider must be a map")
		}
		adapter, _ := provider["adapter"].(string)
		if adapter != "hindsight" {
			return fmt.Errorf("passport compile: config.memory.provider.adapter %q is not a supported adapter (supported: hindsight)", adapter)
		}
		rawURL, _ := provider["url"].(string)
		u, err := url.Parse(rawURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("passport compile: config.memory.provider.url %q is not an absolute http(s) endpoint", rawURL)
		}
		if env, set := provider["token_env"]; set {
			if name, ok := env.(string); !ok || !envRefPattern.MatchString(name) {
				return fmt.Errorf("passport compile: config.memory.provider.token_env must name a secrets variable")
			}
		}
	}
	if raw, set := cfg["hosting"]; set {
		hosting, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("passport compile: config.hosting must be a map")
		}
		slots := asList(hosting["slots"])
		if len(slots) > 4 {
			return fmt.Errorf("passport compile: config.hosting.slots must fit the shared exposure cap of 4")
		}
		seen := map[string]bool{}
		for i, raw := range slots {
			slot, ok := raw.(string)
			if !ok || !exposure.ValidSiteSlot(slot) || seen[slot] {
				return fmt.Errorf("passport compile: config.hosting.slots[%d] must be a unique safe site label", i)
			}
			seen[slot] = true
		}
		image, _ := hosting["image"].(string)
		if image != "" {
			if err := api.ValidPinnedImageRef(image); err != nil {
				return fmt.Errorf("passport compile: config.hosting.image: %w", err)
			}
			if runtime, _ := hosting["runtime"].(string); runtime != "runsc" && runtime != "kata" {
				return fmt.Errorf("passport compile: config.hosting.runtime must be runsc or kata")
			}
		}
	}
	if raw, set := cfg["image_gen"]; set {
		doc, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("passport compile: config.image_gen must be a map")
		}
		str := func(key string) string {
			v, _ := doc[key].(string)
			return v
		}
		gen := media.ImageGen{Provider: str("provider"), Model: str("model"), Delivery: str("delivery")}
		if _, err := gen.Normalize(); err != nil {
			return fmt.Errorf("passport compile: config.image_gen: %w", err)
		}
	}
	if raw, set := cfg["iam"]; set {
		if err := checkIAM(raw, instances); err != nil {
			return err
		}
	}
	context := asMap(asMap(cfg["runtime"])["context"])
	engines := map[string]bool{}
	for i, raw := range asList(context["engines"]) {
		engine, ok := raw.(string)
		if !ok || (engine != "hub" && engine != "native") {
			return fmt.Errorf("passport compile: config.runtime.context.engines[%d] must be hub or native", i)
		}
		engines[engine] = true
	}
	if compaction, _ := context["compaction"].(string); compaction != "" && compaction != "auto" && len(engines) > 0 && !engines[compaction] {
		return fmt.Errorf("passport compile: config.runtime.context.compaction %q is outside the granted engines", compaction)
	}
	goals := asMap(asMap(cfg["runtime"])["goals"])
	if engine, set := goals["engine"]; set {
		v, _ := engine.(string)
		if v != "auto" && v != "hub" && v != "native" {
			return fmt.Errorf("passport compile: config.runtime.goals.engine must be auto, hub or native")
		}
	}
	if judge, set := goals["judge"]; set {
		v, _ := judge.(string)
		if v != "auto" && v != "aux" && v != "main" && v != "self" {
			return fmt.Errorf("passport compile: config.runtime.goals.judge must be auto, aux, main or self")
		}
	}
	if raw, set := goals["max_turns"]; set {
		v, ok := raw.(int)
		if !ok || v < 0 || v > 50 {
			return fmt.Errorf("passport compile: config.runtime.goals.max_turns must be 1..50")
		}
	}
	if err := checkCompressionLeaves(asMap(asMap(cfg["runtime"])["compression"]), "compression"); err != nil {
		return err
	}
	for _, key := range []string{"summary_model", "judge_model"} {
		if raw, set := asMap(asMap(cfg["runtime"])["auxiliary"])[key]; set {
			v, _ := raw.(string)
			if !identity.ValidID(v) {
				return fmt.Errorf("passport compile: config.runtime.auxiliary.%s must be a logical model identifier", key)
			}
		}
	}
	claimed := map[string]string{}
	for name, p := range asMap(cfg["ports"]) {
		owner, known := PortOwners[name]
		if !known {
			return fmt.Errorf("passport compile: config.ports.%s is not a declared port name", name)
		}
		inst, ok := instances[owner]
		if !ok {
			// open-knowledge renders from the tools doc's knowledge_ui
			// marker, not a declared stack service — the feature marker is
			// its instance evidence.
			if owner != "open-knowledge" || !nativeToolsetEnabled(bodies["tools"], "knowledge_ui") {
				return fmt.Errorf("passport compile: config.ports.%s declares a port for %s, which is not in the stack", name, owner)
			}
		} else if inst.mode == "shared" && !in.RendersInfra {
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

// checkIAM validates config.iam at compile time (ADR-0100): external is a
// reachable OIDC provider with a client_id and no bundled services;
// bundled renders the identity-hub/hub-postgres pair it declared shared.
// The stack projection re-verifies the same contract at render.
func checkIAM(raw any, instances map[string]instInfo) error {
	block, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("passport compile: config.iam must be a map")
	}
	mode, _ := block["mode"].(string)
	if mode != "external" && mode != "bundled" {
		return fmt.Errorf("passport compile: config.iam.mode must be external or bundled")
	}
	if iss, _ := block["issuer"].(string); iss != "" {
		u, err := url.Parse(iss)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("passport compile: config.iam.issuer is not an absolute http(s) URL")
		}
		if mode == "bundled" && u.Path != "" && u.Path != "/" {
			return fmt.Errorf("passport compile: config.iam.issuer bundled identity hub serves a host-level issuer, no path")
		}
	}
	_, hasIDH := instances["identity-hub"]
	_, hasPG := instances["hub-postgres"]
	if mode == "external" {
		if hasIDH || hasPG {
			return fmt.Errorf("passport compile: config.iam.mode external cannot run the bundled identity-hub/hub-postgres services")
		}
		if iss, _ := block["issuer"].(string); strings.TrimSpace(iss) == "" {
			return fmt.Errorf("passport compile: config.iam.issuer is required with mode external")
		}
		if c, _ := block["client_id"].(string); strings.TrimSpace(c) == "" {
			return fmt.Errorf("passport compile: config.iam.client_id is required with mode external")
		}
		return nil
	}
	// bundled
	if !hasIDH || !hasPG {
		return fmt.Errorf("passport compile: config.iam.mode bundled requires stack services identity-hub and hub-postgres")
	}
	if instances["identity-hub"].mode != "shared" || instances["hub-postgres"].mode != "shared" {
		return fmt.Errorf("passport compile: config.iam.mode bundled requires identity-hub and hub-postgres with mode shared")
	}
	return nil
}

// checkCompressionLeaves walks the declared compression map and validates
// every leaf against the selfsettings allowlist — unknown paths or
// out-of-range values fail the whole compile with a named error.
func checkCompressionLeaves(section map[string]any, prefix string) error {
	for key, value := range section {
		if strings.ContainsAny(key, ".\x00\r\n") {
			return fmt.Errorf("passport compile: config.runtime.%s: invalid key %q", prefix, key)
		}
		leaf := prefix + "." + key
		if nested, ok := value.(map[string]any); ok {
			if err := checkCompressionLeaves(nested, leaf); err != nil {
				return err
			}
			continue
		}
		if _, err := selfsettings.ValidatePath(leaf, value); err != nil {
			return fmt.Errorf("passport compile: config.runtime.%s: %w", leaf, err)
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

// platformPinnedMembers is the frozen platform layer (ADR-0094 D1): six
// model tools pinned into every space's core, never operator-selectable.
var platformPinnedMembers = []string{
	"platform_bootstrap", "platform_guide", "platform_status", "platform_usage",
	"tool_call", "tool_search",
}

// checkTools validates the core/extended toolset maps against the catalog
// executor contracts, applies the core member budget (an overflowing
// toolset moves whole into extended) and pins the platform layer in core.
func checkTools(bodies map[string]map[string]any, docs catalog.Docs) error {
	t := bodies["tools"]
	toolSpecs := map[string]api.ToolSpec{}
	for _, tool := range agenttools.HubToolsDefinition().Tools {
		toolSpecs[tool.Name] = tool
	}
	checkScope := func(name string, raw any) error {
		scope, ok := raw.(map[string]any)
		if !ok || len(scope) != 1 {
			return fmt.Errorf("passport compile: toolset %q has unsupported scope", name)
		}
		values, ok := scope["paths"].([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("passport compile: toolset %q has unsupported scope", name)
		}
		for _, rawPath := range values {
			value, ok := rawPath.(string)
			if !ok || !api.ValidSelectionPath(value) ||
				value != "/workspace" && value != "/archive" && value != "/organization" &&
					!strings.HasPrefix(value, "/workspace/") && !strings.HasPrefix(value, "/archive/") && !strings.HasPrefix(value, "/organization/") {
				return fmt.Errorf("passport compile: toolset %q has invalid path scope", name)
			}
		}
		return nil
	}
	if raw, present := t["binding"]; present {
		binding, ok := raw.(map[string]any)
		if !ok || len(binding) != 2 {
			return fmt.Errorf("passport compile: tools.binding requires profile_id and generation")
		}
		profileID, ok := binding["profile_id"].(string)
		if !ok || !identity.ValidID(profileID) {
			return fmt.Errorf("passport compile: tools.binding.profile_id is invalid")
		}
		generation, ok := binding["generation"].(int)
		if !ok || generation < 1 {
			return fmt.Errorf("passport compile: tools.binding.generation must be positive")
		}
	}
	core := asMap(t["core"])
	extended := asMap(t["extended"])
	if dup := intersectKeys(core, extended); len(dup) > 0 {
		return fmt.Errorf("passport compile: toolset %s listed in both core and extended", strings.Join(dup, ", "))
	}
	validate := func(name string, raw any) ([]string, error) {
		spec, _ := raw.(map[string]any)
		via := fmt.Sprint(spec["via"])
		if via == "off" {
			// A deny needs no catalog surface: it may name a connector or an
			// organization catalog server the member narrows out, and an off
			// entry takes no selector fields at all.
			for _, k := range []string{"tools", "access", "scope", "rules", "server", "except", "only"} {
				if _, set := spec[k]; set {
					return nil, fmt.Errorf("passport compile: toolset %q via off takes no %q", name, k)
				}
			}
			return nil, nil
		}
		ts, err := ResolveToolset(docs, name)
		if err != nil {
			return nil, err
		}
		if ts.Section == "" {
			return nil, fmt.Errorf("passport compile: toolset %q is denied by construction", name)
		}
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
			if via != "toolhub" {
				return nil, fmt.Errorf("passport compile: toolset %q cannot enforce scope on %q", name, via)
			}
			if err := checkScope(name, scope); err != nil {
				return nil, err
			}
		}
		rules, hasRules := spec["rules"]
		ruleMap, ok := rules.(map[string]any)
		if hasRules && !ok {
			return nil, fmt.Errorf("passport compile: toolset %q has unsupported rules", name)
		}
		for rname, rawRule := range ruleMap {
			if !memberSet[rname] {
				return nil, fmt.Errorf("passport compile: toolset %q rule names unknown member %q", name, rname)
			}
			rule, ok := rawRule.(map[string]any)
			if !ok || len(rule) != 1 {
				return nil, fmt.Errorf("passport compile: toolset %q rule %q is unsupported", name, rname)
			}
			if err := checkScope(name+"."+rname, rule["scope"]); err != nil {
				return nil, err
			}
			tool, ok := toolSpecs[rname]
			if !ok || tool.Executor != "handler:files" || len(tool.Uses) == 0 {
				return nil, fmt.Errorf("passport compile: toolset %q tool %q cannot enforce path scope", name, rname)
			}
			for _, use := range tool.Uses {
				if use.PathArgument != "path" && use.PathArgument != "paths" {
					return nil, fmt.Errorf("passport compile: toolset %q tool %q cannot enforce path scope", name, rname)
				}
			}
		}
		if spec["scope"] != nil || len(ruleMap) > 0 {
			if via != "toolhub" {
				return nil, fmt.Errorf("passport compile: toolset %q cannot enforce scope on %q", name, via)
			}
			for member := range effective {
				if spec["scope"] == nil && ruleMap[member] == nil {
					continue
				}
				tool, ok := toolSpecs[member]
				if ok && spec["access"] == "read" && tool.Effect != api.ReadEffect && ruleMap[member] == nil {
					continue
				}
				if spec["scope"] != nil && ruleMap[member] != nil {
					outer := spec["scope"].(map[string]any)["paths"].([]any)
					inner := ruleMap[member].(map[string]any)["scope"].(map[string]any)["paths"].([]any)
					overlaps := false
					for _, a := range outer {
						for _, b := range inner {
							left, right := a.(string), b.(string)
							if left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/") {
								overlaps = true
							}
						}
					}
					if !overlaps {
						return nil, fmt.Errorf("passport compile: toolset %q tool %q has disjoint path scopes", name, member)
					}
				}
				if !ok || tool.Executor != "handler:files" || len(tool.Uses) == 0 {
					return nil, fmt.Errorf("passport compile: toolset %q tool %q cannot enforce path scope", name, member)
				}
				for _, use := range tool.Uses {
					if use.PathArgument != "path" && use.PathArgument != "paths" {
						return nil, fmt.Errorf("passport compile: toolset %q tool %q cannot enforce path scope", name, member)
					}
				}
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
	if extended == nil {
		extended = map[string]any{}
	}
	if core == nil {
		core = map[string]any{}
	}
	var coreEntries []entry
	for name, raw := range core {
		members, err := validate(name, raw)
		if err != nil {
			return err
		}
		// Uncurated surfaces (raw MCP catalogs) never enter core (ADR-0094
		// R6): an operator writing one into core gets the honest demotion,
		// not a compile error — the projection must exist for the runtime
		// to enumerate its declared servers at all.
		if ts, rerr := ResolveToolset(docs, name); rerr == nil && ts.Uncurated {
			extended[name] = core[name]
			delete(core, name)
			continue
		}
		coreEntries = append(coreEntries, entry{name, members})
	}
	for name, raw := range extended {
		if _, err := validate(name, raw); err != nil {
			return err
		}
	}
	slices.SortFunc(coreEntries, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	// The pinned platform family (ADR-0094): always in core, never
	// operator-selectable, and its six members count inside the same
	// budget — the model surface never exceeds coreBudget tools.
	count := len(platformPinnedMembers)
	for _, e := range coreEntries {
		if count+len(e.members) > coreBudget {
			extended[e.name] = core[e.name]
			delete(core, e.name)
			continue
		}
		count += len(e.members)
	}
	core["platform"] = map[string]any{"via": "toolhub"}
	bodies["tools"] = map[string]any{"core": core, "extended": extended}
	if binding, present := t["binding"]; present {
		bodies["tools"]["binding"] = binding
	}
	if connectors, present := t["connectors"]; present {
		servers, ok := connectors.(map[string]any)
		if !ok {
			return fmt.Errorf("passport compile: connectors must be a map")
		}
		if err := checkConnectors(servers, docs); err != nil {
			return err
		}
		bodies["tools"]["connectors"] = connectors
	}
	return nil
}

// checkConnectors applies the raw-MCP definition contract before
// publication: exactly one of url/command, transport-consistent fields
// and names that cannot shadow a catalog toolset or reviewed connector.
func checkConnectors(servers map[string]any, docs catalog.Docs) error {
	// Namespaced toolsets never resolve bare through docs.Toolset; the
	// reserved set covers them so a connector cannot mint a shadow surface.
	reserved := map[string]bool{"hub": true, "toolhub": true, "file": true, "todo": true,
		"cronjob": true, "messaging": true, "session_search": true, "google_meet": true,
		"vision": true, "image_gen": true}
	for name, raw := range servers {
		if _, err := docs.Toolset(name); err == nil || !identity.ValidID(name) || reserved[name] {
			return fmt.Errorf("passport compile: connector %q is reserved or invalid", name)
		}
		spec, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("passport compile: connector %q must be a map", name)
		}
		for k := range spec {
			if !slices.Contains([]string{"url", "command", "args", "env", "headers", "tools", "auth", "timeout"}, k) {
				return fmt.Errorf("passport compile: connector %q has unknown field %q", name, k)
			}
		}
		u, _ := spec["url"].(string)
		command, _ := spec["command"].(string)
		if (u == "") == (command == "") {
			return fmt.Errorf("passport compile: connector %s requires exactly one of url/command", name)
		}
		if timeout, ok := spec["timeout"].(int); ok && (timeout < 0 || timeout > 600) {
			return fmt.Errorf("passport compile: connector %s timeout must be 0..600", name)
		}
		auth, _ := spec["auth"].(string)
		if auth != "" && auth != "oauth" {
			return fmt.Errorf("passport compile: connector %s auth must be oauth or empty", name)
		}
		if u != "" {
			parsed, e := url.Parse(u)
			if e != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("passport compile: connector %s has an invalid URL", name)
			}
			if len(asList(spec["args"])) > 0 || len(asMap(spec["env"])) > 0 {
				return fmt.Errorf("passport compile: remote connector %s cannot have stdio args/env", name)
			}
		} else if len(asMap(spec["headers"])) > 0 || auth != "" {
			return fmt.Errorf("passport compile: stdio connector %s cannot have HTTP headers/auth", name)
		}
	}
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
	nonPlatform := 0
	for _, area := range []string{"core", "extended"} {
		for name := range asMap(tools[area]) {
			if name != "platform" {
				nonPlatform++
			}
		}
	}
	if nonPlatform == 0 {
		return fmt.Errorf("passport compile: managed revision admits no toolsets beyond the pinned platform layer")
	}
	return nil
}

// ---- shared helpers ----

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// nativeToolsetEnabled reports whether the tools doc carries a `via:
// native` marker for the named toolset in core or extended — the only
// shape feature-rendered services (open-knowledge, memory vault) take in
// the authored vocabulary.
func nativeToolsetEnabled(tools map[string]any, name string) bool {
	for _, section := range []string{"core", "extended"} {
		if spec := asMap(asMap(tools[section])[name]); spec != nil && spec["via"] == "native" {
			return true
		}
	}
	return false
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
