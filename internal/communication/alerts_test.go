package communication

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func alertTestGateway(t *testing.T) (*Gateway, User) {
	t.Helper()
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alice := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	g := &Gateway{
		config: Config{Users: []User{alice}},
		spool:  spool,
		now:    time.Now,
	}
	provisionTestIdentity(t, &g.config)
	return g, alice
}

func pendingAlerts(t *testing.T, g *Gateway) []string {
	t.Helper()
	names, err := g.spool.fsys.ReadDir(g.spool.root + "/outbox/pending")
	if err != nil {
		return nil
	}
	return names
}

func TestAlertOwnerRateBound(t *testing.T) {
	g, alice := alertTestGateway(t)
	for i := 0; i < ownerAlertLimit+3; i++ {
		g.alertOwner(alice, "job-failed", fmt.Sprint(i), "failed")
	}
	if got := len(pendingAlerts(t, g)); got != ownerAlertLimit {
		t.Fatalf("rate bound violated: %d alerts enqueued, want %d", got, ownerAlertLimit)
	}
	if g.alertLedger.suppressed != 3 {
		t.Fatalf("suppressed=%d want 3", g.alertLedger.suppressed)
	}

	// The window rolls: the suppressed count flushes as ONE summary line,
	// then normal alerting resumes.
	g.alertLedger.windowStart = g.now().Add(-ownerAlertWindow - time.Minute)
	g.alertOwner(alice, "job-failed", "fresh", "failed")
	if got := len(pendingAlerts(t, g)); got != ownerAlertLimit+2 {
		t.Fatalf("expected suppressed summary + fresh alert: %d", got)
	}
}

func TestAlertJobFailedEnqueuesOnce(t *testing.T) {
	g, _ := alertTestGateway(t)
	job := Job{ID: "job-1", UserID: "alice"}
	g.alertJobFailed(job)
	g.alertJobFailed(job) // same dedup key — enqueue is idempotent
	got := pendingAlerts(t, g)
	if len(got) != 1 {
		t.Fatalf("dedup failed: %d alerts", len(got))
	}
	if !strings.HasPrefix(got[0], "alert-job-failed-job-1") {
		t.Fatalf("alert id %q lacks bounded dedup key", got[0])
	}
}

func TestAlertCrashBudgetsDedupAndEpoch(t *testing.T) {
	g, _ := alertTestGateway(t)
	g.config.Supervised = true
	g.config.RuntimeAuth = "tok"

	crashes := 3
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"runtimes": []any{map[string]any{
				"principal_id": "alice", "context_id": "ctx", "generation": "g1",
				"phase": "failed", "crash_count": crashes,
				"conditions": []any{
					map[string]any{"name": "ready", "ok": false, "reason": "readiness_unavailable"},
					map[string]any{"name": "crash_budget", "ok": false, "reason": "budget_exhausted"},
				},
			}},
		})
	}))
	defer srv.Close()
	g.config.RuntimeURL = srv.URL

	g.alertCrashBudgets(t.Context())
	g.alertCrashBudgets(t.Context()) // same epoch — deduped
	if got := len(pendingAlerts(t, g)); got != 1 {
		t.Fatalf("wedged state alerted %d times, want 1", got)
	}

	crashes = 4 // a new crash epoch re-alerts
	g.alertCrashBudgets(t.Context())
	if got := len(pendingAlerts(t, g)); got != 2 {
		t.Fatalf("new epoch did not re-alert: %d", got)
	}
}

func TestGatewayMetricsRender(t *testing.T) {
	view := GatewayStatusView{
		Queue:    SpoolQueueStatus{Pending: 2, Failed: 1},
		Outbox:   OutboxStatus{Pending: 3, OldestPendingSeconds: 42},
		Routines: RoutinesStatus{State: "ok", Enabled: 4, Overdue: 1},
	}
	var out strings.Builder
	view.Metrics(&out)
	for _, want := range []string{
		`hub_gateway_queue{state="pending"} 2`,
		`hub_gateway_queue{state="failed"} 1`,
		`hub_gateway_outbox{state="pending"} 3`,
		"hub_gateway_outbox_oldest_seconds 42",
		"hub_gateway_routines_overdue 1",
		"hub_gateway_routines_known 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, out.String())
		}
	}
}
