package stack

import (
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

func TestRuntimeIntrospectionPolicy(t *testing.T) {
	off, on := false, true
	withFlag := func(v *bool) *spawnv1.Config {
		return &spawnv1.Config{Runtime: spawnv1.RuntimeConfig{Introspection: v}}
	}

	if !(Settings{PassportConfig: withFlag(nil)}).RuntimeIntrospectionAllowed() {
		t.Fatal("personal space lost the historical introspection default")
	}
	if (Settings{PassportConfig: withFlag(&off)}).RuntimeIntrospectionAllowed() {
		t.Fatal("personal space flag did not deny introspection")
	}
	if !(Settings{PassportConfig: withFlag(&on)}).RuntimeIntrospectionAllowed() {
		t.Fatal("personal space explicit opt-in was denied")
	}

	group := Settings{ScopeKind: string(GroupScope), PassportConfig: withFlag(nil)}
	if group.RuntimeIntrospectionAllowed() {
		t.Fatal("group scope allowed introspection by default")
	}
	group.PassportConfig.Runtime.Introspection = &on
	if !group.RuntimeIntrospectionAllowed() {
		t.Fatal("group scope explicit opt-in was denied")
	}

	org := Settings{Organization: "acme", PassportConfig: withFlag(&on)}
	if org.RuntimeIntrospectionAllowed() {
		t.Fatal("organization scope allowed introspection without an org ceiling")
	}
	org.OrgRuntimeIntrospection = &off
	if org.RuntimeIntrospectionAllowed() {
		t.Fatal("organization ceiling did not deny introspection")
	}
	org.OrgRuntimeIntrospection = &on
	if !org.RuntimeIntrospectionAllowed() {
		t.Fatal("space opt-in under an organization ceiling was denied")
	}
	org.PassportConfig.Runtime.Introspection = &off
	if org.RuntimeIntrospectionAllowed() {
		t.Fatal("organization ceiling widened a denied space flag")
	}
}
