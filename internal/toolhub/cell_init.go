package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cellWorkspaceBind resolves the request's workspace into the host-side bind
// source; the empty string means the cell mounts nothing at /work.
func (c *genericController) cellWorkspaceBind(req api.CliExecRequest) (hostPath string, rw bool, err error) {
	cfg := c.config.CLI
	scope := req.Workspace.Scope
	if scope == "" {
		scope = "none"
	}
	switch scope {
	case "none":
		if req.Workspace.Path != "" {
			return "", false, fmt.Errorf("%w: workspace path without a scope", api.ErrInvalid)
		}
		return "", false, nil
	case "binding":
		access := req.Workspace.Access
		if access == "" {
			access = "rw"
		}
		if access != "rw" && access != "ro" {
			return "", false, fmt.Errorf("%w: binding workspace access", api.ErrInvalid)
		}
		path, err := filepath.Abs(req.Workspace.Path)
		if err != nil || !api.ContainedPath(c.config.StateRoot, path) || api.NoSymlinkPath(path) != nil {
			return "", false, fmt.Errorf("%w: binding workspace outside state root", api.ErrIsolation)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", false, fmt.Errorf("%w: binding workspace is missing", api.ErrIsolation)
		}
		return api.DockerBindSource(path), access == "rw", nil
	case "principal":
		if cfg.SpacesRoot == "" {
			return "", false, fmt.Errorf("%w: principal workspaces are not configured", api.ErrIsolation)
		}
		access := req.Workspace.Access
		if access == "" {
			access = "ro"
		}
		if access != "rw" && access != "ro" {
			return "", false, fmt.Errorf("%w: principal workspace access", api.ErrInvalid)
		}
		path := filepath.Join(cfg.SpacesRoot, req.Principal, "workspace")
		if cfg.Environment != "" {
			// Managed principals keep their real workspace under
			// managed/<env>/workspace; the top-level dir is the unmanaged
			// layout's vestige. Fall back to it when no managed dir exists.
			managed := filepath.Join(cfg.SpacesRoot, req.Principal, "managed", cfg.Environment, "workspace")
			if info, err := os.Stat(managed); err == nil && info.IsDir() {
				path = managed
			}
		}
		if !api.ContainedPath(cfg.SpacesRoot, path) || api.NoSymlinkPath(path) != nil {
			return "", false, fmt.Errorf("%w: principal workspace path", api.ErrIsolation)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", false, fmt.Errorf("%w: principal workspace is missing", api.ErrIsolation)
		}
		return api.DockerBindSource(path), access == "rw", nil
	default:
		return "", false, fmt.Errorf("%w: unknown workspace scope %q", api.ErrInvalid, scope)
	}
}

// cellSkillsBind resolves the host path bound read-only at /skills: the
// principal's rendered effective skills view, mounted only for
// principal-scoped cells. The daemon interprets bind sources on the host, so
// the container-local path goes through DockerBindSource like /work does.
func (c *genericController) cellSkillsBind(req api.CliExecRequest) string {
	if req.Workspace.Scope != "principal" || c.config.CLI.SpacesRoot == "" {
		return ""
	}
	path := filepath.Join(c.config.CLI.SpacesRoot, req.Principal, "generated", "skills-effective")
	if !api.ContainedPath(c.config.CLI.SpacesRoot, path) || api.NoSymlinkPath(path) != nil {
		return ""
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return ""
	}
	return api.DockerBindSource(path)
}

// cellArgv assembles the docker create argv for a cell. Every isolation claim
// the receipt later makes is set here and verified back from inspect.
func (c *genericController) cellArgv(req api.CliExecRequest, cell *cliCell, runtime string) []string {
	limits := req.Plan.Execution
	args := []string{
		"create", "--name", cell.name,
		"--user", c.cellUID(req, cell.workRW),
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true",
		"--security-opt", "seccomp=" + c.config.SeccompProfile,
		"--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=64m",
		"--cpus", strconv.FormatFloat(float64(limits.CPUMillis)/1000, 'f', 3, 64),
		"--memory", strconv.Itoa(limits.MemoryMiB) + "m",
		"--memory-swap", strconv.Itoa(limits.MemoryMiB) + "m",
		"--pids-limit", strconv.Itoa(limits.MaxPIDs),
		"--workdir", cellWorkGuest,
	}
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}
	if cell.installPackages {
		uid, gid, _ := strings.Cut(c.cellUID(req, cell.workRW), ":")
		homeMiB := limits.MemoryMiB / 2
		if homeMiB < 256 {
			homeMiB = 256
		}
		args = append(args, "--tmpfs", cellHomeGuest+":rw,exec,nosuid,nodev,size="+strconv.Itoa(homeMiB)+"m,mode=0700,uid="+uid+",gid="+gid)
		args = append(args, "--tmpfs", cellUserTmpGuest+":rw,exec,nosuid,nodev,size=256m,mode=0700,uid="+uid+",gid="+gid)
	}
	if cell.network == "" {
		args = append(args, "--network", "none")
	} else {
		args = append(args, "--network", cell.network)
	}
	labels := append(c.scopeLabelArgs(),
		"--label", "hermes-hub.cell=true",
		"--label", "hermes-hub.cell.principal="+req.Principal,
		"--label", "hermes-hub.cell.lifecycle="+cell.lifecycle,
		"--label", api.LabelOwner+"="+req.Principal,
	)
	if req.BindingID != "" {
		labels = append(labels, "--label", "hermes-hub.cell.binding="+req.BindingID)
		labels = append(labels, "--label", api.LabelBinding+"="+req.BindingID)
	}
	if req.JobID != "" {
		labels = append(labels, "--label", "hermes-hub.cell.job="+req.JobID)
	}
	args = append(args, labels...)
	// The helper is mounted ro from the controller-visible copy; the daemon
	// resolves the host path so the cell never sees controller state.
	args = append(args, "--mount", "type=bind,source="+c.cellInitHost()+",target="+cellInitGuest+",readonly")
	if cell.skillsHost != "" {
		args = append(args, "--mount", "type=bind,source="+cell.skillsHost+",target="+cellSkillsGuest+",readonly")
	}
	if cell.workHost != "" {
		mount := "type=bind,source=" + cell.workHost + ",target=" + cellWorkGuest
		if !cell.workRW {
			mount += ",readonly"
		}
		args = append(args, "--mount", mount)
	} else {
		args = append(args, "--tmpfs", cellWorkGuest+":rw,noexec,nosuid,nodev,size=64m")
	}
	for i, ref := range cell.toolset {
		args = append(args, "--mount", "type=image,source="+ref+",target="+cellToolsetGuest+"/"+strconv.Itoa(i)+",readonly")
	}
	args = append(args, "--entrypoint", cellInitGuest, cell.image, "pause")
	return args
}

// cellInitHost returns the state-root copy of cellinit — the source the
// daemon resolves when a sibling bind mounts the helper.
func (c *genericController) cellInitHost() string {
	return api.DockerBindSource(filepath.Join(c.config.StateRoot, "cli-cellinit"))
}

// cellInitSeed copies the shipped cellinit into the state root so a sibling
// bind mount can source it. Re-seeded on every controller start so an
// upgraded binary replaces any stale or tampered copy.
func (c *genericController) cellInitSeed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cellInitOK {
		return nil
	}
	src, err := os.Open(c.config.CLI.CellInit)
	if err != nil {
		return fmt.Errorf("%w: cellinit binary: %v", api.ErrIsolation, err)
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: cellinit is not a regular file", api.ErrIsolation)
	}
	tmp, err := os.CreateTemp(c.config.StateRoot, ".cellinit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.Copy(tmp, io.LimitReader(src, 64<<20)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpName, 0755); err != nil {
		return err
	}
	if err = os.Rename(tmpName, filepath.Join(c.config.StateRoot, "cli-cellinit")); err != nil {
		return err
	}
	c.cellInitOK = true
	return nil
}

// cellDrifted runs the warm-reuse guard: cheap re-inspect of the immutable
// profile plus the in-cell canary (token intact, /tmp clean, no stray
// processes). Any deviation is terminal for the cell.
func (c *genericController) cellDrifted(ctx context.Context, cell *cliCell) string {
	container, err := c.inspectCell(ctx, cell.name)
	if err != nil {
		return "inspect:" + err.Error()
	}
	if problem := cellProfileProblem(container, cell, c.config.SeccompProfile, c.config.StateRoot, true); problem != "" {
		return problem
	}
	if code, err := c.cellRun(ctx, nil, "exec", cell.name, cellInitGuest, "clean", "/tmp", cellCanaryName, cell.canary); err != nil || code != 0 {
		return "canary-tmp"
	}
	if code, err := c.cellRun(ctx, nil, "exec", cell.name, cellInitGuest, "procs"); err != nil || code != 0 {
		return "stray-procs"
	}
	return ""
}

// inspectCell returns the raw inspect record; the caller compares it against
// the profile the create request asked for.
func (c *genericController) inspectCell(ctx context.Context, name string) (genericContainer, error) {
	body, err := c.docker(ctx, "inspect", name)
	if err != nil {
		return genericContainer{}, fmt.Errorf("%w: cell inspect: %v", api.ErrIsolation, err)
	}
	var containers []genericContainer
	if json.Unmarshal(body, &containers) != nil || len(containers) != 1 {
		return genericContainer{}, fmt.Errorf("%w: cell inspect", api.ErrIsolation)
	}
	return containers[0], nil
}

// cellProfileProblem names the first violated cell invariant, or "" when the
// inspected container carries every claim the receipt will make. wantRunning
// distinguishes the pre-start inspect (created) from the post-start one.
func cellProfileProblem(container genericContainer, cell *cliCell, seccomp, stateRoot string, wantRunning bool) string {
	if container.State.Running != wantRunning {
		return "state"
	}
	if container.HostConfig.Privileged {
		return "privileged"
	}
	if container.HostConfig.PidMode != "" {
		return "host-pid"
	}
	if !container.HostConfig.ReadonlyRootfs {
		return "writable-root"
	}
	if len(container.HostConfig.CapAdd) != 0 || len(container.HostConfig.CapDrop) != 1 || container.HostConfig.CapDrop[0] != "ALL" {
		return "caps"
	}
	if !api.Contains(container.HostConfig.SecurityOpt, "no-new-privileges=true") || !api.SecurityOptHasSeccomp(container.HostConfig.SecurityOpt, seccomp) {
		return "security-opt"
	}
	if container.Config.Image != cell.image {
		return "image"
	}
	user := container.Config.User
	if user == "" || user == "0" || user == "0:0" || user == "root" {
		return "root-user"
	}
	if cell.runtime != "" && container.HostConfig.Runtime != cell.runtime {
		return "runtime"
	}
	wantNet := cell.network
	if wantNet == "" {
		wantNet = "none"
	}
	if container.HostConfig.NetworkMode != wantNet {
		return "network"
	}
	labels := container.Config.Labels
	if labels["hermes-hub.cell"] != "true" || labels["hermes-hub.cell.principal"] != cell.principal || labels["hermes-hub.cell.lifecycle"] != cell.lifecycle {
		return "labels"
	}
	// The mount table must contain nothing the cell did not declare:
	// /cellinit ro bind, /tmp tmpfs, /work (bind or scratch tmpfs) plus
	// optional /tools/<i> image layers. Anything else denies — this is what
	// makes /state, credential stores and the docker socket impossible
	// inside a cell. Tmpfs lands in .Mounts on the classic image store and
	// only in HostConfig.Tmpfs on containerd-store daemons; both count.
	wantMounts := map[string]bool{cellInitGuest: true, "/tmp": true, cellWorkGuest: true}
	if cell.skillsHost != "" {
		wantMounts[cellSkillsGuest] = true
	}
	if cell.installPackages {
		wantMounts[cellHomeGuest] = true
		wantMounts[cellUserTmpGuest] = true
	}
	for i := range cell.toolset {
		wantMounts[cellToolsetGuest+"/"+strconv.Itoa(i)] = true
	}
	tmpfs := map[string]bool{}
	for path := range container.HostConfig.Tmpfs {
		tmpfs[path] = true
	}
	seen := map[string]bool{}
	for _, mount := range container.Mounts {
		if !wantMounts[mount.Destination] {
			return "mount:" + mount.Destination
		}
		switch mount.Destination {
		case cellInitGuest:
			if mount.Type != "bind" || mount.RW || mount.Source != api.DockerBindSource(filepath.Join(stateRoot, "cli-cellinit")) {
				return "cellinit-mount"
			}
		case cellSkillsGuest:
			if mount.Type != "bind" || mount.RW || mount.Source != cell.skillsHost {
				return "skills-mount"
			}
		case cellWorkGuest:
			if cell.workHost == "" {
				if mount.Type != "tmpfs" {
					return "work-mount"
				}
			} else if mount.Type != "bind" || mount.Source != cell.workHost || mount.RW != cell.workRW {
				return "work-mount"
			}
		case "/tmp":
			if mount.Type != "tmpfs" {
				return "tmp-mount"
			}
		case cellHomeGuest:
			if mount.Type != "tmpfs" {
				return "home-mount"
			}
		case cellUserTmpGuest:
			if mount.Type != "tmpfs" {
				return "usertmp-mount"
			}
		default:
			if mount.Type != "image" || mount.RW {
				return "toolset-mount"
			}
		}
		if mount.Type == "tmpfs" {
			tmpfs[mount.Destination] = true
		}
		seen[mount.Destination] = true
	}
	if !tmpfs["/tmp"] {
		return "tmp-mount"
	}
	if cell.installPackages && !tmpfs[cellHomeGuest] {
		return "home-mount"
	}
	for path := range tmpfs {
		if path != "/tmp" && !(path == cellWorkGuest && cell.workHost == "") && !(path == cellHomeGuest && cell.installPackages) && !(path == cellUserTmpGuest && cell.installPackages) {
			return "tmpfs:" + path
		}
	}
	if cell.workHost == "" && !tmpfs[cellWorkGuest] {
		return "work-mount"
	}
	if !seen[cellInitGuest] || (cell.workHost != "" && !seen[cellWorkGuest]) {
		return "mount-missing"
	}
	for i := range cell.toolset {
		if !seen[cellToolsetGuest+"/"+strconv.Itoa(i)] {
			return "toolset-mount"
		}
	}
	return ""
}
