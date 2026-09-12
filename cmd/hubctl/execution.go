package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var executionDockerOutput = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "docker", args...).Output()
}

func selectExecution(ctx context.Context, dir, sourceRoot, spool string, selection stack.ExecutionSelection, auth, image string, apply bool) error {
	if spool == "" {
		return errors.New("--spool must name the mounted gateway spool; stop the gateway before apply")
	}
	if err := selection.Validate(); err != nil {
		return err
	}
	settings, err := stack.ReadEnvironment(dir, selection.Environment)
	if err != nil {
		return err
	}
	if settings.User != selection.User {
		return errors.New("selected directory belongs to another user")
	}
	if apply {
		compose := filepath.Join(dir, "generated", "compose."+selection.Environment+".yaml")
		out, err := executionDockerOutput(ctx, "compose", "-f", compose, "ps", "-q", "--status", "running")
		if err != nil {
			return errors.New("cannot verify stopped context services")
		}
		if strings.TrimSpace(string(out)) != "" {
			return errors.New("stop and drain the selected communication/static runtime services before apply")
		}
		if err := checkSelectedSpool(ctx, compose, spool); err != nil {
			return err
		}
		endpoint := selection.SupervisorURL
		if endpoint == "" {
			endpoint = settings.SupervisorURL
		}
		if endpoint == "" {
			endpoint = strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL"))
		}
		if endpoint != "" {
			if err := checkSupervisorStopped(ctx, endpoint, auth, selection.User); err != nil {
				return err
			}
		}
	}
	audit, err := migration.AuditExecutionSpool(spool, selection.User)
	if err != nil {
		return err
	}
	// Rollback cannot transfer an unknown external outcome to a different executor.
	if apply && audit.Uncertain != 0 {
		return errors.New("reconcile uncertain jobs before selecting another executor")
	}
	if apply {
		snapshot, err := migration.InspectNativeCron(ctx, dir, image, executionDockerOutput)
		if err != nil {
			return err
		}
		audit.NativeCron = &snapshot
	}
	report, err := migration.SelectExecution(dir, selection, audit, apply)
	if err != nil {
		return err
	}
	// Rendering is explicit and reversible. A failure leaves the gateway stopped
	// with the durable selection/report available for repair, never half-executed work.
	if apply {
		if err := stack.RenderEnvironment(dir, sourceRoot, selection.Environment); err != nil {
			return fmt.Errorf("selection saved; render failed with services stopped: %w", err)
		}
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}

func checkSelectedSpool(ctx context.Context, compose, spool string) error {
	ids, err := executionDockerOutput(ctx, "compose", "-f", compose, "ps", "-a", "-q", "communication-hub")
	if err != nil {
		return errors.New("cannot identify selected gateway volume")
	}
	containers := strings.Fields(string(ids))
	if len(containers) != 1 {
		return errors.New("exactly one stopped selected gateway is required for migration")
	}
	body, err := executionDockerOutput(ctx, "inspect", "--format", "{{json .Mounts}}", containers[0])
	var mounts []struct{ Source, Destination, Type string }
	if err != nil || len(body) > 64*1024 || json.Unmarshal(body, &mounts) != nil {
		return errors.New("selected gateway mounts unavailable")
	}
	actual, err := filepath.EvalSymlinks(spool)
	if err != nil {
		return errors.New("actual mounted spool unavailable")
	}
	actual, err = filepath.Abs(actual)
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		if mount.Destination == "/data" {
			if (mount.Type != "volume" && mount.Type != "bind") || filepath.Clean(mount.Source) != actual {
				return errors.New("--spool is not the selected gateway's actual data volume")
			}
			return nil
		}
	}
	return errors.New("selected gateway lacks the durable data volume")
}

func checkSupervisorStopped(ctx context.Context, endpoint, auth, user string) error {
	if auth == "" {
		return errors.New("supervisor authentication required for migration verification")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/runtimes", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return errors.New("supervisor unavailable; cannot prove drained runtime")
	}
	defer response.Body.Close()
	var runtimes []supervisor.Runtime
	if response.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(nil, response.Body, 1<<20)).Decode(&runtimes) != nil {
		return errors.New("invalid supervisor migration status")
	}
	for _, runtime := range runtimes {
		if runtime.ContextID == user && (runtime.State != supervisor.Stopped || runtime.Leases != 0 || runtime.Desired) {
			return errors.New("selected supervisor runtime is not drained/stopped")
		}
	}
	return nil
}
