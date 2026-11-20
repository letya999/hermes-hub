package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type cellSidecar struct {
	name   string
	volume string
}

// cellCredProxy runs cellinit cred-proxy in a throwaway companion and writes
// the header rules into its tmpfs — rules hold the real credential, so the
// file goes through docker cp, never argv/env/inspect. The CLI-cell squid
// topology moved to spawn-controller with the workers (S6); this companion
// stays for the ToolHive MCP workload path that still lives here.
func (c *genericController) cellCredProxy(ctx context.Context, cellName, network string, brokered []api.CliBrokeredCred, toolsImage string, labelArgs []string) (cellSidecar, error) {
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
	args := append([]string{"create", "--name", name, "--network", network, "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--mount", "type=bind,source=" + c.cellInitHost() + ",target=" + cellInitGuest + ",readonly"}, labelArgs...)
	args = append(args, "--cpus", "0.25", "--memory", "128m", "--pids-limit", "32", "--entrypoint", cellInitGuest, toolsImage, "cred-proxy", "--rules", "/tmp/rules.json", "--listen", "0.0.0.0:"+strconv.Itoa(credProxyPort))
	if _, err := c.docker(ctx, args...); err != nil {
		return cellSidecar{}, err
	}
	if c.scope != nil {
		c.scope.Register(name)
	}
	if _, err := c.docker(ctx, "network", "connect", "bridge", name); err != nil {
		return cellSidecar{}, err
	}
	if _, err := c.docker(ctx, "start", name); err != nil {
		return cellSidecar{}, err
	}
	// Rules go straight from controller memory into the sidecar tmpfs: docker
	// cp cannot write a read-only rootfs, and a host temp file would put the
	// credential on persistent disk. cellinit waits for the file to appear.
	if _, err := c.dockerStdin(ctx, bytes.NewReader(body), "exec", "-i", name, cellInitGuest, "write", "/tmp/rules.json"); err != nil {
		return cellSidecar{}, err
	}
	return cellSidecar{name: name}, nil
}
