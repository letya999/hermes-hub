package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/hosting"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// HostingProfile is operator-owned; agents never choose images, volumes or runtimes.
type HostingProfile struct {
	AdmissionDigest string `json:"admission_digest,omitempty"`
	Owner           string `json:"owner"`
	Environment     string `json:"environment"`
	Volume          string `json:"volume"`
	Image           string `json:"image"`
	Runtime         string `json:"runtime"`
}

func (c *genericController) hostingProfile(w hosting.Workload) (HostingProfile, error) {
	p, ok := c.config.Hosting[w.Binding.Owner+"/"+w.Binding.Environment]
	if !ok || p.AdmissionDigest != w.AdmissionDigest || p.Owner != w.Binding.Owner || p.Environment != w.Binding.Environment || !identitySiteName(w.Name) || w.Revision == 0 || len(w.Release) != 64 || strings.Trim(w.Release, "0123456789abcdef") != "" || p.Volume == "" || strings.ContainsAny(p.Volume, "/\\:,") || api.ValidPinnedImageRef(p.Image) != nil || (p.Runtime != "runsc" && p.Runtime != "kata") || !filepath.IsAbs(c.config.HostingCellInit) {
		return p, hosting.ErrDenied
	}
	if !time.Now().Before(w.ExpiresAt) || time.Until(w.ExpiresAt) > 30*24*time.Hour {
		return p, hosting.ErrDenied
	}
	return p, nil
}
func identitySiteName(n string) bool {
	if len(n) < 1 || len(n) > 40 || n[0] < 'a' || n[0] > 'z' || n[len(n)-1] == '-' {
		return false
	}
	for _, r := range n {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
func siteObject(w hosting.Workload) string {
	return "site-" + cellHash(w.Binding.Owner, w.Binding.Environment, w.Name, strconv.FormatUint(w.Revision, 10), w.Release)
}
func (c *genericController) siteProfile(ctx context.Context, w hosting.Workload, p HostingProfile, running bool) error {
	body, err := c.docker(ctx, "inspect", siteObject(w))
	if err != nil {
		return hosting.ErrUnavailable
	}
	var items []struct {
		genericContainer
		Config struct {
			Image, User, WorkingDir string
			Entrypoint, Cmd, Env    []string
			Labels                  map[string]string
		}
		HostConfig struct {
			NanoCpus, Memory, MemorySwap, PidsLimit int64
			Privileged, ReadonlyRootfs              bool
			NetworkMode, PidMode, IpcMode, Runtime  string
			CapAdd, CapDrop, SecurityOpt            []string
			Mounts                                  []struct {
				Type, Source, Target string
				ReadOnly             bool
				VolumeOptions        *struct{ Subpath string }
			}
			Devices         []json.RawMessage
			DeviceRequests  []json.RawMessage
			VolumesFrom     []string
			PortBindings    map[string]json.RawMessage
			PublishAllPorts bool
			Tmpfs           map[string]string
			LogConfig       struct{ Type string }
			RestartPolicy   struct{ Name string }
		}
	}
	if json.Unmarshal(body, &items) != nil || len(items) != 1 {
		return hosting.ErrUnavailable
	}
	v := items[0]
	h := v.HostConfig
	if v.State.Running != running || v.Config.Image != p.Image || v.Config.User != "10001:10001" || v.Config.WorkingDir != "/work" || len(v.Config.Entrypoint) != 1 || v.Config.Entrypoint[0] != c.siteInitHost() || len(v.Config.Cmd) != 2 || v.Config.Cmd[0] != "site-go" || v.Config.Cmd[1] != strconv.FormatInt(w.ExpiresAt.Unix(), 10) || h.NanoCpus != 1e9 || h.Memory != 256<<20 || h.MemorySwap != 256<<20 || h.PidsLimit != 64 || h.Privileged || !h.ReadonlyRootfs || h.NetworkMode != "none" || h.PidMode != "" || h.IpcMode != "" && h.IpcMode != "private" && h.IpcMode != "none" || h.Runtime != p.Runtime || len(h.CapAdd) != 0 || len(h.CapDrop) != 1 || h.CapDrop[0] != "ALL" || !api.Contains(h.SecurityOpt, "no-new-privileges=true") || !api.SecurityOptHasSeccomp(h.SecurityOpt, c.config.SeccompProfile) || h.LogConfig.Type != "none" || h.RestartPolicy.Name != "no" {
		return hosting.ErrDenied
	}
	if len(h.Devices) > 0 || len(h.DeviceRequests) > 0 || len(h.VolumesFrom) > 0 || len(h.PortBindings) > 0 || h.PublishAllPorts {
		return hosting.ErrDenied
	}
	for path := range h.Tmpfs {
		if path != "/tmp" {
			return hosting.ErrDenied
		}
	}
	if len(v.NetworkSettings.Networks) != 1 {
		return hosting.ErrDenied
	}
	if _, ok := v.NetworkSettings.Networks["none"]; !ok {
		return hosting.ErrDenied
	}
	if len(h.Mounts) != 2 {
		return hosting.ErrDenied
	}
	for _, m := range h.Mounts {
		if !m.ReadOnly {
			return hosting.ErrDenied
		}
		switch m.Target {
		case "/work":
			if m.Type != "volume" || m.Source != p.Volume || m.VolumeOptions == nil || m.VolumeOptions.Subpath != w.Release {
				return hosting.ErrDenied
			}
		case "/cell-tools":
			if m.Type != "volume" || m.Source != c.config.HostingHelperVolume || m.VolumeOptions != nil && m.VolumeOptions.Subpath != "" {
				return hosting.ErrDenied
			}
		default:
			return hosting.ErrDenied
		}
	}
	labels := v.Config.Labels
	if labels[api.LabelOwner] != w.Binding.Owner || labels["hermes-hub.site"] != w.Name || labels["hermes-hub.site.env"] != w.Binding.Environment || labels["hermes-hub.site.revision"] != strconv.FormatUint(w.Revision, 10) || labels["hermes-hub.site.release"] != w.Release || c.scope != nil && labels["hermes-hub.scope"] != c.scope.Project {
		return hosting.ErrDenied
	}
	for _, e := range v.Config.Env {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			return hosting.ErrDenied
		}
		switch key {
		case "PATH", "LANG", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "GOPATH", "GOLANG_VERSION":
		default:
			return hosting.ErrDenied
		}
	}
	allowed := map[string]bool{"/cell-tools": false, "/work": false, "/tmp": false}
	for _, m := range v.Mounts {
		switch m.Destination {
		case "/cell-tools":
			if m.RW || m.Type != "volume" || m.Name != c.config.HostingHelperVolume {
				return hosting.ErrDenied
			}
		case "/work":
			if m.RW || m.Type != "volume" || m.Name != p.Volume {
				return hosting.ErrDenied
			}
		case "/tmp":
			if m.Type != "tmpfs" {
				return hosting.ErrDenied
			}
		default:
			return hosting.ErrDenied
		}
		allowed[m.Destination] = true
	}
	if tmp, ok := h.Tmpfs["/tmp"]; ok {
		if !strings.Contains(tmp, "size=32m") && !strings.Contains(tmp, "size=33554432") {
			return hosting.ErrDenied
		}
		if !strings.Contains(tmp, "noexec") || !strings.Contains(tmp, "nosuid") || !strings.Contains(tmp, "nodev") {
			return hosting.ErrDenied
		}
		allowed["/tmp"] = true
	}
	if !allowed["/cell-tools"] || !allowed["/work"] || !allowed["/tmp"] {
		return hosting.ErrDenied
	}
	return nil
}
func (c *genericController) siteEnsure(ctx context.Context, w hosting.Workload, p HostingProfile) error {
	runtimes, err := c.dockerRuntimes(ctx)
	if err != nil || !runtimes[p.Runtime] {
		return hosting.ErrUnavailable
	}
	name := siteObject(w)
	if err = c.siteProfile(ctx, w, p, true); err == nil {
		return nil
	}
	if c.siteProfile(ctx, w, p, false) == nil {
		if _, err = c.docker(ctx, "rm", "-f", name); err != nil {
			return hosting.ErrUnavailable
		}
	}
	count, err := c.docker(ctx, "ps", "-a", "--filter", "label=hermes-hub.site.env="+p.Environment, "--filter", "label="+api.LabelOwner+"="+p.Owner, "--format", "{{.ID}}")
	if err != nil || len(strings.Fields(string(count))) >= 2 {
		return hosting.ErrQuota
	}
	volume, err := c.docker(ctx, "volume", "inspect", p.Volume)
	if err != nil {
		return hosting.ErrUnavailable
	}
	var vols []struct{ Labels map[string]string }
	if json.Unmarshal(volume, &vols) != nil || len(vols) != 1 || vols[0].Labels["hermes-hub.hosting.owner"] != p.Owner || vols[0].Labels["hermes-hub.hosting.env"] != p.Environment {
		return hosting.ErrDenied
	}
	fleetMax, capacityErr := c.hostingCapacity(ctx)
	if capacityErr != nil {
		return capacityErr
	}
	fleet := hostingFleet{c.config.HostingFleetRoot, fleetMax}
	if err = fleet.change(ctx, w, false); err != nil {
		return err
	}
	args := []string{"create", "--name", name, "--user", "10001:10001", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--security-opt", "seccomp=" + c.config.SeccompProfile, "--network", "none", "--runtime", p.Runtime, "--cpus", "1", "--memory", "256m", "--memory-swap", "256m", "--pids-limit", "64", "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=32m", "--restart", "no", "--log-driver", "none", "--workdir", "/work", "--label", api.LabelOwner + "=" + p.Owner, "--label", "hermes-hub.site=" + w.Name, "--label", "hermes-hub.site.env=" + p.Environment, "--label", "hermes-hub.site.revision=" + strconv.FormatUint(w.Revision, 10), "--label", "hermes-hub.site.release=" + w.Release}
	args = append(args, c.scopeLabelArgs()...)
	args = append(args, "--mount", "type=volume,source="+c.config.HostingHelperVolume+",target=/cell-tools,readonly", "--mount", "type=volume,source="+p.Volume+",target=/work,volume-subpath="+w.Release+",readonly", "--entrypoint", c.siteInitHost(), p.Image, "site-go", strconv.FormatInt(w.ExpiresAt.Unix(), 10))
	if _, err = c.docker(ctx, args...); err != nil {
		return hosting.ErrUnavailable
	}
	if c.scope != nil {
		c.scope.Register(name)
	}
	good := false
	defer func() {
		if !good {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_, _ = c.docker(cleanup, "rm", "-f", name)
		}
	}()
	if err = c.siteProfile(ctx, w, p, false); err != nil {
		return err
	}
	if _, err = c.docker(ctx, "start", name); err != nil {
		return hosting.ErrUnavailable
	}
	if err = c.siteProfile(ctx, w, p, true); err != nil {
		return err
	}
	if _, err = c.docker(ctx, "exec", name, c.siteInitHost(), "site-canary"); err != nil {
		return hosting.ErrDenied
	}
	// Readiness uses the same destination-fixed relay as subsequent requests.
	payload, _ := json.Marshal(hosting.RelayRequest{Method: "GET", Path: "/", Header: http.Header{}})
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		response, relayErr := c.siteDockerInput(ctx, bytes.NewReader(payload), "exec", "-i", name, c.siteInitHost(), "site-relay")
		var readiness hosting.RelayResponse
		if relayErr == nil && json.Unmarshal(response, &readiness) == nil && readiness.Status >= 200 && readiness.Status < 400 {
			good = true
			return nil
		}

		select {
		case <-ctx.Done():
			return hosting.ErrUnavailable
		case <-deadline.C:
			return hosting.ErrUnavailable
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func (c *genericController) siteRemove(ctx context.Context, w hosting.Workload, p HostingProfile) error {
	body, err := c.docker(ctx, "inspect", siteObject(w))
	if err != nil {
		data, listErr := c.docker(ctx, "ps", "-a", "--filter", "name=^/"+siteObject(w)+"$", "--format", "{{.ID}}")
		if listErr != nil || strings.TrimSpace(string(data)) != "" {
			return hosting.ErrUnavailable
		}
		return (hostingFleet{c.config.HostingFleetRoot, 8}).change(ctx, w, true)
	}
	var items []genericContainer
	if json.Unmarshal(body, &items) != nil || len(items) != 1 {
		return hosting.ErrUnavailable
	}
	labels := items[0].Config.Labels
	if labels[api.LabelOwner] != p.Owner || labels["hermes-hub.site"] != w.Name || labels["hermes-hub.site.env"] != p.Environment || labels["hermes-hub.site.revision"] != strconv.FormatUint(w.Revision, 10) || labels["hermes-hub.site.release"] != w.Release || c.scope != nil && labels["hermes-hub.scope"] != c.scope.Project {
		return hosting.ErrDenied
	}
	if _, err = c.docker(ctx, "rm", "-f", siteObject(w)); err != nil {
		return hosting.ErrUnavailable
	}
	data, err := c.docker(ctx, "ps", "-a", "--filter", "name=^/"+siteObject(w)+"$", "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(data)) != "" {
		return hosting.ErrUnavailable
	}
	return nil
}

func (c *genericController) hostingHTTP(w http.ResponseWriter, r *http.Request) {
	var in hosting.ExecutorRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "denied", http.StatusForbidden)
		return
	}
	p, err := c.hostingProfile(in.Workload)
	if err != nil && in.Action != "remove" {
		http.Error(w, "denied", http.StatusForbidden)
		return
	}
	if in.Action == "remove" {
		p, err = c.hostingRemovalProfile(in.Workload)
	}
	if err != nil {
		http.Error(w, "denied", http.StatusForbidden)
		return
	}
	c.siteMu.Lock()
	defer c.siteMu.Unlock()
	out := hosting.RelayResponse{}
	switch in.Action {
	case "check":
		err = c.siteProfile(r.Context(), in.Workload, p, true)
	case "ensure":
		err = c.siteEnsure(r.Context(), in.Workload, p)
	case "remove":
		err = c.siteRemove(r.Context(), in.Workload, p)
	case "request":
		if in.Request == nil || c.siteProfile(r.Context(), in.Workload, p, true) != nil {
			err = hosting.ErrDenied
			break
		}
		input, _ := json.Marshal(in.Request)
		var body []byte
		body, err = c.siteDockerInput(r.Context(), bytes.NewReader(input), "exec", "-i", siteObject(in.Workload), c.siteInitHost(), "site-relay")
		if err == nil && (len(body) > 12<<20 || json.Unmarshal(body, &out) != nil) {
			err = hosting.ErrUnavailable
		}
	default:
		err = hosting.ErrInvalid
	}
	if err != nil {
		http.Error(w, "hosting workload unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
func (c *genericController) hostingRemovalProfile(w hosting.Workload) (HostingProfile, error) {
	p, ok := c.config.Hosting[w.Binding.Owner+"/"+w.Binding.Environment]
	if !ok || p.Owner != w.Binding.Owner || p.Environment != w.Binding.Environment || !identitySiteName(w.Name) || w.Revision == 0 || len(w.Release) != 64 || strings.Trim(w.Release, "0123456789abcdef") != "" {
		return p, hosting.ErrDenied
	}
	return p, nil
}

func (c *genericController) siteInitHost() string {
	return "/cell-tools/hosting-cellinit"
}
func (c *genericController) siteInitSeed() error {
	if api.ValidateLinuxBridgeBinary(c.config.HostingCellInit) != nil {
		return hosting.ErrUnavailable
	}
	src, err := os.Open(c.config.HostingCellInit)
	if err != nil {
		return hosting.ErrUnavailable
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(c.config.StateRoot, "hosting-cellinit.tmp"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return hosting.ErrUnavailable
	}
	_, copyErr := io.Copy(dst, io.LimitReader(src, 32<<20))
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return hosting.ErrUnavailable
	}
	if os.Chmod(filepath.Join(c.config.StateRoot, "hosting-cellinit.tmp"), 0555) != nil || os.Rename(filepath.Join(c.config.StateRoot, "hosting-cellinit.tmp"), filepath.Join(c.config.StateRoot, "hosting-cellinit")) != nil {
		return hosting.ErrUnavailable
	}
	return nil
}

func (c *genericController) hostingCapacity(ctx context.Context) (int, error) {
	body, err := c.docker(ctx, "info", "--format", "{{.NCPU}} {{.MemTotal}}")
	if err != nil {
		return 0, hosting.ErrUnavailable
	}
	var cpus int
	var memory int64
	if _, err = fmt.Sscan(string(body), &cpus, &memory); err != nil || cpus < 2 || memory < 1280<<20 {
		return 0, hosting.ErrUnavailable
	}
	// Leave at least one CPU and one GiB for the hub before admitting app leases.
	return min(8, cpus-1, int((memory-(1<<30))/(256<<20))), nil
}
