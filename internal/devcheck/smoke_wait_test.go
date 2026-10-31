package devcheck

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"text/template"
	"time"
)

func TestSmokeWaitHealthyFailsFastOnExitedContainer(t *testing.T) {
	err := smokeWaitHealthy(context.Background(),
		func() bool { return false },
		func(name string) string {
			if name == "hermes-smoke-x-browser" {
				return "exited"
			}
			return "running"
		},
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
