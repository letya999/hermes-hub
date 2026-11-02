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
// dynamic map-key segment). Nothing consumes the published revision yet —
// pending names mark the stage that wires each leaf (ADR-0093 D1);
// CHG-0262 forbids pending entries.
var Consumers = map[string]Consumer{
	// stack -> compose project, build set, supervisor spawn (CHG-0257)
	"stack.identity.user":         {"pending:CHG-0257", EffectRebuild},
	"stack.identity.organization": {"pending:CHG-0257", EffectRebuild},
	"stack.services":              {"pending:CHG-0257", EffectRebuild},
	"stack.services.*.mode":       {"pending:CHG-0257", EffectRebuild},

	// config -> effective hermes config, supervisor policy, compose (CHG-0258)
	"config.runtime.timezone":                 {"pending:CHG-0258", EffectRespawn},
	"config.runtime.locale":                   {"pending:CHG-0258", EffectRespawn},
	"config.runtime.context.compaction":       {"pending:CHG-0258", EffectRespawn},
	"config.runtime.context.parallel_lanes":   {"pending:CHG-0258", EffectRespawn},
	"config.supervision.idle_stop":            {"pending:CHG-0258", EffectLive},
	"config.supervision.restart":              {"pending:CHG-0258", EffectLive},
	"config.supervision.watchers":             {"pending:CHG-0258", EffectLive},
	"config.supervision.watchers.*.protected": {"pending:CHG-0258", EffectLive},
	"config.supervision.watchers.*.every":     {"pending:CHG-0258", EffectLive},
	"config.ports":                            {"pending:CHG-0258", EffectRecreate},
	"config.channels":                         {"pending:CHG-0258", EffectRecreate},
	"config.channels.*.bot.ref":               {"pending:CHG-0258", EffectRecreate},
	"config.channels.*.allowed_users.ref":     {"pending:CHG-0258", EffectRecreate},
	"config.channels.*.voice.in":              {"pending:CHG-0258", EffectRecreate},
	"config.channels.*.voice.out":             {"pending:CHG-0258", EffectRecreate},
	"config.memory.backend":                   {"pending:CHG-0258", EffectRespawn},
	"config.memory.nightly_rollup":            {"pending:CHG-0258", EffectRespawn},

	// models -> relay registry, broker leases (CHG-0261)
	"models.default":                           {"pending:CHG-0261", EffectLive},
	"models.routes":                            {"pending:CHG-0261", EffectLive},
	"models.routes.*.ladder":                   {"pending:CHG-0261", EffectLive},
	"models.services":                          {"pending:CHG-0261", EffectRecreate},
	"models.services.*.via":                    {"pending:CHG-0261", EffectRecreate},
	"models.services.*.endpoint":               {"pending:CHG-0261", EffectRecreate},
	"models.services.*.auth.upstream.kind":     {"pending:CHG-0261", EffectRecreate},
	"models.services.*.auth.upstream.ref":      {"pending:CHG-0261", EffectRecreate},
	"models.services.*.auth.proxy_access.kind": {"pending:CHG-0261", EffectRecreate},
	"models.services.*.auth.proxy_access.ref":  {"pending:CHG-0261", EffectRecreate},
	"models.services.*.sharing":                {"pending:CHG-0261", EffectRecreate},
	"models.services.*.limits":                 {"pending:CHG-0261", EffectLive},
	"models.services.*.models":                 {"pending:CHG-0261", EffectLive},

	// workspace -> file-tool PEP, supervisor mount plan (CHG-0259)
	"workspace.tree":            {"pending:CHG-0259", EffectRespawn},
	"workspace.writable":        {"pending:CHG-0259", EffectLive},
	"workspace.read_only":       {"pending:CHG-0259", EffectLive},
	"workspace.mounts":          {"pending:CHG-0259", EffectRespawn},
	"workspace.mounts.*.source": {"pending:CHG-0259", EffectRespawn},
	"workspace.mounts.*.mode":   {"pending:CHG-0259", EffectRespawn},

	// tools -> ToolHub projection + hermes disabled_toolsets (CHG-0260)
	"tools.core":                           {"pending:CHG-0260", EffectLive},
	"tools.core.*.via":                     {"pending:CHG-0260", EffectRespawn},
	"tools.core.*.tools.include":           {"pending:CHG-0260", EffectLive},
	"tools.core.*.tools.exclude":           {"pending:CHG-0260", EffectLive},
	"tools.core.*.access":                  {"pending:CHG-0260", EffectLive},
	"tools.core.*.scope.paths":             {"pending:CHG-0260", EffectLive},
	"tools.core.*.scope.hosts":             {"pending:CHG-0260", EffectLive},
	"tools.core.*.scope.urls":              {"pending:CHG-0260", EffectLive},
	"tools.core.*.rules":                   {"pending:CHG-0260", EffectLive},
	"tools.core.*.rules.*.scope.paths":     {"pending:CHG-0260", EffectLive},
	"tools.core.*.rules.*.scope.hosts":     {"pending:CHG-0260", EffectLive},
	"tools.core.*.rules.*.scope.urls":      {"pending:CHG-0260", EffectLive},
	"tools.extended":                       {"pending:CHG-0260", EffectLive},
	"tools.extended.*.via":                 {"pending:CHG-0260", EffectRespawn},
	"tools.extended.*.tools.include":       {"pending:CHG-0260", EffectLive},
	"tools.extended.*.tools.exclude":       {"pending:CHG-0260", EffectLive},
	"tools.extended.*.access":              {"pending:CHG-0260", EffectLive},
	"tools.extended.*.scope.paths":         {"pending:CHG-0260", EffectLive},
	"tools.extended.*.scope.hosts":         {"pending:CHG-0260", EffectLive},
	"tools.extended.*.scope.urls":          {"pending:CHG-0260", EffectLive},
	"tools.extended.*.rules":               {"pending:CHG-0260", EffectLive},
	"tools.extended.*.rules.*.scope.paths": {"pending:CHG-0260", EffectLive},
	"tools.extended.*.rules.*.scope.hosts": {"pending:CHG-0260", EffectLive},
	"tools.extended.*.rules.*.scope.urls":  {"pending:CHG-0260", EffectLive},

	// grants -> PDP ledger snapshot (CHG-0256)
	"grants.allow": {"pending:CHG-0256", EffectLive},
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
