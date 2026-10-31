package devcheck

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"text/template"
	"time"
)

func TestSmokeWaitHealthyFailsFastOnExitedContainer(t *testing.T) {
	err := smokeWaitHealthy(context.Background(),
		func() error { return errors.New("unhealthy") },
		func(name string) string {
			if name == "hermes-smoke-x-browser" {
				return "exited"
			}
			return "running"
		},
		func(string) {},
		[]string{"hermes-smoke-x", "hermes-smoke-x-browser"},
		50*time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "hermes-smoke-x-browser") || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("expected dead-container error naming the sidecar, got %v", err)
	}
}

func TestSmokeStatusFormatSurfacesExitAndOOM(t *testing.T) {
	// A dead container must surface why — the CI sidecar was seen exiting ~1s
	// after start on both dev and prod, and Status alone cannot distinguish
	// an OOM kill from a process error.
	type state struct {
		Running   bool
		Status    string
		ExitCode  int
		OOMKilled bool
	}
	render := func(s state) string {
		var buf bytes.Buffer
		tmpl := template.Must(template.New("status").Parse(smokeStatusFormat))
		if err := tmpl.Execute(&buf, struct{ State state }{State: s}); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if got := render(state{Running: true, Status: "running"}); got != "running" {
		t.Fatalf("running container must report %q, got %q", "running", got)
	}
	got := render(state{Status: "exited", ExitCode: 1})
	if !strings.Contains(got, "exited") || !strings.Contains(got, "exit=1") || !strings.Contains(got, "oom=") {
		t.Fatalf("dead container must report status, exit code and oom flag, got %q", got)
	}
}

func TestDumpSmokeLogsRequestsFullLogs(t *testing.T) {
	// The sidecar's startup chatter alone exceeds a tail window — a bounded
	// dump cut the first child death out of the CI failure log entirely.
	var calls [][]string
	dumpSmokeLogs(func(args ...string) error {
		calls = append(calls, append([]string{}, args...))
		return nil
	}, "runtime-a", "browser-a")
	if len(calls) != 2 {
		t.Fatalf("expected a log pull per container, got %v", calls)
	}
	for i, name := range []string{"runtime-a", "browser-a"} {
		args := calls[i]
		if len(args) != 2 || args[0] != "logs" || args[1] != name {
			t.Fatalf("expected plain `docker logs %s`, got %v", name, args)
		}
	}
}

func TestSmokeWaitHealthyRestartsExitedContainerOnce(t *testing.T) {
	// CI evidence: the display stack was observed fully up (DevTools listening)
	// then the X server died ~1s in — an environmental race, not a defect the
	// gate should grind 90s against. One bounded restart absorbs it; a second
	// death is a real failure.
	deaths := map[string]int{}
	restarts := []string{}
	status := func(name string) string {
		if name == "browser-a" && deaths[name] < 2 {
			deaths[name]++
			return "exited exit=1 oom=false"
		}
		return "running"
	}
	err := smokeWaitHealthy(context.Background(),
		func() error { return errors.New("unhealthy") },
		status,
		func(name string) { restarts = append(restarts, name) },
		[]string{"runtime-a", "browser-a"},
		3*time.Second,
	)
	if err == nil || !strings.Contains(err.Error(), "browser-a") {
		t.Fatalf("second death must still fail naming the container, got %v", err)
	}
	if len(restarts) != 1 || restarts[0] != "browser-a" {
		t.Fatalf("expected exactly one restart of browser-a, got %v", restarts)
	}
}

func TestSmokeWaitHealthyTimeoutReportsProbeError(t *testing.T) {
	// The runtime's `hub-runtime health` exits nonzero with the reason on
	// stderr — discarding it left the CI timeout undiagnosable even after the
	// sidecar recovered.
	err := smokeWaitHealthy(context.Background(),
		func() error { return errors.New("open /state/runtime.json: no such file") },
		func(string) string { return "running" },
		func(string) {},
		[]string{"runtime-b"},
		50*time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "runtime.json") {
		t.Fatalf("timeout error must carry the probe failure reason, got %v", err)
	}
}
