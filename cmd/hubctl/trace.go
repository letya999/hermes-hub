package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/diagnostics"
)

// Docker access for the trace stays behind seams so tests can stub the daemon.
var (
	traceDockerNames = func(ctx context.Context) ([]string, error) {
		data, err := dockerOutput(ctx, "ps", "-a", "--filter", "label=hermes-hub.owner", "--format", "{{.Names}}")
		if err != nil {
			return nil, err
		}
		var names []string
		for _, name := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if name = strings.TrimSpace(name); name != "" {
				names = append(names, name)
			}
		}
		return names, nil
	}
	traceDockerLogs = func(ctx context.Context, name string) ([]byte, error) {
		return dockerOutput(ctx, "logs", "--tail", "5000", name)
	}
)

// runTrace renders `hubctl trace <job_id>`: one chronological, redacted feed
// of every line the job touched across supervisor log, the owner's space logs
// and managed containers (SPEC-0053 R3.5).
func runTrace(ctx context.Context, out io.Writer, jobID, user, spacesRoot string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	events, err := diagnostics.Trace(ctx, diagnostics.TraceOptions{
		JobID:         jobID,
		User:          user,
		SpacesRoot:    spacesRoot,
		SupervisorLog: filepath.Join(filepath.Dir(spacesRoot), ".local", "supervisor.log"),
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
