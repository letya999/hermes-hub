package stack

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/exposure"
	"github.com/letya999/hermes-hub/internal/sshcap"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
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
	if s.CapabilityMode == "managed" && s.ExecutionMode != "supervisor" {
		return fmt.Errorf("managed runtime requires supervisor-executed runtimes; static managed launch is not proven")
	}
	if s.Has("ssh") {
		// Fail render on a broken capability config rather than shipping a
		// runtime whose ssh tools would all deny.
		if _, err = sshcap.Load(filepath.Join(dir, "connections", "ssh", "config.yaml")); err != nil {
			return fmt.Errorf("ssh config: %w", err)
		}
	}
	if s.Has("exposure") {
		// Same fail-closed contract: a broken exposure config must stop the
		// render rather than ship silently-denying exposure tools.
		if _, err = exposure.Load(filepath.Join(dir, "connections", "exposure", "config.yaml")); err != nil {
			return fmt.Errorf("exposure config: %w", err)
		}
	}
	if s.CapabilityMode != "managed" && strings.TrimSpace(s.GlobalSkillsDir) == "" {
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
	for _, name := range []string{"archive", "runtime", "runtime/artifacts", "runtime/toolhub", "runtime/credentials", "runtime/materialized", "broker", "broker/keys", "broker/tls", "cliproxy", "hermes", "hermes/memories", "hermes/skills", "connections", "connections/google", "connections/telegram", "connections/browser", "connections/ssh/keys", "home", "cache", "workspace", "workspace/browser", "workspace/artifacts", "workspace/artifacts/documents", "workspace/artifacts/images", "skills"} {
		if err = os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			return err
		}
	}
	if s.CapabilityMode == "managed" {
		for _, name := range []string{"runtime", "hermes", "home", "cache", "workspace"} {
			if err = os.MkdirAll(filepath.Join(dir, "managed", s.Environment, name), 0700); err != nil {
				return err
			}
		}
	}
	if s.RendersInfra() && (s.DiagnosticsEnabled() || (s.ExecutionMode == "supervisor" && s.SupervisorURL == "http://supervisor:8876")) {
		// Per-user diagnostics live under the space; the repo-level .local only
		// hosts the shared host-supervisor log, bind-mounted read-only so a
		// missing file can never materialize as a directory inside the stack.
		if err := os.MkdirAll(filepath.Join(dir, "diagnostics"), 0700); err != nil {
			return err
		}
		localDir := filepath.Join(root, ".local")
		if err := os.MkdirAll(localDir, 0700); err != nil {
			return err
		}
		hostLog, err := os.OpenFile(filepath.Join(localDir, "supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err := hostLog.Close(); err != nil {
			return err
		}
	}
	if err = writeHonchoConfig(dir, s); err != nil {
		return err
	}
	if err = ensureRuntimeAuth(filepath.Join(dir, "runtime.auth")); err != nil {
		return err
	}
	if s.CapabilityMode == "managed" {
		if err = writeModelRegistry(dir, s); err != nil {
			return err
		}
		if err = ensureModelRelayAuth(filepath.Join(dir, "model-relay.auth")); err != nil {
			return err
		}
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
			runtimeExtra["HUB_MEDIA_URL"] = "http://hub-media:8090"
			runtimeExtra["HUB_MEDIA_AUTH"] = strings.TrimSpace(auth["HUB_MEDIA_AUTH"])
		}
	}
	if err = writeRuntimeEnvFiles(dir, s.Environment, secrets, orgSecrets, omitted, runtimeExtra); err != nil {
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
		// Spawned runtimes live on a route-less internal network: every
		// reviewed outbound hop goes through the egress relay by proxy env.
		// docker exec children inherit the container env, so the agenttools
		// executor sees the same relay. NO_PROXY keeps the control relays
		// direct — proxying them would loop back through egress anyway.
		if len(managedEgressHosts(s, dir)) > 0 {
			// Uppercase only: env files validate keys as [A-Z0-9_]* and every
			// consumer (Go http.DefaultTransport, requests, httpx) honors it.
			managedEnv["HTTP_PROXY"] = "http://egress-relay:8319"
			managedEnv["HTTPS_PROXY"] = "http://egress-relay:8319"
			managedEnv["NO_PROXY"] = "toolhub,model-relay,egress-relay,localhost,127.0.0.1"
			managedEnv["HUB_EGRESS_PROXY"] = "http://egress-relay:8319"
			for _, key := range s.Web.envKeys() {
				if value := secrets[key]; value != "" {
					managedEnv[key] = value
				}
			}
		}
		if slices.Contains(managedEgressHosts(s, dir), "hub-media") {
			auth, err := runtimeMediaAuth(dir, s)
			if err != nil {
				return err
			}
			if strings.TrimSpace(auth["HUB_MEDIA_AUTH"]) != "" {
				managedEnv["HUB_MEDIA_URL"] = "http://hub-media:8090"
				managedEnv["HUB_MEDIA_AUTH"] = strings.TrimSpace(auth["HUB_MEDIA_AUTH"])
			}
		}
		if slices.Contains(managedEgressHosts(s, dir), "communication-hub") {
			managedEnv["HUB_COMMUNICATION_CONTROL_URL"] = "http://communication-hub:8081"
			managedEnv["HUB_COMMUNICATION_AUTH"] = runtimeAuthToken(filepath.Join(dir, "runtime.auth"))
		}
		if err = writeEnvFile(filepath.Join(dir, "managed-runtime."+s.Environment+".env"), managedEnv); err != nil {
			return err
		}
	}
	if err = writeToolHubFiles(dir, s); err != nil {
		return err
	}
	if err = writeBrowserFiles(dir, s); err != nil {
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
	if err = MaterializeSoulEffective(dir, s); err != nil {
		return err
	}
	for _, p := range []string{"hermes." + environment + ".yaml", "SOUL.md", filepath.Join("generated", "soul-effective."+environment+".md")} {
		if err = os.Chmod(filepath.Join(dir, p), 0644); err != nil {
			return err
		}
	}
	if err = os.Chmod(filepath.Join(dir, "archive"), 0755); err != nil {
		return err
	}
	return nil
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
		hosts["hub-media"] = true
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

// writeToolHubFiles provisions the controller config, its protected admission
// token and the toolhub env file consumed by the generated compose stack.
// The key is generated once and never overwritten.
func writeToolHubFiles(dir string, s Settings) error {
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
	controller := M{"state_root": "/state", "toolhive_binary": "/usr/local/bin/thv", "seccomp_profile": "/opt/hub/seccomp/seccomp-mcp-runtime.json", "docker_fallback": true, "bridge_binary": "/usr/local/bin/hubctl", "credential_mount_root": "/run/broker-materialized", "dynamic_definitions": true, "max_active": 8, "idle_ttl_seconds": 1800, "cli": cliControllerSection(s, dir)}

	if err := writeHostingFiles(dir, s); err != nil {
		return err
	}
	body, err := json.MarshalIndent(controller, "", "  ")
	if err != nil {
		return err
	}
	if err = atomic(filepath.Join(runtimeDir, "generic-controller.json"), append(body, '\n')); err != nil {
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
	return os.Rename(name, path) // #nosec G703 -- temp file renamed onto a rendered path under the owned space dir
}
