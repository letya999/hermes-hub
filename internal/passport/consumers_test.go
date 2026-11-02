package passport

import "testing"

// R0.3: every schema leaf has a registered consumer and effect class —
// a decorative field cannot be added silently; a registry entry naming a
// nonexistent leaf (including a wildcard that hides one) fails too.
func TestEverySchemaLeafHasConsumer(t *testing.T) {
	leaves := schemaLeaves()
	for leaf := range leaves {
		if _, ok := Consumers[leaf]; !ok {
			t.Errorf("schema leaf %q has no registered consumer", leaf)
		}
	}
	for key := range Consumers {
		if !leaves[key] {
			t.Errorf("registry entry %q is not a schema leaf", key)
		}
	}
	if len(Consumers) != len(leaves) {
		t.Errorf("registry holds %d entries for %d leaves — one must not hide", len(Consumers), len(leaves))
	}
}

// specEffects transcribes the SPEC-0092 effect table leaf by leaf: the
// registry must assign exactly these classes.
var specEffects = map[string]Effect{
	// stack services/mode -> rebuild
	"stack.identity.user":         EffectRebuild,
	"stack.identity.organization": EffectRebuild,
	"stack.services":              EffectRebuild,
	"stack.services.*.mode":       EffectRebuild,

	// config runtime -> respawn; supervision -> live; ports/channels -> recreate
	"config.runtime.timezone":                 EffectRespawn,
	"config.runtime.locale":                   EffectRespawn,
	"config.runtime.context.compaction":       EffectRespawn,
	"config.runtime.context.parallel_lanes":   EffectRespawn,
	"config.supervision.idle_stop":            EffectLive,
	"config.supervision.restart":              EffectLive,
	"config.supervision.watchers":             EffectLive,
	"config.supervision.watchers.*.protected": EffectLive,
	"config.supervision.watchers.*.every":     EffectLive,
	"config.ports":                            EffectRecreate,
	"config.channels":                         EffectRecreate,
	"config.channels.*.bot.ref":               EffectRecreate,
	"config.channels.*.allowed_users.ref":     EffectRecreate,
	"config.channels.*.voice.in":              EffectRecreate,
	"config.channels.*.voice.out":             EffectRecreate,
	"config.memory.backend":                   EffectRespawn,
	"config.memory.nightly_rollup":            EffectRespawn,

	// models default/routes/limits/models -> live; service set + auth -> recreate
	"models.default":                           EffectLive,
	"models.routes":                            EffectLive,
	"models.routes.*.ladder":                   EffectLive,
	"models.services":                          EffectRecreate,
	"models.services.*.via":                    EffectRecreate,
	"models.services.*.endpoint":               EffectRecreate,
	"models.services.*.auth.upstream.kind":     EffectRecreate,
	"models.services.*.auth.upstream.ref":      EffectRecreate,
	"models.services.*.auth.proxy_access.kind": EffectRecreate,
	"models.services.*.auth.proxy_access.ref":  EffectRecreate,
	"models.services.*.sharing":                EffectRecreate,
	"models.services.*.limits":                 EffectLive,
	"models.services.*.models":                 EffectLive,

	// workspace writable/read_only -> live; tree/mounts -> respawn
	"workspace.tree":            EffectRespawn,
	"workspace.writable":        EffectLive,
	"workspace.read_only":       EffectLive,
	"workspace.mounts":          EffectRespawn,
	"workspace.mounts.*.source": EffectRespawn,
	"workspace.mounts.*.mode":   EffectRespawn,

	// tools via toolhub/mcp -> live; via (union, may flip to native) -> respawn
	"tools.core":                           EffectLive,
	"tools.core.*.via":                     EffectRespawn,
	"tools.core.*.tools.include":           EffectLive,
	"tools.core.*.tools.exclude":           EffectLive,
	"tools.core.*.access":                  EffectLive,
	"tools.core.*.scope.paths":             EffectLive,
	"tools.core.*.scope.hosts":             EffectLive,
	"tools.core.*.scope.urls":              EffectLive,
	"tools.core.*.rules":                   EffectLive,
	"tools.core.*.rules.*.scope.paths":     EffectLive,
	"tools.core.*.rules.*.scope.hosts":     EffectLive,
	"tools.core.*.rules.*.scope.urls":      EffectLive,
	"tools.extended":                       EffectLive,
	"tools.extended.*.via":                 EffectRespawn,
	"tools.extended.*.tools.include":       EffectLive,
	"tools.extended.*.tools.exclude":       EffectLive,
	"tools.extended.*.access":              EffectLive,
	"tools.extended.*.scope.paths":         EffectLive,
	"tools.extended.*.scope.hosts":         EffectLive,
	"tools.extended.*.scope.urls":          EffectLive,
	"tools.extended.*.rules":               EffectLive,
	"tools.extended.*.rules.*.scope.paths": EffectLive,
	"tools.extended.*.rules.*.scope.hosts": EffectLive,
	"tools.extended.*.rules.*.scope.urls":  EffectLive,

	// grants allow/revoke -> live (PDP epoch on next call)
	"grants.allow": EffectLive,
}

func TestEffectClassesMatchSpecTable(t *testing.T) {
	for leaf, want := range specEffects {
		c, ok := Consumers[leaf]
		if !ok {
			t.Errorf("spec leaf %q missing from registry", leaf)
			continue
		}
		if c.Effect != want {
			t.Errorf("%s: effect %q, SPEC-0092 wants %q", leaf, c.Effect, want)
		}
		if c.Name == "" {
			t.Errorf("%s: no consumer named", leaf)
		}
	}
}
