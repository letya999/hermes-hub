package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"path/filepath"
	"slices"
)

func Config(s Settings) M {
	if s.CapabilityMode == "managed" {
		return managedHermesConfig(s)
	}
	organizationSkills := s.OrganizationSkillsDir
	if organizationSkills == "" && s.OrganizationDir != "" {
		organizationSkills = filepath.Join(s.OrganizationDir, "hermes", "skills")
	}
	servers := M{}
	// The hub executor server is the hub:tools section: governance can cut it
	// without touching the filesystem contract (the snapshot denies calls too).
	if s.govAllows(api.SectionHubTools) && (s.Has("workspace") || s.Has("hh") || s.Has("ssh")) {
		e := env("HH_TOKEN", "HH_USER_AGENT")
		e["HUB_HH_ENABLED"] = fmt.Sprint(s.Has("hh"))
		e["HUB_ORG_ACTIONS"] = "${HUB_ORG_ACTIONS}"
		e["HUB_SELF_ENV_KEYS"] = "${HUB_SELF_ENV_KEYS}"
		e["HUB_PROTECTED_ENV_KEYS"] = "${HUB_PROTECTED_ENV_KEYS}"
		e["HUB_STATE"] = "/state"
		for key, value := range hubCommunicationEnv() {
			e[key] = value
		}
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
		}
		if s.Has("hosting") {
			e["HUB_HOSTING_URL"] = "http://" + hostingName(s) + ":8182"
			e["HUB_HOSTING_TOKEN_FILE"] = "/run/hosting.key"
		}
		if s.Has("exposure") {
			e["HUB_EXPOSURE_CONFIG"] = "/state/exposure/config.yaml"
		}
		if s.Has("ssh") || s.Has("exposure") {
			// The runtime is already a provisioned broker adapter; the tools
			// process acquires/materializes capability keys through the same
			// identity — SSH host keys and exposure tunnel credentials alike.
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
		if s.govAllows(api.SectionHubBrowser) {
			servers["browser"] = browserServer(false, s.Has("browser_act"))
		}
		if s.govAllows("hub:browser_guest") {
			servers["browser_guest"] = browserServer(true, s.Has("browser_act"))
		}
	}
	// Connectors run behind the ToolHub admission boundary; only via: mcp-raw
	// selections render raw mcp: definitions directly into mcp_servers.
	for name, cfg := range rawMCPServers(s) {
		servers[name] = cfg
	}
	// vision and image_gen stay off this list. The hub MCP tools are the
	// workspace-confined profile; the native local tools are not.
	toolsets := []string{"terminal", "file", "web", "skills", "todo", "cronjob", "messaging", "memory", "session_search"}
	for name, entry := range s.Tools {
		mapped := name
		if name == "meet" {
			mapped = "google_meet"
		}
		switch entry.Via {
		case ToolViaNative:
			if !slices.Contains(toolsets, mapped) {
				toolsets = append(toolsets, mapped)
			}
		case ToolViaOff, ToolViaToolHub:
			toolsets = slices.DeleteFunc(toolsets, func(t string) bool { return t == mapped })
		}
	}
	if s.toolEntry("terminal").Via == ToolViaToolHub {
		toolsets = slices.DeleteFunc(toolsets, func(t string) bool { return t == "code_execution" })
	}
	if !s.Has("web") {
		// No web toolset at all: web_search/web_extract do not exist for this
		// space, so disabled web cannot be reached by prompting either.
		toolsets = slices.DeleteFunc(toolsets, func(name string) bool { return name == "web" })
	}
	if s.ExecutionMode == "supervisor" {
		toolsets = slices.DeleteFunc(toolsets, func(name string) bool { return name == "cronjob" })
	}
	// Tool governance: every emitted toolset is its native:<name> section;
	// denied surfaces are filtered at emission, never merely hidden.
	toolsets = slices.DeleteFunc(toolsets, func(name string) bool {
		entry := name
		if name == "google_meet" {
			entry = "meet"
		}
		return !s.govAllows("native:" + entry)
	})
	slices.Sort(toolsets)
	skills := M{}
	external := []string{}
	if organizationSkills != "" {
		external = append(external, "/org/hermes/skills")
	}
	external = append(external, "/opt/hub/skills")
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
	memory := M{"memory_enabled": s.Memory || s.toolEntry("memory").enabled(), "user_profile_enabled": s.Memory || s.toolEntry("memory").enabled()}
	// Keep long-running connector work alive when a user sends a follow-up. Hermes'
	// default interrupt mode cancels the active MCP call; queue mode preserves FIFO
	// turns and lets the existing heartbeat notify the user while a build runs.
	display := M{"busy_input_mode": "queue", "long_running_notifications": true}
	// platform_toolsets must name every platform upstream can serve: a platform
	// missing here falls back to its full-access composite (hermes-api-server,
	// hermes-cron) and re-emits terminal/exec tools the space denied — the
	// api_server surface is what the gateway actually serves to callers, so it
	// gets the same reviewed list as cli/telegram while unserved platforms stay
	// empty (fail-closed). The pinned managed inventory supplies the platform
	// universe.
	inventory, err := ManagedCapabilityInventory()
	if err != nil {
		panic(err) // Corrupt compiled inventory must never render a permissive config.
	}
	platforms := M{}
	for _, platform := range inventory.Platforms {
		platforms[platform] = []string{}
	}
	for _, platform := range []string{"cli", "local", "telegram", "api_server", "cron"} {
		platforms[platform] = toolsets
	}
	// agent.disabled_toolsets is applied last at tool granularity on EVERY
	// platform — the net that still catches platforms a future upstream adds
	// and composite fallbacks. Only leaf toolsets may be subtracted:
	// composites (debugging, safe, yuanbao, search) resolve to shared core
	// tools and would strip surfaces the space legitimately enabled.
	deniedToolsets := make([]string, 0, len(inventory.DisabledToolsets))
	for _, name := range inventory.DisabledToolsets {
		if platformDenylistComposite[name] || slices.Contains(toolsets, name) {
			continue
		}
		deniedToolsets = append(deniedToolsets, name)
	}
	cfg := M{"model": M{"default": s.Model, "provider": "custom", "base_url": s.ModelURL, "api_key": "${OPENAI_API_KEY}"}, "terminal": M{"backend": "local", "cwd": "/workspace", "timeout": 120}, "timeouts": M{"tools": M{"sequential_call": 1800, "concurrent_batch": 1800}}, "platform_toolsets": platforms, "agent": M{"disabled_toolsets": deniedToolsets}, "plugins": M{"enabled": []string{}}, "mcp_servers": servers, "skills": skills, "display": display, "stt": M{"enabled": s.Has("transcription"), "provider": "local", "language": "", "local": M{"model": "small"}}, "timezone": s.Timezone, "hooks": s.Hooks, "memory": memory}
	cfg["auxiliary"] = M{"vision": M{"provider": "main", "timeout": 30, "download_timeout": 15, "max_concurrency": media.InspectConcurrency}}
	if s.Has("web") {
		if web := s.Web.config(); len(web) > 0 {
			cfg["web"] = web
		}
	}
	if s.Has("image_gen") {
		if gen, err := s.ImageGen.Normalize(); err == nil {
			cfg["image_gen"] = M{"provider": gen.Provider, "model": gen.Model, "delivery": gen.Delivery}
		}
	}
	return cfg
}

// PolicyVersion identifies the effective runtime policy used by transport and supervisor.
func PolicyVersion(s Settings) string { return policyVersion(s) }

func policyVersion(s Settings) string {
	// Only rights-bearing settings participate (SPEC-0053 R1.1): model, ports,
	// timezone, environment and transport URLs are deployment state and must
	// not bounce bindings.
	body, _ := json.Marshal(rightsProjection(s))
	hash := sha256.Sum256(body)
	return "policy-" + hex.EncodeToString(hash[:8])
}
func Render(dir, root string) error { return RenderEnvironment(dir, root, "prod") }
