package toolhub

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"
)

type cellSidecar struct {
	name   string
	volume string
}

// cellEgress builds the internal network plus the egress proxies the plan
// earned: a Squid CONNECT allowlist when real egress hosts are declared, and
// a cred-proxy when the request carries brokered credentials.
func (c *genericController) cellEgress(ctx context.Context, cell *cliCell, egress []string, brokered []cliBrokeredCred) (string, []cellSidecar, error) {
	cfg := c.config.CLI
	network := "hermes-" + cell.name + "-net"
	ok := false
	cleanup := func() {
		for _, name := range []string{cell.name + "-proxy", cell.name + "-cred"} {
			_, _ = c.docker(context.Background(), "rm", "-f", name)
		}
		_, _ = c.docker(context.Background(), "network", "rm", network)
		_, _ = c.docker(context.Background(), "volume", "rm", cell.name+"-proxy-config")
	}
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	if _, err := c.docker(ctx, append([]string{"network", "create", "--internal", "--label", "hermes-hub.role=cli-cell"}, append(c.scopeLabelArgs(), network)...)...); err != nil {
		return "", nil, err
	}
	var out []cellSidecar
	if len(egress) > 0 {
		proxyImage, err := c.resolveCellImage(ctx, cfg.ProxyImage)
		if err != nil {
			return "", nil, err
		}
		sidecar, err := c.cellSquid(ctx, cell.name, network, egress, proxyImage)
		if err != nil {
			return "", nil, err
		}
		out = append(out, sidecar)
	}
	if len(brokered) > 0 {
		toolsImage, err := c.resolveCellImage(ctx, cfg.ToolsImage)
		if err != nil {
			return "", nil, err
		}
		sidecar, err := c.cellCredProxy(ctx, cell.name, network, brokered, toolsImage)
		if err != nil {
			return "", nil, err
		}
		out = append(out, sidecar)
	}
	ok = true
	return network, out, nil
}

// cellSquid is the per-cell CONNECT allowlist proxy, the same shape the MCP
// workload path already audits: internal net for the cell, bridge for egress,
// config copied into a volume so no file leaves the controller fs writable.
func (c *genericController) cellSquid(ctx context.Context, cellName, network string, egress []string, proxyImage string) (cellSidecar, error) {
	proxyName := cellName + "-proxy"
	proxyVol := cellName + "-proxy-config"
	if _, err := c.docker(ctx, append([]string{"volume", "create", "--label", "hermes-hub.role=cli-cell"}, append(c.scopeLabelArgs(), proxyVol)...)...); err != nil {
		return cellSidecar{}, err
	}
	proxyConfig, err := genericProxyConfig(egress)
	if err != nil {
		return cellSidecar{}, err
	}
	// docker cp preserves the source mode; the allowlist carries no secrets.
	fileName, err := writeWorldReadableTemp(c.config.StateRoot, ".cellproxy-*", proxyConfig)
	if err != nil {
		return cellSidecar{}, err
	}
	defer os.Remove(fileName)
	args := append([]string{"create", "--name", proxyName, "--network", network, "--read-only", "--user", "31:31", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m", "--mount", "type=volume,source=" + proxyVol + ",target=/etc/squid"}, c.scopeLabelArgs()...)
	args = append(args, "--cpus", "0.25", "--memory", "256m", "--pids-limit", "64", proxyImage)
	if _, err := c.docker(ctx, args...); err != nil {
		return cellSidecar{}, err
	}
	if c.scope != nil {
		c.scope.register(proxyName)
	}
	if _, err := c.docker(ctx, "cp", fileName, proxyName+":/etc/squid/squid.conf"); err != nil {
		return cellSidecar{}, err
	}
	if _, err := c.docker(ctx, "network", "connect", "bridge", proxyName); err != nil {
		return cellSidecar{}, err
	}
	if _, err := c.docker(ctx, "start", proxyName); err != nil {
		return cellSidecar{}, err
	}
	if err := c.waitProxyListen(ctx, proxyName); err != nil {
		return cellSidecar{}, err
	}
	return cellSidecar{name: proxyName, volume: proxyVol}, nil
}

// cellCredProxy runs cellinit cred-proxy in a throwaway companion and writes
// the header rules into its tmpfs — rules hold the real credential, so the
// file goes through docker cp, never argv/env/inspect.
func (c *genericController) cellCredProxy(ctx context.Context, cellName, network string, brokered []cliBrokeredCred, toolsImage string) (cellSidecar, error) {
	name := cellName + "-cred"
	rules := map[string]map[string]string{}
	for _, cred := range brokered {
		rules[strings.ToLower(cred.Host)] = map[string]string{"Authorization": cred.Prefix + cred.Value}
		log.Printf("cli-cell: brokered credential env=%s host=%s", cred.EnvName, cred.Host)
	}
	body, err := json.Marshal(rules)
	if err != nil {
		return cellSidecar{}, err
	}
	file, err := os.CreateTemp(c.config.StateRoot, ".credrules-*")
	if err != nil {
		return cellSidecar{}, err
	}
	fileName := file.Name()
	defer os.Remove(fileName)
	if _, err = file.Write(body); err != nil {
		file.Close()
		return cellSidecar{}, err
	}
	if err = file.Close(); err != nil {
		return cellSidecar{}, err
	}
	args := append([]string{"create", "--name", name, "--network", network, "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--mount", "type=bind,source=" + c.cellInitHost() + ",target=" + cellInitGuest + ",readonly"}, c.scopeLabelArgs()...)
	args = append(args, "--cpus", "0.25", "--memory", "128m", "--pids-limit", "32", "--entrypoint", cellInitGuest, toolsImage, "cred-proxy", "--rules", "/tmp/rules.json", "--listen", "0.0.0.0:"+strconv.Itoa(credProxyPort))
	if _, err := c.docker(ctx, args...); err != nil {
		return cellSidecar{}, err
	}
	if c.scope != nil {
		c.scope.register(name)
	}
	if _, err := c.docker(ctx, "cp", fileName, name+":/tmp/rules.json"); err != nil {
		return cellSidecar{}, err
	}
	if _, err := c.docker(ctx, "network", "connect", "bridge", name); err != nil {
		return cellSidecar{}, err
	}
	if _, err := c.docker(ctx, "start", name); err != nil {
		return cellSidecar{}, err
	}
	return cellSidecar{name: name}, nil
}
