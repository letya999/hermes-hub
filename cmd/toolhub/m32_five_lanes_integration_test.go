//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Five real Hermes processes must reach the held model before any is released.
// Their shared transcript store and ToolHub grants are exercised under five
// distinct job/run identities belonging to one synthetic owner.
func TestM32FiveManagedSessionsExecuteConcurrently(t *testing.T) {
	image, script := os.Getenv("HUB_M32_IMAGE"), os.Getenv("HUB_M32_SCRIPT_IMAGE")
	if image == "" || script == "" {
		t.Skip("set HUB_M32_IMAGE and HUB_M32_SCRIPT_IMAGE for local container probe")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	requests := make(chan int, 32)
	provider, release, _ := m32HeldProvider(t, func(messages json.RawMessage) {
		index := -1
		for i := 0; i < 6; i++ {
			if strings.Contains(string(messages), "Synthetic session "+strconv.Itoa(i)) {
				index = i
				break
			}
		}
		requests <- index
	})
	defer release()
	port := provider.Listener.Addr().(*net.TCPAddr).Port
	f, err := devcheck.NewM32Fixture(t.TempDir(), "dev", fmt.Sprintf("http://host.docker.internal:%d/v1", port), suffix)
	m32Require(t, err)
	h := newM32Lifecycle(t, ctx, image, "dev", f)
	stop := h.startToolHub(t, ctx)
	defer stop()
	user := f.Users[0]
	type outcome struct {
		index  int
		result runtime.ExecuteResponse
		err    error
	}
	outcomes := make(chan outcome, 6)
	for i := 0; i < 5; i++ {
		job := m32LaneJob(f.Auth[user], user, i)
		go func(index int) {
			result, err := h.execute(ctx, job)
			outcomes <- outcome{index, result, err}
		}(i)
	}
	arrived := map[int]bool{}
	for len(arrived) < 5 {
		select {
		case index := <-requests:
			if index >= 0 && index < 5 {
				arrived[index] = true
			} else {
				t.Fatalf("unexpected provider request for session %d before five lanes active", index)
			}
		case outcome := <-outcomes:
			t.Fatalf("run %d ended before five model calls overlapped: %v", outcome.index, outcome.err)
		case <-ctx.Done():
			t.Fatal("five model calls did not overlap")
		}
	}
	var runs [5]api.RunContext
	seenGeneration := map[string]bool{}
	for i := range runs {
		runs[i] = h.waitRun(t, ctx, "m32-five-"+strconv.Itoa(i))
		if runs[i].Status != "running" || !runs[i].MayUseOwnerMemory() || runs[i].RuntimeGeneration == "" || runs[i].RuntimeGeneration != runs[i].CurrentGeneration || seenGeneration[runs[i].RuntimeGeneration] {
			t.Fatalf("lane %d missing distinct active runtime generation", i)
		}
		seenGeneration[runs[i].RuntimeGeneration] = true
		var lane supervisor.Runtime
		for _, mode := range [...]string{"gateway", "gateway-2", "gateway-3", "gateway-4", "gateway-5"} {
			rt, ok, err := h.manager.Status(supervisor.Binding{PrincipalID: user, ContextID: user, RuntimeID: user, UserID: user, RuntimeMode: mode})
			m32Require(t, err)
			if ok && rt.Generation == runs[i].RuntimeGeneration {
				lane = rt
				break
			}
		}
		if lane.Address == "" {
			t.Fatalf("lane %d has no runtime MCP proxy", i)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "m32-lane-probe", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: lane.Address + "/mcp", HTTPClient: &http.Client{Transport: m32Bearer{token: h.tokens[user]}}, DisableStandaloneSSE: true}, nil)
		m32Require(t, err)
		m32SessionCall(t, ctx, session, "memory_card", map[string]any{"op": "remember", "name": "m32-five-" + strconv.Itoa(i), "text": "synthetic-five-lane-" + strconv.Itoa(i)}, true)
		m32Require(t, session.Close())
	}
	job := m32LaneJob(f.Auth[user], user, 5)
	go func() {
		result, err := h.execute(ctx, job)
		outcomes <- outcome{5, result, err}
	}()
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
waiting:
	for {
		select {
		case index := <-requests:
			if index == 5 {
				t.Fatal("sixth run reached model before a lane was free")
			}
		case outcome := <-outcomes:
			t.Fatalf("run %d ended while all lanes occupied: %v", outcome.index, outcome.err)
		case <-timer.C:
			break waiting
		}
	}
	release()
	completed := map[int]bool{}
	for i := 0; i < 6; i++ {
		select {
		case outcome := <-outcomes:
			if outcome.err != nil || outcome.result.Status != "completed" || completed[outcome.index] {
				t.Fatalf("run %d did not complete exactly once: status=%s err=%v", outcome.index, outcome.result.Status, outcome.err)
			}
			completed[outcome.index] = true
		case <-ctx.Done():
			t.Fatal("runs did not finish")
		}
	}
	journal, err := os.ReadFile(filepath.Join(f.Root, "vaults", user, "runtime", "memory", "writes.jsonl"))
	m32Require(t, err)
	for i := range runs {
		count := 0
		for _, line := range strings.Split(string(journal), "\n") {
			var row struct {
				SourceRef string `json:"source_ref"`
				Status    string `json:"status"`
			}
			if json.Unmarshal([]byte(line), &row) == nil && row.SourceRef == runs[i].JobID+"/"+runs[i].RunID && row.Status == "ok" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("lane %d has %d confirmed memory writes", i, count)
		}
	}
	audit, err := os.ReadFile(filepath.Join(f.Root, "audit.jsonl"))
	m32Require(t, err)
	for i := range runs {
		phases := map[string]int{}
		for _, line := range strings.Split(string(audit), "\n") {
			var row struct {
				Kind         string `json:"kind"`
				CapabilityID string `json:"capability_id"`
				JobID        string `json:"job_id"`
				Phase        string `json:"phase"`
			}
			if json.Unmarshal([]byte(line), &row) == nil && row.Kind == "tool-call" && row.CapabilityID == "memory.write" && row.JobID == runs[i].JobID {
				phases[row.Phase]++
			}
		}
		if phases["admitted"] != 1 || phases["completed"] != 1 {
			t.Fatalf("lane %d audit phases=%v", i, phases)
		}
	}
	t.Log("five same-owner Hermes runs reached the model concurrently; sixth waited; each active run wrote one attributed memory card")
}

func m32LaneJob(envelope identity.Envelope, user string, index int) runtime.ExecuteRequest {
	id := "m32-five-" + strconv.Itoa(index)
	envelope.ConversationID = id
	return runtime.ExecuteRequest{Envelope: envelope, JobID: id, IdempotencyKey: id, OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", Trigger: "message", Text: "Synthetic session " + strconv.Itoa(index)}
}
