package api

import (
	"os"
	"os/exec"
	"strings"
)

// DockerBindSource translates a container-local path into the path the Docker
// daemon must resolve for a sibling bind-mount source. When ToolHub or a
// controller runs inside a container, the daemon interprets bind sources on
// the host filesystem, so paths under shared state mounts are rewritten to the
// host-side prefix from HUB_DOCKER_HOST_ROOT ("containerPrefix=hostPrefix"
// pairs, comma-separated). Without the variable the path passes through
// unchanged, which keeps host-native deployments identical.
func DockerBindSource(path string) string {
	for _, pair := range strings.Split(os.Getenv("HUB_DOCKER_HOST_ROOT"), ",") {
		prefix, host, ok := strings.Cut(pair, "=")
		if !ok || prefix == "" || host == "" {
			continue
		}
		if path == prefix {
			return host
		}
		if strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/") {
			return host + path[len(strings.TrimSuffix(prefix, "/")):]
		}
	}
	if volume := os.Getenv("HUB_BROKER_MATERIALIZED_VOLUME"); volume != "" && strings.HasPrefix(path, "/run/broker-materialized/") {
		return volumeBindSource(path, "/run/broker-materialized", volume)
	}
	if volume := os.Getenv("HUB_CRED_STAGING_VOLUME"); volume != "" && strings.HasPrefix(path, "/run/cred-staging/") {
		return volumeBindSource(path, "/run/cred-staging", volume)
	}
	return path
}

// volumeBindSource resolves a path inside a named Docker volume to the
// daemon-side mountpoint. It fails closed: an uninspectable volume returns
// "" so the caller never binds a guessed source for credential material.
func volumeBindSource(path, containerPrefix, volume string) string {
	if volume == "" || !strings.HasPrefix(path, containerPrefix+"/") {
		return ""
	}
	mountpoint, err := exec.Command("docker", "volume", "inspect", "--format", "{{.Mountpoint}}", volume).Output() // #nosec G204 G702 -- volume name comes from operator deploy env.
	root := strings.TrimSpace(string(mountpoint))
	if err != nil || !strings.HasPrefix(root, "/") || strings.Contains(root, "\n") {
		return "" // fail closed: the daemon must never guess the source
	}
	return strings.TrimSuffix(root, "/") + strings.TrimPrefix(path, containerPrefix)
}
