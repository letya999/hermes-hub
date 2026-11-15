package passport

import (
	"strings"
	"testing"
)

func effectiveFixture() Effective {
	return Effective{
		Header: Header{APIVersion: APIVersion, Kind: "stack"},
		Stack: Stack{
			Identity: Identity{User: "alice"},
			Services: map[string]ServiceSpec{
				"runtime": {Mode: "managed"},
				"toolhub": {Mode: "managed"},
			},
		},
		Config: Config{
			Ports: map[string]int{"oauth-callback": 8085},
		},
		Workspace: Workspace{
			Mounts: map[string]Mount{
				"shared": {Source: "/srv/shared", Mode: "ro"},
			},
		},
		Tools: Tools{
			Core: map[string]Toolset{
				"files": {Via: "toolhub", Tools: &Selector{Include: []string{"file_read"}}},
			},
		},
		Models: Models{
			Services: map[string]ModelService{
				"openai": {Via: "cliproxy", Auth: Auth{Upstream: &AuthRef{Kind: "oauth-session", Ref: "cliproxy/openai-main"}}},
			},
		},
	}
}

func findChange(t *testing.T, cs []Change, substr string) Change {
	t.Helper()
	for _, c := range cs {
		if strings.Contains(c.Path, substr) {
			return c
		}
	}
	t.Fatalf("no change at path containing %q in %+v", substr, cs)
	return Change{}
}

func TestDiffClassifiesEffects(t *testing.T) {
	prev := effectiveFixture()

	t.Run("tools core include moves live", func(t *testing.T) {
		next := effectiveFixture()
		next.Tools.Core["files"] = Toolset{Via: "toolhub", Tools: &Selector{Include: []string{"file_read", "file_write"}}}
		c := findChange(t, Diff(prev, next), "tools.core.files")
		if c.Effect != EffectLive {
			t.Fatalf("tools include effect = %q, want live", c.Effect)
		}
	})

	t.Run("workspace mount respawns", func(t *testing.T) {
		next := effectiveFixture()
		next.Workspace.Mounts["shared"] = Mount{Source: "/srv/shared", Mode: "rw"}
		c := findChange(t, Diff(prev, next), "workspace.mounts.shared")
		if c.Effect != EffectRespawn {
			t.Fatalf("mount mode effect = %q, want respawn", c.Effect)
		}
	})

	t.Run("config port recreates", func(t *testing.T) {
		next := effectiveFixture()
		next.Config.Ports["oauth-callback"] = 8086
		c := findChange(t, Diff(prev, next), "config.ports")
		if c.Effect != EffectRecreate {
			t.Fatalf("port effect = %q, want recreate", c.Effect)
		}
	})

	t.Run("stack service rebuilds", func(t *testing.T) {
		next := effectiveFixture()
		next.Stack.Services["cliproxy-openai"] = ServiceSpec{Mode: "managed"}
		c := findChange(t, Diff(prev, next), "stack.services.cliproxy-openai")
		if c.Effect != EffectRebuild {
			t.Fatalf("new service effect = %q, want rebuild", c.Effect)
		}
	})

	t.Run("identical revisions produce no changes", func(t *testing.T) {
		if cs := Diff(prev, effectiveFixture()); len(cs) != 0 {
			t.Fatalf("no-op diff returned %+v", cs)
		}
	})
}

func TestDiffNeverPrintsResolvedSecrets(t *testing.T) {
	prev := effectiveFixture()
	next := effectiveFixture()
	next.Models.Services["openai"] = ModelService{
		Via:  "cliproxy",
		Auth: Auth{Upstream: &AuthRef{Kind: "oauth-session", Ref: "cliproxy/openai-rotated"}},
	}
	cs := Diff(prev, next)
	c := findChange(t, cs, "auth.upstream.ref")
	for _, v := range []string{c.Before, c.After} {
		if strings.Contains(v, "cliproxy/openai") {
			t.Fatalf("diff leaked ref value: before=%q after=%q", c.Before, c.After)
		}
		if !strings.HasPrefix(v, "<ref:") || !strings.HasSuffix(v, ">") {
			t.Fatalf("ref leaf not masked: %q", v)
		}
	}
}
