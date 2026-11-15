package hosting

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/exposure"
)

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

func TestMatchingExposureUsesDNSZone(t *testing.T) {
	cfg := Deployment{Config: Config{Owner: "artem_two", Zone: ZoneForOwner("artem_two"), Domain: "hub.local.test"}, Slots: []string{"m32test"}}
	ec := &exposure.Config{Provider: "sish", Zone: cfg.Zone, Relay: &exposure.Relay{Domain: cfg.Domain}}
	if !matchingExposure(cfg, ec) {
		t.Fatal("reviewed DNS zone rejected")
	}
	ec.Zone = "artem_two"
	if matchingExposure(cfg, ec) {
		t.Fatal("non-DNS owner id accepted as zone")
	}
}

func TestFileKeyExposureDoesNotRequireBroker(t *testing.T) {
	t.Setenv("HUB_CREDENTIAL_BROKER_RUNTIME_URL", "https://invalid.test")
	file := &exposure.Config{KeyRef: "file:sish-key.pem"}
	if _, err := brokerForExposure(file); err != nil {
		t.Fatal("file key should not depend on broker config", err)
	}
	broker := &exposure.Config{KeyRef: "broker:some-reference"}
	if _, err := brokerForExposure(broker); err == nil {
		t.Fatal("broker key must validate broker config")
	}
}
