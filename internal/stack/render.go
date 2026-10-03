package stack

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/media"
	"gopkg.in/yaml.v3"

	"github.com/letya999/hermes-hub/internal/sshcap"
)

type M = map[string]any

// browserReadTools covers navigation and observation only; none of them can
// submit a form, execute page script or accept a dialog.
var browserReadTools = []string{
	"browser_navigate", "browser_navigate_back", "browser_tabs", "browser_snapshot",
	"browser_take_screenshot", "browser_wait_for", "browser_console_messages",
	"browser_network_requests", "browser_network_request", "browser_resize",
	"browser_close", "browser_pdf_save", "browser_find",
}

// browserMutationTools can change state outside the runtime (form submits,
// uploads, dialogs, arbitrary page script). They are rendered only when the
// owner enables the explicit browser_act capability.
var browserMutationTools = []string{
	"browser_click", "browser_drag", "browser_drop", "browser_hover",
	"browser_type", "browser_fill_form", "browser_press_key", "browser_select_option",
	"browser_file_upload", "browser_handle_dialog", "browser_evaluate", "browser_run_code_unsafe",
	"browser_mouse_click_xy", "browser_mouse_down", "browser_mouse_drag_xy",
	"browser_mouse_move_xy", "browser_mouse_up", "browser_mouse_wheel",
}

func browserServer(guest, act bool) M {
	args := []string{"/opt/browser/node_modules/@playwright/mcp/cli.js",
		"--caps", "vision,pdf",
		"--output-dir", "/workspace/browser",
		"--output-max-size", "268435456",
		"--block-service-workers"}
	if guest {
		// Isolated launches its own Chromium with an in-memory profile: it
		// cannot reach /state/browser cookies, storage or the CDP session.
		args = append(args, "--isolated", "--executable-path", "/usr/bin/chromium", "--no-sandbox")
	} else {
		args = append(args, "--cdp-endpoint", "http://127.0.0.1:9222")
	}
	include := slices.Clone(browserReadTools)
	if act {
		include = append(include, browserMutationTools...)
	}
	server := stdio("node", args, nil)
	server["tools"] = MCPTools{Include: include}
	return server
}

func env(names ...string) M {
	m := M{}
	for _, n := range names {
		m[n] = "${" + n + "}"
	}
	return m
}
func stdio(command string, args []string, e M) M {
	return M{"command": command, "args": args, "env": e, "timeout": 120}
}
func Config(s Settings) M {
	organizationSkills := s.OrganizationSkillsDir
	if organizationSkills == "" && s.OrganizationDir != "" {
		organizationSkills = filepath.Join(s.OrganizationDir, "hermes", "skills")
	}
	servers := M{}
	if s.Has("workspace") || s.Has("hh") || s.Has("ssh") {
		e := env("HH_TOKEN", "HH_USER_AGENT")
		e["HUB_HH_ENABLED"] = fmt.Sprint(s.Has("hh"))
		e["HUB_ORG_ACTIONS"] = "${HUB_ORG_ACTIONS}"
		e["HUB_SELF_ENV_KEYS"] = "${HUB_SELF_ENV_KEYS}"
		e["HUB_PROTECTED_ENV_KEYS"] = "${HUB_PROTECTED_ENV_KEYS}"
		e["HUB_STATE"] = "/state"
		if s.Has("image_gen") {
			// The tools subprocess reaches hub-media with the service URL and
			// bearer only; provider keys stay inside the media service.
			e["HUB_MEDIA_URL"] = "${HUB_MEDIA_URL}"
			e["HUB_MEDIA_AUTH"] = "${HUB_MEDIA_AUTH}"
		}
		if s.Has("ssh") {
			e["HUB_SSH_CONFIG"] = "/state/ssh/config.yaml"
			e["HUB_SSH_WRITE"] = fmt.Sprint(s.Has("ssh_write"))
			e["HUB_SSH_SHELL"] = fmt.Sprint(s.Has("ssh_shell"))
			e["HUB_SSH_TUNNEL"] = fmt.Sprint(s.Has("ssh_tunnel"))
			// The runtime is already a provisioned broker adapter; the tools
			// process acquires/materializes SSH keys through the same identity.
			for _, key := range []string{"URL", "KEY_FILE", "KEY_ID", "ISSUER", "CA_FILE"} {
				e["HUB_CREDENTIAL_BROKER_RUNTIME_"+key] = "${HUB_CREDENTIAL_BROKER_RUNTIME_" + key + "}"
			}
		}
		for _, service := range ServiceCatalog() {
			if !service.SelfService {
				continue
			}
			for _, key := range service.Requires {
				e[key] = "${" + key + "}"
			}
		}
		args := []string{"tools", "--workspace", "/workspace", "--archive", "/archive"}
		if s.OrgScoped() {
			args = append(args, "--organization", "/org")
		}
		servers["hub"] = stdio("/usr/local/bin/hubctl", args, e)
	}
	// The browser capability is hub-owned local automation, not an upstream
	// service connector: Playwright MCP runs inside this user's runtime against
	// their own Chromium, so profile, cookies and downloads stay inside the
	// per-space mounts. `browser` attaches to the persistent profile over
	// loopback CDP; `browser_guest` launches a second Chromium with an
	// in-memory profile, so anonymous browsing receives no authenticated
	// state. Mutation tools exist only behind the explicit browser_act
	// capability.
	if s.Has("browser") {
		servers["browser"] = browserServer(false, s.Has("browser_act"))
		servers["browser_guest"] = browserServer(true, s.Has("browser_act"))
	}
	// No upstream MCP servers are embedded directly: connectors run behind the
	// ToolHub admission boundary and are projected over its remote endpoint.
	// Feature flags still drive ports, volumes and auth files, but never write
	// mcp_servers entries for google/github/slack/atlassian/
	// telegram_user/desktop/drafts.
	for name, server := range s.MCP {
		servers[name] = server.Config()
	}
	// vision and image_gen stay off this list. The hub MCP tools are the
	// workspace-confined profile; the native local tools are not.
	toolsets := []string{"terminal", "file", "web", "skills", "todo", "cronjob", "messaging", "memory", "session_search"}
	if !s.Has("web") {
		// No web toolset at all: web_search/web_extract do not exist for this
		// space, so disabled web cannot be reached by prompting either.
		toolsets = slices.DeleteFunc(toolsets, func(name string) bool { return name == "web" })
	}
	if s.ExecutionMode == "supervisor" {
		toolsets = slices.DeleteFunc(toolsets, func(name string) bool { return name == "cronjob" })
	}
	if s.Has("meet") {
		toolsets = append(toolsets, "google_meet")
	}
	skills := M{}
	external := []string{}
	if organizationSkills != "" {
		external = append(external, "/org/hermes/skills")
	}
	if strings.TrimSpace(s.GlobalSkillsDir) != "" {
		external = append(external, "/opt/hub/skills")
	}
	if len(external) > 0 {
		skills["external_dirs"] = external
	}
	// Defense in depth: the filtered /opt/hub/skills mount already excludes
	// gated skills; disabled additionally blocks a same-named org skill.
	var disabled []string
	for skill, feature := range webGatedSkills {
		if !s.Has(feature) {
			disabled = append(disabled, skill)
		}
	}
	if len(disabled) > 0 {
		skills["disabled"] = disabled
	}
	memory := M{"memory_enabled": s.Memory, "user_profile_enabled": s.Memory}
	// Keep long-running connector work alive when a user sends a follow-up. Hermes'
	// default interrupt mode cancels the active MCP call; queue mode preserves FIFO
	// turns and lets the existing heartbeat notify the user while a build runs.
	display := M{"busy_input_mode": "queue", "long_running_notifications": true}
	cfg := M{"model": M{"default": s.Model, "provider": "custom", "base_url": s.ModelURL, "api_key": "${OPENAI_API_KEY}"}, "terminal": M{"backend": "local", "cwd": "/workspace", "timeout": 120}, "timeouts": M{"tools": M{"sequential_call": 1800, "concurrent_batch": 1800}}, "platform_toolsets": M{"cli": toolsets, "telegram": toolsets}, "mcp_servers": servers, "skills": skills, "display": display, "stt": M{"enabled": s.Has("transcription"), "provider": "local", "language": "", "local": M{"model": "small"}}, "timezone": s.Timezone, "hooks": s.Hooks, "memory": memory}
	cfg["auxiliary"] = M{"vision": M{"provider": "main", "timeout": 30, "download_timeout": 15, "max_concurrency": media.InspectConcurrency}}
	if s.Has("web") {
		if web := s.Web.config(); len(web) > 0 {
			cfg["web"] = web
		}
	} else {
		// api_server and other platforms not named in platform_toolsets fall
		// back to upstream composites that include the core web tools, so the
		// explicit-list gate alone leaks web_search/web_extract there. Upstream
		// applies agent.disabled_toolsets at tool granularity last, which
		// subtracts them from every platform including composite fallbacks.
		cfg["agent"] = M{"disabled_toolsets": []string{"web"}}
	}
	if s.Has("image_gen") {
		if gen, err := s.ImageGen.Normalize(); err == nil {
			cfg["image_gen"] = M{"provider": gen.Provider, "model": gen.Model, "delivery": gen.Delivery}
		}
	}
	return cfg
}
func Compose(s Settings, projectRoot, dir string) M {
	return compose(s, projectRoot, dir, true)
}

// RuntimeService keeps supervised and static containers on the same data/env contract.
func RuntimeService(s Settings, projectRoot, dir string) M {
	return compose(s, projectRoot, dir, false)["services"].(M)["hermes-runtime"].(M)
}

// sharedNetworkName is the single external-facing network shared infra and all
// spawned runtimes join. It is created once by the infra-owning compose project
// (name is pinned, never project-prefixed) and declared external by secondary
// spaces. Service names like `toolhub` resolve identically for every runtime.
const sharedNetworkName = "hermes-hub-runtime"

func compose(s Settings, projectRoot, dir string, includeGateway bool) M {
	infra := s.RendersInfra()
	stateVolumes := []any{
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "runtime")), "target": "/state"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "hermes")), "target": "/state/hermes"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "google")), "target": "/state/google"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "telegram")), "target": "/state/telegram"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "browser")), "target": "/state/browser"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "home")), "target": "/state/home"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "cache")), "target": "/state/cache"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "workspace")), "target": "/workspace"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "archive")), "target": "/archive", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "hermes."+s.Environment+".yaml")), "target": "/config/config.yaml", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "SOUL.md")), "target": "/config/SOUL.md", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "SOUL.md")), "target": "/state/hermes/SOUL.md", "read_only": true},
		// The effective Hermes config is materialized on the host and mounted
		// read-only over the agent-writable state dir: mcp_servers must come
		// from ToolHub onboarding, never from terminal edits inside the runtime.
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "hermes-effective."+s.Environment+".yaml")), "target": "/state/hermes/config.yaml", "read_only": true},
	}
	if s.Has("ssh") {
		// The whole SSH capability — host bindings, pinned host keys and any
		// file: credentials — is operator-owned and read-only inside the runtime.
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "ssh")), "target": "/state/ssh", "read_only": true})
	}
	ports := []string{}
	if s.Has("browser") || s.Has("meet") {
		ports = append(ports, fmt.Sprintf("127.0.0.1:%d:6080", s.BrowserPort))
	}
	// Keep the loopback OAuth callback available for a connector enabled from chat.
	ports = append(ports, fmt.Sprintf("127.0.0.1:%d:8000", s.OAuthPort))
	runtimeEnvFiles := []any{M{"path": filepath.ToSlash(filepath.Join(dir, "runtime."+s.Environment+".env")), "format": "raw"}, M{"path": filepath.ToSlash(filepath.Join(dir, "runtime.auth")), "format": "raw"}}
	organizationID := s.Organization
	if organizationID == "" {
		organizationID = "personal"
	}
	host := s.GitLabHost
	if host == "" {
		host = "gitlab.com"
	}
	policy := policyVersion(s)
	contextID := s.User
	if s.OrgScoped() {
		contextID = organizationID
	}
	runtimeEnv := M{"HUB_SHARED_GID": fmt.Sprint(max(0, os.Getgid())), "HUB_ORG_SCOPED": fmt.Sprint(s.OrgScoped()), "HUB_ORG_ACTIONS": strings.Join(s.OrgActions, ","), "HUB_SELF_ENV_KEYS": strings.Join(runtimeSelfEnvKeys(s), ","), "HUB_PROTECTED_ENV_KEYS": strings.Join(runtimeProtectedEnvKeys(s), ","), "HERMES_HOME": "/state/hermes", "HOME": "/state/home", "HUB_STATE": "/state", "HUB_WORKSPACE": "/workspace", "HUB_USER_ID": s.User, "HUB_PRINCIPAL_ID": s.User, "HUB_CONTEXT_ID": contextID, "HUB_ORGANIZATION_ID": organizationID, "HUB_RUNTIME_ID": s.User, "HUB_POLICY_VERSION": policy, "HUB_TOOLHUB_STORE": "${HUB_TOOLHUB_STORE}", "HUB_FEATURES": strings.Join(s.Features, ","), "HUB_RUNTIME_LISTEN": "0.0.0.0:8080", "TZ": s.Timezone, "HUB_BROWSER": fmt.Sprint(s.Has("browser")), "HUB_MEET": fmt.Sprint(s.Has("meet")), "GITLAB_HOST": host, "GOOGLE_EMAIL": s.GoogleEmail, "GOOGLE_OAUTH_REDIRECT_URI": fmt.Sprintf("http://localhost:%d/oauth2callback", s.OAuthPort), "PYTHONDONTWRITEBYTECODE": "1", "XDG_CACHE_HOME": "/state/cache", "HERMES_AGENT_NOTIFY_INTERVAL": "60"}
	if s.ExecutionMode != "supervisor" {
		runtimeEnv["HUB_RUNTIME_GENERATION"] = "static-" + s.User + "-" + s.Environment
	}
	if s.OrgScoped() {
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(s.OrganizationDocsDir), "target": "/org", "read_only": true})
	}
	if strings.TrimSpace(s.GlobalSkillsDir) != "" {
		// The mounted dir is the per-space filtered copy materialized by
		// Render: feature-gated bundled skills are absent entirely when the
		// feature is off, so they cannot be read or followed by prompt.
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(GeneratedSkillsDir(dir)), "target": "/opt/hub/skills", "read_only": true})
	}
	for name, feature := range hubGatedPlugins {
		if !s.Has(feature) {
			continue
		}
		// Hub plugins mount read-only into upstream's bundled plugins dir:
		// source "bundled" + kind "backend" auto-loads them, and the agent
		// cannot tamper with tool code the way it could in its own
		// /state/hermes/plugins dir.
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(GeneratedPluginsDir(dir), name)), "target": "/opt/hermes/plugins/" + name, "read_only": true})
	}
	// Core and control targets share BuildKit layers; only control needs Docker.
	common := M{"image": "hermes-hub:0.3.0-" + s.Environment, "init": true, "restart": "unless-stopped", "user": fmt.Sprintf("10001:%d", max(0, os.Getgid())), "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "shm_size": "1gb", "tmpfs": []string{"/tmp:uid=10001,gid=10001,mode=1777"}, "extra_hosts": []string{"host.docker.internal:host-gateway"}, "logging": M{"driver": "local", "options": M{"max-size": "10m", "max-file": "3"}}}
	coreBuild := M{"context": filepath.ToSlash(projectRoot), "dockerfile": "docker/Dockerfile", "target": s.Environment}
	runtimeService := cloneMap(common)
	runtimeService["env_file"] = runtimeEnvFiles
	runtimeService["environment"] = runtimeEnv
	runtimeService["volumes"] = stateVolumes
	runtimeService["ports"] = ports
	runtimeService["command"] = []string{"serve"}
	if !s.Has("telegram") && s.NativeCron != "unmigrated" {
		runtimeService["command"] = []string{"idle"}
	}
	runtimeService["healthcheck"] = M{"test": []string{"CMD", "hub-runtime", "health"}, "interval": "30s", "timeout": "5s", "retries": 3}
	if s.Environment == "dev" {
		runtimeService["restart"] = "no"
		runtimeService["read_only"] = false
		for _, name := range []string{"cmd", "internal", "services", "docker", "config", "docs", "specs", ".work", ".github", "go.mod", "go.sum", "justfile", "AGENTS.md", "README.md", "SETUP.md", "START_HERE.ru.md", "LICENSE", "SECURITY.md", "CONTRIBUTING.md"} {
			stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(projectRoot, name)), "target": "/src/" + name})
		}
		runtimeService["volumes"] = stateVolumes
		runtimeService["environment"].(M)["GOCACHE"] = "/state/go-build"
		runtimeService["environment"].(M)["GOMODCACHE"] = "/state/go-mod"
	}
	if !infra {
		// Secondary spaces keep only their runtime; it reaches the shared
		// control plane on the shared network where `toolhub`, `credential-broker`
		// and `cliproxy` resolve to the single deployed instances.
		runtimeService["networks"] = []string{"default", sharedNetworkName}
	}
	// hub-media carries the image/video provider credentials; the runtime only
	// gets the service URL and the shared media bearer (media.auth), never
	// FAL_KEY itself. Secondary spaces reach the one deployed instance over
	// the shared network, exactly like toolhub.
	if s.Has("image_gen") {
		runtimeEnv["HUB_MEDIA_URL"] = "http://hub-media:8090"
		runtimeService["env_file"] = append(runtimeEnvFiles, M{"path": filepath.ToSlash(filepath.Join(dir, "media.auth")), "format": "raw"})
	}
	services := M{"hermes-runtime": runtimeService}
	hostRuntimeDir := filepath.ToSlash(filepath.Join(dir, "runtime"))
	hostCliproxyDir := filepath.ToSlash(filepath.Join(dir, "cliproxy"))
	stateBind := M{"type": "bind", "source": hostRuntimeDir, "target": "/state"}
	dockerSock := M{"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"}
	hostRoot := "/state=" + hostRuntimeDir
	brokerMaterializedVolume := "hermes-hub-" + s.User + "-" + s.Environment + "-broker-materialized"
	brokerMaterializedMount := M{"type": "volume", "source": "broker-materialized", "target": "/run/broker-materialized"}
	brokerURL := "https://credential-broker:8787"
	brokerCA := "/run/broker-secrets/server.crt"
	// Broker keys and the CA certificate live in per-service named volumes: the
	// broker securefs checks and the client key check demand real Linux modes
	// (0600, owned by the service uid), which Windows bind mounts cannot provide.
	brokerSecrets := func(service string) M {
		return M{"type": "volume", "source": "broker-secrets-" + service, "target": "/run/broker-secrets", "read_only": true}
	}
	brokerClientEnv := func(prefix, keyID, issuer string) M {
		return M{prefix + "URL": brokerURL, prefix + "KEY_FILE": "/run/broker-secrets/" + keyID + ".private", prefix + "KEY_ID": keyID, prefix + "ISSUER": issuer, prefix + "CA_FILE": brokerCA}
	}
	for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_RUNTIME_", "runtime", "hermes-runtime-adapter") {
		runtimeEnv[key] = value
	}
	runtimeService["volumes"] = append(runtimeService["volumes"].([]any), brokerSecrets("runtime"))

	// Services every spawned or secondary runtime resolves by name attach to the
	// shared network; everything else stays on the project default.
	sharedNetworks := []string{"default", sharedNetworkName}
	if infra {
		cliproxy := cloneMap(common)
		cliproxy["build"] = coreBuild
		cliproxy["entrypoint"] = []string{"cli-proxy-api", "-config", "/cliproxy/config.yaml"}
		cliproxy["volumes"] = []any{M{"type": "bind", "source": hostCliproxyDir, "target": "/cliproxy"}}
		cliproxy["ports"] = []string{"127.0.0.1:8317:8317"}
		cliproxy["networks"] = sharedNetworks
		services["cliproxy"] = cliproxy

		toolhubEnv := M{"HUB_STATE": "/state", "HUB_USER_ID": s.User, "HUB_PRINCIPAL_ID": s.User, "HUB_CONTEXT_ID": contextID, "HUB_RUNTIME_ID": s.User, "HUB_ORGANIZATION_ID": organizationID, "HUB_POLICY_VERSION": policy, "HUB_TOOLHUB_STORE": "/state/toolhub/store.json", "HUB_CREDENTIAL_STORE": "/state/credentials/store.enc", "HUB_CREDENTIAL_KEY_FILE": "/state/credential.key", "HUB_TOOLHUB_LISTEN": "0.0.0.0:8090", "HUB_TOOLHIVE_ADMISSION_ENDPOINT": "http://workload-controller:8545/admit", "HUB_ARTIFACT_DIR": "/state/artifacts", "HUB_BUILD_SECCOMP": "/opt/hub/seccomp/seccomp-buildkit-rootless.json", "HUB_BUILD_CACHE": "1", "HUB_RECIPE_CATALOGS": "mcp-registry,toolhive,docker-mcp,docker-hub,ghcr", "HUB_DOCKER_HOST_ROOT": hostRoot, "HUB_BROKER_MATERIALIZED_VOLUME": brokerMaterializedVolume, "HOME": "/tmp", "TZ": s.Timezone, "HUB_COMMUNICATION_CONTROL_URL": "http://communication-hub:8081"}
		supervisorURL := strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"))
		if s.ExecutionMode != "" {
			supervisorURL = s.SupervisorURL
		}
		if supervisorURL != "" {
			toolhubEnv["HUB_RUNTIME_SUPERVISOR_URL"] = supervisorURL
			toolhubEnv["HUB_COMMUNICATION_AUTH"] = "${HUB_SUPERVISOR_AUTH}"
		}
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_CONTROL_", "toolhub", "hermes-toolhub") {
			toolhubEnv[key] = value
		}
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_RUNTIME_", "runtime", "hermes-runtime-adapter") {
			toolhubEnv[key] = value
		}
		toolhubVolumes := []any{stateBind, dockerSock, brokerSecrets("toolhub"), brokerMaterializedMount}
		if s.DiagnosticsEnabled() {
			toolhubEnv["HUB_DIAGNOSTICS_DIR"] = "/diagnostics"
			toolhubVolumes = append(toolhubVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(projectRoot, ".local")), "target": "/diagnostics"})
		}
		// Extra principal tokens for secondary spaces: one JSON map per deploy,
		// mounted read-only. Rendered only when the operator wrote the file.
		if _, err := os.Stat(filepath.Join(dir, "toolhub-tokens.json")); err == nil {
			toolhubVolumes = append(toolhubVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "toolhub-tokens.json")), "target": "/run/toolhub-tokens.json", "read_only": true})
			toolhubEnv["HUB_TOOLHUB_TOKENS_FILE"] = "/run/toolhub-tokens.json"
		}
		toolhub := cloneMap(common)
		toolhub["image"] = "hermes-hub:0.3.0-" + s.Environment + "-control"
		toolhub["build"] = M{"context": filepath.ToSlash(projectRoot), "dockerfile": "docker/Dockerfile", "target": s.Environment + "-control"}
		toolhub["entrypoint"] = []string{"toolhub"}
		toolhub["env_file"] = []any{M{"path": filepath.ToSlash(filepath.Join(dir, "runtime.auth")), "format": "raw"}, M{"path": filepath.ToSlash(filepath.Join(dir, "toolhub.auth")), "format": "raw"}}
		toolhub["environment"] = toolhubEnv
		toolhub["volumes"] = toolhubVolumes
		toolhub["ports"] = []string{"127.0.0.1:8090:8090"}
		toolhub["networks"] = sharedNetworks
		services["toolhub"] = toolhub

		controller := cloneMap(common)
		controller["image"] = toolhub["image"]
		controller["entrypoint"] = []string{"hubctl", "connector", "generic-controller", "--config", "/state/generic-controller.json", "--listen", "0.0.0.0:8545", "--token-file", "/state/generic-controller.key"}
		controller["environment"] = M{"HUB_STATE": "/state", "HUB_DOCKER_HOST_ROOT": hostRoot, "HUB_BROKER_MATERIALIZED_VOLUME": brokerMaterializedVolume, "HUB_CONTROLLER_REMOTE": "1", "HOME": "/tmp", "TZ": s.Timezone}
		controller["volumes"] = []any{stateBind, dockerSock, brokerMaterializedMount}
		controller["ports"] = []string{"127.0.0.1:8545:8545"}
		services["workload-controller"] = controller

		broker := cloneMap(common)
		broker["entrypoint"] = []string{"credential-broker", "serve", "--config", "/var/lib/credential-broker/config.json"}
		// The dedicated tmpfs volume lets Broker leases reach ToolHub and the
		// controller without exposing Broker's encrypted store to MCP workloads.
		broker["volumes"] = []any{M{"type": "volume", "source": "broker-state", "target": "/var/lib/credential-broker"}, brokerMaterializedMount}
		broker["ports"] = []string{"127.0.0.1:8787:8787"}
		broker["networks"] = sharedNetworks
		// Personal loopback deployment: the connect link opens the credential form
		// directly. Remove BROKER_DIRECT_FORM to restore the trusted-channel
		// confirmation code required by the stricter spec flow.
		broker["environment"] = M{"BROKER_DIRECT_FORM": "1"}
		services["credential-broker"] = broker
	}

	if infra && includeGateway && (s.Has("telegram") || s.Has("slack_app")) {
		supervisorURL := strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"))
		if s.ExecutionMode != "" {
			supervisorURL = s.SupervisorURL
		}
		gateway := cloneMap(common)
		gateway["entrypoint"] = []string{"communication-hub"}
		gatewayEnvFiles := []any{M{"path": filepath.ToSlash(filepath.Join(dir, "communication."+s.Environment+".env")), "format": "raw"}}
		gatewayEnvironment := M{"HUB_USER_ID": s.User, "HUB_ORGANIZATION_ID": organizationID, "HUB_RUNTIME_ID": s.User, "HUB_POLICY_VERSION": policy, "HUB_FEATURES": strings.Join(s.Features, ","), "HUB_RUNTIME_URL": "http://hermes-runtime:8080", "HUB_RUNTIME_SUPERVISOR_URL": "${HUB_RUNTIME_SUPERVISOR_URL}", "HUB_COMMUNICATION_SPOOL": "/data", "HUB_CONFIGURED_ENV": strings.Join(configuredEnvKeys(s, dir), ","), "HUB_NATIVE_CRON": s.NativeCron, "HUB_DIAGNOSTICS_ENABLED": fmt.Sprint(s.DiagnosticsEnabled())}
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_APPROVE_", "communication", "hermes-communication") {
			gatewayEnvironment[key] = value
		}
		runtimeEnv["HUB_COMMUNICATION_CONTROL_URL"] = "http://communication-hub:8081"
		gatewayEnvironment["HUB_COMMUNICATION_LISTEN"] = "0.0.0.0:8081"
		communicationHostPort := 8081
		if s.Has("slack_app") {
			gatewayEnvironment["HUB_COMMUNICATION_LISTEN"] = "0.0.0.0:8081"
			communicationHostPort = slackEventsHostPort(s)
		}
		gatewayEnvironment["HUB_COMMUNICATION_FORM_ORIGIN"] = fmt.Sprintf("http://localhost:%d", communicationHostPort)
		gateway["ports"] = []string{fmt.Sprintf("127.0.0.1:%d:8081", communicationHostPort)}
		if s.Has("transcription") {
			gatewayEnvironment["HUB_STT_URL"] = "http://hub-stt:8090"
			gatewayEnvironment["HUB_TTS_URL"] = "http://hub-tts:8090"
			// The embedded command workers stay configured as the documented
			// fallback; when the URL is set the sidecar wins at selection time.
			gatewayEnvironment["HUB_STT_COMMAND"] = "/usr/local/bin/hub-stt"
			gatewayEnvironment["HUB_TTS_COMMAND"] = "/usr/local/bin/hub-tts"
			gatewayEnvironment["HF_HOME"] = "/data/hf"
			gatewayEnvironment["XDG_CACHE_HOME"] = "/data/cache"
			gatewayEnvFiles = append(gatewayEnvFiles, M{"path": filepath.ToSlash(filepath.Join(dir, "media.auth")), "format": "raw"})
		}
		if s.ExecutionMode != "" {
			gatewayEnvironment["HUB_RUNTIME_SUPERVISOR_URL"] = supervisorURL
		}
		if supervisorURL == "" {
			gatewayEnvFiles = append(gatewayEnvFiles, M{"path": filepath.ToSlash(filepath.Join(dir, "runtime.auth")), "format": "raw"})
		} else {
			gatewayEnvironment["HUB_SUPERVISOR_AUTH"] = "${HUB_SUPERVISOR_AUTH}"
		}
		gateway["env_file"] = gatewayEnvFiles
		gateway["environment"] = gatewayEnvironment
		gateway["networks"] = sharedNetworks
		gateway["volumes"] = []any{M{"type": "volume", "source": "communication-hub-data", "target": "/data"}, brokerSecrets("communication")}
		// Sibling Telegram identities live in this file. TELEGRAM_ALLOWED_USERS
		// stays the single infra-owner fallback when the file is absent.
		usersFile := filepath.Join(dir, "communication.users.yaml")
		if info, err := os.Stat(usersFile); err == nil && info.Mode().IsRegular() {
			gatewayEnvironment["HUB_COMMUNICATION_CONFIG"] = "/config/communication.users.yaml"
			gateway["volumes"] = append(gateway["volumes"].([]any), M{"type": "bind", "source": filepath.ToSlash(usersFile), "target": "/config/communication.users.yaml", "read_only": true})
		}
		if supervisorURL == "" {
			gateway["depends_on"] = M{"hermes-runtime": M{"condition": "service_healthy"}}
		}
		gateway["restart"] = "unless-stopped"
		services["communication-hub"] = gateway
		if supervisorURL != "" {
			delete(services, "hermes-runtime")
		}

		// Standalone speech services: two separate binaries and images
		// (hub-stt, hub-tts), independent lifecycle and scaling, shared
		// bearer auth, durable job data on their own volumes. They reach
		// the gateway's network only — never the docker socket, broker
		// mounts or user spaces. HUB_MEDIA_ENGINE=remote with
		// HUB_MEDIA_UPSTREAM points a service at any OpenAI-compatible
		// upstream (Groq, Speaches, a self-hosted whisperx, NVIDIA NIM).
		if s.Has("transcription") {
			mediaAuth := []any{M{"path": filepath.ToSlash(filepath.Join(dir, "media.auth")), "format": "raw"}}
			for _, role := range []string{"stt", "tts"} {
				name := "hub-" + role
				svc := cloneMap(common)
				// Dedicated image tag: sharing the hub tag would let the
				// last build overwrite the image other services run.
				svc["image"] = "hermes-hub-" + role + ":0.3.0-" + s.Environment
				svc["build"] = M{"context": filepath.ToSlash(projectRoot), "dockerfile": "docker/Dockerfile." + role}
				svc["environment"] = M{
					"HUB_MEDIA_LISTEN": "0.0.0.0:8090",
					"HUB_MEDIA_ENGINE": "command", "HUB_MEDIA_COMMAND": "/usr/local/bin/" + role + "-worker",
					"HUB_MEDIA_DATA": "/data", "HUB_MEDIA_WORKERS": "2",
					"HUB_MEDIA_INPUT_ROOT": "/inputs",
					"HF_HOME":              "/data/hf", "XDG_CACHE_HOME": "/data/cache",
					"HOME": "/tmp", "TZ": s.Timezone,
				}
				// whisper-tiny transcribes Russian poorly; "small" is the sane
				// local default. Host env can override either model name.
				if role == "stt" {
					svc["environment"].(M)["HUB_STT_MODEL"] = "${HUB_STT_MODEL:-small}"
				} else {
					svc["environment"].(M)["HUB_TTS_LANG"] = "${HUB_TTS_LANG:-ru}"
					svc["environment"].(M)["HUB_TTS_MODEL"] = "${HUB_TTS_MODEL:-ru_RU-irina-medium}"
					svc["environment"].(M)["HUB_TTS_VOICE"] = "${HUB_TTS_VOICE:-xenia}"
					svc["environment"].(M)["HUB_TTS_BACKENDS"] = "${HUB_TTS_BACKENDS:-silero,piper,espeak}"
					svc["environment"].(M)["HUB_TTS_DEVICE"] = "${HUB_TTS_DEVICE:-cpu}"
					svc["environment"].(M)["HUB_TTS_VOICES"] = "${HUB_TTS_VOICES:-aidar,baya,kseniya,xenia,eugene,piper/irina,piper/ruslan,piper/dmitri,espeak}"
				}
				svc["env_file"] = mediaAuth
				svc["volumes"] = []any{M{"type": "volume", "source": "hub-" + role + "-data", "target": "/data"}}
				svc["networks"] = sharedNetworks
				svc["restart"] = "unless-stopped"
				services[name] = svc
			}
		}
	}
	// hub-media is the media-generation sidecar: one Go binary from the hub
	// image, durable video jobs on its own volume, internal network only (no
	// published port). Provider keys never enter env files: the service
	// materializes the grant named by HUB_MEDIA_BROKER_GRANT through the
	// credential broker (media keypair in broker-secrets-media). With no grant
	// configured the engines fall back to env keys — meaningful only outside
	// the rendered stack, since compose no longer mounts a media env file.
	if infra && s.Has("image_gen") {
		mediaSvc := cloneMap(common)
		mediaSvc["entrypoint"] = []string{"hub-media"}
		mediaSvc["build"] = coreBuild
		mediaEnv := M{
			"HUB_MEDIA_LISTEN":         "0.0.0.0:8090",
			"HUB_MEDIA_ENGINE":         "${HUB_MEDIA_ENGINE:-remote}",
			"HUB_MEDIA_UPSTREAM":       "${HUB_MEDIA_UPSTREAM:-http://cliproxy:8317}",
			"HUB_MEDIA_QUEUE_UPSTREAM": "${HUB_MEDIA_QUEUE_UPSTREAM:-}",
			"HUB_MEDIA_IMAGE_MODEL":    "${HUB_MEDIA_IMAGE_MODEL:-gpt-image-2}",
			"HUB_MEDIA_IMAGE_MODELS":   "${HUB_MEDIA_IMAGE_MODELS:-}",
			"HUB_MEDIA_CHAT_MODELS":    "${HUB_MEDIA_CHAT_MODELS:-" + strings.Join(media.CLIProxyChatImageModels(), ",") + "}",
			"HUB_MEDIA_VIDEO_MODEL":    "${HUB_MEDIA_VIDEO_MODEL:-}",
			"HUB_MEDIA_VIDEO_MODELS":   "${HUB_MEDIA_VIDEO_MODELS:-}",
			"HUB_MEDIA_FETCH_HOSTS":    "${HUB_MEDIA_FETCH_HOSTS:-fal.media}",
			"HUB_MEDIA_BROKER_GRANT":   "${HUB_MEDIA_BROKER_GRANT:-}",
			"HUB_MEDIA_DATA":           "/data", "HUB_MEDIA_WORKERS": "2",
			// The broker grant pins the actor's policy version: the config hash
			// changes on every settings edit, which would orphan the grant, so
			// the service presents the stable service-level policy instead.
			"HUB_PRINCIPAL_ID": s.User, "HUB_CONTEXT_ID": contextID, "HUB_RUNTIME_ID": s.User, "HUB_POLICY_VERSION": "hub-media",
			"HOME": "/tmp", "TZ": s.Timezone,
		}
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_MEDIA_", "media", "hermes-media") {
			mediaEnv[key] = value
		}
		mediaSvc["environment"] = mediaEnv
		mediaSvc["env_file"] = []any{
			M{"path": filepath.ToSlash(filepath.Join(dir, "media.auth")), "format": "raw"},
		}
		mediaSvc["volumes"] = []any{M{"type": "volume", "source": "hub-media-data", "target": "/data"}, brokerSecrets("media")}
		mediaSvc["networks"] = sharedNetworks
		mediaSvc["healthcheck"] = M{"test": []string{"CMD", "hub-media", "health"}, "interval": "30s", "timeout": "5s", "retries": 3}
		services["hub-media"] = mediaSvc
	}
	if includeGateway && !infra {
		// Secondary spaces never keep a resident runtime: their gateway jobs
		// reach the shared supervisor over the shared network and every control
		// call goes through it. Only file outputs (env, config, volumes) feed
		// the supervisor's spawned runtimes.
		delete(services, "hermes-runtime")
	}
	// Secondary spaces still declare broker-secrets-runtime: the supervisor
	// mounts it by project-derived name into that user's spawned runtimes.
	volumes := M{"broker-secrets-runtime": M{}}
	result := M{"name": "hermes-hub-" + s.User + "-" + s.Environment, "services": services, "volumes": volumes}
	if infra {
		volumes["communication-hub-data"] = M{}
		volumes["broker-state"] = M{"external": true, "name": "hermes-credential-broker-real-prod-20260918"}
		volumes["broker-materialized"] = M{"name": brokerMaterializedVolume, "driver": "local", "driver_opts": M{"type": "tmpfs", "device": "tmpfs", "o": "size=64m,uid=10001,gid=10001,mode=0700"}}
		volumes["broker-secrets-toolhub"] = M{}
		volumes["broker-secrets-communication"] = M{}
		if s.Has("transcription") {
			volumes["hub-stt-data"] = M{}
			volumes["hub-tts-data"] = M{}
		}
		if s.Has("image_gen") {
			volumes["hub-media-data"] = M{}
			volumes["broker-secrets-media"] = M{}
		}
	}
	// The shared runtime network is operator-owned infrastructure: it is
	// created once (docker network create hermes-hub-runtime), outlives any
	// compose project and is attached by spawned runtimes directly. Declaring
	// it external everywhere keeps `compose down` from deleting it and avoids
	// compose-owned label conflicts.
	result["networks"] = M{sharedNetworkName: M{"name": sharedNetworkName, "external": true}}
	return result
}

// PolicyVersion identifies the effective runtime policy used by transport and supervisor.
func PolicyVersion(s Settings) string { return policyVersion(s) }

func policyVersion(s Settings) string {
	// Executor rollout is transport state, not a grant of additional tool rights.
	s.ExecutionMode = ""
	body, _ := yaml.Marshal(Config(s))
	hash := sha256.Sum256(body)
	return "policy-" + hex.EncodeToString(hash[:8])
}

func cloneMap(source M) M {
	result := M{}
	for key, value := range source {
		result[key] = value
	}
	return result
}

func runtimeSelfEnvKeys(s Settings) []string {
	keys := []string{}
	for _, key := range selfEnvKeys(s) {
		if !GatewayOwnedSecret(key) {
			keys = append(keys, key)
		}
	}
	return keys
}

func runtimeProtectedEnvKeys(s Settings) []string {
	keys := append([]string{}, GatewaySecretKeys()...)
	for _, key := range strings.Split(organizationSecretKeys(s), ",") {
		if key != "" && !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func configuredEnvKeys(s Settings, dir string) []string {
	keys := []string{}
	secrets, err := ReadSecrets(filepath.Join(dir, "secrets."+s.Environment+".env"))
	if err == nil {
		for key, value := range secrets {
			if value != "" {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	return keys
}
func Render(dir, root string) error { return RenderEnvironment(dir, root, "prod") }
func RenderEnvironment(dir, root, environment string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	s, err := ReadEnvironment(dir, environment)
	if err != nil {
		return err
	}
	if s.Has("ssh") {
		// Fail render on a broken capability config rather than shipping a
		// runtime whose ssh tools would all deny.
		if _, err = sshcap.Load(filepath.Join(dir, "connections", "ssh", "config.yaml")); err != nil {
			return fmt.Errorf("ssh config: %w", err)
		}
	}
	if strings.TrimSpace(s.GlobalSkillsDir) == "" {
		s.GlobalSkillsDir = filepath.Join(root, "config", "skills")
		if err := os.MkdirAll(s.GlobalSkillsDir, 0755); err != nil {
			return err
		}
	}
	secrets, err := ReadSecrets(filepath.Join(dir, "secrets."+s.Environment+".env"))
	if err != nil {
		return err
	}
	var orgSecrets map[string]string
	if s.OrgScoped() {
		orgSecrets, err = ReadOrganizationSecrets(s, s.Environment)
		if err != nil {
			return err
		}
	}
	for _, name := range []string{"archive", "runtime", "runtime/artifacts", "runtime/toolhub", "runtime/credentials", "runtime/materialized", "broker", "broker/keys", "broker/tls", "cliproxy", "hermes", "hermes/memories", "hermes/skills", "connections", "connections/google", "connections/telegram", "connections/browser", "connections/ssh/keys", "home", "cache", "workspace", "workspace/browser", "workspace/artifacts", "workspace/artifacts/documents", "workspace/artifacts/images", "skills"} {
		if err = os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			return err
		}
	}
	if s.RendersInfra() && s.DiagnosticsEnabled() {
		if err := os.MkdirAll(filepath.Join(root, ".local"), 0700); err != nil {
			return err
		}
	}
	if err = writeHonchoConfig(dir, s); err != nil {
		return err
	}
	if err = ensureRuntimeAuth(filepath.Join(dir, "runtime.auth")); err != nil {
		return err
	}
	if s.Has("transcription") || s.Has("image_gen") {
		if err = ensureMediaAuth(filepath.Join(dir, "media.auth")); err != nil {
			return err
		}
	}
	// Provider keys moved behind the credential broker: a media.<env>.env
	// rendered by an older version must not linger with plaintext secrets.
	if err = removeIfExists(filepath.Join(dir, "media."+environment+".env")); err != nil {
		return err
	}
	if s.RendersInfra() {
		if err = EnrollSiblingRuntimeTokens(dir, environment); err != nil {
			return err
		}
	}
	// FAL_KEY belongs to hub-media: image/video generation always goes
	// through the service, so the runtime never needs the provider credential.
	omitted := []string{"FAL_KEY"}
	// Spawned runtimes reach hub-media through the runtime env file: the
	// supervisor passes runtime.<env>.env to `docker run --env-file`, so the
	// service URL and bearer must live there — compose env_file entries are
	// not replayed for supervisor-spawned containers.
	runtimeExtra := map[string]string{}
	if s.Has("image_gen") {
		if auth, _ := ReadSecrets(filepath.Join(dir, "media.auth")); strings.TrimSpace(auth["HUB_MEDIA_AUTH"]) != "" {
			runtimeExtra["HUB_MEDIA_URL"] = "http://hub-media:8090"
			runtimeExtra["HUB_MEDIA_AUTH"] = strings.TrimSpace(auth["HUB_MEDIA_AUTH"])
		}
	}
	if err = writeRuntimeEnvFiles(dir, s.Environment, secrets, orgSecrets, omitted, runtimeExtra); err != nil {
		return err
	}
	if err = writeToolHubFiles(dir); err != nil {
		return err
	}
	outputs := map[string]any{"hermes." + environment + ".yaml": Config(s), "compose." + environment + ".yaml": Compose(s, root, dir)}
	// Generated files are reproducible; secrets and agent memories are never overwritten.
	for name, v := range outputs {
		b, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		if err = atomic(filepath.Join(dir, "generated", name), b); err != nil {
			return err
		}
		// One-release compatibility for callers that still look beside settings.yaml.
		if err = atomic(filepath.Join(dir, name), b); err != nil {
			return err
		}
	}
	if err = materializeHermesConfig(dir, s); err != nil {
		return err
	}
	if err = MaterializeGlobalSkills(dir, s); err != nil {
		return err
	}
	if err = MaterializeHubPlugins(dir, root, s); err != nil {
		return err
	}
	// Init seeds only a stub; heal it to the global template. Real user edits
	// differ from the stub and are never overwritten.
	if err = HealUserSoul(dir, filepath.Join(root, "config", "SOUL.md")); err != nil {
		return err
	}
	for _, p := range []string{"hermes." + environment + ".yaml", "SOUL.md"} {
		if err = os.Chmod(filepath.Join(dir, p), 0644); err != nil {
			return err
		}
	}
	if err = os.Chmod(filepath.Join(dir, "archive"), 0755); err != nil {
		return err
	}
	return nil
}

// removeIfExists deletes path when present; a missing file is not an error.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ensureRuntimeAuth(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("runtime auth must be a regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return err
	}
	content := []byte("HUB_RUNTIME_AUTH=" + hex.EncodeToString(value) + "\n")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return ensureRuntimeAuth(path)
		}
		return err
	}
	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

// ensureMediaAuth provisions the shared bearer token between communication-hub
// and the media sidecars. One file, three mounts: gateway reads it for
// HUB_STT_AUTH/HUB_TTS_AUTH, sidecars read HUB_MEDIA_AUTH from the same file.
func ensureMediaAuth(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("media auth must be a regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return err
	}
	token := hex.EncodeToString(value)
	content := []byte("HUB_MEDIA_AUTH=" + token + "\nHUB_STT_AUTH=" + token + "\nHUB_TTS_AUTH=" + token + "\n")
	return os.WriteFile(path, content, 0600)
}

// materializeHermesConfig renders the effective Hermes config host-side so the
// runtime container mounts it read-only over the agent-writable state dir.
// Inputs mirror what the in-container startup used to read from the env file
// and runtime.auth produced by this same render.
func materializeHermesConfig(dir string, s Settings) error {
	source := filepath.Join(dir, "hermes."+s.Environment+".yaml")
	dest := filepath.Join(dir, "generated", "hermes-effective."+s.Environment+".yaml")
	runtimeEnv, err := ReadSecrets(filepath.Join(dir, "runtime."+s.Environment+".env"))
	if err != nil {
		return err
	}
	auth, _ := ReadSecrets(filepath.Join(dir, "runtime.auth"))
	tokenEnv := strings.TrimSpace(runtimeEnv["HUB_TOOLHUB_TOKEN_ENV"])
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	return MaterializeHermesConfig(source, dest, MaterializeOptions{
		ToolHubEndpoint:    strings.TrimSpace(runtimeEnv["HUB_TOOLHUB_ENDPOINT"]),
		ToolHubTokenEnv:    tokenEnv,
		RuntimeAuthPresent: strings.TrimSpace(auth[tokenEnv]) != "" || strings.TrimSpace(runtimeEnv[tokenEnv]) != "",
		ToolHubReconnect:   !strings.EqualFold(strings.TrimSpace(runtimeEnv["HUB_TOOLHUB_RECONNECT"]), "false"),
		SelfServicesPath:   filepath.Join(dir, "runtime", "self-services.json"),
	})
}

// GeneratedSkillsDir is the per-space filtered skills tree materialized by
// MaterializeGlobalSkills and mounted at /opt/hub/skills.
func GeneratedSkillsDir(dir string) string {
	return filepath.Join(dir, "generated", "skills")
}

// GeneratedPluginsDir is the per-space filtered hub-plugin tree materialized
// by MaterializeHubPlugins; each gated plugin mounts read-only at
// /opt/hermes/plugins/<name>, which upstream discovers as a bundled backend.
func GeneratedPluginsDir(dir string) string {
	return filepath.Join(dir, "generated", "plugins")
}

// MaterializeHubPlugins mirrors the repo's config/plugins tree into the
// space's generated/plugins mount, dropping plugins whose gating feature is
// disabled. The runtime then sees exactly the plugins this space may load.
func MaterializeHubPlugins(dir, root string, s Settings) error {
	src := filepath.Join(root, "config", "plugins")
	if _, err := os.Stat(src); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		src = ""
	}
	dest := GeneratedPluginsDir(dir)
	if src != "" && filepath.Clean(src) == filepath.Clean(dest) {
		return nil
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	// Every enabled gated plugin gets a dir even when the repo copy is absent:
	// the compose/supervisor mount expects the source path to exist; an empty
	// dir holds no plugin.yaml, so nothing loads.
	for name, feature := range hubGatedPlugins {
		if s.Has(feature) {
			if err := os.MkdirAll(filepath.Join(dest, name), 0755); err != nil {
				return err
			}
		}
	}
	if src == "" {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return fs.SkipDir
			}
			// A plugin directory is <name>/plugin.yaml; gated names skip wholesale.
			if rel != "." {
				if feature, gated := hubGatedPlugins[d.Name()]; gated && !s.Has(feature) && rel == d.Name() {
					return fs.SkipDir
				}
			}
			return os.MkdirAll(filepath.Join(dest, rel), 0755)
		}
		if strings.HasSuffix(d.Name(), ".pyc") {
			return nil
		}
		body, err := os.ReadFile(path) // #nosec G304 -- operator-owned plugins dir
		if err != nil {
			return err
		}
		mode := fs.FileMode(0644)
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
		return os.WriteFile(filepath.Join(dest, rel), body, mode)
	})
}

// MaterializeGlobalSkills mirrors the shared GlobalSkillsDir into the space's
// generated/skills mount, dropping bundled skills whose gating feature is
// disabled. The container then sees exactly the skills this space may run;
// skills.disabled in the rendered config blocks the same names as a fallback.
func MaterializeGlobalSkills(dir string, s Settings) error {
	src := strings.TrimSpace(s.GlobalSkillsDir)
	if src == "" {
		return nil
	}
	dest := GeneratedSkillsDir(dir)
	if filepath.Clean(src) == filepath.Clean(dest) {
		return nil
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A skill directory is <name>/SKILL.md; gated names skip wholesale.
			if rel != "." {
				if feature, gated := webGatedSkills[d.Name()]; gated && !s.Has(feature) && rel == d.Name() {
					return fs.SkipDir
				}
			}
			return os.MkdirAll(filepath.Join(dest, rel), 0755)
		}
		body, err := os.ReadFile(path) // #nosec G304 -- operator-owned skills dir
		if err != nil {
			return err
		}
		mode := fs.FileMode(0644)
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			mode = info.Mode().Perm()
		}
		return os.WriteFile(filepath.Join(dest, rel), body, mode)
	})
}

func writeRuntimeEnvFiles(dir, environment string, user, organization map[string]string, omit []string, extra map[string]string) error {
	merged := map[string]string{}
	for key, value := range organization {
		merged[key] = value
	}
	for key, value := range user {
		if _, exists := merged[key]; exists {
			return fmt.Errorf("secret key duplicated between organization and user scope: %s", key)
		}
		merged[key] = value
	}
	runtime := map[string]string{}
	for key, value := range merged {
		if GatewayOwnedSecret(key) || slices.Contains(omit, key) {
			continue
		}
		runtime[key] = value
	}
	for key, value := range extra {
		runtime[key] = value
	}
	// Every runtime is wired to the single shared ToolHub by default. Spawned
	// contexts and secondary spaces reach it on the shared "hermes-hub-runtime"
	// network where the service name resolves to the one deployed instance. An
	// explicit HUB_TOOLHUB_ENDPOINT in secrets.<env>.env overrides; an empty
	// value opts out.
	if _, ok := runtime["HUB_TOOLHUB_ENDPOINT"]; !ok {
		runtime["HUB_TOOLHUB_ENDPOINT"] = "http://toolhub:8090/mcp"
	}
	gateway := map[string]string{}
	for _, key := range GatewaySecretKeys() {
		if value := user[key]; value != "" {
			gateway[key] = value
		}
	}
	if err := writeEnvFile(filepath.Join(dir, "runtime."+environment+".env"), runtime); err != nil {
		return err
	}
	return writeEnvFile(filepath.Join(dir, "communication."+environment+".env"), gateway)
}

// writeToolHubFiles provisions the controller config, its protected admission
// token and the toolhub env file consumed by the generated compose stack.
// The key is generated once and never overwritten.
func writeToolHubFiles(dir string) error {
	runtimeDir := filepath.Join(dir, "runtime")
	keyPath := filepath.Join(runtimeDir, "generic-controller.key")
	if err := ensureTokenFile(keyPath); err != nil {
		return err
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(key))
	controller := M{"state_root": "/state", "toolhive_binary": "/usr/local/bin/thv", "seccomp_profile": "/opt/hub/seccomp/seccomp-mcp-runtime.json", "docker_fallback": true, "bridge_binary": "/usr/local/bin/hubctl", "credential_mount_root": "/run/broker-materialized", "dynamic_definitions": true, "max_active": 8, "idle_ttl_seconds": 1800}
	body, err := json.MarshalIndent(controller, "", "  ")
	if err != nil {
		return err
	}
	if err = atomic(filepath.Join(runtimeDir, "generic-controller.json"), append(body, '\n')); err != nil {
		return err
	}
	return writeEnvFile(filepath.Join(dir, "toolhub.auth"), map[string]string{"HUB_TOOLHIVE_ADMISSION_TOKEN": token})
}

// ensureTokenFile creates a random 256-bit hex token file unless it exists.
func ensureTokenFile(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("token file must be a regular file")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return ensureTokenFile(path)
		}
		return err
	}
	if _, err = f.Write([]byte(hex.EncodeToString(value) + "\n")); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeHonchoConfig(dir string, s Settings) error {
	if !s.Honcho || strings.TrimSpace(s.HonchoURL) == "" {
		return nil
	}
	body, err := json.Marshal(M{
		"baseUrl": s.HonchoURL,
		"hosts":   M{"hermes": M{"enabled": true, "aiPeer": "hermes", "peerName": s.User, "workspace": "hermes"}},
	})
	if err != nil {
		return err
	}
	return atomic(filepath.Join(dir, "hermes", "honcho.json"), append(body, '\n'))
}

func writeEnvFile(path string, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var body strings.Builder
	for _, key := range keys {
		body.WriteString(key)
		body.WriteByte('=')
		body.WriteString(values[key])
		body.WriteByte('\n')
	}
	return atomic(path, []byte(body.String()))
}

func atomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".render-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
