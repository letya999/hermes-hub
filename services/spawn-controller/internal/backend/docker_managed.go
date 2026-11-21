// Managed-runtime docker launch, moved from v0 internal/supervisor
// ensure.go managedRunArgs / ensureManagedInfra / spawnControlRelay and
// sandbox.go sandboxRelayHosts (CHG-0336 S7c); the v0 copies are deleted
// in the same changeset. The argv shape is the v0 contract verbatim:
// internal agent network only, control relay as the sole inbound route,
// read-only root, sealed extension roots, env files for secrets.
package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

const controlRelayContainerPort = 8091

// startManaged applies the managed-runtime spec: the workload joins the
// Compose-owned internal agent network (never created here — a missing or
// non-internal network means the space was never composed), the reviewed
// relay aliases are pinned as hosts, and the dual-homed control relay is
// the only inbound route, publishing on the host loopback.
func (d *dockerBackend) startManaged(ctx context.Context, spec Spec) (Runtime, error) {
	name := d.container(spec.Key)
	labels := d.labels(spec)
	agentNet := spec.Networks[0]
	out, err := d.command(ctx, nil, "network", "inspect", "--format", "{{.Internal}}", agentNet)
	if err != nil {
		return Runtime{}, fmt.Errorf("managed agent network %s unavailable; run hubctl up for this space first", agentNet)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return Runtime{}, fmt.Errorf("managed agent network %s is not internal", agentNet)
	}
	control := spec.Relay.Control
	if _, err := d.command(ctx, nil, "network", "inspect", "--format", "{{.Id}}", control); err != nil {
		if _, cerr := d.command(ctx, nil, "network", "create", control); cerr != nil {
			return Runtime{}, fmt.Errorf("managed control network %s unavailable: %w", control, cerr)
		}
	}
	// Docker embedded DNS is outside runsc's network stack. Only pin
	// aliases on the already-verified internal space network; never add a
	// public resolver.
	hosts, err := d.sandboxRelayHosts(ctx, agentNet, spec.RelayHosts)
	if err != nil {
		return Runtime{}, err
	}
	user := spec.User
	if user == "" {
		user = fmt.Sprintf("10001:%d", max(0, os.Getgid()))
	}
	args := []string{"run", "-d", "--name", name, "--network", agentNet, "--network-alias", name, "--restart=no", "--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3", "--read-only", "--init", "--user", user, "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true"}
	if spec.Limits.PIDs > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(spec.Limits.PIDs))
	}
	if spec.Limits.Memory != "" {
		args = append(args, "--memory", spec.Limits.Memory)
	}
	if spec.Limits.CPUs != "" {
		args = append(args, "--cpus", spec.Limits.CPUs)
	}
	if spec.OCIRuntime != "" {
		args = append(args, "--runtime", spec.OCIRuntime)
	}
	for _, f := range spec.EnvFiles {
		args = append(args, "--env-file", f)
	}
	args = append(args, hosts...)
	for _, m := range spec.Mounts {
		value := "type=bind,src=" + m.Source + ",dst=" + m.Target
		if m.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	for _, t := range spec.Tmpfs {
		args = append(args, "--tmpfs", t.Target+":"+t.Options)
	}
	for _, entry := range spec.Env {
		envName, _, ok := strings.Cut(entry, "=")
		if !ok || envName == "" {
			return Runtime{}, fmt.Errorf("backend: malformed env entry %q", entry)
		}
		args = append(args, "--env", envName)
	}
	for k, v := range labels {
		args = append(args, "--label", k+"="+v)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	started := false
	defer func() {
		if !started {
			d.cleanup(context.Background(), spec.Key)
		}
	}()
	if out, err := d.command(ctx, spec.Env, args...); err != nil {
		return Runtime{}, fmt.Errorf("backend: start %s: %w: %s", spec.Key, err, firstLine(out))
	}
	// A run argv attaches one network; extra literal networks ride
	// network connect.
	for _, network := range spec.Networks[1:] {
		if out, err := d.command(ctx, nil, "network", "connect", network, name); err != nil {
			return Runtime{}, fmt.Errorf("backend: attach %s: %w: %s", network, err, firstLine(out))
		}
	}
	rt, err := d.spawnControlRelay(ctx, spec, name, agentNet, control, labels)
	if err != nil {
		return Runtime{}, err
	}
	started = true
	return rt, nil
}

// spawnControlRelay starts the dual-homed sidecar (v0 spawnControlRelay):
// the only inbound route to an internal-network runtime, publishing only
// on the host loopback. It answers the workload's Runtime with the
// loopback address and chosen port.
func (d *dockerBackend) spawnControlRelay(ctx context.Context, spec Spec, name, agentNet, control string, labels map[string]string) (Runtime, error) {
	relay := name + "-ctl"
	target := "http://" + name + ":" + strconv.Itoa(spec.Port)
	args := []string{"run", "-d", "--name", relay, "--network", agentNet, "--restart=no", "--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3", "--read-only", "--init", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777", "-e", "HUB_CONTROL_RELAY_TARGET=" + target, "--entrypoint", "hub-runtime"}
	limits := spec.Relay.Limits
	if limits.PIDs > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(limits.PIDs))
	}
	if limits.Memory != "" {
		args = append(args, "--memory", limits.Memory)
	}
	if limits.CPUs != "" {
		args = append(args, "--cpus", limits.CPUs)
	}
	relayLabels := map[string]string{}
	for k, v := range labels {
		relayLabels[k] = v
	}
	relayLabels["hermes-hub.role"] = "control-relay"
	for k, v := range relayLabels {
		args = append(args, "--label", k+"="+v)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Runtime{}, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", port, controlRelayContainerPort))
	if spec.Relay.MountDir != "" {
		args = append(args, "-v", spec.Relay.MountDir+":"+spec.Relay.MountDir)
	}
	args = append(args, spec.Relay.Image, "control-relay")
	if out, err := d.command(ctx, nil, args...); err != nil {
		// A stale same-name relay from an older generation blocks the
		// spawn; reclaim it only when its labels still prove our
		// ownership.
		d.reclaimSidecarName(ctx, spec.Key, relay)
		if retryOut, retryErr := d.command(ctx, nil, args...); retryErr != nil {
			return Runtime{}, fmt.Errorf("start control relay: %w: %s; retry: %s", retryErr, firstLine(retryOut), firstLine(out))
		}
	}
	if out, err := d.command(ctx, nil, "network", "connect", control, relay); err != nil {
		_, _ = d.command(ctx, nil, "rm", "-f", relay)
		return Runtime{}, fmt.Errorf("attach control relay: %w: %s", err, firstLine(out))
	}
	return Runtime{ID: spec.Key, Space: spec.Space, Agent: spec.Agent, State: "running", Address: fmt.Sprintf("http://127.0.0.1:%d", port), Name: name, HostPort: port}, nil
}

// reclaimSidecarName removes a stale same-name container only when its
// labels still prove this backend owns it (v0 reclaimSidecarName).
func (d *dockerBackend) reclaimSidecarName(ctx context.Context, key, name string) {
	out, err := d.command(ctx, nil, "inspect", "--format", "{{json .Config.Labels}}", name)
	if err != nil {
		return
	}
	var lbl map[string]string
	if json.Unmarshal(out, &lbl) != nil {
		return
	}
	if lbl["hermes-hub.key"] != key || lbl[dockerManagedLabel] == "" {
		return
	}
	_, _ = d.command(ctx, nil, "rm", "-f", name)
}

// sandboxRelayHosts resolves the reviewed relay aliases to their IPs on
// the agent network (v0 sandboxRelayHosts): every wanted alias must map
// to exactly one private IPv4 or the spawn refuses — an ambiguous or
// forged alias never steers the workload's egress.
func (d *dockerBackend) sandboxRelayHosts(ctx context.Context, network string, wanted []string) ([]string, error) {
	if len(wanted) == 0 {
		return nil, nil
	}
	body, err := d.command(ctx, nil, "ps", "--filter", "network="+network, "--format", "{{.ID}}")
	ids := strings.Fields(string(body))
	if err != nil || len(body) > 4096 || len(ids) > 16 {
		return nil, errors.New("sandbox relay discovery unavailable")
	}
	hosts := map[string]string{}
	for _, id := range ids {
		body, err := d.command(ctx, nil, "inspect", "--format", `{{json (index .NetworkSettings.Networks "`+network+`")}}`, id)
		var endpoint struct {
			IPAddress string
			Aliases   []string
		}
		if err != nil || len(body) > 16*1024 || json.Unmarshal(body, &endpoint) != nil {
			return nil, errors.New("sandbox relay endpoint unavailable")
		}
		for _, alias := range endpoint.Aliases {
			for _, name := range wanted {
				if alias != name {
					continue
				}
				ip, err := netip.ParseAddr(endpoint.IPAddress)
				if err != nil || !ip.IsPrivate() || !ip.Is4() || hosts[name] != "" && hosts[name] != ip.String() {
					return nil, errors.New("sandbox relay alias is invalid or ambiguous")
				}
				hosts[name] = ip.String()
			}
		}
	}
	var args []string
	for _, name := range wanted {
		if hosts[name] == "" {
			return nil, errors.New("sandbox relay alias is missing")
		}
		args = append(args, "--add-host", name+":"+hosts[name])
	}
	return args, nil
}
