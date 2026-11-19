package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/exposure"
	"github.com/letya999/hermes-hub/internal/sshcap"
)

// RuntimeConnection is the shared env and read-only mount contract for one
// reviewed connection directory. Admission rights remain owned by ToolHub.
type RuntimeConnection struct {
	Name, Source, Target, EnvKey, ConfigPath string
}

func RuntimeConnections(s Settings, dir string) []RuntimeConnection {
	out := []RuntimeConnection{}
	for _, item := range []struct{ name, key string }{{"ssh", "HUB_SSH_CONFIG"}, {"exposure", "HUB_EXPOSURE_CONFIG"}} {
		if !s.Has(item.name) {
			continue
		}
		target := "/state/" + item.name
		out = append(out, RuntimeConnection{item.name, filepath.Join(dir, "connections", item.name), target, item.key, target + "/config.yaml"})
	}
	return out
}

// managedExposureRelay is the only non-HTTPS CONNECT exception; its address
// comes from the reviewed sish config, never an agent-supplied argument.
func managedExposureRelay(s Settings, dir string) string {
	if !s.Has("exposure") {
		return ""
	}
	cfg, err := exposure.Load(filepath.Join(dir, "connections", "exposure", "config.yaml"))
	if err != nil || cfg.Provider != "sish" {
		return ""
	}
	return cfg.Relay.Address()
}

// ValidateRuntimeConnections runs before render and before a supervised spawn.
// Parsing on the host is insufficient: the container runs as uid 10001 with
// the host process gid, which may not be able to read the mounted files.
func ValidateRuntimeConnections(s Settings, dir string) error {
	for _, connection := range RuntimeConnections(s, dir) {
		configFile := filepath.Join(connection.Source, "config.yaml")
		var refs []string
		switch connection.Name {
		case "ssh":
			cfg, err := sshcap.Load(configFile)
			if err != nil {
				return fmt.Errorf("ssh config: %w", err)
			}
			for _, host := range cfg.Hosts {
				for _, ref := range []string{host.KeyRef, host.CertificateRef} {
					if path, ok := strings.CutPrefix(ref, "file:"); ok {
						refs = append(refs, path)
					}
				}
			}
		case "exposure":
			cfg, err := exposure.Load(configFile)
			if err != nil {
				return fmt.Errorf("exposure config: %w", err)
			}
			if cfg.Provider == "sish" && s.PassportServices["egress-relay"] != "per-space" {
				return fmt.Errorf("managed sish exposure requires a per-space egress-relay in stack passport")
			}
			if path, ok := strings.CutPrefix(cfg.KeyRef, "file:"); ok {
				refs = append(refs, path)
			}
		}
		for _, relative := range append([]string{"config.yaml"}, refs...) {
			if err := runtimeConnectionReadable(connection.Source, relative, 10001, max(0, os.Getgid())); err != nil {
				kind := "credential"
				if relative == "config.yaml" {
					kind = "config"
				}
				return fmt.Errorf("%s runtime %s is missing or unreadable by uid 10001", connection.Name, kind)
			}
		}
	}
	return nil
}
