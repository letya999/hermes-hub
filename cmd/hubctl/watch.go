package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// watchOptions drives `hubctl watch`. Job mode polls the supervisor job view
// and/or the durable mapping file; Scope mode polls the runtime list plus all
// mapping files in the scope's spool.
type watchOptions struct {
	Job            string
	Scope          string
	SpoolDir       string
	SupervisorURL  string
	SupervisorAuth string
	Interval       time.Duration
	Timeout        time.Duration // overall deadline; 0 = none
	DeliveryGrace  time.Duration // terminal→outbox-record grace; 0 = 5*Interval
	Client         *http.Client
}

// watchTerminal mirrors supervisor terminalRunStatus; keep in sync.
func watchTerminal(status string) bool {
	return commodel.TerminalStatus(status)
}

func (o watchOptions) fetchJob(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.SupervisorURL+"/v1/jobs/"+o.Job, nil)
	if err != nil {
		return "", err
	}
	if o.SupervisorAuth != "" {
		req.Header.Set("Authorization", "Bearer "+o.SupervisorAuth)
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("supervisor job %s: HTTP %d", o.Job, resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.Status, nil
}

// fetchRuntimes returns per-context states for runtimes owned by the scope.
func (o watchOptions) fetchRuntimes(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.SupervisorURL+"/v1/runtimes", nil)
	if err != nil {
		return nil, err
	}
	if o.SupervisorAuth != "" {
		req.Header.Set("Authorization", "Bearer "+o.SupervisorAuth)
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("supervisor runtimes: HTTP %d", resp.StatusCode)
	}
	var list []struct {
		PrincipalID string `json:"principal_id"`
		ContextID   string `json:"context_id"`
		State       string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	states := map[string]string{}
	for _, rt := range list {
		if rt.PrincipalID == o.Scope {
			states[rt.ContextID] = string(rt.State)
		}
	}
	return states, nil
}

func (o watchOptions) fetchMapping() (string, error) {
	b, err := os.ReadFile(filepath.Join(o.SpoolDir, "mappings", commodel.FileID(o.Job)+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil // job has no durable state yet; keep watching
	}
	if err != nil {
		return "", err
	}
	var m commodel.JobMapping
	if err := json.Unmarshal(b, &m); err != nil {
		return "", nil // torn read during a write; retry next tick
	}
	return m.Status, nil
}

// fetchScopeMappings returns per-job states for the scope's durable mappings;
// the spool is the only source needed — no supervisor call.
func (o watchOptions) fetchScopeMappings() (map[string]string, error) {
	entries, err := os.ReadDir(filepath.Join(o.SpoolDir, "mappings"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	states := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(o.SpoolDir, "mappings", entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // rotated mid-scan
		}
		if err != nil {
			return nil, err
		}
		var m commodel.JobMapping
		if err := json.Unmarshal(b, &m); err != nil {
			continue // torn read during a write; retry next tick
		}
		if m.PrincipalID != o.Scope || m.JobID == "" {
			continue
		}
		states[m.JobID] = m.Status
	}
	return states, nil
}

func (o watchOptions) deliveryStatus() (string, error) {
	if o.SpoolDir == "" {
		return "", nil
	}
	pending, delivered, uncertain := false, false, false
	for _, state := range []string{"pending", "sending", "failed", "done"} {
		entries, err := os.ReadDir(filepath.Join(o.SpoolDir, "outbox", state))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(o.SpoolDir, "outbox", state, entry.Name()))
			if errors.Is(err, os.ErrNotExist) {
				continue
			} // The delivery worker may move this entry.
			if err != nil {
				return "", err
			}
			var delivery struct {
				JobID string `json:"job_id"`
			}
			if err := json.Unmarshal(body, &delivery); err != nil {
				return "", err
			}
			if delivery.JobID != o.Job {
				continue
			}
			switch state {
			case "failed":
				uncertain = true
			case "done":
				delivered = true
			default:
				pending = true
			}
		}
	}
	if uncertain {
		return "uncertain", nil
	}
	if pending {
		return "pending", nil
	}
	if delivered {
		return "delivered", nil
	}
	return "", nil
}

// deliveryGrace bounds how long a terminal job keeps waiting for its outbox
// record: supervisor completion and the gateway's durable write are not atomic.
func (o watchOptions) deliveryGrace() time.Duration {
	if o.DeliveryGrace > 0 {
		return o.DeliveryGrace
	}
	if o.Interval > 0 {
		return 5 * o.Interval
	}
	return 5 * time.Second
}

func runWatch(ctx context.Context, out io.Writer, o watchOptions) error {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	if o.Scope != "" {
		return runWatchScope(ctx, out, o)
	}
	last, lastDelivery := "", ""
	var terminalNoRecord time.Time
	for {
		var status string
		var err error
		if o.SupervisorURL != "" {
			status, err = o.fetchJob(ctx)
		} else if o.SpoolDir != "" {
			status, err = o.fetchMapping()
		}
		if err != nil {
			return err
		}
		if status != "" && status != last {
			last = status
			fmt.Fprintf(out, "%s job %s %s\n", time.Now().UTC().Format(time.RFC3339), o.Job, status)
		}
		delivery, err := o.deliveryStatus()
		if err != nil {
			return err
		}
		if delivery != "" && delivery != lastDelivery {
			lastDelivery = delivery
			fmt.Fprintf(out, "%s job %s delivery %s\n", time.Now().UTC().Format(time.RFC3339), o.Job, delivery)
		}
		if watchTerminal(status) && delivery != "pending" {
			if delivery != "" {
				return nil
			}
			if terminalNoRecord.IsZero() {
				terminalNoRecord = time.Now()
			}
			if time.Since(terminalNoRecord) >= o.deliveryGrace() {
				return nil
			}
		} else {
			terminalNoRecord = time.Time{}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.Interval):
		}
	}
}

// runWatchScope streams per-runtime (supervisor) or per-job (spool) state
// transitions for a scope; it has no terminal state of its own and ends when
// the caller cancels or the deadline hits.
func runWatchScope(ctx context.Context, out io.Writer, o watchOptions) error {
	last := map[string]string{}
	kind := "job"
	fetch := func(context.Context) (map[string]string, error) { return o.fetchScopeMappings() }
	if o.SupervisorURL != "" {
		kind = "runtime"
		fetch = o.fetchRuntimes
	}
	for {
		states, err := fetch(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		for id, state := range states {
			if last[id] != state {
				last[id] = state
				fmt.Fprintf(out, "%s %s %s %s\n", time.Now().UTC().Format(time.RFC3339), kind, id, state)
			}
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return ctx.Err()
		case <-time.After(o.Interval):
		}
	}
}
