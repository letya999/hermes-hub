//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Local dirty-source probe: real ToolHub, supervisor and two managed Hermes
// containers; only the model and channel are synthetic. It does not claim an
// exact-HEAD image gate, which TestM32RealLifecycleMemoryIsolation enforces.
func TestM32LocalConcurrentWriters(t *testing.T) {
	image, script := os.Getenv("HUB_M32_IMAGE"), os.Getenv("HUB_M32_SCRIPT_IMAGE")
	if image == "" || script == "" {
		t.Skip("set HUB_M32_IMAGE and HUB_M32_SCRIPT_IMAGE for local container probe")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	provider, release, entered := m32HeldProvider(t, func(json.RawMessage) {})
	defer release()
	port := provider.Listener.Addr().(*net.TCPAddr).Port
	f, err := devcheck.NewM32Fixture(t.TempDir(), "dev", fmt.Sprintf("http://host.docker.internal:%d/v1", port), suffix)
	m32Require(t, err)
	h := newM32Lifecycle(t, ctx, image, "dev", f)
	stop := h.startToolHub(t, ctx)
	defer stop()
	var runs [2]api.RunContext
	outcomes := make(chan error, 2)
	for i, user := range f.Users {
		job := runtime.ExecuteRequest{Envelope: f.Auth[user], JobID: "m32-overlap-" + strconv.Itoa(i), IdempotencyKey: "m32-overlap-" + strconv.Itoa(i), OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", Trigger: "message", Text: "Synthetic concurrent writer acceptance"}
		go func() { _, err := h.execute(ctx, job); outcomes <- err }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case err := <-outcomes:
			t.Fatalf("managed run ended before both provider requests: %v", err)
		case <-ctx.Done():
			t.Fatal("managed provider overlap timed out")
		}
	}
	for i := range runs {
		runs[i] = h.waitRun(t, ctx, "m32-overlap-"+strconv.Itoa(i))
		authority := task.NewHTTPClient(h.supervisor.URL, h.control, nil)
		for j := 0; j < 100 && (runs[i].Status != "running" || runs[i].RuntimeGeneration == "" || runs[i].RuntimeGeneration != runs[i].CurrentGeneration); j++ {
			time.Sleep(100 * time.Millisecond)
			runs[i], err = authority.Run(ctx, "m32-overlap-"+strconv.Itoa(i))
			m32Require(t, err)
		}
		if runs[i].Status != "running" || runs[i].RuntimeGeneration == "" || runs[i].RuntimeGeneration != runs[i].CurrentGeneration || runs[i].Environment != "dev" || !api.MayUseOwnerMemory(runs[i]) || runs[i].Envelope.PrincipalID != f.Users[i] {
			t.Fatal("run authority did not confirm both active owner runs")
		}
	}
	var sessions [2]*mcp.ClientSession
	for i, user := range f.Users {
		sessions[i] = h.session(t, ctx, user, runs[i])
		defer sessions[i].Close()
	}
	type result struct {
		start, end time.Time
		err        error
		bad        bool
	}
	var got [2]result
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, user := range f.Users {
		wg.Add(1)
		go func(i int, user string) {
			defer wg.Done()
			<-start
			got[i].start = time.Now().UTC()
			res, err := sessions[i].CallTool(ctx, &mcp.CallToolParams{Name: "tool_call", Arguments: map[string]any{"tool": "memory_card", "arguments": map[string]any{"op": "remember", "name": "m32-overlap-" + user, "text": "synthetic-m32-overlap-" + user}}})
			got[i].end = time.Now().UTC()
			got[i].err, got[i].bad = err, res == nil || res.IsError
		}(i, user)
	}
	close(start)
	wg.Wait()
	for i := range got {
		if got[i].err != nil || got[i].bad {
			t.Fatalf("owner %d ToolHub memory_card failed: %v", i, got[i].err)
		}
	}
	if !got[0].start.Before(got[1].end) || !got[1].start.Before(got[0].end) {
		t.Fatalf("ToolHub writer calls did not overlap: intervals=%v/%v and %v/%v", got[0].start, got[0].end, got[1].start, got[1].end)
	}
	for i, user := range f.Users {
		body, err := os.ReadFile(filepath.Join(f.Root, "vaults", user, "runtime", "memory", "writes.jsonl"))
		m32Require(t, err)
		count := 0
		for _, line := range strings.Split(string(body), "\n") {
			var row struct {
				Op        string `json:"op"`
				Status    string `json:"status"`
				Target    string `json:"target"`
				SourceRef string `json:"source_ref"`
			}
			if json.Unmarshal([]byte(line), &row) != nil || row.Op != "remember" || row.Status != "ok" || row.SourceRef != runs[i].JobID+"/"+runs[i].RunID {
				continue
			}
			card, err := os.ReadFile(filepath.Join(f.Root, "vaults", user, filepath.FromSlash(row.Target)))
			m32Require(t, err)
			if strings.Count(string(card), "synthetic-m32-overlap-"+user) != 1 {
				t.Fatal("owner card missing or duplicated")
			}
			count++
		}
		if count != 1 {
			t.Fatalf("owner %d has %d confirmed write journal rows", i, count)
		}
	}
	var admitted, completed [2]time.Time
	auditBody, err := os.ReadFile(filepath.Join(f.Root, "audit.jsonl"))
	m32Require(t, err)
	for _, line := range strings.Split(string(auditBody), "\n") {
		var row struct {
			At           time.Time `json:"at"`
			Kind         string    `json:"kind"`
			Phase        string    `json:"phase"`
			CapabilityID string    `json:"capability_id"`
			JobID        string    `json:"job_id"`
		}
		if json.Unmarshal([]byte(line), &row) != nil || row.Kind != "tool-call" || row.CapabilityID != "memory.write" {
			continue
		}
		for i := range runs {
			if row.JobID == runs[i].JobID {
				switch row.Phase {
				case "admitted":
					admitted[i] = row.At
				case "completed":
					completed[i] = row.At
				}
			}
		}
	}
	if admitted[0].IsZero() || admitted[1].IsZero() || completed[0].IsZero() || completed[1].IsZero() || !admitted[0].Before(completed[1]) || !admitted[1].Before(completed[0]) {
		t.Fatalf("memory.write audit phases did not overlap: admitted=%v completed=%v", admitted, completed)
	}
	t.Logf("two active runs; ToolHub calls and memory.write audit windows overlap; each owner has one card and one ok journal row")
	release()
	for i := 0; i < 2; i++ {
		select {
		case err := <-outcomes:
			m32Require(t, err)
		case <-ctx.Done():
			t.Fatal("managed runs did not finish")
		}
	}
}
