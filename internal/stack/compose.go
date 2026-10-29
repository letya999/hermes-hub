package stack

import (
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type M = map[string]any

// platformDenylistComposite marks upstream toolsets that must NOT be emitted
// into agent.disabled_toolsets: their resolve overlaps the shared core tools
// (debugging includes web+file, safe includes web+vision+image_gen, yuanbao
// re-lists the whole core, search is a subset of web, coding is a posture),
// so subtracting them would strip surfaces the space legitimately enabled.
var platformDenylistComposite = map[string]bool{
	"coding": true, "debugging": true, "safe": true, "yuanbao": true, "search": true,
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

func managedAgentNetwork(s Settings) string {
	return "hermes-hub-agent-" + s.User + "-" + s.Environment
}

func compose(s Settings, projectRoot, dir string, includeGateway bool) M {
	infra := s.RendersInfra()
	stateVolumes := []any{
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "runtime")), "target": "/state"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "hermes")), "target": "/state/hermes"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "google")), "target": "/state/google"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "telegram")), "target": "/state/telegram"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "home")), "target": "/state/home"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "cache")), "target": "/state/cache"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "workspace")), "target": "/workspace"},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "archive")), "target": "/archive", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "hermes."+s.Environment+".yaml")), "target": "/config/config.yaml", "read_only": true},
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "SOUL.md")), "target": "/config/SOUL.md", "read_only": true},
		// Hermes reads the effective SOUL — persona plus the rendered channel
		// contract — not the owner's raw file (SPEC-0053 R1.8/D7).
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "soul-effective."+s.Environment+".md")), "target": "/state/hermes/SOUL.md", "read_only": true},
		// The effective Hermes config is materialized on the host and mounted
		// read-only over the agent-writable state dir: mcp_servers must come
		// from ToolHub onboarding, never from terminal edits inside the runtime.
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "hermes-effective."+s.Environment+".yaml")), "target": "/state/hermes/config.yaml", "read_only": true},
		// The executor's compiled governance extract: written host-side,
		// mounted read-only, consumed by `hubctl tools`/tools-exec via
		// HUB_TOOL_POLICY.
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "tool-policy."+s.Environment+".json")), "target": "/config/tool-policy.json", "read_only": true},
		// The render-time expectation for the runtime tool surface; the
		// runtime's /v1/health probe reads it via HUB_TOOL_MANIFEST and can
		// never write it (read-only bind, agent-writable /state stays clean).
		M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "tool-manifest."+s.Environment+".json")), "target": "/config/tool-manifest.json", "read_only": true},
	}
	if s.CapabilityMode == "managed" {
		// The managed workspace is a durable bind, not tmpfs: admitted file,
		// document, image and artifact writes must survive runtime respawns,
		// which are the supervisor's normal lifecycle under managed mode.
		stateVolumes = []any{
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "managed", s.Environment, "runtime")), "target": "/state"},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "managed", s.Environment, "hermes")), "target": "/state/hermes"},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "managed", s.Environment, "home")), "target": "/state/home"},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "managed", s.Environment, "cache")), "target": "/state/cache"},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "managed", s.Environment, "workspace")), "target": "/workspace"},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "hermes-effective."+s.Environment+".yaml")), "target": "/state/hermes/config.yaml", "read_only": true},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "tool-policy."+s.Environment+".json")), "target": "/config/tool-policy.json", "read_only": true},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "tool-manifest."+s.Environment+".json")), "target": "/config/tool-manifest.json", "read_only": true},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "soul-effective."+s.Environment+".md")), "target": "/state/hermes/SOUL.md", "read_only": true},
		}
	}
	if s.Has("ssh") {
		// The whole SSH capability — host bindings, pinned host keys and any
		// file: credentials — is operator-owned and read-only inside the runtime.
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "ssh")), "target": "/state/ssh", "read_only": true})
	}
	if s.Has("exposure") {
		// The reviewed exposure config — provider, relay, endpoints and any
		// file: key — is operator-owned and read-only inside the runtime.
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "exposure")), "target": "/state/exposure", "read_only": true})
	}
	for _, m := range s.Workspace.Mounts {
		source, readOnly, err := s.resolveMount(m)
		if err != nil {
			continue // validate() already rejected it; render stays total
		}
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(source), "target": m.To, "read_only": readOnly})
	}
	ports := []string{}
	// SPEC-0082 R4.2: the noVNC publish lives on the browser sidecar, not the
	// runtime — the runtime carries no display surface at all. The OAuth port
	// stays: legacy self-service connectors use this callback; managed
	// connectors are admitted through the control plane and do not expose it.
	if s.CapabilityMode != "managed" {
		ports = append(ports, fmt.Sprintf("127.0.0.1:%d:8000", s.OAuthPort))
	}
	runtimeEnvFiles := []any{M{"path": filepath.ToSlash(filepath.Join(dir, "runtime."+s.Environment+".env")), "format": "raw"}, M{"path": filepath.ToSlash(filepath.Join(dir, "runtime.auth")), "format": "raw"}}
	if s.CapabilityMode == "managed" {
		runtimeEnvFiles[0] = M{"path": filepath.ToSlash(filepath.Join(dir, "managed-runtime."+s.Environment+".env")), "format": "raw"}
	}
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
	runtimeEnv := M{"HUB_SHARED_GID": fmt.Sprint(max(0, os.Getgid())), "HUB_ORG_SCOPED": fmt.Sprint(s.OrgScoped()), "HUB_ORG_ACTIONS": strings.Join(s.effectiveOrgActions(), ","), "HUB_TOOLS_RO": strings.Join(s.readOnlyCapabilities(), ","), "HUB_SELF_ENV_KEYS": strings.Join(runtimeSelfEnvKeys(s), ","), "HUB_PROTECTED_ENV_KEYS": strings.Join(runtimeProtectedEnvKeys(s), ","), "HERMES_HOME": "/state/hermes", "HOME": "/state/home", "HUB_STATE": "/state", "HUB_WORKSPACE": "/workspace", "HUB_USER_ID": s.User, "HUB_PRINCIPAL_ID": s.User, "HUB_CONTEXT_ID": contextID, "HUB_ORGANIZATION_ID": organizationID, "HUB_RUNTIME_ID": s.User, "HUB_POLICY_VERSION": policy, "HUB_TOOLHUB_STORE": "/state/toolhub/store.json", "HUB_FEATURES": strings.Join(s.featureList(), ","), "HUB_RUNTIME_LISTEN": "0.0.0.0:8080", "TZ": s.Timezone, "HUB_BROWSER": fmt.Sprint(s.Has("browser")), "HUB_MEET": fmt.Sprint(s.Has("meet")), "GITLAB_HOST": host, "GOOGLE_EMAIL": s.GoogleEmail, "GOOGLE_OAUTH_REDIRECT_URI": fmt.Sprintf("http://localhost:%d/oauth2callback", s.OAuthPort), "PYTHONDONTWRITEBYTECODE": "1", "XDG_CACHE_HOME": "/state/cache", "HERMES_AGENT_NOTIFY_INTERVAL": "60", "HUB_TOOL_POLICY": "/config/tool-policy.json", "HUB_TOOL_MANIFEST": "/config/tool-manifest.json"}
	if s.Workspace.Goal != "" {
		runtimeEnv["HUB_BOOTSTRAP_GOAL"] = s.Workspace.Goal
		runtimeEnv["HUB_BOOTSTRAP_REVISION"] = fmt.Sprint(s.Workspace.BootstrapRevision)
	}
	runtimeEnv["HUB_SCOPE_KIND"] = string(UserScope)
	if s.Has("browser") || s.Has("meet") {
		// R4.3: the browser lives in the per-space sidecar; the runtime's
		// browser_cdp tool and health probe attach over the internal alias.
		runtimeEnv["BROWSER_CDP_URL"] = "http://browser-" + s.User + ":9222"
	}
	if scope, err := ReadScope(dir); err == nil && scope.Kind == GroupScope {
		runtimeEnv["HUB_SCOPE_KIND"] = string(GroupScope)
	}
	if s.ExecutionMode != "supervisor" {
		runtimeEnv["HUB_RUNTIME_GENERATION"] = "static-" + s.User + "-" + s.Environment
	}
	runtimeEnv["HUB_CAPABILITY_MODE"] = s.CapabilityMode
	if s.CapabilityMode == "managed" {
		delete(runtimeEnv, "HUB_TOOLHUB_STORE")
		runtimeEnv["HUB_CAPABILITY_PROFILE_ID"] = s.CapabilityProfileID
		runtimeEnv["HUB_CAPABILITY_GENERATION"] = fmt.Sprint(s.CapabilityGeneration)
		runtimeEnv["HUB_CAPABILITY_ENVIRONMENT"] = s.Environment
		runtimeEnv["HUB_MANAGED_MODEL_ID"] = s.Model
		runtimeEnv["HUB_MANAGED_MODEL_URL"] = s.ModelURL
		runtimeEnv["HUB_NATIVE_TOOLSETS"] = strings.Join(s.nativeCarveouts(), ",")
		runtimeEnv["HERMES_BUNDLES_DIR"] = "/state/hermes/skill-bundles"
		runtimeEnv["HERMES_ENABLE_PROJECT_PLUGINS"] = "0"
		// The effective-config preflight must reconstruct the same web block
		// the host materialized; the provider selection cannot be derived from
		// the carve-out list alone, so it rides in as JSON.
		if web := s.managedWebSettings(); !web.empty() {
			if managedWebJSON, err := json.Marshal(web); err == nil {
				runtimeEnv["HUB_MANAGED_WEB"] = string(managedWebJSON)
			}
		}
		// Same contract for the image_gen grant: the spawned runtime
		// reconstructs the identical config block from this normalized JSON
		// (riding RuntimeService env → -e flags), never from agent-editable
		// state. A non-empty value is itself the "feature enabled" marker —
		// see ValidateManagedEffectiveConfig.
		if gen := s.managedImageGenGrant(); gen != (media.ImageGen{}) {
			if imageGenJSON, err := json.Marshal(gen); err == nil {
				runtimeEnv["HUB_MANAGED_IMAGE_GEN"] = string(imageGenJSON)
			}
		}
		// Same contract for the browser sidecar grant: which endpoints the
		// reviewed render admitted and whether mutation tools joined the
		// surface. The in-container attestation replays these decisions
		// instead of re-reading governance state the agent could edit.
		if grant := s.managedBrowserGrant(); grant != (BrowserGrant{}) {
			if grantJSON, err := json.Marshal(grant); err == nil {
				runtimeEnv["HUB_MANAGED_BROWSER"] = string(grantJSON)
			}
		}
	}
	// The declared context-control section rides env so the materialized
	// effective config and the in-container attestation reconstruct identical
	// bytes. Absent means the reviewed zero template — nothing is rendered.
	if ctx := s.managedContextConfig(); ctx != nil {
		if contextJSON, err := json.Marshal(ctx); err == nil {
			runtimeEnv["HUB_MANAGED_CONTEXT"] = string(contextJSON)
		}
	}
	if s.OrgScoped() && s.CapabilityMode != "managed" {
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(s.OrganizationDocsDir), "target": "/org", "read_only": true})
	}
	// The mounted dir is the read-only effective view materialized by
	// Render: platform > org > user, with gated platform skills absent
	// entirely when the feature is off.
	stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(GeneratedEffectiveSkillsDir(dir)), "target": "/skills", "read_only": true})
	// R5.1: the owner store is writable beside the read-only view; env tells
	// the executor which path is which. Drafts never touch /skills.
	stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "skills")), "target": "/skills-store"})
	runtimeEnv["HUB_SKILLS_STORE"] = "/skills-store"
	runtimeEnv["HUB_SKILLS_EFFECTIVE"] = "/skills"
	// R5.2: org-bound spaces also mount the writable org store so org
	// proposals land as drafts there; absent on personal spaces.
	if s.OrgScoped() && s.OrganizationSkillsDir != "" {
		stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(s.OrganizationSkillsDir), "target": "/skills-store-org"})
		runtimeEnv["HUB_SKILLS_ORG"] = "/skills-store-org"
		// R5.4: the bound member's role gates org publishes at apply time.
		runtimeEnv["HUB_ORGANIZATION_ROLE"] = s.OrganizationRole
	}
	if s.CapabilityMode != "managed" {
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
	}
	// SPEC-0082 R3: role commons — daemons run the lean core image, Docker
	// consumers the control image, only hermes-runtime keeps the agent image.
	common := M{"init": true, "restart": "unless-stopped", "user": fmt.Sprintf("10001:%d", max(0, os.Getgid())), "read_only": true, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "tmpfs": []string{"/tmp:uid=10001,gid=10001,mode=1777"}, "extra_hosts": M{"host.docker.internal": "host-gateway"}, "logging": M{"driver": "local", "options": M{"max-size": "10m", "max-file": "3"}}}
	coreCommon := cloneMap(common)
	coreCommon["image"] = "hermes-hub:0.4.0-" + s.Environment + "-core"
	controlCommon := cloneMap(common)
	controlCommon["image"] = "hermes-hub:0.4.0-" + s.Environment + "-control"
	runtimeCommon := cloneMap(common)
	runtimeCommon["image"] = "hermes-hub:0.4.0-" + s.Environment
	imageBuild := func(target string) M {
		return M{"context": filepath.ToSlash(projectRoot), "dockerfile": "docker/Dockerfile", "target": target}
	}
	coreBuild := imageBuild(s.Environment + "-core")
	runtimeService := cloneMap(runtimeCommon)
	// The service builds its own agent tag: renders that drop hermes-runtime
	// (supervisor/managed) get the image through the explicit hubctl build.
	runtimeService["build"] = imageBuild(s.Environment)
	// SPEC-0082 R4.1: the Chromium workload lives in the browser sidecar —
	// shm_size moved there with it; the runtime allocates none.
	runtimeService["env_file"] = runtimeEnvFiles
	runtimeService["environment"] = runtimeEnv
	runtimeService["volumes"] = stateVolumes
	runtimeService["ports"] = ports
	if s.CapabilityMode == "managed" {
		runtimeService["extra_hosts"] = M{}
		runtimeService["tmpfs"] = append(slices.Clone(common["tmpfs"].([]string)),
			"/state/hermes/skills:ro,mode=0555", "/state/hermes/hooks:ro,mode=0555", "/state/hermes/plugins:ro,mode=0555",
			"/state/hermes/skill-bundles:ro,mode=0555", "/state/hermes/scripts:ro,mode=0555",
			"/state/hermes/bin:ro,mode=0555", "/state/hermes/node:ro,mode=0555", "/state/hermes/lsp:ro,mode=0555")
	}
	if s.CapabilityMode != "managed" {
		// Same seal managed mode applies: upstream skills-sync tolerates an
		// empty read-only /state/hermes/skills, so the agent never gets a
		// writable skill path (SPEC-0083 R2.6/D13).
		runtimeService["tmpfs"] = append(slices.Clone(common["tmpfs"].([]string)),
			"/state/hermes/skills:ro,mode=0555")
	}
	runtimeService["command"] = []string{"serve"}
	if !s.Has("telegram") && s.NativeCron != "unmigrated" {
		runtimeService["command"] = []string{"idle"}
	}
	runtimeService["healthcheck"] = M{"test": []string{"CMD", "hub-runtime", "health"}, "interval": "30s", "timeout": "5s", "retries": 3}
	// The inner Python gateway freezes its event loop for up to ~15s while
	// stopping MCP servers on SIGTERM (upstream known issue); under Docker's
	// default 10s grace it is SIGKILLed and the lifecycle ledger records a
	// false UNCLEAN death on every routine restart.
	runtimeService["stop_grace_period"] = "30s"
	if s.CapabilityMode == "managed" {
		runtimeService["networks"] = []string{managedAgentNetwork(s)}
	}
	if s.Environment == "dev" {
		runtimeService["restart"] = "no"
		if s.CapabilityMode != "managed" {
			runtimeService["read_only"] = false
			for _, name := range []string{"cmd", "internal", "services", "docker", "config", "docs", "specs", ".work", ".github", "go.mod", "go.sum", "justfile", "AGENTS.md", "README.md", "SETUP.md", "START_HERE.ru.md", "LICENSE", "SECURITY.md", "CONTRIBUTING.md"} {
				stateVolumes = append(stateVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(projectRoot, name)), "target": "/src/" + name})
			}
			runtimeService["environment"].(M)["GOCACHE"] = "/state/go-build"
			runtimeService["environment"].(M)["GOMODCACHE"] = "/state/go-mod"
		}
		runtimeService["volumes"] = stateVolumes
	}
	if !infra && s.CapabilityMode != "managed" {
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
		if infra {
			runtimeService["env_file"] = append(runtimeEnvFiles, M{"path": filepath.ToSlash(filepath.Join(dir, "media.auth")), "format": "raw"})
		}
	}
	services := M{"hermes-runtime": runtimeService}
	// SPEC-0082 R4: the per-space browser sidecar owns the headed-Chromium
	// display stack. CDP binds inside the container and is reachable only on
	// the space network through the browser-<user> alias; the sole published
	// port is authenticated noVNC on host loopback.
	if s.Has("browser") || s.Has("meet") {
		browserService := cloneMap(common)
		browserService["image"] = "hermes-hub:0.4.0-" + s.Environment + "-browser"
		browserService["build"] = imageBuild(s.Environment + "-browser")
		browserService["shm_size"] = "1gb"
		// SPEC-0083 R1: bounded like toolhub — Chromium under Xvfb plus two
		// pw-mcp processes plus the meet NodeServer is the heaviest per-space
		// workload; the 1gb shm counts against the memory limit.
		browserService["cpus"] = "2.0"
		browserService["mem_limit"] = "3g"
		browserService["pids_limit"] = 512
		browserService["ports"] = []string{fmt.Sprintf("127.0.0.1:%d:6080", s.BrowserPort)}
		browserService["environment"] = M{"HUB_MEET": fmt.Sprint(s.Has("meet")), "HUB_BROWSER_HOST": "browser-" + s.User}
		// Sidecar downloads/screenshots must land where the agent's own
		// workspace resolves: managed runtimes bind managed/<env>/workspace,
		// unmanaged ones the space root — mounting the other tree strands
		// every browser artifact outside the delivery path.
		browserWorkspace := filepath.Join(dir, "workspace")
		if s.CapabilityMode == "managed" {
			browserWorkspace = filepath.Join(dir, "managed", s.Environment, "workspace")
		}
		browserVolumes := []any{
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "connections", "browser")), "target": "/state/browser"},
			M{"type": "bind", "source": filepath.ToSlash(browserWorkspace), "target": "/workspace"},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "browser-novnc.passwd")), "target": "/run/novnc.pass", "read_only": true},
		}
		if s.Has("meet") {
			// The node shares the runtime's meetings dir so auth_state and
			// out_dir RPC paths resolve identically on both containers; the
			// rendered token lands at upstream's default token_path, mounted
			// read-only — NodeServer reuses it and never writes.
			browserVolumes = append(browserVolumes,
				M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "hermes", "workspace", "meetings")), "target": "/state/hermes/workspace/meetings"},
				M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "meet-node-token.json")), "target": "/state/hermes/workspace/meetings/node_token.json", "read_only": true})
		}
		browserService["volumes"] = browserVolumes
		if s.CapabilityMode == "managed" {
			// Managed runtimes spawn onto the private per-space agent network;
			// CDP stays reachable only through it. Internal networks silently
			// drop published ports, so the sidecar also joins a dedicated
			// publishable network (nothing else attaches) for the loopback
			// noVNC port.
			browserService["networks"] = M{
				managedAgentNetwork(s): M{"aliases": []string{"browser-" + s.User}},
				"browser-publish":      M{},
			}
		} else {
			browserService["networks"] = M{"default": M{"aliases": []string{"browser-" + s.User}}}
		}
		services["browser-"+s.User] = browserService
	}
	hostRuntimeDir := filepath.ToSlash(filepath.Join(dir, "runtime"))
	hostCliproxyDir := filepath.ToSlash(filepath.Join(dir, "cliproxy"))
	stateBind := M{"type": "bind", "source": hostRuntimeDir, "target": "/state"}
	dockerSock := M{"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"}
	hostRoot := "/state=" + hostRuntimeDir
	brokerMaterializedVolume := "hermes-hub-" + s.User + "-" + s.Environment + "-broker-materialized"
	brokerMaterializedMount := M{"type": "volume", "source": "broker-materialized", "target": "/run/broker-materialized"}
	// Per-call credential material (credentials.env, <name>.secret) stages on
	// tmpfs shared by ToolHub and the workload controller — it must never
	// reach the persistent state bind or the host disk.
	credStagingVolume := "hermes-hub-" + s.User + "-" + s.Environment + "-cred-staging"
	credStagingMount := M{"type": "volume", "source": "cred-staging", "target": "/run/cred-staging"}
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
	if s.CapabilityMode != "managed" {
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_RUNTIME_", "runtime", "hermes-runtime-adapter") {
			runtimeEnv[key] = value
		}
		runtimeService["volumes"] = append(runtimeService["volumes"].([]any), brokerSecrets("runtime"))
	}

	// Services every spawned or secondary runtime resolves by name attach to the
	// shared network; everything else stays on the project default.
	sharedNetworks := []string{"default", sharedNetworkName}
	containerSupervisor := infra && includeGateway && s.ExecutionMode == "supervisor" && s.SupervisorURL == "http://supervisor:8876"
	if containerSupervisor {
		svc := cloneMap(controlCommon)
		svc["build"] = imageBuild(s.Environment + "-control")
		svc["entrypoint"] = []string{"hubctl", "supervisor", "--spaces", "/spaces", "--env", s.Environment, "--runtime-image", "hermes-hub:0.4.0-" + s.Environment, "--supervisor-listen", "0.0.0.0:8876", "--supervisor-allow-wildcard-listen"}
		svc["environment"] = M{"HUB_SUPERVISOR_AUTH": "${HUB_SUPERVISOR_AUTH:?set HUB_SUPERVISOR_AUTH or provision supervisor.auth for this stack}", "HUB_SUPERVISOR_HOST_SPACES": filepath.ToSlash(filepath.Dir(dir)), "HOME": "/tmp", "TZ": s.Timezone,
			// Opt-in authorities stay disabled unless the operator provisions
			// them: admin holds need a separate authority token; the personal
			// supervisor needs a managed model endpoint + model name.
			"HUB_SUPERVISOR_ADMIN_AUTH": "${HUB_SUPERVISOR_ADMIN_AUTH:-}",
			"HUB_PERSONAL_MODEL":        "${HUB_PERSONAL_MODEL:-}",
			"HUB_MANAGED_MODEL_URL":     "${HUB_MANAGED_MODEL_URL:-}",
			"OPENAI_API_KEY":            "${OPENAI_API_KEY:-}",
			// Run-budget knobs are operator-tunable for drills and squeeze
			// tests; empty resolves to the code defaults (30m / 60s).
			"HUB_RUN_BUDGET":  "${HUB_RUN_BUDGET:-}",
			"HUB_STREAM_IDLE": "${HUB_STREAM_IDLE:-}"}
		svc["volumes"] = []any{dockerSock,
			M{"type": "bind", "source": filepath.ToSlash(filepath.Dir(dir)), "target": "/spaces"},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(projectRoot, "config")), "target": "/config", "read_only": true},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(projectRoot, ".local", "supervisor.log")), "target": "/.local/supervisor.log"}}
		svc["networks"] = []string{"default", sharedNetworkName, "supervisor-control"}
		svc["stop_grace_period"] = "30s"
		services["supervisor"] = svc
	}
	var egressHosts []string
	if s.CapabilityMode == "managed" {
		egressHosts = managedEgressHosts(s, dir)
	}
	if s.CapabilityMode == "managed" {
		modelRelay := cloneMap(coreCommon)
		modelRelay["entrypoint"] = []string{"hub-runtime", "model-relay"}
		// The merged models.yaml (spaces -> org -> user layers) mounts
		// read-only; provider key_env names resolve from the space's own
		// secrets env file, the owner-facing switch bearer from
		// model-relay.auth — never as literals in compose.
		cliproxyURL := "http://cliproxy:8317"
		if s.Cliproxy == "dedicated" {
			cliproxyURL = "http://cliproxy-" + s.User + ":8317"
		}
		modelRelay["environment"] = M{
			"HUB_MODEL_RELAY_REGISTRY": "/run/model-config/models.yaml",
			"HUB_MODEL_RELAY_STATE":    "/run/model-state/model-select.json",
			"HUB_MODEL_RELAY_OWNER":    s.User,
			"HUB_MODEL_RELAY_CLIPROXY": cliproxyURL,
		}
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_MODEL_", "model", "hermes-model-relay") {
			modelRelay["environment"].(M)[key] = value
		}
		modelRelay["env_file"] = []any{
			M{"path": filepath.ToSlash(filepath.Join(dir, "runtime."+s.Environment+".env")), "format": "raw"},
			M{"path": filepath.ToSlash(filepath.Join(dir, "model-relay."+s.Environment+".env")), "format": "raw"},
			M{"path": filepath.ToSlash(filepath.Join(dir, "model-relay.auth")), "format": "raw"},
		}
		modelRelay["volumes"] = []any{
			brokerSecrets("model"),
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "generated", "model-registry", s.Environment)), "target": "/run/model-config", "read_only": true},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "managed", s.Environment, "model-relay")), "target": "/run/model-state"},
		}
		modelRelay["extra_hosts"] = M{}
		// A stable per-space alias on the shared network lets the gateway of
		// the same space call its own relay (owner model switching) without
		// colliding with other spaces' model-relay service names.
		modelRelay["networks"] = M{sharedNetworkName: M{"aliases": []string{"model-relay-" + s.User}}, managedAgentNetwork(s): M{}}
		services["model-relay"] = modelRelay
		toolhubRelay := cloneMap(coreCommon)
		toolhubRelay["entrypoint"] = []string{"hub-runtime", "toolhub-relay"}
		toolhubRelay["extra_hosts"] = M{}
		toolhubRelay["networks"] = M{sharedNetworkName: M{}, managedAgentNetwork(s): M{"aliases": []string{"toolhub"}}}
		services["toolhub-relay"] = toolhubRelay
		if len(egressHosts) > 0 {
			// The managed agent network has no default route; the reviewed
			// web/sibling reachability for the runtime goes through this
			// allowlisted forward proxy and nowhere else.
			egressRelay := cloneMap(coreCommon)
			egressRelay["entrypoint"] = []string{"hub-runtime", "egress-relay"}
			egressRelay["environment"] = M{"HUB_EGRESS_RELAY_HOSTS": strings.Join(egressHosts, ",")}
			egressRelay["extra_hosts"] = M{}
			egressRelay["networks"] = M{sharedNetworkName: M{}, managedAgentNetwork(s): M{"aliases": []string{"egress-relay"}}}
			services["egress-relay"] = egressRelay
		}
	}
	if infra {
		cliproxy := cloneMap(coreCommon)
		cliproxy["build"] = coreBuild
		cliproxy["entrypoint"] = []string{"cli-proxy-api", "-config", "/cliproxy/config.yaml"}
		cliproxy["volumes"] = []any{M{"type": "bind", "source": hostCliproxyDir, "target": "/cliproxy"}}
		cliproxy["ports"] = []string{"127.0.0.1:8317:8317"}
		cliproxy["networks"] = sharedNetworks
		services["cliproxy"] = cliproxy

		toolhubEnv := M{"HUB_STATE": "/state", "HUB_USER_ID": s.User, "HUB_PRINCIPAL_ID": s.User, "HUB_CONTEXT_ID": contextID, "HUB_RUNTIME_ID": s.User, "HUB_ORGANIZATION_ID": organizationID, "HUB_POLICY_VERSION": policy, "HUB_TOOLHUB_STORE": "/state/toolhub/store.json", "HUB_AUDIT_LEDGER": "/state/toolhub/audit.jsonl", "HUB_CREDENTIAL_STORE": "/state/credentials/store.enc", "HUB_CREDENTIAL_KEY_FILE": "/state/credential.key", "HUB_TOOLHUB_LISTEN": "0.0.0.0:8090", "HUB_TOOLHIVE_ADMISSION_ENDPOINT": "http://workload-controller:8545/admit", "HUB_ARTIFACT_DIR": "/state/artifacts", "HUB_BUILD_SECCOMP": "/opt/hub/seccomp/seccomp-buildkit-rootless.json", "HUB_BUILD_CACHE": "1", "HUB_RECIPE_CATALOGS": "mcp-registry,toolhive,docker-mcp,docker-hub,ghcr,github-search", "HUB_DOCKER_HOST_ROOT": hostRoot, "HUB_BROKER_MATERIALIZED_VOLUME": brokerMaterializedVolume, "HUB_CRED_STAGING": "/run/cred-staging", "HUB_CRED_STAGING_VOLUME": credStagingVolume, "HOME": "/tmp", "TZ": s.Timezone, "HUB_COMMUNICATION_CONTROL_URL": "http://communication-hub:8081", "HUB_TOOL_GOVERNANCE": "/state/toolhub/governance.json"}
		if s.CapabilityMode == "managed" {
			toolhubEnv["HUB_CAPABILITY_MODE"] = s.CapabilityMode
			toolhubEnv["HUB_CAPABILITY_PROFILE_ID"] = s.CapabilityProfileID
			toolhubEnv["HUB_CAPABILITY_GENERATION"] = fmt.Sprint(s.CapabilityGeneration)
			toolhubEnv["HUB_CAPABILITY_ENVIRONMENT"] = s.Environment
		}
		if s.ExecutionMode == "supervisor" {
			// The agent-tools executor runs inside the owning runtime
			// container; the supervisor owns its naming contract and docker
			// exec is the only control-plane path that never crosses the
			// agent network. Applies to every supervised runtime, managed or
			// not — otherwise unmanaged spaces lose agent-tools entirely.
			toolhubEnv["HUB_AGENT_EXEC_MODE"] = "supervisor"
		}
		supervisorURL := strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"))
		if s.ExecutionMode != "" {
			supervisorURL = s.SupervisorURL
		}
		if supervisorURL != "" {
			toolhubEnv["HUB_RUNTIME_SUPERVISOR_URL"] = supervisorURL
			toolhubEnv["HUB_COMMUNICATION_AUTH"] = "${HUB_SUPERVISOR_AUTH:?set HUB_SUPERVISOR_AUTH or provision supervisor.auth for this stack}"
		}
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_CONTROL_", "toolhub", "hermes-toolhub") {
			toolhubEnv[key] = value
		}
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_RUNTIME_", "runtime", "hermes-runtime-adapter") {
			toolhubEnv[key] = value
		}
		dockerScope := "hermes-hub-" + s.User + "-" + s.Environment
		toolhubEnv["HUB_DOCKER_SCOPE"] = dockerScope
		// Bounded-cli owner registration: bare command names the owner may
		// declare; mirrors the controller's cli.UserCommands admission set.
		// bash admits an owner-registered `bash -c` shell cell; the cell is
		// the boundary — owner argv still lands in a mountless sandbox.
		toolhubEnv["HUB_CLI_ALLOWLIST"] = "rg,bash"
		toolhubEnv["HUB_DOCKER_AGENT_NET"] = managedAgentNetwork(s)
		toolhubEnv["HUB_SKILLS_EFFECTIVE"] = "/skills-effective"
		toolhubEnv["HUB_SKILLS_STORE"] = "/skills-store"
		toolhubVolumes := []any{stateBind, brokerSecrets("toolhub"), brokerMaterializedMount, credStagingMount,
			M{"type": "bind", "source": filepath.ToSlash(GeneratedEffectiveSkillsDir(dir)), "target": "/skills-effective", "read_only": true},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "skills")), "target": "/skills-store", "read_only": true}}
		if s.usesDockerSocket() {
			toolhubVolumes = append(toolhubVolumes, dockerSock)
		}
		if s.DiagnosticsEnabled() {
			toolhubEnv["HUB_DIAGNOSTICS_DIR"] = "/diagnostics"
			toolhubEnv["HUB_DIAGNOSTICS_PROJECT"] = "hermes-hub-" + s.User + "-" + s.Environment
			toolhubEnv["HUB_DIAGNOSTICS_AGENT_NET"] = managedAgentNetwork(s)
			toolhubEnv["HUB_DIAGNOSTICS_OWNER"] = s.User
			toolhubVolumes = append(toolhubVolumes,
				M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "diagnostics")), "target": "/diagnostics"},
				M{"type": "bind", "source": filepath.ToSlash(filepath.Join(projectRoot, ".local", "supervisor.log")), "target": "/diagnostics/supervisor.log", "read_only": true})
		}
		// Extra principal tokens for secondary spaces: one JSON map per deploy,
		// mounted read-only. Rendered only when the operator wrote the file.
		if _, err := os.Stat(filepath.Join(dir, "toolhub-tokens.json")); err == nil {
			toolhubVolumes = append(toolhubVolumes, M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "toolhub-tokens.json")), "target": "/run/toolhub-tokens.json", "read_only": true})
			toolhubEnv["HUB_TOOLHUB_TOKENS_FILE"] = "/run/toolhub-tokens.json"
		}
		toolhub := cloneMap(controlCommon)
		toolhub["build"] = imageBuild(s.Environment + "-control")
		toolhub["entrypoint"] = []string{"toolhub"}
		toolhub["env_file"] = []any{M{"path": filepath.ToSlash(filepath.Join(dir, "runtime.auth")), "format": "raw"}, M{"path": filepath.ToSlash(filepath.Join(dir, "toolhub.auth")), "format": "raw"}}
		toolhub["environment"] = toolhubEnv
		toolhub["volumes"] = toolhubVolumes
		toolhub["ports"] = []string{"127.0.0.1:8090:8090"}
		toolhub["networks"] = sharedNetworks
		if s.CapabilityMode == "managed" || supervisorURL != "" {
			toolhub["networks"] = M{"default": M{}, sharedNetworkName: M{"aliases": []string{"toolhub-control"}}}
		}
		// Bounded-cli children share this cgroup: the admission receipt can
		// claim cpu/memory/pid bounds only when the measured container cap is
		// at or under the plan, so the cap matches the CLI plan floor.
		toolhub["cpus"] = "2.0"
		toolhub["mem_limit"] = "2g"
		toolhub["pids_limit"] = 128
		services["toolhub"] = toolhub

		controller := cloneMap(controlCommon)
		controller["entrypoint"] = []string{"hubctl", "connector", "generic-controller", "--config", "/state/generic-controller.json", "--listen", "0.0.0.0:8545", "--token-file", "/state/generic-controller.key"}
		// The controller resolves principal-scoped cell binds inside /spaces —
		// a read-only view of the whole spaces root — and translates them to
		// host paths for the sibling container through the /spaces mapping.
		spacesHostDir := filepath.ToSlash(filepath.Dir(dir))
		spacesBind := M{"type": "bind", "source": spacesHostDir, "target": "/spaces", "read_only": true}
		controller["environment"] = M{"HUB_STATE": "/state", "HUB_DOCKER_HOST_ROOT": hostRoot + ",/spaces=" + spacesHostDir, "HUB_BROKER_MATERIALIZED_VOLUME": brokerMaterializedVolume, "HUB_CRED_STAGING": "/run/cred-staging", "HUB_CRED_STAGING_VOLUME": credStagingVolume, "HUB_CONTROLLER_REMOTE": "1", "HUB_DOCKER_SCOPE": dockerScope, "HUB_DOCKER_AGENT_NET": managedAgentNetwork(s), "HOME": "/tmp", "TZ": s.Timezone}
		controllerVolumes := []any{stateBind, brokerMaterializedMount, credStagingMount, spacesBind}
		if s.usesDockerSocket() {
			controllerVolumes = append(controllerVolumes, dockerSock)
		}
		controller["volumes"] = controllerVolumes
		controller["ports"] = []string{"127.0.0.1:8545:8545"}
		services["workload-controller"] = controller

		broker := cloneMap(coreCommon)
		broker["entrypoint"] = []string{"credential-broker", "serve", "--config", "/var/lib/credential-broker/config.json", "--state-witness-dir", "/var/lib/credential-broker-recovery"}
		// The dedicated tmpfs volume lets Broker leases reach ToolHub and the
		// controller without exposing Broker's encrypted store to MCP workloads.
		broker["volumes"] = []any{M{"type": "volume", "source": "broker-state", "target": "/var/lib/credential-broker"}, M{"type": "volume", "source": "broker-recovery", "target": "/var/lib/credential-broker-recovery"}, M{"type": "volume", "source": "broker-recovery-key", "target": "/var/lib/credential-broker-recovery-key"}, brokerMaterializedMount}
		broker["ports"] = []string{"127.0.0.1:8787:8787"}
		broker["networks"] = sharedNetworks
		// Personal loopback deployment: the connect link opens the credential form
		// directly. Remove BROKER_DIRECT_FORM to restore the trusted-channel
		// confirmation code required by the stricter spec flow.
		broker["environment"] = M{"BROKER_DIRECT_FORM": "1"}
		services["credential-broker"] = broker
	}

	// cliproxy: dedicated gives a secondary space its own proxy instance,
	// reachable as cliproxy-<user> on both networks. The service is named
	// after the owner so its implicit Compose aliases can never collide
	// with the shared infra "cliproxy" name on the runtime network.
	if !infra && s.Cliproxy == "dedicated" {
		cliproxy := cloneMap(coreCommon)
		cliproxy["build"] = coreBuild
		cliproxy["entrypoint"] = []string{"cli-proxy-api", "-config", "/cliproxy/config.yaml"}
		cliproxy["volumes"] = []any{M{"type": "bind", "source": hostCliproxyDir, "target": "/cliproxy"}}
		name := "cliproxy-" + s.User
		cliproxy["networks"] = M{"default": M{"aliases": []string{name}}, sharedNetworkName: M{"aliases": []string{name}}}
		services[name] = cliproxy
	}

	if infra && includeGateway && (s.Has("telegram") || s.Has("slack_app")) {
		supervisorURL := strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"))
		if s.ExecutionMode != "" {
			supervisorURL = s.SupervisorURL
		}
		gateway := cloneMap(coreCommon)
		gateway["entrypoint"] = []string{"communication-hub"}
		gatewayEnvFiles := []any{M{"path": filepath.ToSlash(filepath.Join(dir, "communication."+s.Environment+".env")), "format": "raw"}}
		if s.Has("telegram_auth") {
			authEnv := filepath.Join(dir, "telegram-auth."+s.Environment+".env")
			if info, err := os.Stat(authEnv); err == nil && info.Mode().IsRegular() {
				gatewayEnvFiles = append(gatewayEnvFiles, M{"path": filepath.ToSlash(authEnv), "format": "raw"})
			}
		}
		gatewayEnvironment := M{"HUB_USER_ID": s.User, "HUB_ORGANIZATION_ID": organizationID, "HUB_RUNTIME_ID": s.User, "HUB_POLICY_VERSION": policy, "HUB_FEATURES": strings.Join(s.featureList(), ","), "HUB_RUNTIME_URL": "http://hermes-runtime:8080", "HUB_RUNTIME_SUPERVISOR_URL": "${HUB_RUNTIME_SUPERVISOR_URL}", "HUB_COMMUNICATION_SPOOL": "/data", "HUB_CONFIGURED_ENV": strings.Join(configuredEnvKeys(s, dir), ","), "HUB_NATIVE_CRON": s.NativeCron, "HUB_DIAGNOSTICS_ENABLED": fmt.Sprint(s.DiagnosticsEnabled())}
		if s.Has("telegram") {
			gatewayEnvironment["HUB_TELEGRAM_AUTH_ENABLED"] = fmt.Sprint(s.Has("telegram_auth"))
		}
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_APPROVE_", "communication", "hermes-communication") {
			gatewayEnvironment[key] = value
		}
		gatewayEnvironment["HUB_COMMUNICATION_LISTEN"] = "0.0.0.0:8081"
		communicationHostPort := 8081
		if s.Has("slack_app") {
			gatewayEnvironment["HUB_COMMUNICATION_LISTEN"] = "0.0.0.0:8081"
			communicationHostPort = slackEventsHostPort(s)
		}
		gatewayEnvironment["HUB_COMMUNICATION_FORM_ORIGIN"] = fmt.Sprintf("http://localhost:%d", communicationHostPort)
		gateway["ports"] = []string{fmt.Sprintf("127.0.0.1:%d:8081", communicationHostPort)}
		if s.CapabilityMode == "managed" {
			gatewayEnvironment["HUB_MODEL_RELAY_URL"] = "http://model-relay-" + s.User + ":8318"
			gatewayEnvFiles = append(gatewayEnvFiles, M{"path": filepath.ToSlash(filepath.Join(dir, "model-relay.auth")), "format": "raw"})
		}
		if s.Has("transcription") {
			// SPEC-0082 R2: voice is service-only — the gateway talks to the
			// dedicated hub-stt/hub-tts services or reports voice unavailable.
			gatewayEnvironment["HUB_STT_URL"] = "http://hub-stt:8090"
			gatewayEnvironment["HUB_TTS_URL"] = "http://hub-tts:8090"
			// Per-language voice overrides reach the gateway unchanged; the
			// sidecar's own env carries the installed-voice list.
			for _, key := range []string{"HUB_TTS_VOICE", "HUB_TTS_VOICE_EN", "HUB_TTS_VOICE_RU", "HUB_TTS_VOICE_KK", "HUB_TTS_VOICE_ES"} {
				gatewayEnvironment[key] = "${" + key + ":-}"
			}
			gatewayEnvFiles = append(gatewayEnvFiles, M{"path": filepath.ToSlash(filepath.Join(dir, "media.auth")), "format": "raw"})
		}
		if s.ExecutionMode != "" {
			gatewayEnvironment["HUB_RUNTIME_SUPERVISOR_URL"] = supervisorURL
		}
		if supervisorURL == "" {
			gatewayEnvFiles = append(gatewayEnvFiles, M{"path": filepath.ToSlash(filepath.Join(dir, "runtime.auth")), "format": "raw"})
			gatewayEnvironment["HUB_TOOLHUB_URL"] = "http://toolhub:8090"
			// The skillgit ExecCell posts to the controller /cli-exec derived
			// from this endpoint; toolhub.auth carries the admission token.
			gatewayEnvFiles = append(gatewayEnvFiles, M{"path": filepath.ToSlash(filepath.Join(dir, "toolhub.auth")), "format": "raw"})
			gatewayEnvironment["HUB_TOOLHIVE_ADMISSION_ENDPOINT"] = "http://workload-controller:8545/admit"
		} else {
			gatewayEnvironment["HUB_SUPERVISOR_AUTH"] = "${HUB_SUPERVISOR_AUTH:?set HUB_SUPERVISOR_AUTH or provision supervisor.auth for this stack}"
			gatewayEnvironment["HUB_TOOLHUB_URL"] = "http://toolhub-control:8090"
		}
		// Harness-side /name and $name expansion reads the ro effective view
		// (R6.5); same convention as the toolhub mount above. The owner store
		// stays writable for skill-scoped credential forms (R8.2).
		gatewayEnvironment["HUB_SKILLS_EFFECTIVE"] = "/skills-effective"
		gatewayEnvironment["HUB_SKILLS_STORE"] = "/skills-store"
		gatewayEnvironment["HUB_GITLAB_HOST"] = s.GitLabHost
		gateway["env_file"] = gatewayEnvFiles
		gateway["environment"] = gatewayEnvironment
		gateway["networks"] = sharedNetworks
		gateway["volumes"] = []any{
			M{"type": "volume", "source": "communication-hub-data", "target": "/data"},
			M{"type": "bind", "source": filepath.ToSlash(GeneratedEffectiveSkillsDir(dir)), "target": "/skills-effective", "read_only": true},
			M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "skills")), "target": "/skills-store"},
			brokerSecrets("communication"),
		}
		// Sibling Telegram identities live in this file. TELEGRAM_ALLOWED_USERS
		// stays the single infra-owner fallback when the file is absent.
		usersFile := filepath.Join(dir, "communication.users.yaml")
		if info, err := os.Stat(usersFile); err == nil && info.Mode().IsRegular() {
			gatewayEnvironment["HUB_COMMUNICATION_CONFIG"] = "/config/communication.users.yaml"
			gateway["volumes"] = append(gateway["volumes"].([]any), M{"type": "bind", "source": filepath.ToSlash(usersFile), "target": "/config/communication.users.yaml", "read_only": true})
		}
		// Sibling runtimes authenticate control calls (credential forms,
		// prepare-outcome, routines) with their own enrolled tokens.
		if info, err := os.Stat(filepath.Join(dir, "toolhub-tokens.json")); err == nil && info.Mode().IsRegular() {
			gatewayEnvironment["HUB_COMMUNICATION_TOKENS_FILE"] = "/config/toolhub-tokens.json"
			gateway["volumes"] = append(gateway["volumes"].([]any), M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "toolhub-tokens.json")), "target": "/config/toolhub-tokens.json", "read_only": true})
		}
		// Per-principal relay routing for /model: the shared gateway resolves
		// the caller's own relay URL + admin bearer from this enrolled file.
		if info, err := os.Stat(filepath.Join(dir, "model-relay-admins.json")); err == nil && info.Mode().IsRegular() {
			gatewayEnvironment["HUB_MODEL_RELAY_ADMINS_FILE"] = "/config/model-relay-admins.json"
			gateway["volumes"] = append(gateway["volumes"].([]any), M{"type": "bind", "source": filepath.ToSlash(filepath.Join(dir, "model-relay-admins.json")), "target": "/config/model-relay-admins.json", "read_only": true})
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
				svc["image"] = "hermes-hub-" + role + ":0.4.0-" + s.Environment
				svc["build"] = M{"context": filepath.ToSlash(projectRoot), "dockerfile": "docker/Dockerfile." + role}
				engine := "command"
				if role == "stt" {
					// command-serve keeps stt-worker resident: faster-whisper
					// spends tens of seconds on WhisperModel() per cold exec,
					// which dominated voice-message latency.
					engine = "command-serve"
				}
				svc["environment"] = M{
					"HUB_MEDIA_LISTEN": "0.0.0.0:8090",
					"HUB_MEDIA_ENGINE": engine, "HUB_MEDIA_COMMAND": "/usr/local/bin/" + role + "-worker",
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
		mediaSvc := cloneMap(coreCommon)
		mediaSvc["entrypoint"] = []string{"hub-media"}
		mediaEnv := M{
			"HUB_MEDIA_LISTEN": "0.0.0.0:8090",
			"HUB_MEDIA_ENGINE": "${HUB_MEDIA_ENGINE:-remote}",
			// Multi-engine ladder and logical-model routes come from the
			// deploy env; "command" is the local engine (HUB_MEDIA_COMMAND
			// points at a mounted runner script).
			"HUB_MEDIA_ENGINES":        "${HUB_MEDIA_ENGINES:-}",
			"HUB_MEDIA_MODEL_ROUTES":   "${HUB_MEDIA_MODEL_ROUTES:-}",
			"HUB_MEDIA_PROVIDERS":      "${HUB_MEDIA_PROVIDERS:-}",
			"HUB_MEDIA_COMMAND":        "${HUB_MEDIA_COMMAND:-}",
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
	if includeGateway && s.CapabilityMode == "managed" {
		// Managed runtimes are spawned by the supervisor onto the private agent
		// network. Compose owns only the control plane and the narrow relays.
		// RuntimeService (includeGateway=false) keeps the entry for its env contract.
		delete(services, "hermes-runtime")
	}
	// Secondary spaces still declare broker-secrets-runtime: the supervisor
	// mounts it by project-derived name into that user's spawned runtimes.
	volumes := M{"broker-secrets-runtime": M{}}
	if s.Has("hosting") {
		hostingVolumes := runtimeService["volumes"].([]any)
		addHosting(services, volumes, s, dir, common, runtimeEnv, &hostingVolumes)
		hostingService := services[hostingName(s)].(M)
		for key, value := range brokerClientEnv("HUB_CREDENTIAL_BROKER_RUNTIME_", "runtime", "hermes-runtime-adapter") {
			hostingService["environment"].(M)[key] = value
		}
		hostingService["volumes"] = append(hostingService["volumes"].([]any), brokerSecrets("runtime"))
		runtimeService["volumes"] = hostingVolumes
	}
	if s.CapabilityMode == "managed" {
		volumes["broker-secrets-model"] = M{}
	}
	result := M{"name": "hermes-hub-" + s.User + "-" + s.Environment, "services": services, "volumes": volumes}
	if infra {
		volumes["communication-hub-data"] = M{}
		// The broker store is a singleton per environment, not per host: a dev
		// stack must never attach prod credential material. Operators migrating
		// the pre-split volume set HUB_BROKER_STATE_VOLUME to its legacy name.
		brokerState := "hermes-credential-broker-" + s.Environment
		if override := strings.TrimSpace(os.Getenv("HUB_BROKER_STATE_VOLUME")); override != "" {
			brokerState = override
		}
		volumes["broker-state"] = M{"external": true, "name": brokerState}
		volumes["broker-recovery"] = M{"external": true, "name": "hermes-credential-broker-recovery-" + s.Environment}
		volumes["broker-recovery-key"] = M{"external": true, "name": "hermes-credential-broker-recovery-key-" + s.Environment}
		volumes["broker-materialized"] = M{"name": brokerMaterializedVolume, "driver": "local", "driver_opts": M{"type": "tmpfs", "device": "tmpfs", "o": "size=64m,uid=10001,gid=10001,mode=0700"}}
		volumes["cred-staging"] = M{"name": credStagingVolume, "driver": "local", "driver_opts": M{"type": "tmpfs", "device": "tmpfs", "o": "size=64m,uid=10001,gid=10001,mode=0700"}}
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
	if containerSupervisor {
		result["networks"].(M)["supervisor-control"] = M{"name": "hermes-hub-control", "internal": true}
	}
	if s.CapabilityMode == "managed" {
		result["networks"].(M)[managedAgentNetwork(s)] = M{"name": managedAgentNetwork(s), "internal": true}
		if s.Has("browser") || s.Has("meet") {
			result["networks"].(M)["browser-publish"] = M{}
		}
	}
	return result
}

// cliControllerSection renders the bounded-cli cell envelope the workload
// controller enforces per call. Every execution runs in a sibling sandbox
// cell on the dedicated cli-tools image — read-only root, no ToolHub mounts,
// network none unless the plan declares reviewed egress. Approvals are
// compiled from the shipped catalog so the plan tuple can never drift from
// the definition the store registers.
func cliControllerSection(s Settings, dir string) api.CLIControllerConfig {
	// *.githubusercontent.com serves github release assets and archive
	// redirects — the same trust class as github.com already allowed here,
	// and required for package managers that fetch toolchains from releases
	// (uv managed-python, gh release download).
	egress := []string{"127.0.0.1", "github.com", "api.github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "gitlab.com", "bitbucket.org", "codeberg.org"}
	gitHosts := []string{"github.com", "gitlab.com"}
	if s.GitLabHost != "" && s.GitLabHost != "gitlab.com" {
		egress = append(egress, s.GitLabHost)
		gitHosts = append(gitHosts, s.GitLabHost)
	}
	for _, registry := range s.CLIRegistries {
		egress = append(egress, cliRegistryHosts[registry]...)
	}
	definitions := api.CLICatalogDefinitions()
	approved := make([]api.CLIApproval, 0, len(definitions))
	for _, definition := range definitions {
		approved = append(approved, api.CLIApproval{DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, Command: definition.Source.Command, Execution: definition.Execution})
	}
	// The skillgit plan is admitted as exact tuples — one per git host the
	// render knows — so git never reaches UserCommands, where an owner spec
	// could run it without the hardened argv the gitsource runner emits.
	for _, host := range gitHosts {
		approved = append(approved, api.CLIApproval{DefinitionID: "skillgit", DefinitionVersion: "1.0.0", Command: "git",
			Execution: api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 256 << 10, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64, Egress: []string{host}}})
	}
	return api.CLIControllerConfig{
		AllowPackageInstall: len(s.CLIRegistries) > 0,
		// The rendered tag resolves to the immutable image ID at controller
		// start; cells are created from that content reference, never the
		// moving tag.
		ToolsImage:    "hermes-hub-cli-tools:0.4.0-" + s.Environment,
		CellInit:      "/usr/local/bin/cellinit",
		ProxyImage:    "hermes-hub-cell-proxy:0.4.0-" + s.Environment,
		AllowedEgress: egress,
		Ceiling:       api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: egress},
		Approved:      approved,
		// /spaces is the read-only view of the spaces root the controller
		// container carries; Environment selects the managed/<env> workspace
		// layout used by managed principals.
		SpacesRoot:      "/spaces",
		Environment:     s.Environment,
		Runtime:         s.CLIRuntime,
		PerPrincipalUID: s.CLIPerPrincipalUID,
		Toolboxes:       maps.Clone(s.CLIToolboxes),
		// git stays out of owner-registered specs: .git/hooks inside a
		// writable workspace would execute arbitrary code in the cell.
		// rg cannot spawn programs once --pre/--pre-glob are denied at
		// registration. bash is admitted deliberately: a `bash -c` cell is
		// the shell surface for spaces where the terminal is policy-routed
		// through ToolHub — the cell's mounts/network are still the boundary.
		UserCommands:      []string{"rg", "bash"},
		WarmMaxAgeSeconds: 24 * 3600,
		WarmMaxReuse:      256,
		PoolSize:          2,
	}
}

var cliRegistryHosts = map[string][]string{
	"pypi":  {"pypi.org", "files.pythonhosted.org"},
	"npm":   {"registry.npmjs.org"},
	"cargo": {"index.crates.io", "static.crates.io", "crates.io"},
	"go":    {"proxy.golang.org", "sum.golang.org", "storage.googleapis.com"},
}
