package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestControlReleaseUnderQueuedAdmission(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	budget, err := api.NewWorkloadBudget(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	c.command, c.commandEnv = fakeFallbackCommands(t, d, parts[1], &calls)
	first := genericPlan(d)
	first.WorkloadID = "active"
	if err := budget.Acquire(t.Context(), first.WorkloadID); err != nil {
		t.Fatal(err)
	}
	c.workloads[first.WorkloadID] = genericWorkload{plan: first, proxyName: "active-egress", proxyVol: "active-egress-config", dockerFallback: true}
	next := genericPlan(d)
	next.WorkloadID = "queued"
	body, _ := json.Marshal(next)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequest("POST", "/admit", bytes.NewReader(body)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer synthetic-controller")
	admitted := httptest.NewRecorder()
	admitDone := make(chan struct{})
	handler := c.handler("synthetic-controller")
	go func() { handler.ServeHTTP(admitted, request); close(admitDone) }()
	deadline := time.Now().Add(3 * time.Second)
	queued := false
	for time.Now().Before(deadline) {
		_, pending := budget.Snapshot()
		if len(pending) == 1 {
			queued = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !queued {
		cancel()
		<-admitDone
		t.Fatalf("second request never queued: HTTP %d", admitted.Code)
	}
	release := httptest.NewRequest("POST", "/release", strings.NewReader(`{"workload_id":"active"}`))
	release.Header.Set("Authorization", "Bearer synthetic-controller")
	released := httptest.NewRecorder()
	releaseDone := make(chan struct{})
	go func() { handler.ServeHTTP(released, release); close(releaseDone) }()
	select {
	case <-releaseDone:
	case <-time.After(time.Second):
		t.Error("queued admission blocked release control")
		cancel()
		<-releaseDone
	}
	select {
	case <-admitDone:
	case <-time.After(5 * time.Second):
		cancel()
		<-admitDone
		t.Error("released slot did not admit queued work")
	}
	if released.Code != 204 || admitted.Code != 200 {
		t.Errorf("control/admission outcomes: release=%d admit=%d", released.Code, admitted.Code)
	}
	active, pending := budget.Snapshot()
	if len(active) != 1 || active[0] != "queued" || len(pending) != 0 {
		t.Errorf("slot not transferred exactly once: active=%v pending=%v", active, pending)
	}
}
