package exposure

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validSishYAML = `schema: 1
provider: sish
relay:
  host: tunnel.example.com
  port: 2222
  user: alice
  domain: hub.example.com
  host_keys:
    - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEa6/V1v39RJXNZvJSJp7hEDWdTsFc9n/k8G+sMFBqGX"
zone: alice
key_ref: "broker:grant-01"
max_exposures: 3
idle_seconds: 900
lifetime_seconds: 3600
endpoints:
  onboarding:
    target: hub_public_listener
  demo:
    target: "service:mysite:8080"
  raw:
    target: "10.0.0.5:9000"
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValidSish(t *testing.T) {
	cfg, err := Load(writeConfig(t, validSishYAML))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Provider != "sish" || cfg.Relay.Address() != "tunnel.example.com:2222" {
		t.Fatalf("relay: %+v", cfg.Relay)
	}
	if cfg.Max() != 3 || cfg.Idle() != 900*time.Second || cfg.Lifetime() != time.Hour {
		t.Fatalf("bounds: %d %v %v", cfg.Max(), cfg.Idle(), cfg.Lifetime())
	}
	ep, err := cfg.EndpointFor("demo")
	if err != nil || ep.Target != "service:mysite:8080" {
		t.Fatalf("endpoint: %v %+v", err, ep)
	}
	bind, err := cfg.BindName("demo")
	if err != nil || bind != "demo.alice" {
		t.Fatalf("bind: %v %q", err, bind)
	}
	pub, err := cfg.PublicName("demo")
	if err != nil || pub != "demo.alice.hub.example.com" {
		t.Fatalf("public: %v %q", err, pub)
	}
}

func TestLoadRejects(t *testing.T) {
	sishHead := `schema: 1
provider: sish
relay: {host: v.example.com, user: u, domain: hub.example.com, host_keys: ["sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"]}
zone: alice
key_ref: "broker:grant-01"
`
	cases := map[string]string{
		"unknown field": validSishYAML + "bogus: 1\n",
		"schema":        `schema: 2` + validSishYAML[len(`schema: 1`):],
		"bad provider": `schema: 1
provider: ngrok
key_ref: "broker:grant-01"
endpoints: {a: {target: "hub_public_listener"}}
`,
		"sish no relay": `schema: 1
provider: sish
zone: alice
key_ref: "broker:grant-01"
endpoints: {a: {target: "hub_public_listener"}}
`,
		"sish no zone": sishHead[:strings.Index(sishHead, "zone:")] + `key_ref: "broker:grant-01"
endpoints: {a: {target: "hub_public_listener"}}
`,
		"cf keeps relay": `schema: 1
provider: cloudflared
relay: {host: v.example.com, user: u, domain: hub.example.com, host_keys: ["sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"]}
key_ref: "broker:grant-01"
endpoints: {a: {target: "hub_public_listener"}}
`,
		"bad endpoint name": sishHead + `endpoints: {"Bad_Name": {target: "hub_public_listener"}}
`,
		"no endpoints": sishHead + `endpoints: {}
`,
		"cap overflow": sishHead + `max_exposures: 99
endpoints: {a: {target: "hub_public_listener"}}
`,
		"key_ref bad scheme": `schema: 1
provider: sish
relay: {host: v.example.com, user: u, domain: hub.example.com, host_keys: ["sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"]}
zone: alice
key_ref: "env:SECRET"
endpoints: {a: {target: "hub_public_listener"}}
`,
		"file ref traversal": `schema: 1
provider: sish
relay: {host: v.example.com, user: u, domain: hub.example.com, host_keys: ["sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"]}
zone: alice
key_ref: "file:../escape.pem"
endpoints: {a: {target: "hub_public_listener"}}
`,
		"two documents": validSishYAML + "---\nschema: 1\n",
	}
	for name, body := range cases {
		if _, err := Load(writeConfig(t, body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `schema: 1
provider: sish
relay: {host: 10.1.2.3, user: u, domain: example.org, host_keys: ["sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"]}
zone: z1
key_ref: "file:key.pem"
endpoints: {a: {target: "hub_public_listener"}}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Relay.Address() != "10.1.2.3:22" || cfg.Max() != defaultMax ||
		cfg.Idle() != defaultIdle*time.Second || cfg.Lifetime() != defaultLifetime*time.Second {
		t.Fatalf("defaults: %v %d %v %v", cfg.Relay.Address(), cfg.Max(), cfg.Idle(), cfg.Lifetime())
	}
}

func TestLoadRejectsSymlinkAndOversize(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.yaml")
	if err := os.WriteFile(real, []byte(validSishYAML), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := Load(link); !errors.Is(err, ErrInvalid) {
		t.Fatalf("symlink: %v", err)
	}
	big := filepath.Join(dir, "big.yaml")
	if err := os.WriteFile(big, make([]byte, maxConfigBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(big); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize: %v", err)
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing: %v", err)
	}
}

func TestEndpointForFailsClosed(t *testing.T) {
	cfg, err := Load(writeConfig(t, validSishYAML))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nope", "admin", "demo/../x", "DEMO"} {
		if _, err := cfg.EndpointFor(name); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: want not found, got %v", name, err)
		}
		if _, err := cfg.BindName(name); !errors.Is(err, ErrNotFound) {
			t.Errorf("bind %q: %v", name, err)
		}
	}
}

func TestBindNameSishOnly(t *testing.T) {
	cfg, err := Load(writeConfig(t, `schema: 1
provider: cloudflared
key_ref: "broker:grant-01"
endpoints: {a: {target: "hub_public_listener"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.BindName("a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cloudflared bind: %v", err)
	}
}

func TestValidateTargetRejects(t *testing.T) {
	for _, target := range []string{
		"anything goes", "host:", ":80", "service:Bad Name:80", "service:ok:notaport",
		"service:ok:0", "bad host!:80", "10.0.0.1:99999",
	} {
		if err := validateTarget(target); err == nil {
			t.Errorf("%q: want rejection", target)
		}
	}
}
