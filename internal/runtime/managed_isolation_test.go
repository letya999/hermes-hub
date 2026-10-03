package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// managedIsolationFixture installs fakes for the four environment probes and
// returns the hermesHome directory populated like a launched managed runtime.
func managedIsolationFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, name := range managedExtensionRoots {
		if err := os.Mkdir(filepath.Join(home, name), 0555); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(config, []byte("model: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config, 0400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_CAPABILITY_PROFILE_ID", "alice-default")
	t.Setenv("HUB_CAPABILITY_GENERATION", "7")
	t.Setenv("HUB_CAPABILITY_ENVIRONMENT", "prod")
	t.Setenv("HUB_MANAGED_MODEL_ID", "synthetic")
	t.Setenv("HUB_MANAGED_MODEL_URL", managedModelURL)
	oldRoutes, oldLookup, oldDial, oldWritable := managedReadRoutes, managedLookupHost, managedDialTCP, managedProbeWritable
	managedReadRoutes = func() ([]byte, []byte, error) {
		return []byte("Iface\tDestination\tGateway\tFlags\neth0\t000011AC\t00000000\t0001\n"), []byte(""), nil
	}
	managedLookupHost = func(host string) error {
		for _, denied := range managedDeniedHosts {
			if host == denied {
				return errors.New("no such host")
			}
		}
		if host == "toolhub" || host == "model-relay" {
			return nil
		}
		return errors.New("no such host")
	}
	managedDialTCP = func(address string) error {
		if address == "toolhub:8090" || address == "model-relay:8318" {
			return nil
		}
		return errors.New("refused")
	}
	managedProbeWritable = func(string) bool { return false }
	t.Cleanup(func() {
		managedReadRoutes, managedLookupHost, managedDialTCP, managedProbeWritable = oldRoutes, oldLookup, oldDial, oldWritable
	})
	return home
}

func TestManagedIsolationAttestsCompleteTopology(t *testing.T) {
	home := managedIsolationFixture(t)
	if err := verifyManagedIsolation(home); err != nil {
		t.Fatalf("reviewed managed topology rejected: %v", err)
	}
}

func TestManagedIsolationFailsClosed(t *testing.T) {
	cases := map[string]func(t *testing.T, home string){
		"profile-unset":     func(t *testing.T, _ string) { t.Setenv("HUB_CAPABILITY_PROFILE_ID", "") },
		"generation-unset":  func(t *testing.T, _ string) { t.Setenv("HUB_CAPABILITY_GENERATION", "0") },
		"generation-bad":    func(t *testing.T, _ string) { t.Setenv("HUB_CAPABILITY_GENERATION", "abc") },
		"environment-bad":   func(t *testing.T, _ string) { t.Setenv("HUB_CAPABILITY_ENVIRONMENT", "staging") },
		"model-id-unset":    func(t *testing.T, _ string) { t.Setenv("HUB_MANAGED_MODEL_ID", "") },
		"model-url-bypass":  func(t *testing.T, _ string) { t.Setenv("HUB_MANAGED_MODEL_URL", "http://cliproxy:8317/v1") },
		"extension-missing": func(t *testing.T, home string) { _ = os.Remove(filepath.Join(home, "hooks")) },
		"extension-symlink": func(t *testing.T, home string) {
			_ = os.Remove(filepath.Join(home, "plugins"))
			_ = os.Symlink(home, filepath.Join(home, "plugins"))
		},
		"extension-nonempty": func(t *testing.T, home string) {
			_ = os.WriteFile(filepath.Join(home, "skills", "evil.md"), []byte("x"), 0400)
		},
		"extension-writable": func(t *testing.T, _ string) { managedProbeWritable = func(string) bool { return true } },
		"config-writable":    func(t *testing.T, home string) { _ = os.Chmod(filepath.Join(home, "config.yaml"), 0600) },
		"default-route-v4": func(t *testing.T, _ string) {
			managedReadRoutes = func() ([]byte, []byte, error) {
				return []byte("Iface\tDestination\tGateway\tFlags\neth0\t00000000\t010011AC\t0003\n"), nil, nil
			}
		},
		"default-route-v6": func(t *testing.T, _ string) {
			managedReadRoutes = func() ([]byte, []byte, error) {
				return nil, []byte("00000000000000000000000000000000 00 00000000000000000000000000000000 00 00000000000000000000000000000000 00000064 00000000 00000000 00000001 eth0\n"), nil
			}
		},
		"routes-unavailable": func(t *testing.T, _ string) {
			managedReadRoutes = func() ([]byte, []byte, error) { return nil, nil, errors.New("denied") }
		},
		"control-resolves": func(t *testing.T, _ string) { managedLookupHost = func(string) error { return nil } },
		"relay-unresolved": func(t *testing.T, _ string) {
			managedLookupHost = func(host string) error {
				if host == "toolhub" {
					return errors.New("no such host")
				}
				return errors.New("no such host")
			}
		},
		"relay-dial-fails": func(t *testing.T, _ string) { managedDialTCP = func(string) error { return errors.New("refused") } },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			home := managedIsolationFixture(t)
			mutate(t, home)
			if err := verifyManagedIsolation(home); err == nil {
				t.Fatal("degraded managed topology accepted")
			}
		})
	}
}

func TestRouteTableParsers(t *testing.T) {
	if hasDefaultRouteIPv4([]byte("Iface\tDestination\neth0\t000011AC\t00000000\n")) {
		t.Fatal("non-default v4 route flagged")
	}
	if !hasDefaultRouteIPv4([]byte("Iface\tDestination\neth0\t00000000\t010011AC\n")) {
		t.Fatal("default v4 route missed")
	}
	v6Default := "00000000000000000000000000000000 00 ffffffffffffffffffffffffffffffff 00 00000000000000000000000000000000 00000064 00000000 00000000 00000001 eth0\n"
	if !hasDefaultRouteIPv6([]byte(v6Default)) {
		t.Fatal("default v6 route missed")
	}
	if hasDefaultRouteIPv6([]byte(strings.Replace(v6Default, "eth0", "lo", 1))) {
		t.Fatal("loopback v6 flagged")
	}
	if hasDefaultRouteIPv6([]byte("20010db8000000000000000000000000 40 ffffffffffffffffffffffffffffffff 00 00000000000000000000000000000000 00000064 00000000 00000000 00000001 eth0\n")) {
		t.Fatal("prefix v6 route flagged")
	}
}
