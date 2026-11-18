package toolhub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type researchRunnerFunc func(context.Context, researchRequest) (researchResult, error)

func (f researchRunnerFunc) RunJob(ctx context.Context, request researchRequest) (researchResult, error) {
	return f(ctx, request)
}

func TestResearchStorageIsolationAndConcurrentCreate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "research")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	owner := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	other := identity.TelegramEnvelope("bob", 22, "bob", "policy-1")
	h, err := newResearchHost(root, researchRunnerFunc(func(context.Context, researchRequest) (researchResult, error) {
		t.Error("unauthorized research reached executor")
		return researchResult{}, nil
	}), func(string) (identity.Envelope, error) { return identity.Envelope{}, api.ErrUnauthorized }, NewStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.create(owner, map[string]any{"topic": "x", "sources": []any{"https://go.dev/?token=secret"}}); err == nil {
		t.Fatal("credential URL accepted")
	}
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := h.create(owner, map[string]any{"topic": "Go testing"})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- job.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate research ID %s", id)
		}
		seen[id] = true
		if _, err := h.read(other, id); err == nil {
			t.Fatal("foreign owner read succeeded")
		}
		if _, err := h.read(owner, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 12 {
		t.Fatalf("got %d research jobs", len(seen))
	}
	jobs, err := h.list(owner, 20)
	if err != nil || len(jobs) != 12 {
		t.Fatalf("list: %d, %v", len(jobs), err)
	}
	if err := os.Symlink(root, filepath.Join(t.TempDir(), "link")); err == nil {
		// A hostile owner directory may never be followed by read or write.
		if err := os.RemoveAll(filepath.Join(root, "bob")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "alice"), filepath.Join(root, "bob")); err == nil {
			if _, err := h.list(other, 1); err == nil {
				t.Fatal("symlink owner directory accepted")
			}
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.active)
		h.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d research goroutines still active", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResearchSupervisorStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/holds" {
			if r.Header.Get("Authorization") != "Bearer control" {
				t.Error("hold read missing control authority")
			}
			_, _ = w.Write([]byte(`[{"principal_id":"alice","context_id":"alice","epoch":7}]`))
			return
		}
		if r.URL.Path != "/v1/jobs" || r.Header.Get("Authorization") != "Bearer control" || r.Header.Get("Accept") != "application/x-ndjson" {
			t.Errorf("unexpected research transport: %s", r.URL.Path)
		}
		var request researchRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.JobID != "research-123" || request.Trigger != "research" || request.Channel != "toolhub" || request.OperationalEpoch != 7 {
			t.Errorf("unexpected research request")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"job_id":"research-123","status":"running"}` + "\n" + `{"job_id":"research-123","status":"completed","text":"Report https://go.dev/doc/tutorial/add-a-test"}` + "\n"))
	}))
	defer server.Close()
	runner := httpResearchRunner{URL: server.URL, Token: "control"}
	result, err := runner.RunJob(t.Context(), researchRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: "research-123", Trigger: "research", Channel: "toolhub"})
	if err != nil || result.Status != "completed" || !strings.Contains(result.Text, "https://go.dev/") {
		t.Fatalf("result: %+v, %v", result, err)
	}
}

func TestResearchRunRequiresCurrentGrantAndProjection(t *testing.T) {
	store, auth, policy, profile := managedStore(t)
	definition := r5ExecutorDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.Selections = append(profile.Selections, api.CapabilitySelection{CapabilityID: "research.create", DefinitionID: definition.DefinitionID,
		DefinitionVersion: definition.Version, ImplementationDigest: api.DefinitionDigest(definition), ToolName: "research_create", Name: "research_create"})
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	for _, tool := range definition.Tools {
		if tool.Name == "research_create" {
			grantDefinitionRights(t, store, auth, api.ToolDefinition{Tools: []api.ToolSpec{tool}})
			break
		}
	}
	root := filepath.Join(t.TempDir(), "research")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	called := make(chan researchRequest, 1)
	h, err := newResearchHost(root, researchRunnerFunc(func(_ context.Context, request researchRequest) (researchResult, error) {
		called <- request
		return researchResult{JobID: request.JobID, Status: "completed", Text: "Verified https://go.dev/doc/tutorial/add-a-test"}, nil
	}), func(string) (identity.Envelope, error) { return auth, nil }, store)
	if err != nil {
		t.Fatal(err)
	}
	job, err := h.create(auth, map[string]any{"topic": "How does Go test?"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-called:
		if request.ConversationID != job.ID || request.PrincipalID != auth.PrincipalID || request.IdempotencyKey != job.ID {
			t.Fatalf("research binding mismatch: %+v", request)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("granted research did not run")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := h.read(auth, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "completed" {
			if !strings.Contains(got.Report, "https://go.dev/") {
				t.Fatal("completed research lost report")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("research remained %s", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.runner = researchRunnerFunc(func(_ context.Context, request researchRequest) (researchResult, error) {
		return researchResult{JobID: request.JobID, Status: "completed"}, nil
	})
	empty, err := h.create(auth, map[string]any{"topic": "Empty report must fail"})
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		got, err := h.read(auth, empty.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == "failed" {
			if got.Report != "" || got.Error == "" {
				t.Fatalf("empty result accepted: %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("empty report remained %s", got.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = policy
}
