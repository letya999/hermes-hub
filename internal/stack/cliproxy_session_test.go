package stack

import (
	"strings"
	"testing"
)

// R7.2: every declared cliproxy instance receives its OAuth session through a
// dedicated tmpfs session volume — materialized by its own init companion —
// never through a bind or a shared lease view. A session file can therefore
// only ever exist inside the instance it was granted to.
func TestCliproxyInstanceGetsOnlyItsSession(t *testing.T) {
	s := managedPassportSibling()
	s.PassportServices = map[string]string{
		"hermes-runtime":    "supervised",
		"model-relay":       "per-space",
		"toolhub-relay":     "per-space",
		"cliproxy-google-1": "per-space",
		"cliproxy-google-2": "per-space",
	}
	rendered := Compose(s, t.TempDir(), t.TempDir())
	services := rendered["services"].(M)
	volumes, _ := rendered["volumes"].(M)
	for _, name := range []string{"cliproxy-google-1", "cliproxy-google-2"} {
		svc, ok := services[name].(M)
		if !ok {
			t.Fatalf("declared %s missing", name)
		}
		sessionVol := "cliproxy-session-" + name
		cfgVol := "cliproxy-cfg-" + name
		for _, vol := range []string{sessionVol, cfgVol} {
			if _, ok := volumes[vol]; !ok {
				t.Fatalf("%s volume %s not declared: %v", name, vol, volumes)
			}
		}
		var authTarget, cfgTarget bool
		for _, v := range svc["volumes"].([]any) {
			m := v.(M)
			if m["source"] == sessionVol && m["target"] == "/cliproxy/auths" {
				authTarget = true
			}
			if m["source"] == cfgVol && m["target"] == "/run/cliproxy-cfg" {
				cfgTarget = true
			}
			if src, _ := m["source"].(string); strings.Contains(src, "broker-materialized") {
				t.Fatalf("%s must never see the shared materialized volume", name)
			}
		}
		if !authTarget || !cfgTarget {
			t.Fatalf("%s session/cfg volumes not mounted: %v", name, svc["volumes"])
		}
		if ep, _ := svc["entrypoint"].([]string); len(ep) == 0 || ep[len(ep)-1] != "/run/cliproxy-cfg/config.yaml" {
			t.Fatalf("%s must read the rendered config: %v", name, svc["entrypoint"])
		}
		// A tmpfs volume only exists while a live container holds its mount: the
		// service may start only once the companion reports both files written
		// and stays mounted for the whole service lifetime.
		dep, _ := svc["depends_on"].(M)[name+"-init"].(M)
		if dep["condition"] != "service_healthy" {
			t.Fatalf("%s must wait for the materialized tmpfs (service_healthy): %v", name, svc["depends_on"])
		}
		init, ok := services[name+"-init"].(M)
		if !ok {
			t.Fatalf("%s init companion missing: %v", name, serviceKeys(services))
		}
		var initSeesSession, initSeesLease, initSeesCfg, initSeesGen bool
		for _, v := range init["volumes"].([]any) {
			m := v.(M)
			if m["source"] == sessionVol {
				initSeesSession = true
			}
			if m["source"] == "broker-materialized" {
				initSeesLease = true
				if m["read_only"] != true {
					t.Fatalf("%s-init only reads the lease view — broker-materialized must be read_only: %v", name, m)
				}
			}
			if m["source"] == cfgVol {
				initSeesCfg = true
			}
			if m["target"] == "/run/model-gen" {
				initSeesGen = true
			}
		}
		if !initSeesSession || !initSeesLease || !initSeesCfg || !initSeesGen {
			t.Fatalf("%s-init must mount %s + %s + broker-materialized + generated: %v", name, sessionVol, cfgVol, init["volumes"])
		}
		env, _ := init["environment"].(M)
		if _, hasOwner := env["HUB_USER_ID"]; !hasOwner {
			t.Fatalf("%s-init needs HUB_USER_ID: %v", name, init["environment"])
		}
		// The companion resolves credentials against credential-broker, which
		// lives on the shared runtime network — without it the acquire call
		// can never leave the project network.
		if nets, ok := init["networks"].(M); !ok || nets[sharedNetworkName] == nil {
			t.Fatalf("%s-init must join %s to reach the broker: %v", name, sharedNetworkName, init["networks"])
		}
		// Once the materialized files exist the companion stays up holding the
		// tmpfs mounts — exiting early would discard the volume before the
		// service can mount it. The healthcheck reports exactly that moment.
		hc, _ := init["healthcheck"].(M)
		test, _ := hc["test"].([]string)
		joined := strings.Join(test, " ")
		if !strings.Contains(joined, "/run/cliproxy-cfg/config.yaml") || !strings.Contains(joined, "/cliproxy/auths/session.json") {
			t.Fatalf("%s-init must healthcheck the materialized files: %v", name, init["healthcheck"])
		}
	}
}
