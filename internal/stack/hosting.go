package stack

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/exposure"
	"github.com/letya999/hermes-hub/internal/hosting"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type HostingSettings struct {
	Slots   []string `yaml:"slots,omitempty"`
	Image   string   `yaml:"image,omitempty"`
	Runtime string   `yaml:"runtime,omitempty"`
}

func hostingVolume(s Settings) string { return "hermes-hub-sites-" + s.User + "-" + s.Environment }
func hostingName(s Settings) string   { return "hosting-" + s.User + "-" + s.Environment }
func (s Settings) hostingWorkspace(dir string) string {
	if s.CapabilityMode == "managed" {
		return filepath.Join(dir, "managed", s.Environment, "workspace")
	}
	return filepath.Join(dir, "workspace")
}
func hostingConfig(s Settings) hosting.Deployment {
	cfg := hosting.Deployment{ContextID: s.User, PolicyVersion: PolicyVersion(s), Config: hosting.Config{Owner: s.User, Environment: s.Environment, Zone: s.User, Domain: "invalid.test", Workspace: "/workspace", Store: "/sites", AdmissionDigest: hostingAdmissionDigest(s)}, Slots: s.Hosting.Slots, ExposureConfig: "/exposure/config.yaml", ControlTokenFile: "/run/hosting.key", DataListen: "127.0.0.1:8181", ControlListen: "0.0.0.0:8182"}
	if s.OrgScoped() {
		cfg.ContextID = s.Organization
	}
	if ec, err := exposure.Load(filepath.Join(s.SpaceDir, "connections", "exposure", "config.yaml")); err == nil && ec.Relay != nil {
		cfg.Domain = ec.Relay.Domain
	}
	if s.Hosting.Image != "" {
		cfg.ControllerURL = "http://" + hostingName(s) + "-ctl:8545/hosting"
		cfg.ControllerTokenFile = "/run/controller.key"
	}
	return cfg
}
func writeHostingFiles(dir string, s Settings) error {
	if !s.Has("hosting") {
		return nil
	}
	if s.SpaceDir == "" {
		s.SpaceDir = dir
	}
	if issues := hostingIssues(s); len(issues) > 0 {
		return fmt.Errorf("hosting configuration invalid: %s", issues[0])
	}
	cfg := hostingConfig(s)
	if err := ensureTokenFile(filepath.Join(dir, "runtime", "hosting."+s.Environment+".key")); err != nil {
		return err
	}
	if s.Hosting.Image != "" {
		if err := ensureTokenFile(filepath.Join(dir, "runtime", "hosting-controller."+s.Environment+".key")); err != nil {
			return err
		}
		controller := toolhub.GenericControllerConfig{Hosting: hostingControllerSection(s), HostingOnly: true, HostingFleetRoot: "/fleet", HostingHelperVolume: hostingVolume(s) + "-controller", HostingCellInit: "/usr/local/bin/cellinit", StateRoot: "/state", ToolHiveBinary: "/usr/local/bin/thv", SeccompProfile: "/opt/hub/seccomp/seccomp-mcp-runtime.json", DynamicDefinitions: true, MaxActive: 2, IdleTTLSeconds: 1800}
		data, err := json.MarshalIndent(controller, "", "  ")
		if err != nil {
			return err
		}
		if err = atomic(filepath.Join(dir, "generated", "hosting-controller."+s.Environment+".json"), data); err != nil {
			return err
		}
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomic(filepath.Join(dir, "generated", "hosting."+s.Environment+".json"), append(body, '\n'))
}
func hostingIssues(s Settings) []string {
	if !s.Has("hosting") {
		return nil
	}
	issues := []string{}
	if !s.Has("exposure") {
		issues = append(issues, "hosting requires the reviewed exposure feature")
	}
	ec, err := exposure.Load(filepath.Join(s.SpaceDir, "connections", "exposure", "config.yaml"))
	if err != nil || ec.Provider != "sish" || ec.Zone != s.User || ec.Relay == nil {
		return append(issues, "hosting requires a valid owner-bound sish exposure config")
	}
	if len(s.Hosting.Slots) < 1 || len(s.Hosting.Slots) > 4 || len(s.Hosting.Slots)+len(ec.Endpoints) > ec.Max() {
		issues = append(issues, "hosting slots and manual endpoints must fit the shared exposure cap")
	}
	seen := map[string]bool{}
	for _, n := range s.Hosting.Slots {
		if !identitySiteSlot(n) || seen[n] {
			issues = append(issues, "hosting slots must be unique safe site labels")
		}
		seen[n] = true
		if _, ok := ec.Endpoints[n]; ok {
			issues = append(issues, "hosting slot conflicts with a manual exposure endpoint")
		}
	}
	if s.Hosting.Image != "" && (api.ValidPinnedImageRef(s.Hosting.Image) != nil || (s.Hosting.Runtime != "runsc" && s.Hosting.Runtime != "kata")) {
		issues = append(issues, "dynamic hosting requires a digest-pinned image and explicit runsc or kata")
	}
	return issues
}
func identitySiteSlot(n string) bool {
	if len(n) < 1 || len(n) > 40 || n[0] < 'a' || n[0] > 'z' || n[len(n)-1] == '-' {
		return false
	}
	switch n {
	case "api", "admin", "auth", "broker", "hub", "control", "localhost":
		return false
	}
	for _, r := range n {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
func addHosting(services, volumes M, s Settings, dir string, common M, runtimeEnv M, runtimeVolumes *[]any) {
	if !s.Has("hosting") {
		return
	}
	name := hostingName(s)
	runtimeEnv["HUB_HOSTING_URL"] = "http://" + name + ":8182"
	*runtimeVolumes = append(*runtimeVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "runtime", "hosting."+s.Environment+".key")), "target": "/run/hosting.key", "read_only": true})
	runtimeEnv["HUB_HOSTING_TOKEN_FILE"] = "/run/hosting.key"
	volumes["hosting-zone"] = M{"name": "hermes-hub-hosting-zone-" + s.User, "labels": M{"hermes-hub.hosting.owner": s.User}}
	volumes["hosting-sites"] = M{"name": hostingVolume(s), "labels": M{"hermes-hub.hosting.owner": s.User, "hermes-hub.hosting.env": s.Environment, "hermes-hub.scope": "hermes-hub-" + s.User + "-" + s.Environment}}
	init := cloneMap(common)
	init["image"] = "hermes-hub:0.5.0-" + s.Environment + "-control"
	init["entrypoint"] = []string{"hubctl", "hosting-init"}
	init["command"] = []string{}
	init["user"] = "0:0"
	init["cap_add"] = []string{"CHOWN", "FOWNER"}
	init["restart"] = "no"
	init["volumes"] = []any{M{"type": "volume", "source": "hosting-sites", "target": "/sites"}, M{"type": "volume", "source": "hosting-zone", "target": "/zone"}}
	delete(init, "networks")
	init["network_mode"] = "none"
	delete(init, "build")
	delete(init, "env_file")
	delete(init, "ports")
	services[name+"-init"] = init
	svc := cloneMap(common)
	svc["image"] = init["image"]
	delete(svc, "build")
	svc["user"] = common["user"]
	svc["entrypoint"] = []string{"hubctl", "hosting", "--config", "/config/hosting.json"}
	svc["command"] = []string{}
	svc["environment"] = M{"HOME": "/tmp", "TZ": s.Timezone}
	svc["env_file"] = []any{}
	svc["ports"] = []string{}
	svc["networks"] = []string{"default", sharedNetworkName}
	if s.CapabilityMode == "managed" {
		svc["networks"] = []string{"default", sharedNetworkName, managedAgentNetwork(s)}
	}
	svc["volumes"] = []any{
		M{"type": "volume", "source": "hosting-zone", "target": "/zone"},
		M{"type": "volume", "source": "hosting-sites", "target": "/sites"},
		M{"type": "bind", "source": filepath.ToSlash(s.hostingWorkspace(dir)), "target": "/workspace", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "hosting."+s.Environment+".json")), "target": "/config/hosting.json", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "runtime", "hosting."+s.Environment+".key")), "target": "/run/hosting.key", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "exposure")), "target": "/exposure", "read_only": true},
	}
	if s.Hosting.Image != "" {
		svc["volumes"] = append(svc["volumes"].([]any), M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "runtime", "hosting-controller."+s.Environment+".key")), "target": "/run/controller.key", "read_only": true})
	}
	svc["depends_on"] = M{name + "-init": M{"condition": "service_completed_successfully"}}
	svc["cpus"] = "1"
	svc["mem_limit"] = "256m"
	svc["pids_limit"] = 64
	services[name] = svc
	if s.Hosting.Image != "" {
		volumes["hosting-fleet"] = M{"name": "hermes-hub-hosting-fleet", "labels": M{"hermes-hub.role": "hosting-fleet-metadata"}}
		init["volumes"] = append(init["volumes"].([]any), M{"type": "volume", "source": "hosting-fleet", "target": "/fleet"})
		volumes["hosting-controller-state"] = M{"name": hostingVolume(s) + "-controller", "labels": M{"hermes-hub.scope": "hermes-hub-" + s.User + "-" + s.Environment}}
		init["volumes"] = append(init["volumes"].([]any), M{"type": "volume", "source": "hosting-controller-state", "target": "/controller"})
		controller := cloneMap(common)
		delete(controller, "build")
		controller["image"] = init["image"]
		controller["user"] = common["user"]
		controller["entrypoint"] = []string{"hubctl", "connector", "generic-controller", "--config", "/config/hosting-controller.json", "--listen", "0.0.0.0:8545", "--token-file", "/run/controller.key"}
		controller["command"] = []string{}
		controller["env_file"] = []any{}
		controller["ports"] = []string{}
		controller["networks"] = []string{"default"}
		controller["environment"] = M{"HOME": "/tmp", "TZ": s.Timezone, "HUB_CONTROLLER_REMOTE": "1", "HUB_DOCKER_SCOPE": "hermes-hub-" + s.User + "-" + s.Environment}
		controller["volumes"] = []any{M{"type": "volume", "source": "hosting-fleet", "target": "/fleet"}, M{"type": "volume", "source": "hosting-controller-state", "target": "/state"}, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "hosting-controller."+s.Environment+".json")), "target": "/config/hosting-controller.json", "read_only": true}, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "runtime", "hosting-controller."+s.Environment+".key")), "target": "/run/controller.key", "read_only": true}, M{"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"}}
		controller["depends_on"] = M{name + "-init": M{"condition": "service_completed_successfully"}}
		controller["cpus"] = "1"
		controller["mem_limit"] = "256m"
		controller["pids_limit"] = 64
		controller["stop_grace_period"] = "40s"
		services[name+"-ctl"] = controller
	}

}
func hostingControllerSection(s Settings) map[string]toolhub.HostingProfile {
	if !s.Has("hosting") || s.Hosting.Image == "" {
		return nil
	}
	return map[string]toolhub.HostingProfile{s.User + "/" + s.Environment: {AdmissionDigest: hostingAdmissionDigest(s), Owner: s.User, Environment: s.Environment, Volume: hostingVolume(s), Image: s.Hosting.Image, Runtime: s.Hosting.Runtime}}
}

func hostingAdmissionDigest(s Settings) string {
	if s.Hosting.Image == "" {
		return ""
	}
	return api.ConfirmationDigest(struct{ Owner, Environment, Image, Runtime, Profile string }{s.User, s.Environment, s.Hosting.Image, s.Hosting.Runtime, "go-http-v1"})
}
