package passport

import (
	"reflect"
	"strings"
	"time"
)

// Effect is the class a field change triggers (ADR-0093 D5, SPEC-0092
// effect table): live applies in the open session, respawn on the next
// runtime start, recreate rebuilds one service container, rebuild
// re-renders images and the compose project.
type Effect string

const (
	EffectLive     Effect = "live"
	EffectRespawn  Effect = "respawn"
	EffectRecreate Effect = "recreate"
	EffectRebuild  Effect = "rebuild"
)

// Consumer binds one schema leaf to the component that reads it and the
// effect class of its change. A field nobody reads is a schema bug, so the
// registry covers every leaf.
type Consumer struct {
	Name   string
	Effect Effect
}

// Consumers is the R0.3 registry keyed by schema leaf path ("*" is the
// dynamic map-key segment). Every leaf names the component that reads it —
// the compose render, the hot-reloaded relay registry, the mtime-reloaded
// admission PEP, the publish-derived ledger/policy stores, the rendered env,
// or the rendered hermes config/supervisor policy; CHG-0262 forbids pending
// names (guarded by TestNoPendingConsumers).
var Consumers = map[string]Consumer{
	// stack -> compose project, build set, supervisor spawn (CHG-0257)
	"stack.identity.user":         {"compose", EffectRebuild},
	"stack.identity.organization": {"compose", EffectRebuild},
	"stack.services":              {"compose", EffectRebuild},
	"stack.services.*.mode":       {"compose", EffectRebuild},

	// config -> effective hermes config, supervisor policy, compose (CHG-0258)
	"config.runtime.timezone":                 {"hermes-config", EffectRespawn},
	"config.runtime.locale":                   {"hermes-config", EffectRespawn},
	"config.runtime.context.compaction":       {"hermes-config", EffectRespawn},
	"config.runtime.context.parallel_lanes":   {"hermes-config", EffectRespawn},
	"config.supervision.idle_stop":            {"supervisor-policy", EffectLive},
	"config.supervision.restart":              {"supervisor-policy", EffectLive},
	"config.supervision.watchers":             {"supervisor-policy", EffectLive},
	"config.supervision.watchers.*.protected": {"supervisor-policy", EffectLive},
	"config.supervision.watchers.*.every":     {"supervisor-policy", EffectLive},
	"config.ports":                            {"compose", EffectRecreate},
	"config.channels":                         {"communication", EffectRecreate},
	"config.channels.*.bot.ref":               {"communication", EffectRecreate},
	"config.channels.*.allowed_users.ref":     {"communication", EffectRecreate},
	"config.channels.*.voice.in":              {"communication", EffectRecreate},
	"config.channels.*.voice.out":             {"communication", EffectRecreate},
	"config.memory.backend":                   {"hermes-config", EffectRespawn},
	// The nightly rollup schedule rides the published config doc the
	// supervisor reads for each space (CHG-0206 anchors the per-space
	// scheduler in internal/supervisor; the rendered policy file is its
	// live input channel, the doc is the schedule's authority).
	"config.memory.nightly_rollup": {"supervisor", EffectRespawn},

	// models -> relay registry (hot-reloaded live) / service set (recreate)
	"models.default":                           {"model-relay", EffectLive},
	"models.routes":                            {"model-relay", EffectLive},
	"models.routes.*.ladder":                   {"model-relay", EffectLive},
	"models.services":                          {"model-relay", EffectRecreate},
	"models.services.*.via":                    {"model-relay", EffectRecreate},
	"models.services.*.endpoint":               {"model-relay", EffectRecreate},
	"models.services.*.auth.upstream.kind":     {"model-relay", EffectRecreate},
	"models.services.*.auth.upstream.ref":      {"model-relay", EffectRecreate},
	"models.services.*.auth.proxy_access.kind": {"model-relay", EffectRecreate},
	"models.services.*.auth.proxy_access.ref":  {"model-relay", EffectRecreate},
	"models.services.*.sharing":                {"model-relay", EffectRecreate},
	"models.services.*.limits":                 {"model-relay", EffectLive},
	"models.services.*.models":                 {"model-relay", EffectLive},

	// workspace -> file-tool PEP (mtime reload), supervisor mount plan
	"workspace.tree":            {"mount-plan", EffectRespawn},
	"workspace.writable":        {"file-admission", EffectLive},
	"workspace.read_only":       {"file-admission", EffectLive},
	"workspace.mounts":          {"mount-plan", EffectRespawn},
	"workspace.mounts.*.source": {"mount-plan", EffectRespawn},
	"workspace.mounts.*.mode":   {"mount-plan", EffectRespawn},

	// tools -> ToolHub admission policy (publish-derived) + runtime env (via)
	"tools.core":                           {"tool-policy", EffectLive},
	"tools.core.*.via":                     {"runtime-env", EffectRespawn},
	"tools.core.*.tools.include":           {"tool-policy", EffectLive},
	"tools.core.*.tools.exclude":           {"tool-policy", EffectLive},
	"tools.core.*.access":                  {"tool-policy", EffectLive},
	"tools.core.*.scope.paths":             {"tool-policy", EffectLive},
	"tools.core.*.scope.hosts":             {"tool-policy", EffectLive},
	"tools.core.*.scope.urls":              {"tool-policy", EffectLive},
	"tools.core.*.rules":                   {"tool-policy", EffectLive},
	"tools.core.*.rules.*.scope.paths":     {"tool-policy", EffectLive},
	"tools.core.*.rules.*.scope.hosts":     {"tool-policy", EffectLive},
	"tools.core.*.rules.*.scope.urls":      {"tool-policy", EffectLive},
	"tools.extended":                       {"tool-policy", EffectLive},
	"tools.extended.*.via":                 {"runtime-env", EffectRespawn},
	"tools.extended.*.tools.include":       {"tool-policy", EffectLive},
	"tools.extended.*.tools.exclude":       {"tool-policy", EffectLive},
	"tools.extended.*.access":              {"tool-policy", EffectLive},
	"tools.extended.*.scope.paths":         {"tool-policy", EffectLive},
	"tools.extended.*.scope.hosts":         {"tool-policy", EffectLive},
	"tools.extended.*.scope.urls":          {"tool-policy", EffectLive},
	"tools.extended.*.rules":               {"tool-policy", EffectLive},
	"tools.extended.*.rules.*.scope.paths": {"tool-policy", EffectLive},
	"tools.extended.*.rules.*.scope.hosts": {"tool-policy", EffectLive},
	"tools.extended.*.rules.*.scope.urls":  {"tool-policy", EffectLive},

	// grants -> PDP ledger snapshot (DeriveSpaceRights at publish)
	"grants.allow": {"rights-ledger", EffectLive},
}

// schemaLeaves enumerates every leaf path of the six v2 bodies. A map is
// itself a leaf (its key set is a value); a map whose values are structs
// also recurses as <path>.*.<field>. Slices and scalars are atomic leaves.
// A registry key that is not a computed leaf — or a leaf with no key — is
// a schema gap the test suite fails on.
func schemaLeaves() map[string]bool {
	out := map[string]bool{}
	bodies := map[string]reflect.Type{
		"stack":     reflect.TypeOf(Stack{}),
		"config":    reflect.TypeOf(Config{}),
		"models":    reflect.TypeOf(Models{}),
		"workspace": reflect.TypeOf(Workspace{}),
		"tools":     reflect.TypeOf(Tools{}),
		"grants":    reflect.TypeOf(Grants{}),
	}
	for _, doc := range Names {
		leafPaths(doc, bodies[doc], out)
	}
	return out
}

func leafPaths(prefix string, t reflect.Type, out map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) {
			out[prefix] = true
			return
		}
		for i := 0; i < t.NumField(); i++ {
			if key := yamlKey(t.Field(i)); key != "" {
				leafPaths(prefix+"."+key, t.Field(i).Type, out)
			}
		}
	case reflect.Map:
		out[prefix] = true
		et := t.Elem()
		for et.Kind() == reflect.Pointer {
			et = et.Elem()
		}
		if et.Kind() == reflect.Struct && et != reflect.TypeOf(time.Time{}) {
			leafPaths(prefix+".*", et, out)
		}
	default:
		out[prefix] = true
	}
}

// yamlKey resolves the field's YAML name exactly as yaml.v3 does; "-" drops
// the field from the schema entirely.
func yamlKey(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	switch name {
	case "-":
		return ""
	case "":
		return strings.ToLower(f.Name)
	}
	return name
}
