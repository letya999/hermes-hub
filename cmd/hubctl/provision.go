package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"gopkg.in/yaml.v3"
)

// runProvision is `hubctl provision --user <id>`: one confirmed operator
// command that takes a space from nothing to a running managed runtime —
// init from a provisioning profile, port allocation, capability bundle
// (policy + profile + rights + agent-tools binding), space apply, compose
// up on existing images, sibling token enrollment. Each stage is
// idempotent: re-running converges instead of duplicating records.
func runProvision(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("provision", flag.ContinueOnError)
	user := f.String("user", "", "principal to provision")
	dir := f.String("dir", "", "space directory (default spaces/<user>)")
	root := f.String("root", ".", "source root")
	env := f.String("env", "dev", "dev or prod")
	profile := f.String("profile", "standard-user", "provisioning profile name")
	issuer := f.String("issuer", "operator", "record issuer")
	confirm := f.Bool("confirm", false, "commit the provisioning plan")
	expires := f.String("expires", "8760h", "right grant lifetime")
	skipUp := f.Bool("skip-up", false, "stop after capability records and apply")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *user == "" || f.NArg() != 0 {
		return errors.New("usage: hubctl provision --user <id> [--dir spaces/<id>] [--env dev] [--profile standard-user] [--confirm]")
	}
	if *env != "dev" && *env != "prod" {
		return errors.New("provision: --env must be dev or prod")
	}
	if *dir == "" {
		*dir = filepath.Join("spaces", *user)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	// Stage 1: scaffold from the provisioning profile when the space has no
	// authored passports yet; an existing space keeps its passports.
	scaffolded := false
	if _, err := os.Lstat(filepath.Join(abs, "passports")); os.IsNotExist(err) {
		if err := stack.InitEnvironmentProvision(abs, *user, *env, *profile); err != nil {
			return err
		}
		scaffolded = true
	} else if err != nil {
		return err
	}

	// Stage 2: materialize port placeholders left by the profile template.
	if _, err := provisionPorts(abs); err != nil {
		return err
	}

	// Stage 3: aggregate preflight — collect every finding before writing.
	findings := provisionPreflight(abs, *user, *env)
	if len(findings) > 0 {
		for _, finding := range findings {
			fmt.Fprintf(os.Stderr, "provision: %s\n", finding)
		}
		return fmt.Errorf("provision preflight failed with %d finding(s)", len(findings))
	}

	// Stage 4: capability bundle — policy, bound profile, right grants and
	// the agent-tools binding, one confirmed batch into the infra store.
	bundle, err := buildProvisionBundle(abs, *user, *env, *root, *profile, *expires, *issuer, now)
	if err != nil {
		return err
	}
	if !*confirm {
		fmt.Fprintf(os.Stderr, "provision plan: scaffold=%t policy=%s rights=%d binding=%s:%s\nreview digests: %s / %s\nre-run with --confirm to commit\n",
			scaffolded, bundle.policy.PolicyID, len(bundle.rights),
			bundle.definitionID, bundle.definitionVersion,
			api.ConfirmationDigest(&bundle.policy), api.ConfirmationDigest(&bundle.profile))
		return errors.New("provision not confirmed")
	}
	if err := bundle.commit(*issuer, now); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "provision: capability bundle committed (policy %s rev %d, profile %s, %d rights, %s:%s)\n",
		bundle.policy.PolicyID, bundle.policy.Revision, bundle.profile.ProfileID, len(bundle.rights), bundle.definitionID, bundle.definitionVersion)

	// Stage 5: publish the authored passports.
	if err := runSpaceApply(ctx, []string{"--dir", abs, "--env", *env, "--confirm"}); err != nil {
		return err
	}
	if *skipUp {
		fmt.Fprintln(os.Stderr, "provision: apply complete; skipped compose up")
		return nil
	}

	// Stage 6: broker key volumes must exist before the relay reads them —
	// materialize the shared platform signing identity from the infra space's
	// canonical volume, then start the space on already-built images.
	if err := provisionBrokerVolumes(ctx, abs, *env); err != nil {
		return err
	}
	if err := provisionComposeUp(ctx, abs, *root, *env); err != nil {
		return err
	}

	// Stage 7: enroll the sibling runtime/model-relay tokens with the infra
	// owner so the shared control plane accepts the new principal.
	if err := refreshSharedRuntimeTokens(abs, *env); err != nil {
		return err
	}
	if infra := stack.InfraSpaceDir(abs); infra != abs {
		if err := stack.EnrollSiblingModelRelayAdmins(infra, *env); err != nil {
			return err
		}
	}
	if err := provisionRestartInfraToolhub(ctx, *env); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "provision: %s ready (env %s)\n", *user, *env)
	return nil
}

// provisionRestartInfraToolhub resyncs the live ToolHub after the bundle
// committed to its store.db: the store CAS-fails on digest drift, so a
// running writer poisoned by external commits replays conflicts forever —
// operations.md already warns "stop toolhub during store writes", provision
// performs the resync restart itself instead of leaving a wedged control
// plane. A stopped/absent toolhub is a no-op.
func provisionRestartInfraToolhub(ctx context.Context, env string) error {
	out, err := dockerOutput(ctx, "ps", "--format", "{{.Names}}",
		"--filter", "name=-toolhub-1", "--filter", "status=running")
	if err != nil {
		return fmt.Errorf("toolhub discovery: %w", err)
	}
	var name string
	for _, candidate := range strings.Fields(string(out)) {
		if strings.HasSuffix(candidate, "-"+env+"-toolhub-1") || strings.HasSuffix(candidate, "shared-"+env+"-toolhub-1") {
			name = candidate
			break
		}
	}
	if name == "" {
		return nil
	}
	fmt.Fprintf(os.Stderr, "provision: restarting %s to resync the store digest\n", name)
	if _, err := dockerOutput(ctx, "restart", name); err != nil {
		return fmt.Errorf("toolhub restart: %w", err)
	}
	return nil
}

// provisionPorts rewrites __PORT_*__ placeholders in the authored config
// passport with the lowest ports not already claimed by sibling spaces.
// Returns the allocated values for diagnostics.
func provisionPorts(dir string) (map[string]int, error) {
	path := filepath.Join(dir, "passports", "config.yaml")
	body, err := os.ReadFile(path) // #nosec G304 -- fixed authored passport path
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(body), "__PORT_") {
		return nil, nil
	}
	used := map[string]map[int]bool{"browser-vnc": {}, "oauth-callback": {}}
	parent := filepath.Dir(dir)
	if siblings, err := os.ReadDir(parent); err == nil {
		for _, sibling := range siblings {
			if !sibling.IsDir() || sameTextDir(sibling.Name(), filepath.Base(dir)) {
				continue
			}
			sibBody, err := os.ReadFile(filepath.Join(parent, sibling.Name(), "passports", "config.yaml"))
			if err != nil {
				continue
			}
			var doc struct {
				Ports map[string]int `yaml:"ports"`
			}
			if yaml.Unmarshal(sibBody, &doc) == nil {
				for key, value := range doc.Ports {
					if set, ok := used[key]; ok {
						set[value] = true
					}
				}
			}
		}
	}
	next := func(key string, base int) int {
		for port := base; port < base+500; port++ {
			if !used[key][port] {
				return port
			}
		}
		return base
	}
	allocated := map[string]int{
		"browser-vnc":    next("browser-vnc", 18300),
		"oauth-callback": next("oauth-callback", 8100),
	}
	replaced := strings.NewReplacer(
		"__PORT_BROWSER_VNC__", fmt.Sprint(allocated["browser-vnc"]),
		"__PORT_OAUTH_CALLBACK__", fmt.Sprint(allocated["oauth-callback"]),
	).Replace(string(body))
	return allocated, os.WriteFile(path, []byte(replaced), 0600) // #nosec G703 -- path is fixed under the operator-selected provisioning directory
}

func sameTextDir(name, base string) bool { return strings.EqualFold(name, base) }

// provisionPreflight collects every blocking finding instead of stopping at
// the first error — the operator fixes one report, not five retries.
func provisionPreflight(dir, user, env string) []string {
	findings := []string{}
	passports := []string{"stack", "config", "models", "tools", "grants", "workspace"}
	for _, name := range passports {
		body, err := os.ReadFile(filepath.Join(dir, "passports", name+".yaml"))
		if err != nil {
			findings = append(findings, fmt.Sprintf("passport %s: %v", name, err))
			continue
		}
		if strings.Contains(string(body), "__USER__") || strings.Contains(string(body), "__PORT_") {
			findings = append(findings, fmt.Sprintf("passport %s still contains unresolved template tokens", name))
		}
	}
	secrets, err := stack.ReadSecrets(filepath.Join(dir, "secrets."+env+".env"))
	if err != nil {
		findings = append(findings, fmt.Sprintf("secrets.%s.env: %v", env, err))
	} else if strings.TrimSpace(secrets["OPENAI_API_KEY"]) == "" {
		findings = append(findings, fmt.Sprintf("secrets.%s.env: set OPENAI_API_KEY (or the local provider's documented nonempty value)", env))
	}
	infra := stack.InfraSpaceDir(dir)
	if infra == dir {
		findings = append(findings, "no infra-owner sibling found; provision requires an existing shared control plane")
	} else {
		settings, err := stack.ReadEffective(infra, env)
		if err != nil {
			findings = append(findings, fmt.Sprintf("infra space %s: %v", filepath.Base(infra), err))
		} else if !settings.RendersInfra() {
			findings = append(findings, fmt.Sprintf("infra space %s does not render infra", filepath.Base(infra)))
		}
		if _, err := os.Lstat(filepath.Join(infra, "runtime", "toolhub", "store.db")); err != nil {
			findings = append(findings, fmt.Sprintf("infra toolhub store: %v", err))
		}
	}
	// Grants vocabulary: every authored cap.op must exist in the pinned
	// rights catalog — this is the check `space apply` enforces one error
	// at a time today.
	body, err := os.ReadFile(filepath.Join(dir, "passports", "grants.yaml"))
	if err == nil {
		var grants struct {
			Allow map[string][]string `yaml:"allow"`
		}
		if yaml.Unmarshal(body, &grants) == nil {
			if docs, catErr := catalog.LoadPinned(); catErr != nil {
				findings = append(findings, fmt.Sprintf("rights catalog: %v", catErr))
			} else {
				caps := make([]string, 0, len(grants.Allow))
				for cap := range grants.Allow {
					caps = append(caps, cap)
				}
				sort.Strings(caps)
				for _, cap := range caps {
					for _, op := range grants.Allow[cap] {
						if _, ok := docs.Rights.Lookup(cap, op); !ok {
							findings = append(findings, fmt.Sprintf("grants allow %s.%s is not in the pinned rights catalog", cap, op))
						}
					}
				}
			}
		}
	}
	return findings
}

// provisionBundle is the capability authority one provision writes: the
// principal policy, the bound profile, the governance right grants and the
// compiled agent-tools definition pin.
type provisionBundle struct {
	dir               string
	storePath         string
	policy            api.CapabilityPolicy
	profile           api.CapabilityProfile
	rights            []api.RightGrant
	definitionID      string
	definitionVersion string
	definition        api.ToolDefinition
	principalEnvelope identity.Envelope
}

func buildProvisionBundle(dir, user, env, root, profileName, expires, issuer string, now time.Time) (*provisionBundle, error) {
	infra := stack.InfraSpaceDir(dir)
	storePath := filepath.Join(infra, "runtime", "toolhub", "store.db")
	definition := agenttools.HubToolsDefinition()
	digest := api.DefinitionDigest(definition)

	// The capability ceiling ships with the provisioning profile as reviewed
	// vocabulary; each rule is stamped with the bound definition digest.
	ceiling, err := provisionCeiling(root, profileName, digest)
	if err != nil {
		return nil, err
	}

	// The grants ask is both the ledger mint set and the policy right
	// ceiling — a standard space may hold exactly the rights it requests.
	grantsBody, err := os.ReadFile(filepath.Join(dir, "passports", "grants.yaml")) // #nosec G304 -- authored passport
	if err != nil {
		return nil, err
	}
	var grants struct {
		Allow map[string][]string `yaml:"allow"`
	}
	if err := yaml.Unmarshal(grantsBody, &grants); err != nil {
		return nil, fmt.Errorf("grants passport: %w", err)
	}
	rightCeiling := []api.RightRule{}
	expiresAt, err := parseExpiry(expires, now)
	if err != nil {
		return nil, err
	}
	rights := []api.RightGrant{}
	caps := make([]string, 0, len(grants.Allow))
	for cap := range grants.Allow {
		caps = append(caps, cap)
	}
	sort.Strings(caps)
	for _, cap := range caps {
		ops := append([]string{}, grants.Allow[cap]...)
		sort.Strings(ops)
		for _, op := range ops {
			rightCeiling = append(rightCeiling, api.RightRule{Capability: cap, Operation: op})
			rights = append(rights, api.NewRightGrant(
				deterministicRightID(user, cap, op, "", ""),
				user, cap, op, "", "provision "+user+" "+profileName, issuer, expiresAt))
		}
	}

	policy := api.CapabilityPolicy{
		Schema: api.SchemaVersion, PolicyID: user + "-policy", Members: []string{user},
		Revision: 1, IssuedBy: issuer, IssuedAt: now, Reason: "provision " + user,
		Status: api.ActiveStatus, Ceiling: ceiling, RightCeiling: rightCeiling,
	}
	profile := api.CapabilityProfile{
		Schema: api.SchemaVersion, ProfileID: user + "-default",
		PrincipalID: user, ContextID: user, RuntimeID: user,
		Environment: env, Generation: 1,
		PolicyVersion: "policy-0000000000000000",
		PolicyID:      policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: issuer, IssuedAt: now, Reason: "provision " + user,
		Status: api.ActiveStatus, Connectors: "personal",
	}
	envelope := identity.Envelope{Schema: identity.Schema, PrincipalID: user, ExternalIdentityID: user,
		ContextID: user, RuntimeID: user, ConversationID: "operator", DeliveryTargetID: "operator",
		PolicyVersion: "policy-0000000000000000"}
	return &provisionBundle{
		dir: dir, storePath: storePath, policy: policy, profile: profile, rights: rights,
		definitionID: definition.DefinitionID, definitionVersion: definition.Version,
		definition: definition, principalEnvelope: envelope,
	}, nil
}

// provisionCeiling loads the profile's reviewed capability ceiling and
// stamps every rule with the bound definition digest plus default limits.
func provisionCeiling(root, profileName, digest string) ([]api.CapabilityRule, error) {
	path := filepath.Join(root, "policies", "profiles", profileName, "policy.yaml")
	body, err := os.ReadFile(path) // #nosec G304 -- reviewed profile asset
	if err != nil {
		return nil, fmt.Errorf("provisioning ceiling %s: %w", profileName, err)
	}
	var doc struct {
		Ceiling []struct {
			CapabilityID string `yaml:"capability_id"`
			Action       string `yaml:"action"`
			Resource     string `yaml:"resource"`
			ConnectionID string `yaml:"connection_id"`
			PathPrefix   string `yaml:"path_prefix"`
			Limits       struct {
				OutputBytes    int `yaml:"output_bytes"`
				TimeoutSeconds int `yaml:"timeout_seconds"`
			} `yaml:"limits"`
		} `yaml:"ceiling"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil || len(doc.Ceiling) == 0 {
		return nil, fmt.Errorf("provisioning ceiling %s is empty or invalid", profileName)
	}
	rules := make([]api.CapabilityRule, 0, len(doc.Ceiling))
	for _, r := range doc.Ceiling {
		limits := api.CapabilityLimits{OutputBytes: 2097152, TimeoutSeconds: 120}
		if r.Limits.OutputBytes > 0 {
			limits.OutputBytes = r.Limits.OutputBytes
		}
		if r.Limits.TimeoutSeconds > 0 {
			limits.TimeoutSeconds = r.Limits.TimeoutSeconds
		}
		rules = append(rules, api.CapabilityRule{
			CapabilityID: r.CapabilityID, ImplementationDigest: digest,
			Action: r.Action, Resource: r.Resource, ConnectionID: r.ConnectionID,
			PathPrefix: r.PathPrefix, Limits: limits,
		})
	}
	return rules, nil
}

// commit writes the bundle under the operator's confirmation: policy and
// profile into the capability store (idempotent puts — an identical record
// replays as a no-op), right grants into the governance ledger skipping
// already-active pairs, then the agent-tools binding.
func (b *provisionBundle) commit(issuer string, now time.Time) error {
	if err := api.Confirm(&b.policy, issuer, now); err != nil {
		return err
	}
	if err := api.Confirm(&b.profile, issuer, now); err != nil {
		return err
	}
	// The live ToolHub also writes store.db and its Put saves are CAS-checked:
	// a conflicted save reloads and replays the whole bundle with backoff
	// until it lands in a quiet window, rather than failing the provision.
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(150+attempt*80) * time.Millisecond)
		}
		store, err := loadOperatorStore(b.storePath)
		if err != nil {
			return fmt.Errorf("toolhub store: %w", err)
		}
		err = b.commitStore(store)
		_ = store.CloseDB()
		if err == nil {
			break
		}
		lastErr = err
		if !retryableStoreConflict(err) {
			return err
		}
	}
	if lastErr != nil {
		return lastErr
	}

	docs, err := catalog.LoadPinned()
	if err != nil {
		return fmt.Errorf("rights vocabulary: %w", err)
	}
	gov := toolhub.NewGovernanceStore(toolhub.GovernanceStorePath(b.storePath))
	existing, err := gov.RightGrants()
	if err != nil {
		return fmt.Errorf("governance rights: %w", err)
	}
	active := map[string]bool{}
	for _, grant := range existing {
		if grant.PrincipalID == b.profile.PrincipalID && grant.Status != api.GrantRevoked {
			active[grant.Capability+"."+grant.Operation] = true
		}
	}
	for i := range b.rights {
		grant := &b.rights[i]
		if active[grant.Capability+"."+grant.Operation] {
			continue
		}
		if err := api.Confirm(grant, issuer, now); err != nil {
			return err
		}
		if err := gov.GrantRight(*grant, docs.Rights, now); err != nil {
			return fmt.Errorf("right grant %s.%s: %w", grant.Capability, grant.Operation, err)
		}
	}
	return nil
}

// commitStore applies the store-resident half of the bundle in one attempt.
// An existing record that differs only in issue stamps (an earlier provision
// attempt committed it) converges instead of conflicting on revision; a
// semantically different record under the same id is a real authority
// divergence and stops the provision for operator review.
func (b *provisionBundle) commitStore(store *toolhub.Store) error {
	if existing, ok := store.CapabilityPolicy(b.policy.PolicyID); ok {
		if !provisionPolicyEquivalent(existing, b.policy) {
			return fmt.Errorf("capability policy %s already exists with different content (rev %d): review it before re-provisioning", b.policy.PolicyID, existing.Revision)
		}
		b.policy = existing
	} else if err := store.PutCapabilityPolicy(b.policy); err != nil {
		return fmt.Errorf("capability policy: %w", err)
	}
	if existing, ok := store.CapabilityProfile(b.profile.ProfileID); ok {
		if !provisionProfileEquivalent(existing, b.profile) {
			return fmt.Errorf("capability profile %s already exists with different content (rev %d): review it before re-provisioning", b.profile.ProfileID, existing.Revision)
		}
	} else if err := store.PutCapabilityProfile(b.profile); err != nil {
		return fmt.Errorf("capability profile: %w", err)
	}
	if err := store.RegisterDefinition(b.definition); err != nil {
		return fmt.Errorf("agent-tools definition: %w", err)
	}
	if _, err := store.Enable(b.principalEnvelope, b.definitionID, b.definitionVersion); err != nil {
		return fmt.Errorf("agent-tools binding: %w", err)
	}
	return store.Save(b.storePath)
}

// provisionPolicyEquivalent compares policies on authority-bearing fields,
// ignoring the per-run issue stamps (IssuedAt, Confirmation).
func provisionPolicyEquivalent(a, b api.CapabilityPolicy) bool {
	a.IssuedAt, a.Confirmation = b.IssuedAt, b.Confirmation
	return api.RecordsEqual(a, b)
}

// provisionProfileEquivalent compares only the authored fields provision
// owns: the identity tuple, policy binding and declared posture. Derived
// stamps (PolicyVersion, PassportRevision, Selections, Allows, Rights,
// CoreTools, Toolsets) belong to apply/derive and are ignored so an
// already-published profile still converges.
func provisionProfileEquivalent(a, b api.CapabilityProfile) bool {
	return a.ProfileID == b.ProfileID && a.PrincipalID == b.PrincipalID &&
		a.ContextID == b.ContextID && a.RuntimeID == b.RuntimeID &&
		a.Environment == b.Environment && a.Generation == b.Generation &&
		a.PolicyID == b.PolicyID && a.PolicyRevision == b.PolicyRevision &&
		a.Status == b.Status && a.Connectors == b.Connectors &&
		a.SelfInstall == b.SelfInstall && slices.Equal(a.ControlOperations, b.ControlOperations)
}

// provisionBrokerVolumes materializes the shared broker signing identity
// (server.crt + model.private) into the space's broker-secrets-model volume
// from the infra space's canonical volume. The `model` key is a platform
// identity — every space's model-relay signs with it; a fresh volume starts
// empty and the relay crash-loops on `credential broker CA file is invalid`
// until seeded. Idempotent: a populated volume is left untouched.
func provisionBrokerVolumes(ctx context.Context, dir, env string) error {
	infra := stack.InfraSpaceDir(dir)
	if infra == dir {
		return nil
	}
	settings, err := stack.ReadEffective(infra, env)
	if err != nil {
		return fmt.Errorf("infra space: %w", err)
	}
	source := "hermes-hub-" + settings.User + "-" + env + "_broker-secrets-model"
	target := "hermes-hub-" + filepath.Base(dir) + "-" + env + "_broker-secrets-model"
	// Populate only a volume that lacks server.crt — an existing identity
	// stays; a missing target volume is created implicitly by the mount.
	script := "if [ ! -f /src/server.crt ]; then echo 'provision: infra broker-secrets-model has no server.crt' >&2; exit 2; fi;" +
		"if [ -f /dst/server.crt ]; then exit 0; fi;" +
		"cp /src/model.private /src/server.crt /dst/ && " +
		"chown 10001:10001 /dst/server.crt && chown 10001:0 /dst/model.private && " +
		"chmod 600 /dst/model.private /dst/server.crt"
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"-v", source+":/src:ro", "-v", target+":/dst",
		"alpine", "sh", "-c", script) // #nosec G204 -- fixed docker argv
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("broker secrets materialize: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// provisionComposeUp starts the space's compose on existing images — the
// fast path `up --no-build` takes, without the build stage.
func provisionComposeUp(ctx context.Context, dir, root, env string) error {
	if err := renderEnvironment(dir, root, env); err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	composePath := filepath.Join(abs, "generated", "compose."+env+".yaml")
	composeEnv := supervisorComposeEnv(abs)
	docker := func(a ...string) error {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", composePath}, a...)...) // #nosec G204 G702 -- fixed docker executable; args are fixed compose subcommands on an operator-selected file
		cmd.Env = composeEnv
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	if err := docker("config", "--quiet"); err != nil {
		return fmt.Errorf("compose configuration preflight: %w", err)
	}
	return docker("up", "-d")
}
