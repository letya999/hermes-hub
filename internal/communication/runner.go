package communication

import (
	"bytes"
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/stack"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type Runner interface {
	Run(context.Context, Job, User) (string, error)
}

type outcomeRunner interface {
	RunOutcome(context.Context, Job) (RunOutcome, error)
}

type HermesRunner struct {
	Command string
	Timeout time.Duration
}

func (r HermesRunner) Run(ctx context.Context, job Job, user User) (string, error) {
	if job.Text == "" {
		return "", errors.New("empty Hermes prompt")
	}
	if r.Command == "" {
		r.Command = "hermes"
	}
	if r.Timeout <= 0 {
		r.Timeout = 120 * time.Second
	}
	if err := os.MkdirAll(user.StateDir, 0700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(user.WorkspaceDir, 0700); err != nil {
		return "", err
	}
	jobCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(jobCtx, r.Command, "-z", job.Text)
	cmd.Dir = user.WorkspaceDir
	cmd.Env = processEnv(user.Env)
	cmd.Env = append(cmd.Env,
		"HERMES_HOME="+filepath.Join(user.StateDir, "hermes"),
		"HOME="+filepath.Join(user.StateDir, "home"),
		"HUB_STATE="+user.StateDir,
		"HUB_WORKSPACE="+user.WorkspaceDir,
		"HUB_USER_ID="+user.ID,
		"HUB_ORGANIZATION_ID="+job.OrganizationID,
		"HUB_FEATURES="+strings.Join(user.Features, ","),
		"HUB_DEFER_RUNTIME_RESTART=true",
	)
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, io.Discard
	if err := cmd.Run(); err != nil {
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			return "", errors.New("hermes timed out")
		}
		return "", errors.New("hermes invocation failed")
	}
	response := strings.TrimSpace(stdout.String())
	if response == "" {
		return "", errors.New("hermes returned an empty response")
	}
	return response, nil
}

func processEnv(userEnv map[string]string) []string {
	env := []string{}
	for _, key := range []string{"PATH", "Path", "SystemRoot", "TEMP", "TMP", "LANG", "LC_ALL", "TZ", "HUB_ORG_ACTIONS", "HUB_SELF_ENV_KEYS", "HUB_PROTECTED_ENV_KEYS"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	keys := make([]string, 0, len(userEnv))
	for key := range userEnv {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if envNamePattern.MatchString(key) && !gatewayOwnedEnv[key] && !stack.GatewayOwnedSecret(key) {
			env = append(env, key+"="+userEnv[key])
		}
	}
	return env
}
