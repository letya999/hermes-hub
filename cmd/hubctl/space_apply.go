package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/internal/stack"
)

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
	rev, err := migration.CompileSources(abs, *env, time.Now().UTC())
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
	changes := passport.Diff(prev, next)
	actions := reconcile.Plan(changes)
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
func applyConfirm(ctx context.Context, abs, rootAbs, env string, rev passport.Revision, prevID string, actions []reconcile.Action, handlers reconcile.Handlers) (reconcile.Report, error) {
	var report reconcile.Report
	if err := rev.Publish(abs); err != nil {
		return report, fmt.Errorf("space apply: %w", err)
	}
	// Reconcile acts on generated compose files — they must describe the
	// revision just published, or rebuild/recreate hit "no such service".
	if err := stack.RenderEnvironment(abs, rootAbs, env); err != nil {
		return report, fmt.Errorf("space apply: render: %w", err)
	}
	// The published grants revision is the managed-rights authority: stamp
	// the space's ToolHub profiles + ledger from it before reconcile acts —
	// a reconcile failure re-derives the previous revision's allow so the
	// ledger never advertises rights the active passport does not carry.
	derive := func(revision passport.Revision) error {
		docs, err := catalog.LoadPinned()
		if err != nil {
			return err
		}
		return migration.DeriveSpaceRights(abs, filepath.Base(abs), revision, docs, time.Now().UTC())
	}
	if err := derive(rev); err != nil {
		return report, fmt.Errorf("space apply: rights derivation: %w", err)
	}
	rollback := func() error {
		if prevID == "" {
			return passport.PointCurrent(abs, "")
		}
		return passport.PointCurrent(abs, prevID)
	}
	report = reconcile.Run(actions, handlers, rollback, rev.ID, prevID)
	if report.Failed != nil {
		if prevID != "" {
			if b, err := os.ReadFile(filepath.Join(abs, "generated", "passports", prevID, "grants.yaml")); err == nil {
				_ = derive(passport.Revision{Docs: map[string][]byte{"grants": b}}) // best-effort: ledger returns to the rolled-back allow
			}
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
				args = append(args, service)
			}
			return docker(args...)
		},
		Recreate: func(service string) error {
			return docker("compose", "-f", composeFile, "up", "-d", "--wait", "--wait-timeout", "180", "--force-recreate", "--no-deps", service)
		},
		// One sweep covers every instance the new revision dropped:
		// containers whose services no longer exist in the just-rendered
		// file are compose orphans.
		Remove: func() error {
			return docker("compose", "-f", composeFile, "up", "-d", "--remove-orphans", "--wait", "--wait-timeout", "180")
		},
		Respawn: func() error {
			return respawnRuntime(ctx, dir)
		},
		Live: map[string]func() error{},
	}
}

// respawnRuntime marks the space's managed runtime for a deferred restart —
// the supervisor drains it when the current turn finishes. A space without a
// supervisor endpoint has no runtime to respawn, which is a no-op rather
// than a failure.
func respawnRuntime(ctx context.Context, dir string) error {
	settings, err := stack.Read(dir)
	if err != nil {
		return err
	}
	endpoint := strings.TrimSpace(settings.SupervisorURL)
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"))
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(endpoint, "/")+"/v1/restart-request", bytes.NewReader(body)) // #nosec G704 -- operator-configured hub endpoint
	if err != nil {
		return err
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req) // #nosec G704 -- operator-configured hub endpoint
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("supervisor restart-request: %s", resp.Status)
	}
	return nil
}
