package passport

import (
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
)

func v2FixtureInputs(t *testing.T) Inputs {
	t.Helper()
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return Inputs{
		Space: "alice", Environment: "dev", Catalog: docs,
		Now: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		PlatformCeiling: Layer{Name: "platform/ceiling", Version: 1, Docs: map[string]map[string]any{
			"stack": {
				"identity": map[string]any{"user": "alice", "organization": "acme"},
				"services": map[string]any{
					"hermes-runtime":    map[string]any{"mode": "supervised"},
					"toolhub":           map[string]any{"mode": "shared"},
					"model-relay":       map[string]any{"mode": "shared"},
					"communication-hub": map[string]any{"mode": "shared"},
					"credential-broker": map[string]any{"mode": "shared"},
					"supervisor":        map[string]any{"mode": "shared"},
					"cliproxy-google-1": map[string]any{"mode": "per-space"},
				},
			},
			"config": {
				"memory":      map[string]any{"backend": []any{"local", "vault"}},
				"supervision": map[string]any{"idle_stop": "5m", "restart": "on-failure"},
			},
			"models": {
				"services": map[string]any{
					"google-pro-1": map[string]any{"via": "cliproxy"},
				},
			},
			"workspace": {
				"writable":  []any{"/workspace", "/vault"},
				"read_only": []any{"/config", "/skills", "/shared"},
				"layout":    []any{"/workspace/work", "/workspace/inbox"},
				"mounts":    map[string]any{"/shared/acme-docs": map[string]any{"source": "org/acme/docs", "mode": "ro"}},
			},
			"tools": {
				"core":     map[string]any{"files": map[string]any{"via": "toolhub"}},
				"extended": map[string]any{"connectors": map[string]any{"via": "toolhub"}},
			},
			"grants": {"allow": []any{"workspace", "tools"}},
		}},
		PlatformTemplate: Layer{Name: "platform/personal", Version: 3, Docs: map[string]map[string]any{
			"config": {"memory": map[string]any{"backend": "local"}},
		}},
		User: Layer{Name: "alice", Version: 1, Docs: map[string]map[string]any{
			"stack": {"services": map[string]any{
				"cliproxy-google-1": map[string]any{"mode": "per-space"},
			}},
			"config": {"runtime": map[string]any{"timezone": "Europe/Moscow"},
				"ports": map[string]any{"oauth-callback": 8008}},
			"models": {
				"default": "everyday",
				"routes":  map[string]any{"everyday": map[string]any{"ladder": []any{"google-pro-1"}}},
				"services": map[string]any{
					"google-pro-1": map[string]any{
						"via":      "cliproxy",
						"endpoint": "http://cliproxy-google-1:8317/v1",
						"auth": map[string]any{
							"upstream":     map[string]any{"kind": "oauth-session", "ref": "google-one-1"},
							"proxy_access": map[string]any{"kind": "broker-key", "ref": "cliproxy-google-1"},
						},
						"models": []any{map[string]any{"id": "gemini-3.8-flash-high", "reasoning": "high"}},
					},
				},
			},
			"workspace": {"writable": []any{"/workspace"}},
			"tools":     {"extended": map[string]any{"connectors": map[string]any{"via": "toolhub", "tools": map[string]any{"exclude": []any{"connector_remove"}}}}},
		}},
		Grants: map[string][]string{
			"workspace": {"read", "write"},
			"tools":     {"list", "select"},
		},
	}
}

func TestCompiledRevisionDecodesStrict(t *testing.T) {
	rev, err := Compile(v2FixtureInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Names {
		if _, err := Decode(name, rev.Docs[name]); err != nil {
			t.Fatalf("compiled %s does not decode as v2: %v\n%s", name, err, rev.Docs[name])
		}
	}
}

func TestStackPassportNormativeShape(t *testing.T) {
	rev, err := Compile(v2FixtureInputs(t))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Decode("stack", rev.Docs["stack"])
	if err != nil {
		t.Fatal(err)
	}
	s := doc.(*StackDoc).Stack
	if s.Identity.User != "alice" || s.Identity.Organization != "acme" {
		t.Fatalf("identity: %+v", s.Identity)
	}
	for name, want := range map[string]string{
		"hermes-runtime":    "supervised",
		"toolhub":           "shared",
		"cliproxy-google-1": "per-space",
	} {
		if got := s.Services[name].Mode; got != want {
			t.Fatalf("services.%s.mode = %q, want %q", name, got, want)
		}
	}
	if _, stray := s.Services["mode"]; stray {
		t.Fatal("legacy mode leaked into services map")
	}
}

// config.yaml is the v2 runtime contract: runtime, supervision, ports,
// channels and memory all decode into the strict view (SPEC-0092 R4.1).
func TestConfigPassportNormativeShape(t *testing.T) {
	in := v2FixtureInputs(t)
	in.PlatformCeiling.Docs["config"]["channels"] = map[string]any{
		"telegram": map[string]any{"bot": map[string]any{"ref": "x"}},
	}
	in.User.Docs["config"]["channels"] = map[string]any{
		"telegram": map[string]any{
			"bot":           map[string]any{"ref": "telegram-bot-alice"},
			"allowed_users": map[string]any{"ref": "telegram-allow-alice"},
		},
	}
	rev, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Decode("config", rev.Docs["config"])
	if err != nil {
		t.Fatal(err)
	}
	c := raw.(*ConfigDoc).Config
	if c.Runtime.Timezone != "Europe/Moscow" {
		t.Fatalf("runtime.timezone lost: %+v", c.Runtime)
	}
	if c.Supervision.IdleStop != "5m" || c.Supervision.Restart != "on-failure" {
		t.Fatalf("supervision lost: %+v", c.Supervision)
	}
	if c.Ports["oauth-callback"] != 8008 {
		t.Fatalf("ports lost: %+v", c.Ports)
	}
	if c.Channels["telegram"].Bot.Ref != "telegram-bot-alice" || c.Channels["telegram"].AllowedUsers.Ref != "telegram-allow-alice" {
		t.Fatalf("channels lost refs: %+v", c.Channels)
	}
	if c.Memory.Backend != "local" {
		t.Fatalf("memory.backend lost: %+v", c.Memory)
	}
}

func TestCompileRejectsM29Vocabulary(t *testing.T) {
	for _, bad := range []map[string]map[string]any{
		{"stack": {"mode": "managed"}},
		{"stack": {"features": []any{"x"}}},
		{"stack": {"environment": "dev"}},
		{"tools": {"sections": []any{"hub:files"}}},
		{"tools": {"discovery": []any{"tools_list"}}},
		{"models": {"services": []any{"cliproxy"}}},
		{"workspace": {"layout": "notalist"}},
	} {
		in := v2FixtureInputs(t)
		for doc, kv := range bad {
			for k, v := range kv {
				in.User.Docs[doc][k] = v
			}
		}
		if _, err := Compile(in); err == nil {
			t.Fatalf("legacy vocabulary accepted: %v", bad)
		}
	}
}

func TestCompileRejectsUnknownInstanceAndBadMode(t *testing.T) {
	in := v2FixtureInputs(t)
	in.User.Docs["stack"]["services"] = map[string]any{
		"hermes-runtime": map[string]any{"mode": "supervised"},
		"nonsense-svc":   map[string]any{"mode": "shared"},
	}
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "nonsense") {
		t.Fatalf("unknown instance: %v", err)
	}
	in = v2FixtureInputs(t)
	in.User.Docs["stack"]["services"] = map[string]any{
		"hermes-runtime": map[string]any{"mode": "per-space"},
	}
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "per-runtime") {
		t.Fatalf("bad mode accepted: %v", err)
	}
}

func TestCompileKeysetBoundOutsideCeiling(t *testing.T) {
	in := v2FixtureInputs(t)
	// Ceiling declares no "cliproxy-codex" instance; the user layer may not
	// mint it.
	in.User.Docs["stack"]["services"] = map[string]any{
		"cliproxy-codex": map[string]any{"mode": "per-space"},
	}
	if _, err := Compile(in); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("service outside ceiling bound accepted: %v", err)
	}
}
