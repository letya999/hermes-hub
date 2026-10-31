package devcheck

import (
	"context"
	"strings"
	"testing"
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
