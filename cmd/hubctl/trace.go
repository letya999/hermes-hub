package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/diagnostics"
)

// Docker access for the trace stays behind seams so tests can stub the daemon.
var (
	traceDockerNames = func(ctx context.Context, principal, contextID, environment string) ([]string, error) {
		key := sha256.Sum256([]byte(principal + "\x00" + contextID + "\x00gateway"))
		selectors := [][]string{
			{"--filter", "label=com.docker.compose.project=hermes-hub-" + contextID + "-" + environment},
			{"--filter", "label=hermes-hub.context=" + hex.EncodeToString(key[:]), "--filter", "network=hermes-hub-agent-" + contextID + "-" + environment},
		}
		var names []string
		for _, selector := range selectors {
			args := append([]string{"ps", "-a"}, selector...)
			args = append(args, "--format", "{{.ID}}")
			data, err := diagnostics.Docker(ctx, args...)
			if err != nil {
				return nil, err
			}
			names = append(names, strings.Fields(string(data))...)
		}
		return names, nil
	}
	traceDockerLogs = func(ctx context.Context, name string) ([]byte, error) {
		return diagnostics.Docker(ctx, "logs", "--timestamps", "--tail", "1001", name)
	}
)

// runTrace renders `hubctl trace <job_id>`: one chronological, redacted feed
// of every line the job touched across supervisor log, the owner's space logs
// and managed containers (SPEC-0053 R3.5).
func runTrace(ctx context.Context, out io.Writer, jobID, user, spacesRoot string, environments ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	environment := "prod"
	if len(environments) > 0 {
		environment = environments[0]
	}
	events, err := diagnostics.Trace(ctx, diagnostics.TraceOptions{
		JobID:       jobID,
		Environment: environment,
		Home: func(contextID, environment string) (string, error) {
			if !identity.ValidID(contextID) {
				return "", os.ErrInvalid
			}
			dir := filepath.Join(spacesRoot, contextID)
			settings, err := stack.ReadEffective(dir, environment)
			if err != nil {
				// Trace is diagnostics: a space that was never applied (or
				// whose published revision is broken) must still be
				// inspectable — the logs under the space dir exist
				// regardless of whether an effective view resolves.
				if _, statErr := os.Stat(dir); statErr == nil || os.IsNotExist(statErr) { // #nosec G703 -- contextID is validated and Trace checks the owned path.
					return dir, nil
				} else {
					return "", statErr
				}
			}
			return stack.RuntimeHermesHome(dir, settings), nil
		},
		User:          user,
		SpacesRoot:    spacesRoot,
		SupervisorLog: filepath.Join(filepath.Dir(spacesRoot), ".local", "diagnostics", "supervisor.log"),
		DockerNames:   traceDockerNames,
		DockerLogs:    traceDockerLogs,
	})
	if err != nil {
		return err
	}
	for _, e := range events {
		stamp := "-"
		if !e.Time.IsZero() {
			stamp = e.Time.UTC().Format("2006-01-02T15:04:05Z")
		}
		fmt.Fprintf(out, "%s  %-28s  %s\n", stamp, e.Source, e.Line)
	}
	return nil
}
