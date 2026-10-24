package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"os"
	"strconv"
	"strings"
	"time"
)

func (c *genericController) inspect(ctx context.Context, workload genericWorkload) (api.AdmissionReceipt, error) {
	inspectArgs := []string{"inspect", workload.plan.WorkloadID, workload.proxyName}
	if workload.dockerFallback {
		inspectArgs = append(inspectArgs, workload.relayName)
	}
	body, err := c.docker(ctx, inspectArgs...)
	if err != nil {
		return api.AdmissionReceipt{}, err
	}
	var containers []genericContainer
	expectedContainers := 2
	if workload.dockerFallback {
		expectedContainers = 3
	}
	if json.Unmarshal(body, &containers) != nil || len(containers) != expectedContainers {
		return api.AdmissionReceipt{}, api.ErrIsolation
	}
	network := "hermes-" + workload.plan.WorkloadID
	for i, container := range containers {
		if problem := genericProfileProblem(container, network, c.config.SeccompProfile, workload.plan.Execution); problem != "" {
			return api.AdmissionReceipt{}, fmt.Errorf("%w: generic runtime profile container %d: %s", api.ErrIsolation, i, problem)
		}
		credentialMountOK := credentialMountsOK(container.Mounts, workload.plan.CredentialMounts)
		bridgeMountOK := !workload.dockerFallback && credentialMountOK
		if workload.dockerFallback {
			bridgeMountOK = fallbackBridgeMountsOK(container.Mounts, workload)
		}
		if i == 0 && (!artifactImageMatches(container.Config.Image, workload.plan, workload.imageRef) || container.Config.User == "" || container.Config.User == "0" || container.Config.User == "0:0" || (!workload.dockerFallback && !credentialMountOK) || (workload.dockerFallback && !bridgeMountOK) || len(container.NetworkSettings.Networks) != 1) {
			mounts := make([]string, 0, len(container.Mounts))
			for _, mount := range container.Mounts {
				mounts = append(mounts, mount.Type+":"+mount.Destination+":"+strconv.FormatBool(mount.RW))
			}
			return api.AdmissionReceipt{}, fmt.Errorf("%w: MCP container image=%t user=%t credentials=%t bridge=%t mounts=%v expected=%d networks=%d", api.ErrIsolation, artifactImageMatches(container.Config.Image, workload.plan, workload.imageRef), container.Config.User != "" && container.Config.User != "0" && container.Config.User != "0:0", credentialMountOK, bridgeMountOK, mounts, len(workload.stateVols)+len(workload.plan.CredentialMounts)+1, len(container.NetworkSettings.Networks))
		}
		if i == 1 && (len(container.HostConfig.Binds) != 0 || len(container.Mounts) != 1 || container.Mounts[0].Type != "volume" || (container.Mounts[0].Name != workload.proxyVol && container.Mounts[0].Source != workload.proxyVol) || container.Mounts[0].Destination != "/etc/squid" || len(container.NetworkSettings.Networks) != 2 || container.Image == "") {
			return api.AdmissionReceipt{}, fmt.Errorf("%w: egress proxy identity or mounts", api.ErrIsolation)
		}
		if i == 2 && workload.dockerFallback && (container.Config.Image != workload.plan.SidecarImages[0] || container.Config.User == "" || container.Config.User == "0" || container.Config.User == "0:0" || len(container.HostConfig.Binds) != 0 || len(container.Mounts) != 1 || container.Mounts[0].Type != "volume" || (container.Mounts[0].Name != workload.relayVol && container.Mounts[0].Source != workload.relayVol) || container.Mounts[0].Destination != "/hermes-relay" || container.Mounts[0].RW || len(container.NetworkSettings.Networks) != 2 || !api.EnvHasBridgeToken(container.Config.Env)) {
			return api.AdmissionReceipt{}, fmt.Errorf("%w: relay identity or mounts", api.ErrIsolation)
		}
		if workload.dockerFallback && i == 0 && api.EnvContainsForwardingSecret(container.Config.Env) {
			return api.AdmissionReceipt{}, fmt.Errorf("%w: MCP container received a forwarding secret", api.ErrIsolation)
		}
	}
	return api.AdmissionReceipt{WorkloadID: workload.plan.WorkloadID, State: "running", Enforced: true, Endpoint: workload.endpoint, ImageDigest: workload.plan.Digest, SidecarImages: workload.plan.SidecarImages, Execution: workload.plan.Execution}, nil
}

func credentialMountsOK(actual []genericMount, declared []api.Mount) bool {
	if len(actual) != len(declared) {
		return false
	}
	for _, mount := range actual {
		if mount.Type != "bind" {
			return false
		}
		matched := false
		for _, want := range declared {
			if mount.Source == api.DockerBindSource(want.Source) && mount.Destination == want.Target && mount.RW != want.ReadOnly {
				if matched {
					return false
				}
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func genericProfileProblem(container genericContainer, network, seccomp string, execution api.ExecutionPolicy) string {
	checks := []struct {
		failed bool
		name   string
	}{
		{!container.State.Running, fmt.Sprintf("not-running(exit=%d)", container.State.ExitCode)},
		{container.HostConfig.Privileged, "privileged"},
		{!container.HostConfig.ReadonlyRootfs, "writable-root"},
		{container.HostConfig.NetworkMode != network, "network"},
		{container.HostConfig.PidMode != "", "pid-mode"},
		{len(container.HostConfig.CapAdd) != 0, "cap-add"},
		{len(container.HostConfig.CapDrop) != 1 || container.HostConfig.CapDrop[0] != "ALL", "cap-drop"},
		{!api.Contains(container.HostConfig.SecurityOpt, "no-new-privileges=true"), "no-new-privileges"},
		{!api.SecurityOptHasSeccomp(container.HostConfig.SecurityOpt, seccomp), "seccomp"},
		{container.HostConfig.NanoCpus != int64(execution.CPUMillis)*1000000, "cpu"},
		{container.HostConfig.Memory != int64(execution.MemoryMiB)*1048576, "memory"},
		{container.HostConfig.PidsLimit != int64(execution.MaxPIDs), "pids"},
	}
	for _, check := range checks {
		if check.failed {
			return check.name
		}
	}
	return ""
}

func fallbackBridgeMountsOK(mounts []genericMount, workload genericWorkload) bool {
	if len(mounts) != len(workload.stateVols)+len(workload.plan.CredentialMounts)+1 {
		return false
	}
	bridge := false
	states := make([]bool, len(workload.stateVols))
	for _, mount := range mounts {
		if mount.Type != "volume" {
			if mount.Type != "bind" {
				return false
			}
			matched := false
			for _, declared := range workload.plan.CredentialMounts {
				if mount.Source == api.DockerBindSource(declared.Source) && mount.Destination == declared.Target && mount.RW != declared.ReadOnly {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
			continue
		}
		if mount.Destination == "/hermes-bridge" {
			if bridge || mount.RW || (mount.Name != workload.bridgeVol && mount.Source != workload.bridgeVol) {
				return false
			}
			bridge = true
			continue
		}
		matched := false
		for i, declared := range workload.plan.Execution.Mounts {
			if mount.Destination == declared.Target && (mount.Name == workload.stateVols[i] || mount.Source == workload.stateVols[i]) && mount.RW != declared.ReadOnly {
				if states[i] {
					return false
				}
				states[i], matched = true, true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if !bridge {
		return false
	}
	for _, present := range states {
		if !present {
			return false
		}
	}
	return true
}

func (c *genericController) CleanupIdle(ctx context.Context, now time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := c.config.Budget.CleanupIdle(now)
	for _, id := range ids {
		workload, ok := c.workloads[id]
		if !ok {
			continue
		}
		if err := c.removeWorkload(ctx, workload, workload.plan.Definition.Workload.Stateful); err != nil {
			continue
		}
		delete(c.workloads, id)
	}
	return ids
}

// purgeWorkloadByName removes the leftover resources of a workload the
// controller map no longer records (idle-stopped earlier, state lost in a
// restart). Names derive deterministically from the workload ID, so a purge
// never touches foreign objects: the workload and companion containers plus
// every `<id>-state-N` volume carrying the state label.
func (c *genericController) purgeWorkloadByName(ctx context.Context, workloadID string) error {
	resources := genericFallbackResources(workloadID)
	var firstErr error
	note := func(_ []byte, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	note(c.docker(ctx, "rm", "--force", workloadID, resources.ProxyName, resources.RelayName, resources.BridgeSeed, resources.CredName))
	filters := []string{"volume", "ls", "-q", "--filter", "label=hermes-hub.role=generic-mcp-state"}
	if label := c.scopeLabel(); label != "" {
		filters = append(filters, "--filter", "label="+label)
	}
	listed, err := c.docker(ctx, filters...)
	if err != nil {
		note(nil, err)
	} else {
		for _, name := range strings.Fields(string(listed)) {
			if strings.HasPrefix(name, workloadID+"-state-") {
				note(c.docker(ctx, "volume", "rm", name))
			}
		}
	}
	return firstErr
}

func (c *genericController) removeWorkload(ctx context.Context, workload genericWorkload, keepState bool) error {
	if workload.remoteName != "" {
		if workload.remoteState != "" {
			_, _ = c.commandEnv(ctx, fallbackToolHiveEnv(workload.remoteState, nil), c.config.ToolHiveBinary, "rm", workload.remoteName)
		} else {
			_, _ = c.toolhive(ctx, "rm", workload.remoteName)
		}
	}
	names := []string{workload.plan.WorkloadID, workload.proxyName}
	if workload.relayName != "" {
		names = append(names, workload.relayName)
	}
	if workload.credName != "" {
		names = append(names, workload.credName)
	}
	if _, err := c.docker(ctx, append([]string{"rm", "--force"}, names...)...); err != nil {
		return err
	}
	_, _ = c.docker(ctx, "network", "rm", "hermes-"+workload.plan.WorkloadID)
	_, _ = c.docker(ctx, "volume", "rm", workload.proxyVol)
	if workload.bridgeVol != "" {
		_, _ = c.docker(ctx, "volume", "rm", workload.bridgeVol)
	}
	if workload.relayVol != "" {
		_, _ = c.docker(ctx, "volume", "rm", workload.relayVol)
	}
	if !keepState {
		var errs []error
		for _, volume := range workload.stateVols {
			if _, err := c.docker(ctx, "volume", "rm", volume); err != nil {
				errs = append(errs, err)
			}
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
	}
	if workload.remoteState != "" {
		_ = os.RemoveAll(workload.remoteState)
	}
	return nil
}
