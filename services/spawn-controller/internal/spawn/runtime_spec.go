package spawn

// The managed-runtime spec builder (CHG-0336 S7c): the v0 supervisor's
// managedRunArgs / prepareSpawnFiles / materializeHermesConfig contract
// moved into the claim path — same mounts, same env wiring, same
// materialization order — emitting the backend.Spec the launch backends
// apply. The claim's stored effective bundle is the policy: nothing here
// re-reads the published file tree for settings.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	identityv1 "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/stack"
	thapi "github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/shared/store"
)

// ClaimRequest carries the caller's job binding into a managed-runtime
// claim (contract ClaimRuntimeRequest, mapped at the api edge): the
// routing values that ride into the workload's environment and the
// identity material the caller's template resolved to. A warm row is
// only reused when every bound key still matches.
type ClaimRequest struct {
	Agent            string
	RuntimeID        string
	RuntimeMode      string
	UserID           string
	OrganizationID   string
	PolicyVersion    string
	IdentityRevision string
	Restrictions     *identityv1.Restrictions
}

// managedAgentNetworkName mirrors the Compose topology: one internal
// network per user/environment where only the reviewed relays and runtime
// resolve.
func managedAgentNetworkName(user, env string) string {
	return "hermes-hub-agent-" + user + "-" + env
}

// controlNetworkName is the spawn-owned bridge carrying the per-runtime
// control relay's loopback-published port. It is not Compose-owned so a
// project teardown never strands a live runtime.
const controlNetworkName = "hermes-hub-control"

// managedExtensionRootTmpfs are the empty read-only overlays covering every
// executable import path under HERMES_HOME.
var managedExtensionRootTmpfs = []TmpfsEntry{
	{"/state/hermes/skills", "ro,mode=0555"}, {"/state/hermes/hooks", "ro,mode=0555"},
	{"/state/hermes/plugins", "ro,mode=0555"}, {"/state/hermes/skill-bundles", "ro,mode=0555"},
	{"/state/hermes/scripts", "ro,mode=0555"}, {"/state/hermes/bin", "ro,mode=0555"},
	{"/state/hermes/node", "ro,mode=0555"}, {"/state/hermes/lsp", "ro,mode=0555"},
}

// TmpfsEntry keeps the spec builder's tmpfs table terse; it maps to
// backend.Tmpfs at Spec assembly.
type TmpfsEntry struct{ Target, Options string }

// managedSpec assembles the backend.Spec for one managed-runtime claim:
// the stored effective bundle becomes settings, the caller's identity
// material narrows them, the materialize steps run before the backend is
// asked, and the returned spec carries the full v0 mount/env contract.
func (r *Runtimes) managedSpec(ctx context.Context, t store.Tenant, spaceID, id, digest string, req ClaimRequest, gen int64) (backend.Spec, error) {
	// No spaces root wired: the claim still binds tenant and digests on
	// the row, but there is no space tree to mount. The controller always
	// wires SpacesRoot; the minimal spec is the row-lifecycle path tests
	// and non-filesystem deployments take.
	if r.SpacesRoot == "" || r.Revisions == nil {
		return backend.Spec{
			Key: id, Org: t.Org, Owner: t.Owner, Space: spaceID, Agent: req.Agent,
			Image: r.Image, OCIRuntime: r.OCIRuntime,
		}, nil
	}
	dir := filepath.Join(r.SpacesRoot, spaceID)
	spec, err := r.Revisions.Effective(ctx, t, spaceID)
	if err != nil {
		return backend.Spec{}, err
	}
	space, err := r.Revisions.space(ctx, t, spaceID)
	if err != nil {
		return backend.Spec{}, err
	}
	eff, err := passportview.Assemble(spec.Documents)
	if err != nil {
		return backend.Spec{}, err
	}
	env := space.Environment
	settings, err := stack.SettingsForEffective(dir, eff, env)
	if err != nil {
		return backend.Spec{}, err
	}
	if req.Restrictions != nil {
		settings.IdentityRestrictions = req.Restrictions
		settings.IdentityRevision = req.IdentityRevision
	}
	if err := stack.ValidateRuntimeConnections(settings, dir); err != nil {
		return backend.Spec{}, err
	}
	// prepareSpawnFiles: heal an Init SOUL stub from the repo template and,
	// for the infra owner, enroll sibling runtime tokens before the
	// container mount is built. A skipped heal never blocks the spawn.
	template := filepath.Join(r.platformRoot(), "config", "SOUL.md")
	if info, terr := os.Stat(template); terr == nil && info.Mode().IsRegular() {
		if herr := stack.HealUserSoul(dir, template); herr != nil {
			log.Printf("spawn soul heal skipped")
		}
	}
	if settings.RendersInfra() {
		if err := stack.EnrollSiblingRuntimeTokens(dir, env); err != nil {
			log.Printf("spawn token enrollment skipped")
		}
	}
	settings.Environment = env
	managedDir := filepath.Join(dir, "managed", env)
	runtimeDir := filepath.Join(managedDir, "runtime")
	mode := req.RuntimeMode
	if mode == "" {
		mode = "gateway"
	}
	if mode != "gateway" {
		runtimeDir = filepath.Join(runtimeDir, mode)
	}
	for _, path := range []string{runtimeDir, filepath.Join(managedDir, "hermes"), filepath.Join(managedDir, "home"), filepath.Join(managedDir, "cache")} {
		if err := os.MkdirAll(path, 0o770); err != nil {
			return backend.Spec{}, fmt.Errorf("managed runtime state dir: %w", err)
		}
	}
	out := backend.Spec{
		Key:        id,
		Org:        t.Org,
		Owner:      t.Owner,
		Space:      spaceID,
		Agent:      req.Agent,
		Limits:     r.Limits,
		Image:      r.Image,
		Command:    []string{"serve"},
		OCIRuntime: r.OCIRuntime,
		Port:       r.RuntimePort,
		Networks:   []string{managedAgentNetworkName(settings.User, env)},
		EnvFiles: []string{
			filepath.Join(dir, "managed-runtime."+env+".env"),
			envFileFor(dir, spec),
		},
		Labels: map[string]string{"hermes-hub.generation": "gen-" + strconv.FormatInt(gen, 10)},
	}
	// The reviewed service environment rides as plain env pairs, sorted —
	// identical to the v0 render and the Compose contract.
	if service := stack.RuntimeService(settings, "", dir); service != nil {
		if envMap, ok := service["environment"].(stack.M); ok {
			keys := make([]string, 0, len(envMap))
			for key := range envMap {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				out.Env = append(out.Env, key+"="+fmt.Sprint(envMap[key]))
			}
		}
	}
	bind := func(source, target string, ro bool) {
		out.Mounts = append(out.Mounts, backend.Mount{Source: thapi.DockerBindSource(source), Target: target, ReadOnly: ro})
	}
	bind(runtimeDir, "/state", false)
	bind(filepath.Join(managedDir, "hermes"), "/state/hermes", false)
	bind(filepath.Join(managedDir, "home"), "/state/home", false)
	bind(filepath.Join(managedDir, "cache"), "/state/cache", false)
	// The workspace passport owns the container filesystem once a revision
	// is published (SPEC-0092 R5.2): /config, /workspace with read-only
	// sub-binds, /skills and /shared/* come from the MountPlan — identical
	// to the rendered Compose service. Without a revision the space keeps
	// the legacy workspace bind.
	if settings.PassportWorkspace != nil {
		for _, pm := range stack.MountPlan(dir, env, managedDir, settings, *settings.PassportWorkspace) {
			bind(pm.Source, pm.Target, pm.ReadOnly)
		}
	} else {
		bind(filepath.Join(managedDir, "workspace"), "/workspace", false)
	}
	if settings.Has("hosting") {
		bind(filepath.Join(dir, "runtime", "hosting."+env+".key"), "/run/hosting.key", true)
	}
	for _, connection := range stack.RuntimeConnections(settings, dir) {
		bind(connection.Source, connection.Target, true)
	}
	// Skill contract: the hub-built effective view mounts read-only at
	// /skills and a failed build refuses the spawn.
	if strings.TrimSpace(settings.GlobalSkillsDir) == "" {
		settings.DefaultGlobalSkillsDir(filepath.Join(r.platformRoot(), "config", "skills"))
		if err := os.MkdirAll(settings.GlobalSkillsDir, 0o755); err != nil {
			return backend.Spec{}, fmt.Errorf("platform skills dir: %w", err)
		}
	}
	if _, err := stack.MaterializeEffectiveSkills(dir, settings); err != nil {
		return backend.Spec{}, fmt.Errorf("effective skills view: %w", err)
	}
	// A published workspace passport carries /skills through the MountPlan.
	if settings.PassportWorkspace == nil {
		bind(stack.EffectiveSkillsDir(dir, settings), "/skills", true)
	}
	// Post-publish rebuild contract: the platform tier and the render
	// snapshot mount read-only; the view gets a second writable bind so
	// /v1/skill-apply can recompose it in place without a respawn.
	bind(stack.GeneratedSkillsDir(dir), "/skills-platform", true)
	bind(stack.EffectiveSkillsDir(dir, settings), "/skills-effective-rw", false)
	bind(stack.EffectiveSkillsMetaDir(dir, settings), "/skills-meta", true)
	// R5.1: the owner store (not a private managed copy) is writable at
	// /skills-store so drafts, publishes and the effective view all see
	// the same store.
	bind(filepath.Join(dir, "skills"), "/skills-store", false)
	out.Env = append(out.Env, "HUB_SKILLS_STORE=/skills-store", "HUB_SKILLS_EFFECTIVE=/skills")
	if settings.OrgScoped() && settings.OrganizationSkillsDir != "" {
		bind(settings.OrganizationSkillsDir, "/skills-store-org", false)
	}
	// Hub plugins: a feature-filtered copy materialized at spawn mounts
	// read-only into upstream's bundled plugins dir.
	if err := stack.MaterializeHubPlugins(dir, r.platformRoot(), settings); err != nil {
		log.Printf("spawn plugins materialization skipped: %v", err)
	}
	if entries, err := os.ReadDir(stack.GeneratedPluginsDir(dir)); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			bind(filepath.Join(stack.GeneratedPluginsDir(dir), entry.Name()), "/opt/hermes/plugins/"+entry.Name(), true)
		}
	}
	// materializeHermesConfig: the effective Hermes config renders on the
	// host so the runtime mounts it read-only. A missing, unreadable or
	// malformed runtime env refuses the spawn: an empty secret map would
	// drop the ToolHub endpoint and token reference silently.
	effectiveConfig, err := materializeManagedConfig(dir, settings, env)
	if err != nil {
		return backend.Spec{}, err
	}
	bind(effectiveConfig, "/state/hermes/config.yaml", true)
	if soul := stack.EffectiveSoulPath(dir, settings); fileExists(soul) {
		bind(soul, "/state/hermes/SOUL.md", true)
	}
	policyPath, err := stack.MaterializeToolPolicySnapshot(dir, settings)
	if err != nil {
		return backend.Spec{}, fmt.Errorf("materialize tool policy: %w", err)
	}
	// A published workspace passport owns /config as one read-only
	// directory bind: nested per-file mounts cannot create mountpoints
	// under it, so the snapshot and manifest ride inside the view instead
	// (SPEC-0092 R5.2).
	if settings.PassportWorkspace == nil {
		bind(policyPath, "/config/tool-policy.json", true)
		if manifest := filepath.Join(dir, "generated", "tool-manifest."+env+".json"); fileExists(manifest) {
			bind(manifest, "/config/tool-manifest.json", true)
			out.Env = append(out.Env, "HUB_TOOL_MANIFEST=/config/tool-manifest.json")
		}
	}
	out.Tmpfs = append(out.Tmpfs, backend.Tmpfs{Target: "/tmp", Options: "uid=10001,gid=10001,mode=1777"})
	for _, t := range managedExtensionRootTmpfs {
		out.Tmpfs = append(out.Tmpfs, backend.Tmpfs{Target: t.Target, Options: t.Options})
	}
	out.Env = append(out.Env,
		"HUB_RUNTIME_LISTEN=0.0.0.0:"+strconv.Itoa(r.RuntimePort),
		"HUB_STATE=/state", "HUB_WORKSPACE=/workspace",
		"HERMES_HOME=/state/hermes", "HOME=/state/home",
		"HUB_USER_ID="+req.UserID,
		"HUB_ORGANIZATION_ID="+req.OrganizationID,
		"HUB_RUNTIME_ID="+req.RuntimeID,
		"HUB_POLICY_VERSION="+req.PolicyVersion,
		"API_SERVER_ENABLED=true", "API_SERVER_HOST=127.0.0.1", "API_SERVER_PORT=8642",
		"HUB_RUNTIME_GENERATION=gen-"+strconv.FormatInt(gen, 10))
	// Operator-tunable run watchdog rides into the spawned runtime when set.
	if value := strings.TrimSpace(os.Getenv("HUB_RUN_STALL_TIMEOUT")); value != "" {
		out.Env = append(out.Env, "HUB_RUN_STALL_TIMEOUT="+value)
	}
	image, _, err := stack.ResolveRuntimeImage(dir, settings.User, spaceID, env, r.Image)
	if err != nil {
		return backend.Spec{}, err
	}
	out.Image = image
	// Relay host pins: the reviewed sandbox relays the runsc network stack
	// cannot resolve through embedded DNS.
	wanted := []string{"toolhub", "model-relay"}
	if len(stack.RuntimeEgressEnvironment(settings, dir)) != 0 {
		wanted = append(wanted, "egress-relay")
	}
	out.RelayHosts = wanted
	out.Relay = &backend.RelaySpec{Image: image, Control: controlNetworkName, Limits: out.Limits}
	// Revision re-check: an activation that landed mid-assembly means the
	// spec describes a revoked policy — the plan's next pass re-reads.
	current, err := r.currentDigest(ctx, t, spaceID)
	if err != nil {
		return backend.Spec{}, err
	}
	if current != digest {
		return backend.Spec{}, errors.New("managed runtime passport activation changed during spawn")
	}
	return out, nil
}

// envFileFor resolves the space's runtime credential env file — the
// runtime.auth the space bootstrap wrote; never caller-controlled.
func envFileFor(dir string, _ EffectiveSpec) string {
	return filepath.Join(dir, "runtime.auth")
}

// currentDigest is the spec builder's end-of-assembly revision check: the
// space's stored pointer read fresh, so an activation that landed mid-
// assembly refuses the stale spec the way the v0 file re-read did.
func (r *Runtimes) currentDigest(ctx context.Context, t store.Tenant, spaceID string) (string, error) {
	var digest string
	err := r.Store.ReadTx(ctx, func(tx *store.Tx) error {
		return tx.Scoped(t).QueryRow("SELECT effective_digest FROM spaces WHERE {tenant} AND id = ?", spaceID).Scan(&digest)
	})
	return digest, err
}

// materializeManagedConfig renders the effective Hermes config on the host
// so the spawned runtime mounts it read-only (v0 materializeHermesConfig,
// minus the file-tree settings read — the claim's stored bundle is the
// policy). Inputs mirror the rendered runtime contract:
// managed-runtime.<env>.env endpoint overrides.
func materializeManagedConfig(dir string, settings stack.Settings, env string) (string, error) {
	source := filepath.Join(dir, "generated", "hermes."+env+".yaml")
	dest := filepath.Join(dir, "generated", "hermes-effective."+env+".yaml")
	envName := "managed-runtime." + env + ".env"
	secrets, err := stack.ReadSecrets(filepath.Join(dir, envName))
	if err != nil {
		return "", fmt.Errorf("runtime environment %s unavailable", envName)
	}
	tokenEnv := strings.TrimSpace(secrets["HUB_TOOLHUB_TOKEN_ENV"])
	if tokenEnv == "" {
		tokenEnv = "HUB_RUNTIME_AUTH"
	}
	authPresent := strings.TrimSpace(secrets[tokenEnv]) != ""
	envFile := filepath.Join(dir, "runtime.auth")
	if !authPresent {
		if envSecrets, err := stack.ReadSecrets(envFile); err == nil && strings.TrimSpace(envSecrets[tokenEnv]) != "" {
			authPresent = true
		}
	}
	opts := stack.MaterializeOptions{
		ToolHubEndpoint:    strings.TrimSpace(secrets["HUB_TOOLHUB_ENDPOINT"]),
		ToolHubTokenEnv:    tokenEnv,
		RuntimeAuthPresent: authPresent,
		ToolHubReconnect:   !strings.EqualFold(strings.TrimSpace(secrets["HUB_TOOLHUB_RECONNECT"]), "false"),
		NativeToolsets:     settings.NativeCarveouts(),
		Web:                settings.Web,
		ImageGen:           settings.ManagedImageGenGrant(),
		Context:            settings.ManagedContextConfig(),
		// The reviewed render's browser admission is the sole authority the
		// attestation replays; without it the source-vs-template check
		// rejects every browser-carrying managed config.
		Browser: settings.ManagedBrowserGrant(),
		// The hub-side subsystem flags replay the reviewed render's
		// admission the same way the browser grant does.
		MemoryVault: settings.ManagedMemoryVaultFlag(),
		User:        settings.User,
	}
	if _, present := secrets["HUB_TOOLHUB_ENDPOINT"]; !present {
		opts.ToolHubEndpoint = "http://toolhub:8090/mcp"
	}
	if err := stack.MaterializeHermesConfig(source, dest, opts); err != nil {
		return "", fmt.Errorf("materialize Hermes config: %w", err)
	}
	settings.Environment = env
	if err := stack.MaterializeSoulEffective(dir, settings); err != nil {
		return "", fmt.Errorf("materialize system contract: %w", err)
	}
	return dest, nil
}

// fileExists mirrors the v0 helper: a regular file under the validated
// space root, never a symlink or directory.
func fileExists(path string) bool {
	info, err := os.Lstat(path) // #nosec G703 -- paths are assembled under the validated space root
	return err == nil && info.Mode().IsRegular()
}
