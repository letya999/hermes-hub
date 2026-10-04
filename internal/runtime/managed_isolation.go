package runtime

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Managed extension roots must be empty read-only overlays: they are where
// Hermes would otherwise import executable skills, hooks, plugins, bundles,
// scripts and installed binaries. Anything present or writable there is an
// unapproved code path into the managed runtime.
var managedExtensionRoots = []string{"skills", "hooks", "plugins", "skill-bundles", "scripts", "bin", "node", "lsp"}

// Control-plane and sibling names must not resolve from the agent network:
// reaching them directly would bypass the narrow ToolHub/model relays.
var managedDeniedHosts = []string{
	"credential-broker", "broker", "cliproxy", "toolhub-control",
	"workload-controller", "communication-hub", "host.docker.internal",
}

const managedModelURL = "http://model-relay:8318/v1"

// managedScopeEtcDir is the upstream default managed-scope location; a variable
// so tests can point it at a temp directory.
var managedScopeEtcDir = "/etc/hermes"

var (
	managedReadRoutes = func() (ipv4, ipv6 []byte, err error) {
		if ipv4, err = os.ReadFile("/proc/net/route"); err != nil {
			return nil, nil, err
		}
		if ipv6, err = os.ReadFile("/proc/net/ipv6_route"); err != nil {
			return nil, nil, err
		}
		return ipv4, ipv6, nil
	}
	managedLookupHost = func(host string) error {
		_, err := net.LookupHost(host)
		return err
	}
	managedDialTCP = func(address string) error {
		conn, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err != nil {
			return err
		}
		return conn.Close()
	}
	managedProbeWritable = func(dir string) bool {
		probe, err := os.MkdirTemp(dir, ".probe-")
		if err != nil {
			return false
		}
		_ = os.Remove(probe)
		return true
	}
)

// verifyManagedIsolation attests, from inside the container, that the managed
// boundary actually holds before Hermes is allowed to start. The supervisor
// builds the topology; this refuses launch whenever it is absent or degraded.
func verifyManagedIsolation(hermesHome string) error {
	if os.Getenv("HUB_CAPABILITY_PROFILE_ID") == "" {
		return fmt.Errorf("managed capability profile is unset")
	}
	generation, err := parseGeneration(os.Getenv("HUB_CAPABILITY_GENERATION"))
	if err != nil || generation == 0 {
		return fmt.Errorf("managed capability generation is unset")
	}
	if environment := os.Getenv("HUB_CAPABILITY_ENVIRONMENT"); environment != "dev" && environment != "prod" {
		return fmt.Errorf("managed capability environment is invalid")
	}
	if os.Getenv("HUB_MANAGED_MODEL_ID") == "" || os.Getenv("HUB_MANAGED_MODEL_URL") != managedModelURL {
		return fmt.Errorf("managed model route is not the reviewed relay")
	}
	for _, name := range managedExtensionRoots {
		dir := filepath.Join(hermesHome, name)
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed extension root %s missing", name)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			return fmt.Errorf("managed extension root %s is not empty", name)
		}
		if managedProbeWritable(dir) {
			return fmt.Errorf("managed extension root %s is writable", name)
		}
	}
	config := filepath.Join(hermesHome, "config.yaml")
	if file, err := os.OpenFile(config, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_ = file.Close()
		return fmt.Errorf("managed effective config is writable")
	}
	// Hermes loads $HERMES_HOME/.env (and .op.env) with override=True at gateway
	// start and reloads it per turn: either file would re-point pinned discovery
	// or proxy env after this preflight. Both must be absent, not merely empty —
	// an existing file also activates upstream's managed-key cleanup.
	for _, name := range []string{".env", ".op.env"} {
		target := filepath.Join(hermesHome, name)
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("managed environment file %s present in hermes home", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("managed environment file %s check: %w", name, err)
		}
	}
	// Upstream honors its own admin managed scope: $HERMES_MANAGED_DIR or
	// /etc/hermes overlays config and env in-process at load time, bypassing the
	// attested read-only file. Neither source may exist in a managed runtime.
	if strings.TrimSpace(os.Getenv("HERMES_MANAGED_DIR")) != "" {
		return fmt.Errorf("managed runtime forbids HERMES_MANAGED_DIR")
	}
	if info, err := os.Lstat(managedScopeEtcDir); err == nil && info.IsDir() {
		return fmt.Errorf("managed runtime forbids upstream managed scope at %s", managedScopeEtcDir)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("managed scope check: %w", err)
	}
	ipv4, ipv6, err := managedReadRoutes()
	if err != nil {
		return fmt.Errorf("managed route tables unavailable: %w", err)
	}
	if hasDefaultRouteIPv4(ipv4) || hasDefaultRouteIPv6(ipv6) {
		return fmt.Errorf("managed agent network has a default route")
	}
	for _, host := range managedDeniedHosts {
		if err := managedLookupHost(host); err == nil {
			return fmt.Errorf("managed network resolves control host %s", host)
		}
	}
	for _, host := range []string{"toolhub", "model-relay"} {
		if err := managedLookupHost(host); err != nil {
			return fmt.Errorf("managed relay %s unreachable: %w", host, err)
		}
	}
	for _, address := range []string{"toolhub:8090", "model-relay:8318"} {
		if err := managedDialTCP(address); err != nil {
			return fmt.Errorf("managed relay %s refused: %w", address, err)
		}
	}
	return nil
}

func parseGeneration(value string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(value), 10, 64)
}

// hasDefaultRouteIPv4 mirrors the Docker canary: any route-table line whose
// destination is 00000000 is a default route.
func hasDefaultRouteIPv4(table []byte) bool {
	for i, line := range strings.Split(string(table), "\n") {
		fields := strings.Fields(line)
		if i == 0 || len(fields) < 2 {
			continue
		}
		if fields[1] == "00000000" {
			return true
		}
	}
	return false
}

// hasDefaultRouteIPv6: all-zero destination, zero prefix length, non-lo iface.
func hasDefaultRouteIPv6(table []byte) bool {
	for _, line := range strings.Split(string(table), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		if fields[0] == "00000000000000000000000000000000" && fields[1] == "00" && fields[len(fields)-1] != "lo" {
			return true
		}
	}
	return false
}
