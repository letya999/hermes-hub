package stack

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/skillstore"
	"github.com/letya999/hermes-hub/internal/vault"
	"gopkg.in/yaml.v3"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

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
	keys := append(append([]string{}, GatewaySecretKeys()...), ModelRelaySecretKeys()...)
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
func RenderEnvironment(dir, root, environment string) error {
	return renderEnvironment(dir, root, environment, "")
}

// RenderRevisionEnvironment prepares generated outputs from a staged bundle.
// The active read path remains gated until the publisher commits current.
func RenderRevisionEnvironment(dir, root, environment, revisionID string) error {
	return renderEnvironment(dir, root, environment, revisionID)
}

func renderEnvironment(dir, root, environment, revisionID string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	var s Settings
	if revisionID == "" {
		s, err = ReadEffective(dir, environment)
	} else {
		s, err = ReadEffectiveRevision(dir, environment, revisionID)
	}
	if err != nil {
		return err
	}
	if s.CapabilityMode == "managed" && s.ExecutionMode != "supervisor" {
		return fmt.Errorf("managed runtime requires supervisor-executed runtimes; static managed launch is not proven")
	}
	if err := ValidateRuntimeConnections(s, dir); err != nil {
		return err
	}
	if strings.TrimSpace(s.GlobalSkillsDir) == "" {
		s.DefaultGlobalSkillsDir(filepath.Join(root, "config", "skills"))
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
	for _, name := range []string{"archive", "runtime", "runtime/artifacts", "runtime/toolhub", "runtime/credentials", "runtime/materialized", "broker", "broker/keys", "broker/tls", "cliproxy", "hermes", "hermes/memories", "hermes/skills", "connections", "connections/google", "connections/telegram", "connections/browser", "connections/ssh/keys", "home", "cache", "workspace", "workspace/browser", "workspace/artifacts", "workspace/artifacts/documents", "workspace/artifacts/images", "workspace/artifacts/videos", "workspace/artifacts/archives", "workspace/artifacts/services", "workspace/inbox", "workspace/inbox/people", "workspace/inbox/services", "workspace/projects", "workspace/work", "workspace/outbox", "skills"} {
		if err = os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			return err
		}
	}
	ownerAutomation := filepath.Join(dir, "automation", s.Environment)
	if err := os.MkdirAll(ownerAutomation, 0770); err != nil {
		return err
	}
	if info, err := os.Lstat(ownerAutomation); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe automation owner directory")
	}
	if os.Getgid() >= 0 {
		if err := os.Chown(ownerAutomation, -1, os.Getgid()); err != nil {
			return err
		}
	}
	if err := os.Chmod(ownerAutomation, 0770); err != nil {
		return err
	}
	if s.RendersInfra() {
		if err := renderAutomationRouting(dir, s, secrets); err != nil {
			return err
		}
	}
	if s.Has("memory_vault") {
		// The git-backed vault is materialized with the rest of the space
		// tree; Ensure is idempotent so re-renders never disturb content.
		if err = vault.Ensure(filepath.Join(dir, "vault")); err != nil {
			return err
		}
	}
	// SPEC-0085 R11: the external provider record renders or revokes with
	// the capability — a stale file can never drive a later replay.
	if err = renderProviderConfig(dir, s); err != nil {
		return err
	}
	// SPEC-0083 R3.3: upstream must never seed bundled skills into the
	// runtime home — the hub-built effective view at /skills is the only
	// skill path. The marker suppresses upstream seeding beyond its
	// hardcoded essentials; the /skills mount and the disabled `skills`
	// toolset close the rest.
	if err = os.WriteFile(filepath.Join(dir, "hermes", ".no-bundled-skills"), []byte("# hub-managed: upstream bundled-skill seeding stays off (SPEC-0083 R3.3)\n"), 0600); err != nil {
		return err
	}
	// Pre-M24 spaces keep authored skills under hermes/skills; the unified
	// store at skills/ is authoritative now. Idempotent: consented registry
	// entries publish, unrecorded content lands as drafts (SPEC-0083 R2.7).
	if _, err = skillstore.MigrateLegacySpaceWithPlatform(dir, s.GlobalSkillsDir, true); err != nil {
		return fmt.Errorf("legacy skills migration: %w", err)
	}
	// Same move for the organization tier: legacy <org>/hermes/skills content
	// lands in the unified <org>/skills store (SPEC-0083 R2.7).
	if s.OrgScoped() && s.OrganizationDir != "" {
		if _, err = skillstore.MigrateLegacyOrg(s.OrganizationDir, true); err != nil {
			return fmt.Errorf("organization skills migration: %w", err)
		}
	}
	if s.CapabilityMode == "managed" {
		for _, name := range []string{"runtime", "hermes", "home", "cache", "workspace", "skills"} {
			if err = os.MkdirAll(filepath.Join(dir, "managed", s.Environment, name), 0700); err != nil {
				return err
			}
		}
	}
	// A published workspace passport declares the layout: Render creates
	// every boundary directory the MountPlan binds — including the /vault
	// skeleton — without writing content into them (SPEC-0092 R5.2).
	if s.PassportWorkspace != nil {
		bases := []string{dir}
		if s.CapabilityMode == "managed" {
			bases = append(bases, filepath.Join(dir, "managed", s.Environment))
		}
		for _, base := range bases {
			if err = MaterializeWorkspaceLayout(base, *s.PassportWorkspace); err != nil {
				return err
			}
		}
	}
	if s.RendersInfra() && (s.DiagnosticsEnabled() || (s.ExecutionMode == "supervisor" && s.SupervisorURL == "http://supervisor:8876")) {
		// Dedicated metadata-only directories survive rotation without exposing
		// neighboring auth/control files. The service uid shares the host gid.
		for _, logDir := range []string{filepath.Join(dir, "diagnostics"), filepath.Join(root, ".local", "diagnostics")} {
			if err := os.MkdirAll(logDir, 0770); err != nil {
				return err
			}
			info, err := os.Lstat(logDir)
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("unsafe diagnostic directory")
			}
			if err := os.Chmod(logDir, 0770); err != nil {
				return err
			}
		}
	}
	if err = writeHonchoConfig(dir, s); err != nil {
		return err
	}
	if err = ensureRuntimeAuth(filepath.Join(dir, "runtime.auth")); err != nil {
		return err
	}
	if err = allowServiceGroupRead(filepath.Join(dir, "runtime.auth")); err != nil {
		return err
	}
	if s.CapabilityMode == "managed" {
		if err = writeModelRegistryRevision(dir, s, revisionID); err != nil {
			return err
		}
		if err = ensureModelRelayAuth(filepath.Join(dir, "model-relay.auth")); err != nil {
			return err
		}
		if err = writeModelRelayCallerAuth(dir); err != nil {
			return err
		}
	}
	if voiceServicesEnabled(s) || s.Has("image_gen") {
		if err = ensureMediaAuth(filepath.Join(dir, "media.auth")); err != nil {
			return err
		}
	}
	if s.Has("image_gen") && s.PassportServices["hub-media"] == "per-space" {
		if s.CapabilityMode != "managed" {
			return fmt.Errorf("per-space hub-media requires a managed model relay")
		}
		relayAuth, readErr := ReadSecrets(filepath.Join(dir, "model-relay.auth"))
		if readErr != nil || strings.TrimSpace(relayAuth["HUB_MODEL_RELAY_AUTH"]) == "" {
			return fmt.Errorf("per-space hub-media model relay auth is unavailable")
		}
		path := filepath.Join(dir, "media-relay.auth")
		if err = writeEnvFile(path, map[string]string{"HUB_MEDIA_UPSTREAM_KEY": relayAuth["HUB_MODEL_RELAY_AUTH"]}); err != nil {
			return err
		}
		if err = allowServiceGroupRead(path); err != nil {
			return err
		}
	}
	// Provider keys moved behind the credential broker: a media.<env>.env
	// rendered by an older version must not linger with plaintext secrets.
	if err = removeIfExists(filepath.Join(dir, "media."+environment+".env")); err != nil {
		return err
	}
	if s.RendersInfra() {
		if err = enrollSiblingRuntimeTokens(dir, environment, s); err != nil {
			return err
		}
		if err = EnrollSiblingModelRelayAdmins(dir, environment); err != nil {
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
		auth, err := runtimeMediaAuth(dir, s)
		if err != nil {
			return err
		}
		if strings.TrimSpace(auth["HUB_MEDIA_AUTH"]) != "" {
			runtimeExtra["HUB_MEDIA_URL"] = "http://" + mediaServiceName(s) + ":8090"
			runtimeExtra["HUB_MEDIA_AUTH"] = strings.TrimSpace(auth["HUB_MEDIA_AUTH"])
		}
	}
	if err = writeRuntimeEnvFiles(dir, s.Environment, secrets, orgSecrets, omitted, runtimeExtra); err != nil {
		return err
	}
	if err = allowServiceGroupRead(filepath.Join(dir, "runtime."+s.Environment+".env")); err != nil {
		return err
	}
	if err = writeSupervisorPolicy(dir, s); err != nil {
		return err
	}
	if s.CapabilityMode == "managed" {
		relayAuth, err := ReadSecrets(filepath.Join(dir, "model-relay.auth"))
		if err != nil {
			return err
		}
		managedEnv := map[string]string{
			"OPENAI_API_KEY": relayAuth["HUB_MODEL_RELAY_AUTH"], "HUB_TOOLHUB_ENDPOINT": "http://toolhub:8090/mcp",
		}
		if layer, loadErr := passportRegistryLayer(dir, revisionID); loadErr == nil {
			if _, ok := layer.Models["vision"]; ok {
				managedEnv["HUB_MEDIA_VISION_MODEL"] = "vision"
			}
		} else {
			return loadErr
		}
		// Spawned runtimes live on a route-less internal network: every
		// reviewed outbound hop goes through the egress relay by proxy env.
		// docker exec children inherit the container env, so the agenttools
		// executor sees the same relay. NO_PROXY keeps the control relays
		// direct — proxying them would loop back through egress anyway.
		if len(managedEgressHosts(s, dir)) > 0 || managedExposureRelay(s, dir) != "" {
			// Uppercase only: env files validate keys as [A-Z0-9_]* and every
			// consumer (Go http.DefaultTransport, requests, httpx) honors it.
			managedEnv["HTTP_PROXY"] = "http://egress-relay:8319"
			managedEnv["HTTPS_PROXY"] = "http://egress-relay:8319"
			managedEnv["NO_PROXY"] = "toolhub,model-relay,egress-relay,localhost,127.0.0.1"
			if s.Has("browser") {
				// The browser MCP sidecar is internal like the relays:
				// proxying it through egress breaks the streamable-HTTP
				// handshake (the relay rightly refuses arbitrary forwards).
				managedEnv["NO_PROXY"] += ",browser-" + s.User
			}
			managedEnv["HUB_EGRESS_PROXY"] = "http://egress-relay:8319"
			for _, key := range s.Web.envKeys() {
				if value := secrets[key]; value != "" {
					managedEnv[key] = value
				}
			}
		}
		if slices.Contains(managedEgressHosts(s, dir), mediaServiceName(s)) {
			auth, err := runtimeMediaAuth(dir, s)
			if err != nil {
				return err
			}
			if strings.TrimSpace(auth["HUB_MEDIA_AUTH"]) != "" {
				managedEnv["HUB_MEDIA_URL"] = "http://" + mediaServiceName(s) + ":8090"
				managedEnv["HUB_MEDIA_AUTH"] = strings.TrimSpace(auth["HUB_MEDIA_AUTH"])
				if _, declared := s.PassportServices["hub-stt"]; declared {
					managedEnv["HUB_STT_URL"] = "http://hub-stt:8090"
				}
			}
		}
		if slices.Contains(managedEgressHosts(s, dir), "communication-hub") {
			managedEnv["HUB_COMMUNICATION_CONTROL_URL"] = "http://communication-hub:8081"
			managedEnv["HUB_COMMUNICATION_AUTH"] = runtimeAuthToken(filepath.Join(dir, "runtime.auth"))
		}
		if err = writeEnvFile(filepath.Join(dir, "managed-runtime."+s.Environment+".env"), managedEnv); err != nil {
			return err
		}
		if err = allowServiceGroupRead(filepath.Join(dir, "managed-runtime."+s.Environment+".env")); err != nil {
			return err
		}
	}
	if err = writeToolHubFiles(dir, s); err != nil {
		return err
	}
	if err = writeBrowserFiles(dir, s); err != nil {
		return err
	}
	composeRoot, composeDir := composeHostPaths(root, dir, os.Getenv("HUB_SUPERVISOR_HOST_SPACES"))
	compose, err := ComposeChecked(s, composeRoot, composeDir)
	if err != nil {
		return err
	}
	outputs := map[string]any{"hermes." + environment + ".yaml": Config(s), "compose." + environment + ".yaml": compose}
	if shared, user := SplitInfraCompose(s, compose); user != nil {
		outputs["compose.shared."+environment+".yaml"] = shared
		outputs["compose.user."+environment+".yaml"] = user
	} else if s.RendersInfra() {
		// A shared-only owner still deploys under the standalone shared
		// project name — the union compose file is that plan.
		outputs["compose."+environment+".yaml"] = shared
	}
	// Generated files are reproducible; secrets and agent memories are never overwritten.
	for name, v := range outputs {
		b, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		if err = atomic(filepath.Join(dir, "generated", name), b); err != nil {
			return err
		}
		// The retired root mirror is no longer written or read; a stale copy
		// must never resurrect when generated/ is absent.
		if err = os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err = materializeHermesConfig(dir, s); err != nil {
		return err
	}
	// The executor's governance extract is part of the same render contract:
	// a stale or absent snapshot must never widen what the config emits.
	if err = writeToolPolicySnapshot(dir, s); err != nil {
		return err
	}
	if err = migrateToolhubBindings(dir, s); err != nil {
		return err
	}
	if err = writeToolManifest(dir, s); err != nil {
		return err
	}
	if _, err = MaterializeEffectiveSkills(dir, s); err != nil {
		return err
	}
	if err = MaterializeHubPlugins(dir, root, s); err != nil {
		return err
	}
	if err = shareGeneratedRuntimeViews(dir); err != nil {
		return err
	}
	// Init seeds only a stub; heal it to the global template. Real user edits
	// differ from the stub and are never overwritten.
	if err = HealUserSoul(dir, filepath.Join(root, "config", "SOUL.md")); err != nil {
		return err
	}
	if err = MaterializeSoulEffective(dir, s); err != nil {
		return err
	}
	for _, p := range []string{filepath.Join("generated", "hermes."+environment+".yaml"), "SOUL.md", filepath.Join("generated", "soul-effective."+environment+".md")} {
		if err = os.Chmod(filepath.Join(dir, p), 0644); err != nil {
			return err
		}
	}
	if err = os.Chmod(filepath.Join(dir, "archive"), 0755); err != nil {
		return err
	}
	return nil
}

// The supervisor rebuilds these generated views as uid 10001 with the
// renderer's primary group; keep all other generated outputs untouched.
func shareGeneratedRuntimeViews(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	generated := filepath.Join(dir, "generated")
	if info, err := os.Lstat(generated); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe generated directory")
	}
	if err := os.Chown(generated, -1, os.Getgid()); err != nil {
		return err
	}
	if err := os.Chmod(generated, 0775); err != nil {
		return err
	}
	for _, root := range []string{GeneratedSkillsDir(dir), GeneratedEffectiveSkillsDir(dir), GeneratedSkillsMetaDir(dir), GeneratedPluginsDir(dir)} {
		if root == GeneratedPluginsDir(dir) {
			if _, err := os.Lstat(root); os.IsNotExist(err) {
				continue
			}
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("generated skill view contains symlink")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if err := os.Chown(path, -1, os.Getgid()); err != nil { // #nosec G122 -- path is a walk entry under the generated view we just symlink-checked.
				return err
			}
			mode := info.Mode().Perm() | 0060
			if entry.IsDir() {
				mode |= 0010
			} else if info.Mode().Perm()&0100 != 0 {
				mode |= 0010
			}
			return os.Chmod(path, mode) // #nosec G122 -- same walk-entry path under the generated view.
		}); err != nil {
			return err
		}
	}
	return nil
}

func composeHostPaths(root, dir, hostSpaces string) (string, string) {
	if filepath.ToSlash(root) != "/" || path.Dir(filepath.ToSlash(dir)) != "/spaces" || hostSpaces == "" {
		return root, dir
	}
	hostSpaces = strings.TrimRight(strings.ReplaceAll(hostSpaces, `\`, "/"), "/")
	return path.Dir(hostSpaces), hostSpaces + "/" + path.Base(filepath.ToSlash(dir))
}

// writeBrowserFiles provisions the per-space browser secrets the sidecar
// mounts read-only: the noVNC password (x11vnc -passwdfile) and, for meet
// spaces, the token upstream's NodeServer serves plus the runtime-side node
// registry entry — generated once, never rotated by a re-render.
func writeBrowserFiles(dir string, s Settings) error {
	if !s.Has("browser") && !s.Has("meet") {
		return nil
	}
	if err := ensureTokenFile(filepath.Join(dir, "browser-novnc.passwd")); err != nil {
		return err
	}
	if !s.Has("meet") {
		return nil
	}
	tokenPath := filepath.Join(dir, "meet-node-token.json")
	if _, err := os.Lstat(tokenPath); os.IsNotExist(err) {
		value := make([]byte, 32)
		if _, err = rand.Read(value); err != nil {
			return err
		}
		body, err := json.Marshal(M{"token": hex.EncodeToString(value)})
		if err != nil {
			return err
		}
		if err = os.WriteFile(tokenPath, body, 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	tokenData, err := os.ReadFile(tokenPath)
	if err != nil {
		return err
	}
	var tokenDoc struct {
		Token string `json:"token"`
	}
	if err = json.Unmarshal(tokenData, &tokenDoc); err != nil || tokenDoc.Token == "" {
		return fmt.Errorf("meet node token file is unreadable")
	}
	// Seed the runtime-side registry: plugins.google_meet.node.registry
	// resolves node='auto' through $HERMES_HOME/workspace/meetings/nodes.json.
	// The sidecar's token file holds the same secret, so the entry is
	// pre-approved without the operator running `meet node approve`.
	nodesPath := filepath.Join(dir, "hermes", "workspace", "meetings", "nodes.json")
	name := "browser-" + s.User
	nodes := M{"nodes": M{name: M{"url": "ws://" + name + ":18789", "token": tokenDoc.Token}}}
	body, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(nodesPath), 0770); err != nil {
		return err
	}
	return atomic(nodesPath, append(body, '\n'))
}

// removeIfExists deletes path when present; a missing file is not an error.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) { // #nosec G703 -- internal render path
		return err
	}
	return nil
}

// managedEgressHosts computes the host allowlist the managed egress relay
// enforces: provider endpoints for carved-out native web toolsets plus the
// shared-network siblings admitted agenttools executors legitimately reach.
// Empty result means no egress relay is rendered at all.
func managedEgressHosts(s Settings, dir string) []string {
	if s.CapabilityMode != "managed" {
		return nil
	}
	hosts := map[string]bool{}
	webOn := s.Has("web")
	for _, name := range []string{"web", "search", "x_search"} {
		if entry := s.Tools[name]; entry.Via == ToolViaNative {
			webOn = true
		}
	}
	if webOn {
		secrets, _ := ReadSecrets(filepath.Join(dir, "secrets."+s.Environment+".env"))
		for _, host := range s.Web.egressHosts(secrets) {
			hosts[host] = true
		}
	}
	if s.Has("image_gen") {
		// Image generation always goes through the hub-media sidecar; the
		// runtime never holds provider keys.
		hosts[mediaServiceName(s)] = true
		if _, declared := s.PassportServices["hub-stt"]; declared {
			hosts["hub-stt"] = true
		}
	}
	if s.providerEnabled() {
		// SPEC-0085 R11.2: the provider endpoint is the reviewed egress hop
		// for provider-bound capture; revoking the capability drops the
		// host from the relay allowlist with the render.
		if host := providerEgressHost(s.MemoryProviderURL); host != "" {
			hosts[host] = true
		}
	}
	for name, entry := range s.Tools {
		if entry.Via != ToolViaToolHub {
			continue
		}
		switch name {
		case "routines", "services", "assignments":
			hosts["communication-hub"] = true
		case "hh":
			hosts["api.hh.ru"] = true
		}
	}
	out := make([]string, 0, len(hosts))
	for host := range hosts {
		out = append(out, host)
	}
	slices.Sort(out)
	return out
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
	runtime["HUB_COMMUNICATION_CONTROL_URL"] = "http://communication-hub:8081"
	gateway := map[string]string{}
	for _, key := range GatewaySecretKeys() {
		if value := user[key]; value != "" {
			gateway[key] = value
		}
	}
	// The night rollup's model seam (SPEC-0085 R5.1) resolves the relay
	// through the env pair. Managed spaces get both rendered onto the
	// service; an unmanaged owner opts in through secrets — the keys stay
	// runtime-visible too since they are not gateway-owned secrets.
	for _, key := range []string{"HUB_MODEL_RELAY_URL", "HUB_MODEL_RELAY_AUTH", "HUB_MODEL_RELAY_ADMIN"} {
		if value := user[key]; value != "" {
			gateway[key] = value
		}
	}
	// The managed runtime authenticates control-plane calls (credential forms,
	// prepare outcomes, routine management) with its own HUB_RUNTIME_AUTH. In
	// the infra owner's space that token becomes the gateway's primary control
	// bearer; sibling-space tokens are admitted through toolhub-tokens.json.
	if token := runtimeAuthToken(filepath.Join(dir, "runtime.auth")); token != "" {
		gateway["HUB_COMMUNICATION_AUTH"] = token
	}
	relay := map[string]string{}
	for _, key := range ModelRelaySecretKeys() {
		if value := user[key]; value != "" {
			relay[key] = value
		}
	}
	if err := writeEnvFile(filepath.Join(dir, "runtime."+environment+".env"), runtime); err != nil {
		return err
	}
	if err := writeEnvFile(filepath.Join(dir, "model-relay."+environment+".env"), relay); err != nil {
		return err
	}
	return writeEnvFile(filepath.Join(dir, "communication."+environment+".env"), gateway)
}

// writeSupervisorPolicy renders the space's published config.supervision as
// runtime.<env>.policy.json beside the runtime env file (SPEC-0092 R4.2). The
// supervisor re-reads it per decision, so supervision changes apply live.
// An empty supervision section removes the file — the deployment defaults
// (WarmTTL, remediate-unhealthy) then govern.
func writeSupervisorPolicy(dir string, s Settings) error {
	path := filepath.Join(dir, "runtime."+s.Environment+".policy.json")
	type watcher struct {
		Protected bool   `json:"protected,omitempty"`
		Every     string `json:"every,omitempty"`
	}
	type policy struct {
		IdleStop string             `json:"idle_stop,omitempty"`
		Restart  string             `json:"restart,omitempty"`
		Watchers map[string]watcher `json:"watchers,omitempty"`
	}
	if s.PassportConfig == nil {
		return removeIfExists(path)
	}
	sup := s.PassportConfig.Supervision
	if strings.TrimSpace(sup.IdleStop) == "" && strings.TrimSpace(sup.Restart) == "" && len(sup.Watchers) == 0 {
		return removeIfExists(path)
	}
	out := policy{IdleStop: sup.IdleStop, Restart: sup.Restart}
	if len(sup.Watchers) > 0 {
		out.Watchers = make(map[string]watcher, len(sup.Watchers))
		for name, w := range sup.Watchers {
			out.Watchers[name] = watcher{Protected: w.Protected, Every: w.Every}
		}
	}
	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0600)
}

// writeToolHubFiles provisions the controller config, its protected admission
// token and the toolhub env file consumed by the generated compose stack.
// The key is generated once and never overwritten.
func writeToolHubFiles(dir string, s Settings) error {
	runtimeDir := filepath.Join(dir, "runtime")
	keyPath := filepath.Join(runtimeDir, "generic-controller.key")
	if err := ensureContainerTokenFile(keyPath); err != nil {
		return err
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(key))
	controller := M{"state_root": "/state", "toolhive_binary": "/usr/local/bin/thv", "seccomp_profile": "/opt/hub/seccomp/seccomp-mcp-runtime.json", "docker_fallback": true, "bridge_binary": "/usr/local/bin/hubctl", "credential_mount_root": "/run/broker-materialized", "dynamic_definitions": true, "max_active": 8, "idle_ttl_seconds": 1800, "cli": cliControllerSection(s, dir)}

	if err := writeHostingFiles(dir, s); err != nil {
		return err
	}
	body, err := json.MarshalIndent(controller, "", "  ")
	if err != nil {
		return err
	}
	if err = atomicMode(filepath.Join(runtimeDir, "generic-controller.json"), append(body, '\n'), 0644); err != nil {
		return err
	}
	return writeEnvFile(filepath.Join(dir, "toolhub.auth"), map[string]string{"HUB_TOOLHIVE_ADMISSION_TOKEN": token})
}

func writeHonchoConfig(dir string, s Settings) error {
	if !s.Honcho || strings.TrimSpace(s.HonchoURL) == "" {
		err := os.Remove(filepath.Join(dir, "hermes", "honcho.json"))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	body, err := json.Marshal(M{
		"baseUrl": s.HonchoURL,
		"hosts":   M{"hermes": M{"enabled": true, "aiPeer": "hermes", "peerName": s.User, "workspace": "hermes-" + s.User}},
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
	return atomicMode(path, b, 0600)
}

func atomicMode(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".render-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err != nil {
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
	return replaceRenderedFile(name, path)
}
