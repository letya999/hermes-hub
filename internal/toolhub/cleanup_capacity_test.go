package toolhub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestFailedCleanupKeepsCapacityReserved(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	budget, err := api.NewWorkloadBudget(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	fail := true
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		if fail {
			return nil, errors.New("synthetic cleanup failure")
		}
		return nil, nil
	}
	plan := genericPlan(d)
	plan.WorkloadID = "retained"
	if err := budget.Acquire(t.Context(), plan.WorkloadID); err != nil {
		t.Fatal(err)
	}
	c.workloads[plan.WorkloadID] = genericWorkload{plan: plan, proxyName: "retained-egress", proxyVol: "retained-config"}
	if removed := c.CleanupIdle(t.Context(), time.Now().Add(2*time.Minute)); len(removed) != 0 {
		t.Errorf("failed stop reported removed: %v", removed)
	}
	active, _ := budget.Snapshot()
	if len(active) != 1 || active[0] != "retained" {
		t.Errorf("live process no longer consumes capacity: %v", active)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	if err := budget.Acquire(ctx, "replacement"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("capacity oversubscribed after failed stop: %v", err)
	}
	cancel()
	fail = false
	removed := c.CleanupIdle(t.Context(), time.Now().Add(3*time.Minute))
	if len(removed) != 1 || removed[0] != "retained" {
		t.Errorf("failed cleanup not retried: %v", removed)
	}
	active, _ = budget.Snapshot()
	if len(active) != 0 {
		t.Errorf("confirmed stop did not release capacity: %v", active)
	}
}
