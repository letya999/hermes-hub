package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
)

// hubctl status (issue #201): one command answering what is running, what
// failed, and how deep the queues are — assembled from the two bounded
// control-plane endpoints, never from logs or container internals. Output
// renders exactly the phase+conditions the supervisor persists; unknown
// endpoints degrade to a state line instead of a guessed answer.

type statusOptions struct {
	SupervisorURL  string
	GatewayURL     string
	Token          string
	Timeout        time.Duration
	Client         *http.Client
	SkipSupervisor bool
	SkipGateway    bool
}

type gatewayStatusWire struct {
	GeneratedAt time.Time `json:"generated_at"`
	Queue       struct {
		Pending, Running, Failed int
	} `json:"queue"`
	Outbox struct {
		Pending, Sending, Failed int
		OldestPendingSeconds     int64 `json:"oldest_pending_seconds,omitempty"`
	} `json:"outbox"`
	Routines struct {
		State                   string `json:"state"`
		Total, Enabled, Overdue int
		NextDue                 *time.Time `json:"next_due,omitempty"`
	} `json:"routines"`
}

type hubStatus struct {
	GeneratedAt time.Time              `json:"generated_at"`
	Supervisor  *supervisor.StatusView `json:"supervisor,omitempty"`
	Gateway     *gatewayStatusWire     `json:"gateway,omitempty"`
	Errors      []string               `json:"errors,omitempty"`
}

func fetchJSON(ctx context.Context, client *http.Client, url, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) // #nosec G704 -- operator-configured control-plane origin
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func collectStatus(ctx context.Context, o statusOptions) hubStatus {
	client := o.Client
	if client == nil {
		timeout := o.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	status := hubStatus{GeneratedAt: time.Now().UTC()}
	if !o.SkipSupervisor {
		var view supervisor.StatusView
		if err := fetchJSON(ctx, client, strings.TrimRight(o.SupervisorURL, "/")+"/v1/status", o.Token, &view); err != nil {
			status.Errors = append(status.Errors, "supervisor: "+err.Error())
		} else {
			status.Supervisor = &view
		}
	}
	if !o.SkipGateway {
		var view gatewayStatusWire
		if err := fetchJSON(ctx, client, strings.TrimRight(o.GatewayURL, "/")+"/v1/status", o.Token, &view); err != nil {
			status.Errors = append(status.Errors, "gateway: "+err.Error())
		} else {
			status.Gateway = &view
		}
	}
	return status
}

func conditionSummary(conds []supervisor.Condition) string {
	var parts []string
	for _, c := range conds {
		if c.OK {
			continue
		}
		parts = append(parts, c.Name+"="+c.Reason)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

func renderStatus(out io.Writer, s hubStatus) {
	w := func(format string, args ...any) { fmt.Fprintf(out, format+"\n", args...) }
	w("hub status %s", s.GeneratedAt.Format(time.RFC3339))
	if len(s.Errors) > 0 {
		for _, e := range s.Errors {
			w("  unreachable: %s", e)
		}
	}
	if sup := s.Supervisor; sup != nil {
		w("")
		w("supervisor  watchdog sweeps=%d entries=%d last=%s", sup.Watchdog.Sweeps, sup.Watchdog.Entries, sup.Watchdog.LastSweepAt.Format(time.RFC3339))
		w("%-20s %-12s %-12s %-8s %s", "scope", "phase", "readiness", "crashes", "conditions")
		rows := append([]supervisor.RuntimeStatus(nil), sup.Runtimes...)
		sort.Slice(rows, func(i, j int) bool { return rows[i].Phase < rows[j].Phase })
		for _, rt := range rows {
			readiness := "-"
			for _, c := range rt.Conditions {
				if c.Name == supervisor.CondReady {
					readiness = c.Reason
				}
			}
			w("%-20s %-12s %-12s %-8d %s", rt.PrincipalID+":"+rt.ContextID, rt.Phase, readiness, rt.CrashCount, conditionSummary(rt.Conditions))
		}
		if len(sup.Runtimes) == 0 {
			w("  (no runtimes)")
		}
		for _, hold := range sup.Holds {
			w("  hold: %s:%s reason=%s", hold.PrincipalID, hold.ContextID, hold.Reason)
		}
		var phases []string
		for p, n := range sup.Jobs.ByPhase {
			phases = append(phases, fmt.Sprintf("%s=%d", p, n))
		}
		sort.Strings(phases)
		w("jobs  total=%d unconfirmed=%d %s", sup.Jobs.Total, sup.Jobs.Unconfirmed, strings.Join(phases, " "))
		for _, j := range sup.Jobs.Open {
			w("  open: %s phase=%s %s", j.JobID, j.Phase, conditionSummary(j.Conditions))
		}
	}
	if gw := s.Gateway; gw != nil {
		w("")
		w("gateway  queue pending=%d running=%d failed=%d", gw.Queue.Pending, gw.Queue.Running, gw.Queue.Failed)
		w("outbox   pending=%d sending=%d failed=%d oldest=%ds", gw.Outbox.Pending, gw.Outbox.Sending, gw.Outbox.Failed, gw.Outbox.OldestPendingSeconds)
		routines := fmt.Sprintf("routines state=%s", gw.Routines.State)
		if gw.Routines.State == "ok" {
			routines = fmt.Sprintf("routines total=%d enabled=%d overdue=%d", gw.Routines.Total, gw.Routines.Enabled, gw.Routines.Overdue)
			if gw.Routines.NextDue != nil {
				routines += " next_due=" + gw.Routines.NextDue.Format(time.RFC3339)
			}
		}
		w("%s", routines)
	}
}

// resolveStatusToken: flag > env (HUB_SUPERVISOR_AUTH / HUB_RUNTIME_AUTH) >
// the infra space's supervisor.auth — the same control-plane token chain
// supervisorComposeEnv uses for compose interpolation.
func resolveStatusToken(dir string) string {
	if tok := supervisorAuthFromEnv(); tok != "" {
		return tok
	}
	if auth, err := stack.ReadSecrets(filepath.Join(stack.InfraSpaceDir(dir), "supervisor.auth")); err == nil {
		return strings.TrimSpace(auth["HUB_SUPERVISOR_AUTH"])
	}
	return ""
}
