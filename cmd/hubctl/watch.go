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
	Timeout        time.Duration
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

func runWatch(ctx context.Context, out io.Writer, o watchOptions) error {
	if o.Scope != "" {
		return runWatchScope(ctx, out, o)
	}
	last := ""
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
		if watchTerminal(status) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.Interval):
		}
	}
}

// runWatchScope streams per-runtime state transitions for a scope; it has no
// terminal state of its own and ends when the caller cancels (clean exit).
func runWatchScope(ctx context.Context, out io.Writer, o watchOptions) error {
	last := map[string]string{}
	for {
		states, err := o.fetchRuntimes(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		for ctxID, state := range states {
			if last[ctxID] != state {
				last[ctxID] = state
				fmt.Fprintf(out, "%s runtime %s %s\n", time.Now().UTC().Format(time.RFC3339), ctxID, state)
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
