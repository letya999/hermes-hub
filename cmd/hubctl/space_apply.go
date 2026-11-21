package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/shared/locale"
	"gopkg.in/yaml.v3"
)

// passportEditRight maps each of the six passports to the distinct current
// right its edits require (SPEC-0095 R1.4, ADR-0096 D3): the right must be
// both issued to the principal and carried by the active published allow.
var passportEditRight = map[string][2]string{
	"stack":     {"stack", "change"},
	"config":    {"config", "edit_own"},
	"models":    {"models", "configure"},
	"workspace": {"workspace", "configure"},
	"tools":     {"tools", "configure"},
	"grants":    {"grants", "configure"},
}

// applyReceipt is the space apply JSON: the classified diff plus the
// reconcile report once --confirm publishes. The revision fields carry
// effective digests — the contract's content-addressed revision identity.
type applyReceipt struct {
	Revision         string             `json:"revision"`
	PreviousRevision string             `json:"previous_revision,omitempty"`
	FirstApply       bool               `json:"first_apply,omitempty"`
	Changes          []spawnv1.Change   `json:"changes"`
	Actions          []reconcile.Action `json:"actions"`
	Report           *reconcile.Report  `json:"report,omitempty"`
	Confirmed        bool               `json:"confirmed"`
}

// runSpaceApply is the passport-writer client: it submits the authored
// layer documents to spawn-controller, which validates, compiles and —
// under --confirm — activates them atomically on the base-digest CAS.
// Side effects stay here (Decision 13): every confirm first converges
// them to the CURRENTLY active digest — the crash recovery the journal
// used to carry — then activates, then converges to the new digest and
// runs the action plan.
func runSpaceApply(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("space apply", flag.ContinueOnError)
	dir := f.String("dir", "", "space directory")
	root := f.String("root", ".", "source root")
	env := f.String("env", "prod", "dev or prod")
	confirm := f.Bool("confirm", false, "publish the compiled revision and reconcile; default prints the classified diff receipt")
	_ = f.Bool("json", false, "accepted for symmetry; output is always the JSON receipt")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *dir == "" || f.NArg() != 0 {
		return fmt.Errorf("usage: hubctl space apply --dir spaces/<user> --env <dev|prod> [--confirm] [--json]")
	}
	if *env != "dev" && *env != "prod" {
		return fmt.Errorf("space apply: --env must be dev or prod")
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	// Applies on one space serialize on a file lock: a second apply refuses
	// rather than interleaving converge, apply and reconcile effects.
	if err := os.MkdirAll(filepath.Join(abs, "generated"), 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(abs, "generated", "apply.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if !locked {
		return errors.New("space apply: another apply is in progress for this space")
	}
	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	space := filepath.Base(abs)
	client, tenant, err := applySpawnClient(abs)
	if err != nil {
		return err
	}
	// The space row is the tenant record apply and settings write against;
	// re-creating an existing one with the same environment is a no-op.
	if _, err := client.CreateSpace(ctx, spawnv1.CreateSpaceRequest{Org: tenant.Org, Owner: tenant.Owner, ID: space, Environment: *env, IdempotencyKey: "apply-space"}); err != nil && !errors.Is(err, spawnv1.ErrConflict) {
		return err
	}
	// (a) Converge side effects to the currently active digest — the
	// crash recovery a previous apply may have left mid-run.
	if err := convergeApply(ctx, abs, rootAbs, *env, client, tenant); err != nil {
		return fmt.Errorf("space apply: converge: %w", err)
	}
	authored, err := passport.ReadAuthored(abs)
	if err != nil {
		return err
	}
	if len(authored) == 0 {
		return errors.New("space apply: no authored passports to apply")
	}
	eff, err := client.Effective(ctx, tenant, space)
	var baseDigest string
	var prevDocs map[string]json.RawMessage
	firstApply := false
	switch {
	case errors.Is(err, spawnv1.ErrNotFound):
		firstApply = true
	case err != nil:
		return err
	default:
		baseDigest, prevDocs = eff.Digest, eff.Documents
	}
	docs, err := catalog.Generate()
	if err != nil {
		return err
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(stack.InfraSpaceDir(abs), "runtime", "toolhub", "governance.json"))
	issued, err := toolhub.IssuedRightCeiling(gov, docs.Rights, filepath.Base(abs), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("space apply: issued rights: %w", err)
	}
	// (b) Dry-run apply: the service validates + compiles the would-be
	// set; the receipt feeds every client-side gate before confirmation.
	dry, err := client.Apply(ctx, space, spawnv1.ApplyRequest{
		Org: tenant.Org, Owner: tenant.Owner, BaseDigest: baseDigest, DryRun: true,
		IdempotencyKey: "apply-dry-" + fmt.Sprint(time.Now().UnixNano()),
		Layers:         map[string]map[string]map[string]any{"user": authored},
	})
	if err != nil {
		return fmt.Errorf("space apply: %w", err)
	}
	nextDocs := dry.Documents
	if err := applyGates(abs, *env, dry.Changes, nextDocs, prevDocs, issued, docs); err != nil {
		return err
	}
	actions := reconcileActions(dry.Actions)
	// The first publication establishes authority; `hubctl up` starts its
	// containers after the operator has supplied images and service secrets.
	if firstApply {
		actions = nil
	}
	receipt := applyReceipt{
		Revision:         dry.EffectiveDigest,
		PreviousRevision: baseDigest,
		FirstApply:       firstApply,
		Changes:          dry.Changes,
		Actions:          actions,
		Confirmed:        *confirm,
	}
	if !*confirm {
		return printApply(receipt)
	}
	applied, err := client.Apply(ctx, space, spawnv1.ApplyRequest{
		Org: tenant.Org, Owner: tenant.Owner, BaseDigest: baseDigest,
		IdempotencyKey: "apply-" + fmt.Sprint(time.Now().UnixNano()),
		Layers:         map[string]map[string]map[string]any{"user": authored},
	})
	if err != nil {
		return fmt.Errorf("space apply: %w", err)
	}
	receipt.Revision = applied.EffectiveDigest
	// (c) Converge to the new digest and run the action plan; a failed
	// action re-activates the previous set (CAS on the new digest) and
	// converges back — the rollback reconcile.Run replays.
	rollback := func() error {
		if len(applied.PreviousRevisions) == 0 {
			return nil
		}
		if _, err := client.ActivateRevisions(ctx, space, spawnv1.ActivationRequest{
			Org: tenant.Org, Owner: tenant.Owner, Revisions: applied.PreviousRevisions,
			BaseDigest: applied.EffectiveDigest, IdempotencyKey: "apply-rollback-" + fmt.Sprint(time.Now().UnixNano()),
		}); err != nil {
			return err
		}
		return convergeApply(context.WithoutCancel(ctx), abs, rootAbs, *env, client, tenant)
	}
	if err := convergeApply(ctx, abs, rootAbs, *env, client, tenant); err != nil {
		return fmt.Errorf("space apply: converge: %w", err)
	}
	report := reconcile.Run(actions, applyHandlers(ctx, abs, rootAbs, *env), rollback, applied.EffectiveDigest, baseDigest)
	if report.Failed != nil {
		if !report.RolledBack {
			return fmt.Errorf("space apply: %s %s failed and rollback diverged — activation state is uncertain", report.Failed.Kind, report.Failed.Target)
		}
		receipt.Report = &report
		_ = printApply(receipt)
		return fmt.Errorf("space apply: %s %s failed, rolled back to %s", report.Failed.Kind, report.Failed.Target, baseDigest)
	}
	receipt.Report = &report
	return printApply(receipt)
}

// applyGates runs the operator-side publish checks the dry-run receipt
// feeds: connector selection, organization binding, the immutable scope
// kind, and the per-passport edit right (R1.4) — the same gates the local
// compile enforced, now driven by the service's own diff and documents.
func applyGates(abs, environment string, changes []spawnv1.Change, nextDocs, prevDocs map[string]json.RawMessage, issued map[string][]string, docs catalog.Docs) error {
	lang := locale.LangOf(filepath.Join(abs, "workspace"))
	toolsBody, err := yaml.Marshal(documentBody(nextDocs["tools"]))
	if err != nil {
		return err
	}
	deferred, err := migration.ValidateSelectedConnectors(abs, toolsBody, docs)
	if err != nil {
		return fmt.Errorf("space apply: connector selection: %w", err)
	}
	for _, name := range deferred {
		fmt.Fprintf(os.Stderr, "space apply: connector %q deferred — no active binding; selections materialize when the connector goes live\n", name)
	}
	stackDoc := documentBody(nextDocs["stack"])
	toolsDoc := documentBody(nextDocs["tools"])
	organization, _ := leafAt(stackDoc, "identity.organization").(string)
	if organization != "" {
		profileID, _ := leafAt(toolsDoc, "binding.profile_id").(string)
		if profileID == "" {
			return errors.New(locale.T(lang, "hubctl.space-apply-organization-binding"))
		}
		generation, _ := leafAt(toolsDoc, "binding.generation").(float64)
		store, err := toolhub.Load(api.ResolveStorePath(filepath.Join(stack.InfraSpaceDir(abs), "runtime", "toolhub")))
		if err != nil {
			return errors.New(locale.T(lang, "hubctl.space-apply-organization-policy"))
		}
		if err := store.CheckOrganizationRights(filepath.Base(abs), environment, organization, profileID, uint64(generation), grantsAllow(prevDocs["grants"])); err != nil {
			return errors.New(locale.T(lang, "hubctl.space-apply-organization-policy"))
		}
	}
	if prevDocs != nil {
		for _, change := range changes {
			if change.Path == "stack.identity.kind" {
				return errors.New(locale.T(lang, "hubctl.space-apply-scope-kind"))
			}
		}
		// R1.4: each passport's edits require its own current right — held
		// both in the issued ledger ceiling and the active published allow —
		// before a new revision carrying that doc's changes may publish.
		needed := map[[2]string]bool{}
		for _, change := range changes {
			doc, _, _ := strings.Cut(change.Path, ".")
			if right, ok := passportEditRight[doc]; ok {
				needed[right] = true
			}
		}
		prevAllow := grantsAllow(prevDocs["grants"])
		for right := range needed {
			if !slices.Contains(issued[right[0]], right[1]) || !slices.Contains(prevAllow[right[0]], right[1]) {
				return errors.New(locale.T(lang, "hubctl.space-apply-passport-right", right[0]+"."+right[1]))
			}
		}
	}
	return nil
}

// reconcileActions converts the contract's plan into the reconcile units
// the local effect handlers run.
func reconcileActions(in []spawnv1.Action) []reconcile.Action {
	out := make([]reconcile.Action, 0, len(in))
	for _, a := range in {
		out = append(out, reconcile.Action{Kind: reconcile.Kind(a.Kind), Target: a.Target, Consumers: a.Consumers, Paths: a.Paths})
	}
	return out
}

// documentBody unwraps one emitted passport doc: the apiVersion/kind/
// metadata envelope is compiler bookkeeping, the body is the document.
func documentBody(raw json.RawMessage) map[string]any {
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return map[string]any{}
	}
	body := map[string]any{}
	for k, v := range full {
		if k == "apiVersion" || k == "kind" || k == "metadata" {
			continue
		}
		body[k] = v
	}
	return body
}

func leafAt(doc map[string]any, path string) any {
	var value any = doc
	for _, part := range strings.Split(path, ".") {
		section, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value, ok = section[part]
		if !ok {
			return nil
		}
	}
	return value
}

// grantsAllow extracts {allow: {family: [ops]}} from an emitted grants doc.
func grantsAllow(raw json.RawMessage) map[string][]string {
	out := map[string][]string{}
	allow, _ := documentBody(raw)["allow"].(map[string]any)
	for family, ops := range allow {
		for _, op := range ops.([]any) {
			if s, ok := op.(string); ok {
				out[family] = append(out[family], s)
			}
		}
	}
	return out
}

// applyStep is the test seam for convergence progress.
var applyStep = func(string) {}

// convergeApply drives every apply-owned side effect to the space's
// currently active digest — idempotent, so it doubles as crash recovery
// at the head of every apply (Decision 13a). The mirror's .policy file
// resolves the emitted revision the render and rights derivation consume;
// a pending mirror surfaces as a retry-able error, never a guess.
func convergeApply(ctx context.Context, dir, root, environment string, client spawnv1.Client, tenant spawnv1.Tenant) error {
	space := filepath.Base(dir)
	eff, err := client.Effective(ctx, tenant, space)
	if errors.Is(err, spawnv1.ErrNotFound) {
		return nil // nothing active yet — first apply has nothing to converge
	}
	if err != nil {
		return err
	}
	applyStep("converge")
	revisionID, err := mirrorRevisionID(dir, eff.Digest)
	if err != nil {
		return fmt.Errorf("mirror pending for %s: %w", eff.Digest, err)
	}
	if err := renderRevisionEnvironment(dir, root, environment, revisionID); err != nil {
		return err
	}
	rev, err := loadMirrorRevision(dir, revisionID)
	if err != nil {
		return err
	}
	docs, err := catalog.LoadPinned()
	if err != nil {
		return err
	}
	if err := migration.DeriveSpaceRights(dir, space, environment, rev, docs, time.Now().UTC()); err != nil {
		return err
	}
	if publisher, err := applyPolicyPublisher(); err != nil {
		return err
	} else if publisher != nil {
		active, err := publisher.ActivePassports(ctx, space, environment)
		if err != nil && !errors.Is(err, contract.ErrNotFound) {
			return err
		}
		if active != eff.Digest {
			bundle, err := rev.PolicyBundle()
			if err != nil {
				return err
			}
			staged, err := publisher.StagePassports(ctx, bundle)
			if err != nil {
				return err
			}
			if err := publisher.ActivatePassports(ctx, space, environment, active, staged.Digest); err != nil {
				return err
			}
		}
		applyStep("policy")
	}
	if err := refreshSharedRuntimeTokens(dir, environment); err != nil {
		return err
	}
	bound, _ := rev.Value("tools", "binding.profile_id")
	profileID, _ := bound.(string)
	return migration.DeactivateStaleBoundProfiles(dir, space, environment, profileID, time.Now().UTC())
}

// mirrorRevisionID resolves an effective digest to its emitted revision
// directory — the .policy/<hex> indirection the mirror publishes.
func mirrorRevisionID(dir, digest string) (string, error) {
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", ".policy", hexDigest)) // #nosec G703 -- digest is a sha256 hex pointer under the space's generated tree
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(body))
	if !identity.ValidID(id) {
		return "", fmt.Errorf("invalid mirror revision %q", id)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated", "passports", id)); err != nil {
		return "", err
	}
	return id, nil
}

// loadMirrorRevision rebuilds the passport.Revision the mirror published —
// the immutable doc files DeriveSpaceRights and the publisher consume.
func loadMirrorRevision(dir, revisionID string) (passport.Revision, error) {
	rev := passport.Revision{ID: revisionID, Docs: map[string][]byte{}}
	for _, name := range passport.Names {
		body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", revisionID, name+".yaml")) // #nosec G703 -- validated revision dir under the space's generated tree
		if err != nil {
			return rev, err
		}
		rev.Docs[name] = body
	}
	return rev, nil
}

// printApply emits the JSON receipt — the classified diff, the ordered
// action plan and, once confirmed, the reconcile report. All values are
// data; no localized literals are needed.
func printApply(r applyReceipt) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// applyHandlers wires the real effects: rebuild through the rendered compose
// file's build path (BuildKit via dockerCLIEnv), recreate through compose up
// --force-recreate, respawn through the supervisor's deferred restart
// request. Live hooks stay empty — consumers are wired by their own stages.
// respawnHTTPClient bounds operator-side respawn calls: a container replace
// runs destroy+spawn+readiness inside the supervisor's 4-minute operation
// context — a shorter client bound would fail the apply mid-replacement.
var respawnHTTPClient = &http.Client{Timeout: 5 * time.Minute}

// dockerCompose is the compose invocation the apply handlers run — a var so
// tests can capture argv without a daemon.
var dockerCompose = func(ctx context.Context, dir string, a ...string) error {
	cmd := exec.CommandContext(ctx, "docker", a...) // #nosec G204 -- fixed executable, catalog service names
	cmd.Env = supervisorComposeEnv(dir)
	cmd.Dir = filepath.Dir(dir)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

var applyImageBuild func(context.Context, []string) error

func init() { applyImageBuild = run }

func applyHandlers(ctx context.Context, dir, root, env string) reconcile.Handlers {
	composeFile := filepath.Join(dir, "generated", "compose."+env+".yaml")
	composeFor := func(settings stack.Settings, service string) string {
		if !settings.RendersInfra() || !stack.HasPerSpaceServices(settings) {
			return composeFile
		}
		part := "shared"
		if settings.PassportServices[service] == "per-space" || service == "hosting-controller" && settings.PassportServices["hosting"] == "per-space" {
			part = "user"
		}
		return filepath.Join(dir, "generated", "compose."+part+"."+env+".yaml")
	}
	converge := func(settings stack.Settings) error {
		if !settings.RendersInfra() || !stack.HasPerSpaceServices(settings) {
			return dockerCompose(ctx, dir, "compose", "-f", composeFile, "up", "-d", "--remove-orphans", "--wait", "--wait-timeout", "180")
		}
		for _, part := range []string{"shared", "user"} {
			if part == "user" {
				if err := ensureAgentNetwork(ctx, stack.ManagedAgentNetwork(settings), dockerOutput); err != nil {
					return err
				}
			}
			file := filepath.Join(dir, "generated", "compose."+part+"."+env+".yaml")
			if err := dockerCompose(ctx, dir, "compose", "-f", file, "up", "-d", "--remove-orphans", "--wait", "--wait-timeout", "180"); err != nil {
				return err
			}
		}
		return nil
	}
	docker := func(a ...string) error {
		return dockerCompose(ctx, dir, a...)
	}
	return reconcile.Handlers{
		Rebuild: func(service string) error {
			buildDir, selected := dir, []string{}
			if service != "*" {
				settings, err := stack.ReadEffective(dir, env)
				if err != nil {
					return err
				}
				name := stack.ComposeServiceName(settings, service)
				if service == "hermes-runtime" && settings.PassportServices[service] == "supervised" {
					selected = []string{"--image", "runtime"}
				} else {
					if name == "" {
						buildDir, name = stack.InfraSpaceDir(dir), service
					}
					selected = []string{name}
				}
			}
			// Managed renders have no Compose build sections. Reuse the same
			// image selection and BuildKit path as an operator's scoped build.
			args := []string{"build", "--dir", buildDir, "--root", root, "--env", env}
			return applyImageBuild(ctx, append(args, selected...))
		},
		Recreate: func(service string) error {
			settings, err := stack.ReadEffective(dir, env)
			if err != nil {
				return err
			}
			if (service == "hosting-controller" && settings.Hosting.Image == "") || (service != "hosting-controller" && settings.PassportServices[service] == "") {
				// A rollback can remove a newly created service. Converge the
				// restored project so its orphaned container is removed.
				return converge(settings)
			}
			name := stack.ComposeServiceName(settings, service)
			if name == "" {
				if service == "hermes-runtime" {
					// Supervised runtimes render no compose service — the
					// deferred-restart path replaces the container.
					return respawnRuntime(ctx, dir, env)
				}
				// Shared services render in the infra space's project.
				infraDir := stack.InfraSpaceDir(dir)
				if service == "toolhub" && infraDir != dir {
					if err := renderEnvironment(infraDir, filepath.Dir(filepath.Dir(dir)), env); err != nil {
						return err
					}
				}
				infraCompose := filepath.Join(infraDir, "generated", "compose."+env+".yaml")
				if infraSettings, readErr := stack.ReadEffective(infraDir, env); readErr == nil && infraSettings.RendersInfra() && stack.HasPerSpaceServices(infraSettings) {
					infraCompose = filepath.Join(infraDir, "generated", "compose.shared."+env+".yaml")
				}
				return docker("compose", "-f", infraCompose, "up", "-d", "--wait", "--wait-timeout", "180", "--force-recreate", "--no-deps", service)
			}
			if settings.RendersInfra() && settings.PassportServices[service] == "per-space" {
				if err := ensureAgentNetwork(ctx, stack.ManagedAgentNetwork(settings), dockerOutput); err != nil {
					return err
				}
			}
			return docker("compose", "-f", composeFor(settings, service), "up", "-d", "--wait", "--wait-timeout", "180", "--force-recreate", "--no-deps", name)
		},
		// One sweep covers every instance the new revision dropped:
		// containers whose services no longer exist in the just-rendered
		// file are compose orphans.
		Remove: func() error {
			settings, err := stack.ReadEffective(dir, env)
			if err != nil {
				return err
			}
			return converge(settings)
		},
		Respawn: func() error {
			return respawnRuntime(ctx, dir, env)
		},
		Live: map[string]func() error{
			// convergeApply renders before reconcile runs: the new
			// runtime.<env>.policy.json is already on disk and the
			// supervisor re-reads it per decision — the leaf is applied.
			"supervisor-policy": func() error { return nil },
			// The mounted model registry re-reads on change; the render
			// already swapped it in place.
			"model-relay": func() error { return nil },
			// File admission reloads the active /config/workspace.yaml; the
			// mounted passport file is already the new revision.
			"file-admission": func() error { return nil },
			// DeriveSpaceRights rewrote the managed profiles/ledger at
			// publish; admission reads them per call.
			"tool-policy":   func() error { return nil },
			"rights-ledger": func() error { return nil },
		},
	}
}

// applyPolicyPublisher resolves the identity-hub passport publisher the
// converge stages and activates policy digests through — unchanged from
// the v0 apply path; nil when the installation runs none.
func applyPolicyPublisher() (contract.PassportPublisher, error) {
	token := strings.TrimSpace(os.Getenv("HUB_SUPERVISOR_ADMIN_AUTH"))
	if token == "" {
		return nil, nil
	}
	endpoint := strings.TrimSpace(os.Getenv("HUB_PASSPORT_PUBLISHER_URL"))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8876"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, contract.ErrInvalid
	}
	httpClient := &http.Client{Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return contract.NewPassportPublisherHTTPClient(endpoint, token, httpClient), nil
}

// applySpawnClient resolves the spawn-controller contract client the way
// explain does: an explicit HUB_SPAWN_CONTROLLER_URL wins (process mode
// exports it), else the compose stack's loopback publication. The service
// token chain is HUB_SERVICE_TOKEN, the supervisor env, then the infra
// space's supervisor.auth — resolved against the --dir space's infra
// sibling. Tenant: org "local", owner the space itself (Decision 13's
// managed-space tenant).
func applySpawnClient(abs string) (spawnv1.Client, spawnv1.Tenant, error) {
	explicit := os.Getenv("HUB_SPAWN_CONTROLLER_URL")
	base := explicit
	if base == "" {
		base = "http://127.0.0.1:8877"
	}
	token := os.Getenv("HUB_SERVICE_TOKEN")
	if token == "" {
		token = supervisorAuthFromEnv()
	}
	if token == "" {
		if auth, err := stack.ReadSecrets(filepath.Join(stack.InfraSpaceDir(abs), "supervisor.auth")); err == nil {
			token = strings.TrimSpace(auth["HUB_SUPERVISOR_AUTH"])
		}
	}
	if token == "" && explicit == "" {
		return nil, spawnv1.Tenant{}, errors.New(locale.T(locale.Operator(), "hubctl.explain-no-controller"))
	}
	return spawnv1.NewHTTPClient(base, token, nil), spawnv1.Tenant{Org: "local", Owner: filepath.Base(abs)}, nil
}

func refreshSharedRuntimeTokens(dir, environment string) error {
	if infra := stack.InfraSpaceDir(dir); infra != dir {
		if err := stack.EnrollSiblingRuntimeTokens(infra, environment); err != nil {
			return err
		}
		return stack.RefreshAutomationRouting(infra, environment)
	}
	return nil
}

// respawnRuntime asks the supervisor to replace the space's runtime container
// — the recreate/respawn effect for a supervised runtime: an idle container
// is re-created from the published revision so env-carried config applies; a
// busy one converges on its next spawn. A space without a supervisor endpoint
// has no runtime to respawn, which is a no-op rather than a failure.
func respawnRuntime(ctx context.Context, dir, env string) error {
	settings, err := stack.ReadEffective(dir, env)
	if err != nil {
		return err
	}
	// Apply runs on the operator's host: the recorded supervisor_url names
	// the in-network endpoint, so an explicit operator endpoint wins.
	endpoint := strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"))
	if endpoint == "" {
		endpoint = strings.TrimSpace(settings.SupervisorURL)
	}
	if endpoint == "" {
		return nil
	}
	auth := supervisorAuthFromEnv()
	if auth == "" {
		if secrets, err := stack.ReadSecrets(filepath.Join(stack.InfraSpaceDir(dir), "supervisor.auth")); err == nil {
			auth = strings.TrimSpace(secrets["HUB_SUPERVISOR_AUTH"])
		}
	}
	body, _ := json.Marshal(map[string]string{
		"principal_id": settings.User,
		"context_id":   settings.User,
	})
	post := func(path string) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(endpoint, "/")+path, bytes.NewReader(body)) // #nosec G704 -- operator-configured hub endpoint
		if err != nil {
			return 0, err
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		// The respawn gate binds the request to the digest the apply just
		// committed — the supervisor authorizes it only while that pointer is live.
		if pointer, err := os.ReadFile(filepath.Join(dir, "generated", "passports", "current")); err == nil { // #nosec G703 -- fixed pointer filename under the operator's space dir
			if reference := strings.TrimSpace(string(pointer)); strings.HasPrefix(reference, "sha256:") {
				req.Header.Set("X-Hub-Apply-Revision", reference)
			}
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := respawnHTTPClient.Do(req) // #nosec G704 -- operator-configured hub endpoint
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}
	// Recreate needs a real container replacement for env-carried leaves —
	// the /v1/respawn route replaces the container; an older supervisor image
	// answers 404 and falls back to the deferred restart-request marker.
	if status, err := post("/v1/respawn"); err != nil {
		return err
	} else if status == http.StatusNotFound {
		if fallback, err := post("/v1/restart-request"); err != nil {
			return err
		} else if fallback >= 300 {
			return fmt.Errorf("supervisor restart-request: %d", fallback)
		}
		return nil
	} else if status >= 300 {
		return fmt.Errorf("supervisor respawn: %d", status)
	}
	return nil
}
