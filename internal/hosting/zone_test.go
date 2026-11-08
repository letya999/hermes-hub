package hosting

import "testing"

func TestZoneForOwnerWithNonDNSIdentity(t *testing.T) {
	if ZoneForOwner("alice") != "alice" || ZoneForOwner("artem_two") == ZoneForOwner("artem-two") || !label.MatchString(ZoneForOwner("artem_two")) {
		t.Fatal("owner zone mapping")
	}
	cfg := Config{Owner: "artem_two", Environment: "dev", Zone: ZoneForOwner("artem_two"), Domain: "hub.local.test", Workspace: "/workspace", Store: "/sites"}
	if err := cfg.validate(); err != nil {
		t.Fatal("valid owner zone rejected", err)
	}
	cfg.Zone = "artem-two"
	if err := cfg.validate(); err == nil {
		t.Fatal("unbound owner zone accepted")
	}
}
