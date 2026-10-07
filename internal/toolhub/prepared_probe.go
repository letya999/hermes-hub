package toolhub

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"
)

// startPreparedProbeProxy gives credentialed preflight the same internal-network
// egress boundary as a running owner workload. Nothing containing credentials
// enters the proxy configuration or Docker command line.
func startPreparedProbeProxy(ctx context.Context, egress []string) (string, string, func(), error) {
	return startPreparedProbeProxyWithDocker(ctx, egress, func(commandCtx context.Context, args ...string) ([]byte, error) {
		return exec.CommandContext(commandCtx, "docker", args...).CombinedOutput() // #nosec G204 -- fixed Docker verbs and validated generated names.
	})
}

func startPreparedProbeProxyWithDocker(ctx context.Context, egress []string, run func(context.Context, ...string) ([]byte, error)) (string, string, func(), error) {
	config, err := genericProxyConfig(egress)
	if err != nil {
		return "", "", nil, err
	}
	base, err := randomPreflightContainerName("hermes-probe-")
	if err != nil {
		return "", "", nil, err
	}
	network, volume, proxy := base+"-net", base+"-config", base+"-proxy"
	stop := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = run(cleanupCtx, "rm", "-f", proxy)
		_, _ = run(cleanupCtx, "network", "rm", network)
		_, _ = run(cleanupCtx, "volume", "rm", volume)
	}
	fail := func(err error) (string, string, func(), error) {
		stop()
		return "", "", nil, err
	}
	if _, err := run(ctx, "network", "create", "--internal", "--label", "hermes-hub.role=artifact-preflight", network); err != nil {
		return fail(fmt.Errorf("%w: preflight network: %v", ErrIsolation, err))
	}
	if _, err := run(ctx, "volume", "create", "--label", "hermes-hub.role=artifact-preflight", volume); err != nil {
		return fail(fmt.Errorf("%w: preflight proxy volume: %v", ErrIsolation, err))
	}
	args := []string{"create", "--name", proxy, "--network", network, "--read-only", "--user", "31:31", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--cpus", "0.25", "--memory", "256m", "--pids-limit", "64", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--mount", "type=volume,source=" + volume + ",target=/etc/squid", restrictedBuildProxyImage, "-N", "-d", "1", "-f", "/etc/squid/squid.conf"}
	if _, err := run(ctx, args...); err != nil {
		return fail(fmt.Errorf("%w: preflight proxy create: %v", ErrIsolation, err))
	}
	file, err := os.CreateTemp("", "hermes-probe-squid-")
	if err != nil {
		return fail(err)
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(config); err != nil {
		file.Close()
		return fail(err)
	}
	if err := file.Chmod(0644); err != nil {
		file.Close()
		return fail(err)
	}
	if err := file.Close(); err != nil {
		return fail(err)
	}
	if _, err := run(ctx, "cp", file.Name(), proxy+":/etc/squid/squid.conf"); err != nil {
		return fail(fmt.Errorf("%w: preflight proxy config: %v", ErrIsolation, err))
	}
	if _, err := run(ctx, "network", "connect", "bridge", proxy); err != nil {
		return fail(fmt.Errorf("%w: preflight proxy uplink: %v", ErrIsolation, err))
	}
	if _, err := run(ctx, "start", proxy); err != nil {
		return fail(fmt.Errorf("%w: preflight proxy start: %v", ErrIsolation, err))
	}
	info, err := run(ctx, "inspect", "--format", "{{(index .NetworkSettings.Networks \""+network+"\").IPAddress}}", proxy)
	if err != nil {
		return fail(fmt.Errorf("%w: preflight proxy address: %v", ErrIsolation, err))
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(info)))
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return fail(fmt.Errorf("%w: invalid preflight proxy address", ErrIsolation))
	}
	return network, "http://" + ip.String() + ":3128", stop, nil
}
