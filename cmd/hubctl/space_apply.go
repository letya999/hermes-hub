package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
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
// reconcile report once --confirm publishes.
type applyReceipt struct {
	Revision         string             `json:"revision"`
	PreviousRevision string             `json:"previous_revision,omitempty"`
	FirstApply       bool               `json:"first_apply,omitempty"`
	Changes          []passport.Change  `json:"changes"`
	Actions          []reconcile.Action `json:"actions"`
	Report           *reconcile.Report  `json:"report,omitempty"`
	Confirmed        bool               `json:"confirmed"`
}

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
	// rather than interleaving compile, publish, render, rights derivation
	// and reconcile into a mixed revision (SPEC-0095).
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
	docs, err := catalog.Generate()
	if err != nil {
		return err
	}
	gov := toolhub.NewGovernanceStore(filepath.Join(stack.InfraSpaceDir(abs), "runtime", "toolhub", "governance.json"))
	issued, err := toolhub.IssuedRightCeiling(gov, docs.Rights, filepath.Base(abs), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("space apply: issued rights: %w", err)
	}
	rev, err := passport.CompileAuthored(abs, *env, time.Now().UTC(), docs, stack.ReservedSiblingPorts(abs), issued)
	if err != nil {
		return err
	}
	var prev passport.Effective
	prevID := ""
	firstApply := false
	if id, err := passport.Current(abs); err == nil && id != "" {
		loaded, err := passport.LoadEffective(abs)
		if err != nil {
			return fmt.Errorf("space apply: current revision %s is unreadable: %w", id, err)
		}
		prev, prevID = loaded, id
	} else {
		firstApply = true
	}
	next, err := rev.Effective()
	if err != nil {
		return fmt.Errorf("space apply: %w", err)
	}
	if err := migration.ValidateSelectedConnectors(abs, rev.Docs["tools"], docs); err != nil {
		return fmt.Errorf("space apply: connector selection: %w", err)
	}
	if next.Stack.Identity.Organization != "" {
		lang := locale.LangOf(filepath.Join(abs, "workspace"))
		if next.Tools.Binding == nil {
			return errors.New(locale.T(lang, "hubctl.space-apply-organization-binding"))
		}
		storePath := filepath.Join(stack.InfraSpaceDir(abs), "runtime", "toolhub", "store.json")
		if _, err := os.Stat(filepath.Join(filepath.Dir(storePath), "store.db")); err == nil {
			if _, err := os.Stat(storePath); errors.Is(err, os.ErrNotExist) {
				storePath = filepath.Join(filepath.Dir(storePath), "store.db")
			}
		}
		store, err := toolhub.Load(storePath)
		if err != nil {
			return errors.New(locale.T(lang, "hubctl.space-apply-organization-policy"))
		}
		if err := store.CheckOrganizationRights(filepath.Base(abs), *env, next.Stack.Identity.Organization, next.Tools.Binding.ProfileID, next.Tools.Binding.Generation, next.Grants.Allow); err != nil {
			return errors.New(locale.T(lang, "hubctl.space-apply-organization-policy"))
		}
	}
	changes := passport.Diff(prev, next)
	if prevID != "" {
		for _, change := range changes {
			if change.Path == "stack.identity.kind" {
				return errors.New(locale.T(locale.LangOf(filepath.Join(abs, "workspace")), "hubctl.space-apply-scope-kind"))
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
		for right := range needed {
			if !slices.Contains(issued[right[0]], right[1]) || !slices.Contains(prev.Grants.Allow[right[0]], right[1]) {
				return errors.New(locale.T(locale.LangOf(filepath.Join(abs, "workspace")), "hubctl.space-apply-passport-right", right[0]+"."+right[1]))
			}
		}
	}
	actions := reconcile.Plan(changes, next.Stack.Services)
	receipt := applyReceipt{
		Revision:         rev.ID,
		PreviousRevision: prevID,
		FirstApply:       firstApply,
		Changes:          changes,
		Actions:          actions,
		Confirmed:        *confirm,
	}
	if !*confirm {
		return printApply(receipt)
	}
	rootAbs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	report, err := applyConfirm(ctx, abs, rootAbs, *env, rev, prevID, actions, applyHandlers(ctx, abs, *env))
	if err != nil {
		return err
	}
	receipt.Report = &report
	return printApply(receipt)
}

// applyConfirm publishes the revision, stamps the managed-rights ledger and
// runs the action plan.
var applyStep = func(string) {}

func applyConfirm(ctx context.Context, abs, rootAbs, env string, rev passport.Revision, prevID string, actions []reconcile.Action, handlers reconcile.Handlers) (reconcile.Report, error) {
	var report reconcile.Report
	bound, _ := rev.Value("tools", "binding.profile_id")
	profileID, _ := bound.(string)
	if profileID == "" {
		return report, fmt.Errorf("space apply: revision has no bound tools profile")
	}
	// The compiled diff, plan and derivation all assume prevID is still the
	// active revision; a pointer that moved since means this apply is stale.
	current, err := passport.Current(abs)
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return report, fmt.Errorf("space apply: current revision: %w", err)
	case errors.Is(err, os.ErrNotExist) && prevID != "":
		return report, fmt.Errorf("space apply: stale base %s: no active revision", prevID)
	case err == nil && current != prevID:
		return report, fmt.Errorf("space apply: stale base %s: revision %s is active", prevID, current)
	}
	if err := rev.Prepare(abs, applyStep); err != nil {
		return report, fmt.Errorf("space apply: %w", err)
	}
	applyStep("prepare")
	restoreRights := func() error {
		docs, err := catalog.LoadPinned()
		if err != nil {
			return err
		}
		if prevID == "" {
			return migration.RestoreSpaceRights(abs, filepath.Base(abs), env, profileID, "", map[string][]string{}, nil, docs, time.Now().UTC())
		}
		previous := filepath.Join(abs, "generated", "passports", prevID)
		grantsBody, err := os.ReadFile(filepath.Join(previous, "grants.yaml"))
		if err != nil {
			return err
		}
		toolsBody, err := os.ReadFile(filepath.Join(previous, "tools.yaml"))
		if err != nil {
			return err
		}
		allow, err := (passport.Revision{Docs: map[string][]byte{"grants": grantsBody}}).GrantAllow()
		if err != nil {
			return err
		}
		bound, _ := (passport.Revision{Docs: map[string][]byte{"tools": toolsBody}}).Value("tools", "binding.profile_id")
		previousProfileID, _ := bound.(string)
		return migration.RestoreSpaceRights(abs, filepath.Base(abs), env, previousProfileID, prevID, allow, toolsBody, docs, time.Now().UTC())
	}
	rollback := func() error {
		if err := passport.RestoreCurrent(abs, prevID, env); err != nil {
			return err
		}
		if prevID != "" {
			if err := stack.RenderEnvironment(abs, rootAbs, env); err != nil {
				return errors.Join(err, restoreRights())
			}
		}
		return restoreRights()
	}
	// Reconcile acts on generated compose files — they must describe the
	// revision just published, or rebuild/recreate hit "no such service".
	if err := stack.RenderRevisionEnvironment(abs, rootAbs, env, rev.ID); err != nil {
		return report, fmt.Errorf("space apply: render: %w", errors.Join(err, rollback()))
	}
	applyStep("render")
	// The published grants revision is the managed-rights authority: stamp
	// the space's ToolHub profiles + ledger from it before reconcile acts —
	// a reconcile failure re-derives the previous revision's allow so the
	// ledger never advertises rights the active passport does not carry.
	derive := func(revision passport.Revision) error {
		docs, err := catalog.LoadPinned()
		if err != nil {
			return err
		}
		return migration.DeriveSpaceRights(abs, filepath.Base(abs), env, revision, docs, time.Now().UTC())
	}
	if err := derive(rev); err != nil {
		return report, fmt.Errorf("space apply: rights derivation: %w", errors.Join(err, rollback()))
	}
	applyStep("rights")
	if err := rev.Activate(abs); err != nil {
		return report, fmt.Errorf("space apply: activation: %w", errors.Join(err, rollback()))
	}
	applyStep("activate")
	report = reconcile.Run(actions, handlers, rollback, rev.ID, prevID)
	if report.Failed != nil {
		if !report.RolledBack {
			return report, fmt.Errorf("space apply: %s %s failed and rollback diverged — pointer/ledger state is uncertain", report.Failed.Kind, report.Failed.Target)
		}
		return report, fmt.Errorf("space apply: %s %s failed, rolled back to %s", report.Failed.Kind, report.Failed.Target, prevID)
	}
	return report, nil
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

func applyHandlers(ctx context.Context, dir, env string) reconcile.Handlers {
	composeFile := filepath.Join(dir, "generated", "compose."+env+".yaml")
	docker := func(a ...string) error {
		return dockerCompose(ctx, dir, a...)
	}
	return reconcile.Handlers{
		Rebuild: func(service string) error {
			args := []string{"compose", "-f", composeFile, "build"}
			if service != "*" {
				settings, err := stack.ReadEffective(dir, env)
				if err != nil {
					return err
				}
				name := stack.ComposeServiceName(settings, service)
				if name == "" {
					// Shared services render in the infra space's project —
					// the same fallback Recreate uses for them.
					infraCompose := filepath.Join(stack.InfraSpaceDir(dir), "generated", "compose."+env+".yaml")
					return docker("compose", "-f", infraCompose, "build", service)
				}
				args = append(args, name)
			}
			return docker(args...)
		},
		Recreate: func(service string) error {
			settings, err := stack.ReadEffective(dir, env)
			if err != nil {
				return err
			}
			if (service == "hosting-controller" && settings.Hosting.Image == "") || (service != "hosting-controller" && settings.PassportServices[service] == "") {
				// A rollback can remove a newly created service. Converge the
				// restored project so its orphaned container is removed.
				return docker("compose", "-f", composeFile, "up", "-d", "--remove-orphans", "--wait", "--wait-timeout", "180")
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
					if err := stack.RenderEnvironment(infraDir, filepath.Dir(filepath.Dir(dir)), env); err != nil {
						return err
					}
				}
				infraCompose := filepath.Join(infraDir, "generated", "compose."+env+".yaml")
				return docker("compose", "-f", infraCompose, "up", "-d", "--wait", "--wait-timeout", "180", "--force-recreate", "--no-deps", service)
			}
			return docker("compose", "-f", composeFile, "up", "-d", "--wait", "--wait-timeout", "180", "--force-recreate", "--no-deps", name)
		},
		// One sweep covers every instance the new revision dropped:
		// containers whose services no longer exist in the just-rendered
		// file are compose orphans.
		Remove: func() error {
			return docker("compose", "-f", composeFile, "up", "-d", "--remove-orphans", "--wait", "--wait-timeout", "180")
		},
		Respawn: func() error {
			return respawnRuntime(ctx, dir, env)
		},
		Live: map[string]func() error{
			// applyConfirm renders before reconcile runs: the new
			// runtime.<env>.policy.json is already on disk and the
			// supervisor re-reads it per decision — the leaf is applied.
			"supervisor-policy": func() error { return nil },
			// The mounted model registry re-reads on change; the render
			// already swapped it in place.
			"model-relay": func() error { return nil },
			// File admission reloads /config/workspace.yaml by mtime; the
			// mounted passport file is already the new revision.
			"file-admission": func() error { return nil },
			// DeriveSpaceRights rewrote the managed profiles/ledger at
			// publish; admission reads them per call.
			"tool-policy":   func() error { return nil },
			"rights-ledger": func() error { return nil },
		},
	}
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
