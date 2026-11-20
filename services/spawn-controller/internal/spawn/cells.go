package spawn

// Cell spec construction, moved from v0 internal/toolhub/{cell,cell_init,
// cell_net}.go (CHG-0336 S6): ToolHub keeps deciding which cell a call
// needs; spawn-controller owns every bind, tmpfs, sidecar and limit the
// cell runs under. The moved functions keep their v0 algorithms — only
// their inputs (env config, request fields) changed shape.

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	thapi "github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/shared/httpapi"
	"github.com/letya999/hermes-hub/shared/store"
)

// Cell guest layout — the v0 internal/toolhub/cell.go constants.
const (
	cellInitGuest    = "/cellinit"
	cellWorkGuest    = "/work"
	cellHomeGuest    = "/cellhome"
	cellUserTmpGuest = "/usertmp"
	cellCanaryPath   = "/tmp/.hermes-canary"
	cellCanaryName   = ".hermes-canary"
	cellToolsetGuest = "/tools"
	cellSkillsGuest  = "/skills"
	credProxyPort    = 3129
	cellProxyPort    = 3128
)

// WorkspaceScope names the workspace bind a claim asks for.
type WorkspaceScope string

const (
	WorkspaceNone        WorkspaceScope = "none"
	WorkspaceBinding     WorkspaceScope = "binding"
	WorkspacePrincipal   WorkspaceScope = "principal"
	WorkspaceSkillSource WorkspaceScope = "skill-source"
)

// WorkerClaim is everything a claim needs to build the cell spec: the
// reuse key, the pinned image and the shape ToolHub already admitted.
// Brokered carries live credential values — they reach the cred sidecar
// through the exec file channel only and are never persisted, logged or
// returned.
type WorkerClaim struct {
	CellKey         string
	Image           string
	Agent           string
	Principal       string
	BindingID       string
	JobID           string
	Workspace       CellWorkspace
	Skills          bool
	Toolset         []string
	Egress          []string
	Brokered        []CellCred
	InstallPackages bool
	Limits          CellLimits
}

// CellWorkspace is the workspace bind request: a scope plus the
// scope-shaped path/access payload.
type CellWorkspace struct {
	Scope  WorkspaceScope
	Path   string
	Access string // "rw" | "ro"; "" = scope default
}

// CellCred is one brokered credential the cred sidecar injects on the
// declared host — the moved v0 CliBrokeredCred.
type CellCred struct {
	EnvName string
	Host    string
	Prefix  string
	Value   string
}

// CellLimits is the compute envelope the plan earned.
type CellLimits struct {
	CPUMillis      int
	MemoryMiB      int
	MaxPIDs        int
	TimeoutSeconds int
	OutputBytes    int64
}

// Cells is the operator configuration the spec builder reads once: the
// resolved images, the cellinit binary, the containment roots and the
// daemon-facing security profile. Cells are enabled only when both
// images and the cellinit binary are configured.
type Cells struct {
	ToolsImage      string
	ProxyImage      string
	CellInitSource  string // shipped cellinit binary; seeded under StateRoot
	StateRoot       string // cellinit seed + binding-scope containment root
	SpacesRoot      string // principal workspace root ("" disables it)
	Environment     string // managed env dir under the principal's space
	Runtime         string // OCI runtime for cells; "" = runc
	SeccompProfile  string // host path to the seccomp profile
	PerPrincipalUID bool   // principal-derived uid on rw workspace binds

	mu         sync.Mutex
	cellInitOK bool
}

// Enabled reports whether cells can be claimed at all (V5).
func (c *Cells) Enabled() bool {
	return c != nil && c.ToolsImage != "" && c.CellInitSource != ""
}

// cellInitHost returns the state-root copy of cellinit — the source the
// daemon resolves when a sibling bind mounts the helper (moved from v0
// cell_init.go cellInitHost).
func (c *Cells) cellInitHost() string {
	return thapi.DockerBindSource(filepath.Join(c.StateRoot, "cli-cellinit"))
}

// cellInitSeed copies the shipped cellinit into the state root so a
// sibling bind mount can source it; re-seeded on every claim path entry so
// an upgraded binary replaces any stale or tampered copy (moved from v0
// cell_init.go cellInitSeed).
func (c *Cells) cellInitSeed() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cellInitOK {
		return nil
	}
	src, err := os.Open(c.CellInitSource)
	if err != nil {
		return fmt.Errorf("cells: cellinit binary: %w", err)
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("cells: cellinit is not a regular file")
	}
	tmp, err := os.CreateTemp(c.StateRoot, ".cellinit-*")
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
	if err = os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err = os.Rename(tmpName, filepath.Join(c.StateRoot, "cli-cellinit")); err != nil {
		return err
	}
	c.cellInitOK = true
	return nil
}

// skillSourceIDPattern is the only workspace payload a skill-source cell
// may bind: one <16-hex>.git mirror under the principal's .sources
// (moved from v0 cell_init.go).
var skillSourceIDPattern = regexp.MustCompile(`^[0-9a-f]{16}\.git$`)

// cellWorkspaceBind resolves the claim's workspace into the host-side bind
// source; the empty string means the cell mounts nothing at /work (moved
// from v0 cell_init.go cellWorkspaceBind — scope paths are the space's own,
// so a mount can never reach a different space's tree).
func (c *Cells) cellWorkspaceBind(spaceID string, req WorkerClaim) (hostPath string, rw bool, err error) {
	scope := req.Workspace.Scope
	if scope == "" {
		scope = WorkspaceNone
	}
	access := req.Workspace.Access
	switch scope {
	case WorkspaceNone:
		if req.Workspace.Path != "" {
			return "", false, fmt.Errorf("%w: workspace path without a scope", httpapi.ErrInvalid)
		}
		return "", false, nil
	case WorkspaceBinding:
		if access == "" {
			access = "rw"
		}
		if access != "rw" && access != "ro" {
			return "", false, fmt.Errorf("%w: binding workspace access", httpapi.ErrInvalid)
		}
		path, err := filepath.Abs(req.Workspace.Path)
		if err != nil || !thapi.ContainedPath(c.StateRoot, path) || thapi.NoSymlinkPath(path) != nil {
			return "", false, fmt.Errorf("%w: binding workspace outside state root", httpapi.ErrForbidden)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", false, fmt.Errorf("%w: binding workspace is missing", httpapi.ErrForbidden)
		}
		return thapi.DockerBindSource(path), access == "rw", nil
	case WorkspacePrincipal:
		if c.SpacesRoot == "" {
			return "", false, fmt.Errorf("%w: principal workspaces are not configured", httpapi.ErrForbidden)
		}
		if access == "" {
			access = "ro"
		}
		if access != "rw" && access != "ro" {
			return "", false, fmt.Errorf("%w: principal workspace access", httpapi.ErrInvalid)
		}
		if req.Principal != spaceID {
			return "", false, fmt.Errorf("%w: workspace belongs to another space", httpapi.ErrForbidden)
		}
		path := filepath.Join(c.SpacesRoot, spaceID, "workspace")
		if c.Environment != "" {
			// Managed principals keep their real workspace under
			// managed/<env>/workspace; the top-level dir is the unmanaged
			// layout's vestige. Fall back to it when no managed dir exists.
			managed := filepath.Join(c.SpacesRoot, spaceID, "managed", c.Environment, "workspace")
			if info, err := os.Stat(managed); err == nil && info.IsDir() {
				path = managed
			}
		}
		if !thapi.ContainedPath(c.SpacesRoot, path) || thapi.NoSymlinkPath(path) != nil {
			return "", false, fmt.Errorf("%w: principal workspace path", httpapi.ErrForbidden)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", false, fmt.Errorf("%w: principal workspace is missing", httpapi.ErrForbidden)
		}
		return thapi.DockerBindSource(path), access == "rw", nil
	case WorkspaceSkillSource:
		if access != "rw" {
			return "", false, fmt.Errorf("%w: skill-source access", httpapi.ErrInvalid)
		}
		if !skillSourceIDPattern.MatchString(req.Workspace.Path) {
			return "", false, fmt.Errorf("%w: skill-source workspace id", httpapi.ErrInvalid)
		}
		if req.Principal != spaceID || c.SpacesRoot == "" {
			return "", false, fmt.Errorf("%w: workspace belongs to another space", httpapi.ErrForbidden)
		}
		sources := filepath.Join(c.SpacesRoot, spaceID, "skills", ".sources")
		path := filepath.Join(sources, req.Workspace.Path)
		if !thapi.ContainedPath(sources, path) || thapi.NoSymlinkPath(path) != nil {
			return "", false, fmt.Errorf("%w: skill-source workspace path", httpapi.ErrForbidden)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", false, fmt.Errorf("%w: skill-source mirror is missing", httpapi.ErrForbidden)
		}
		return thapi.DockerBindSource(path), true, nil
	default:
		return "", false, fmt.Errorf("%w: unknown workspace scope %q", httpapi.ErrInvalid, scope)
	}
}

// cellSkillsBind resolves the host path bound read-only at /skills: the
// principal's rendered effective skills view, mounted only for
// principal-scoped cells (moved from v0 cell_init.go cellSkillsBind).
func (c *Cells) cellSkillsBind(spaceID string, req WorkerClaim) string {
	if !req.Skills || req.Workspace.Scope != WorkspacePrincipal || c.SpacesRoot == "" || req.Principal != spaceID {
		return ""
	}
	path := filepath.Join(c.SpacesRoot, spaceID, "generated", "skills-effective")
	if !thapi.ContainedPath(c.SpacesRoot, path) || thapi.NoSymlinkPath(path) != nil {
		return ""
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return ""
	}
	return thapi.DockerBindSource(path)
}

// cellUID picks the cell user: 10001 by default, or a stable per-principal
// uid (20000+) when the operator opted into uid separation (moved from v0
// cell.go cellUID).
func (c *Cells) cellUID(principal string, rw bool) string {
	if c.PerPrincipalUID && rw {
		h := fnv.New32a()
		_, _ = h.Write([]byte(principal))
		uid := 20000 + int(h.Sum32()%30000)
		return strconv.Itoa(uid) + ":" + strconv.Itoa(uid)
	}
	return "10001:10001"
}

// realEgress strips the loopback-only marker hosts; a plan whose egress is
// only loopback runs with --network none (moved from v0 cell.go
// realEgress).
func realEgress(egress []string) []string {
	out := make([]string, 0, len(egress))
	for _, host := range egress {
		h := strings.ToLower(strings.TrimSpace(host))
		if h == "" || h == "127.0.0.1" || h == "localhost" || h == "::1" {
			continue
		}
		out = append(out, host)
	}
	return out
}

// proxyConfig renders the squid allowlist the egress hosts earned — the
// v0 api.GenericProxyConfig, still shared with ToolHub's workload path.
// The allowlist carries no secrets.
func proxyConfig(egress []string) ([]byte, error) {
	out, err := thapi.GenericProxyConfig(egress)
	if err != nil {
		return nil, fmt.Errorf("%w: proxy config", httpapi.ErrInvalid)
	}
	return []byte(out), nil
}

// spec resolves a claim into the backend Spec the cell runs under — the
// moved v0 cellArgv/cellEgress construction. Every isolation claim the
// receipt makes is declared here; the backend applies or refuses it.
func (c *Cells) spec(t store.Tenant, spaceID string, req WorkerClaim, key string) (backend.Spec, error) {
	if err := c.cellInitSeed(); err != nil {
		return backend.Spec{}, err
	}
	runtime := c.Runtime
	if runtime == "" {
		runtime = "runc"
	}
	workHost, workRW, err := c.cellWorkspaceBind(spaceID, req)
	if err != nil {
		return backend.Spec{}, err
	}
	skillsHost := c.cellSkillsBind(spaceID, req)
	uid := c.cellUID(req.Principal, workRW)
	limits := req.Limits
	spec := backend.Spec{
		Key:            key,
		Org:            t.Org,
		Owner:          t.Owner,
		Space:          spaceID,
		Agent:          req.Agent,
		Image:          req.Image,
		User:           uid,
		Entrypoint:     cellInitGuest,
		Command:        []string{"pause"},
		Workdir:        cellWorkGuest,
		OCIRuntime:     runtime,
		SeccompProfile: c.SeccompProfile,
		Tmpfs: []backend.Tmpfs{
			{Target: "/tmp", Options: "rw,noexec,nosuid,nodev,size=64m"},
		},
		Limits: backend.Limits{
			Memory:     strconv.Itoa(limits.MemoryMiB) + "m",
			MemorySwap: strconv.Itoa(limits.MemoryMiB) + "m",
			CPUs:       strconv.FormatFloat(float64(limits.CPUMillis)/1000, 'f', 3, 64),
			PIDs:       limits.MaxPIDs,
		},
	}
	// Ownership labels the v0 cell create stamped; the label query the
	// reconciler runs still finds cells by owner and binding.
	if req.BindingID != "" {
		spec.Labels = map[string]string{"hermes-hub.binding": req.BindingID}
	}
	if req.JobID != "" {
		if spec.Labels == nil {
			spec.Labels = map[string]string{}
		}
		spec.Labels["hermes-hub.job"] = req.JobID
	}
	if req.InstallPackages {
		homeMiB := limits.MemoryMiB / 2
		if homeMiB < 256 {
			homeMiB = 256
		}
		spec.Tmpfs = append(spec.Tmpfs,
			backend.Tmpfs{Target: cellHomeGuest, Options: "rw,exec,nosuid,nodev,size=" + strconv.Itoa(homeMiB) + "m,mode=0700,uid=" + strings.Split(uid, ":")[0] + ",gid=" + strings.Split(uid, ":")[1]},
			backend.Tmpfs{Target: cellUserTmpGuest, Options: "rw,exec,nosuid,nodev,size=256m,mode=0700,uid=" + strings.Split(uid, ":")[0] + ",gid=" + strings.Split(uid, ":")[1]},
		)
	}
	spec.Mounts = append(spec.Mounts, backend.Mount{Source: c.cellInitHost(), Target: cellInitGuest, ReadOnly: true})
	if skillsHost != "" {
		spec.Mounts = append(spec.Mounts, backend.Mount{Source: skillsHost, Target: cellSkillsGuest, ReadOnly: true})
	}
	if workHost != "" {
		spec.Mounts = append(spec.Mounts, backend.Mount{Source: workHost, Target: cellWorkGuest, ReadOnly: !workRW})
	} else {
		spec.Tmpfs = append(spec.Tmpfs, backend.Tmpfs{Target: cellWorkGuest, Options: "rw,noexec,nosuid,nodev,size=64m"})
	}
	for i, ref := range req.Toolset {
		spec.ImageMounts = append(spec.ImageMounts, backend.Mount{Source: ref, Target: cellToolsetGuest + "/" + strconv.Itoa(i), ReadOnly: true})
	}
	egress := realEgress(req.Egress)
	if len(egress) > 0 {
		sidecar, err := c.squidSidecar(egress)
		if err != nil {
			return backend.Spec{}, err
		}
		spec.Sidecars = append(spec.Sidecars, sidecar)
	}
	if len(req.Brokered) > 0 {
		sidecar, err := c.credSidecar(req.Brokered)
		if err != nil {
			return backend.Spec{}, err
		}
		spec.Sidecars = append(spec.Sidecars, sidecar)
	}
	return spec, nil
}

// squidSidecar is the per-cell CONNECT allowlist proxy as a spec: internal
// net for the cell, bridge for egress, config delivered by docker cp into a
// per-worker volume (moved from v0 cell_net.go cellSquid).
func (c *Cells) squidSidecar(egress []string) (backend.SidecarSpec, error) {
	if c.ProxyImage == "" {
		return backend.SidecarSpec{}, fmt.Errorf("%w: egress cells need a proxy image", httpapi.ErrForbidden)
	}
	config, err := proxyConfig(egress)
	if err != nil {
		return backend.SidecarSpec{}, err
	}
	return backend.SidecarSpec{
		Name: "proxy", Image: c.ProxyImage, User: "31:31",
		Tmpfs:    []backend.Tmpfs{{Target: "/tmp", Options: "rw,nosuid,nodev,size=64m"}},
		Volumes:  []backend.Mount{{Source: "proxy-config", Target: "/etc/squid"}},
		Bridge:   true,
		WaitPort: cellProxyPort,
		Files:    []backend.SidecarFile{{Target: "/etc/squid/squid.conf", Content: config}},
		Limits:   backend.Limits{CPUs: "0.25", Memory: "256m", PIDs: 64},
	}, nil
}

// credSidecar runs cellinit cred-proxy in a companion with the rules
// streamed into its tmpfs — rules hold the real credential, so they go
// through the exec file channel, never argv/env/inspect (moved from v0
// cell_net.go cellCredProxy).
func (c *Cells) credSidecar(brokered []CellCred) (backend.SidecarSpec, error) {
	rules := map[string]map[string]string{}
	for _, cred := range brokered {
		rules[strings.ToLower(cred.Host)] = map[string]string{"Authorization": cred.Prefix + cred.Value}
	}
	body, err := json.Marshal(rules)
	if err != nil {
		return backend.SidecarSpec{}, err
	}
	return backend.SidecarSpec{
		Name: "cred", Image: c.ToolsImage, User: "10001:10001",
		Entrypoint: cellInitGuest,
		Command:    []string{"cred-proxy", "--rules", "/tmp/rules.json", "--listen", "0.0.0.0:" + strconv.Itoa(credProxyPort)},
		Tmpfs:      []backend.Tmpfs{{Target: "/tmp", Options: "rw,nosuid,nodev,size=16m"}},
		Mounts:     []backend.Mount{{Source: c.cellInitHost(), Target: cellInitGuest, ReadOnly: true}},
		Bridge:     true,
		Files:      []backend.SidecarFile{{Target: "/tmp/rules.json", Content: body, ViaExec: true}},
		Limits:     backend.Limits{CPUs: "0.25", Memory: "128m", PIDs: 32},
	}, nil
}
