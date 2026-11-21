package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// Issue #602 S2: at most HUB_SESSION_SLOTS distinct sessions hold an active
// run at once, and one session never runs two runs in parallel.
func TestSessionSlotsBoundConcurrentSessions(t *testing.T) {
	t.Setenv("HUB_SESSION_SLOTS", "2")
	handler := &runtimeHTTP{}
	releaseOne, err := handler.acquireSessionSlot(t.Context(), "session-one")
	if err != nil {
		t.Fatal(err)
	}
	releaseTwo, err := handler.acquireSessionSlot(t.Context(), "session-two")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseTwo()
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := handler.acquireSessionSlot(short, "session-three"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third session admitted past the slot bound: %v", err)
	}
	// Per-session FIFO: the same session never holds two runs, even while
	// other sessions leave slots open.
	if _, err := handler.acquireSessionSlot(short, "session-one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second run entered a busy session: %v", err)
	}
	releaseOne()
	releaseThree, err := handler.acquireSessionSlot(t.Context(), "session-three")
	if err != nil {
		t.Fatalf("freed slot did not admit the waiting session: %v", err)
	}
	releaseThree()
}

// Issue #602 S2: run identity resolves only through this runtime's own
// session table — a caller can name an admission, never create one.
func TestRunIdentityNotTakenFromRequest(t *testing.T) {
	for key, value := range map[string]string{"HUB_RUNTIME_AUTH": "secret", "HUB_USER_ID": "alice", "HUB_ORGANIZATION_ID": "personal", "HUB_RUNTIME_ID": "alice", "HUB_POLICY_VERSION": "policy-1", "HUB_RUNTIME_GENERATION": "gen-1"} {
		t.Setenv(key, value)
	}
	var hermesHits atomic.Int32
	var wantSession atomic.Value
	wantSession.Store("")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hermesHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "session_id": wantSession.Load().(string), "output": "done"})
	}))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)

	handler := runtimeHandler().(*runtimeHTTP)
	request := validExecuteRequest("alice", "personal", "identity", "unused")
	request.JobID = "job-a"
	sessionA := sessionIDFor(request)
	wantSession.Store(sessionA)
	call := func(path string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	control := RunControl{RunReference: RunReference{ExecuteRequest: request, RunID: "run-foreign", SessionID: sessionA, RuntimeGeneration: "gen-1"}, Action: "cancel"}
	if rec := call("/v1/control", control); rec.Code != http.StatusConflict {
		t.Fatalf("unbound run controlled: %d", rec.Code)
	}
	ref := RunReference{ExecuteRequest: request, RunID: "run-foreign", SessionID: sessionA, RuntimeGeneration: "gen-1"}
	if rec := call("/v1/observe", ref); rec.Code != http.StatusConflict {
		t.Fatalf("unbound run observed: %d", rec.Code)
	}
	// A run bound under this job resolves; another session or job naming the
	// same run id still misses the table row.
	if err := handler.bindAuthorizedRun("job-a", "run-owned", "", sessionA); err != nil {
		t.Fatal(err)
	}
	foreignJob := control
	foreignJob.RunID = "run-owned"
	foreignJob.JobID = "job-b"
	if rec := call("/v1/control", foreignJob); rec.Code != http.StatusConflict {
		t.Fatalf("run resolved under a foreign job: %d", rec.Code)
	}
	otherConversation := validExecuteRequest("alice", "personal", "identity", "unused")
	otherConversation.JobID = "job-a"
	otherConversation.ConversationID = "telegram-99"
	foreignSession := control
	foreignSession.RunReference.ExecuteRequest = otherConversation
	foreignSession.SessionID = sessionIDFor(otherConversation)
	foreignSession.RunID = "run-owned"
	if rec := call("/v1/control", foreignSession); rec.Code != http.StatusConflict {
		t.Fatalf("run resolved under a foreign session: %d", rec.Code)
	}
	if got := hermesHits.Load(); got != 0 {
		t.Fatalf("unbound resolutions reached Hermes: %d", got)
	}
	bound := control
	bound.RunID = "run-owned"
	if rec := call("/v1/control", bound); rec.Code != http.StatusOK {
		t.Fatalf("bound run refused: %d %s", rec.Code, rec.Body.String())
	}
}

// Issue #602 S3: one runtime serves exactly its rendered (space, agent,
// policy digest) — an execute signed for another policy generation is
// refused, so a narrower-rights subagent can never borrow this runtime; it
// must claim a separate one through spawn-controller.
func TestNarrowerRightsNeedSeparateRuntime(t *testing.T) {
	for key, value := range map[string]string{"HUB_RUNTIME_AUTH": "secret", "HUB_USER_ID": "alice", "HUB_ORGANIZATION_ID": "personal", "HUB_RUNTIME_ID": "alice", "HUB_POLICY_VERSION": "policy-1", "HUB_RUNTIME_GENERATION": "gen-1"} {
		t.Setenv(key, value)
	}
	handler := runtimeHandler()
	narrower := validExecuteRequest("alice", "personal", "rights", "unused")
	narrower.JobID = "job-narrower"
	narrower.Envelope.PolicyVersion = "policy-2"
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(mustJSON(t, narrower)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("foreign-policy execute admitted: %d %s", rec.Code, rec.Body.String())
	}
}

// Issue #602 S3: workspace files never serve as config — the supervisor
// launches children with cwd under the state dir, so upstream cwd-relative
// discovery (.hermes/, mcp.json, project plugins) cannot reach /workspace.
func TestWorkspaceConfigIgnored(t *testing.T) {
	oldState, oldCommand := state, command
	state = t.TempDir()
	var spawned *exec.Cmd
	command = func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeExitHelper")
		cmd.Env = append(os.Environ(), "HUB_TEST_EXIT_HELPER=1")
		spawned = cmd
		return cmd
	}
	t.Cleanup(func() { state, command = oldState, oldCommand })
	children := newChildSet()
	defer children.stopAll()
	if err := children.start("hermes", "gateway", "run"); err != nil {
		t.Fatal(err)
	}
	if spawned.Dir != state {
		t.Fatalf("child cwd = %q, want state dir %q", spawned.Dir, state)
	}
	if filepath.Clean(spawned.Dir) == filepath.Clean(workspace) {
		t.Fatal("child cwd is the workspace")
	}
}

// Issue #602 S2: distinct sessions admit in parallel — an admission in one
// conversation never blocks another conversation's run.
func TestParallelSessionsIsolated(t *testing.T) {
	for key, value := range map[string]string{"HUB_RUNTIME_AUTH": "secret", "HUB_USER_ID": "alice", "HUB_ORGANIZATION_ID": "personal", "HUB_RUNTIME_ID": "alice", "HUB_POLICY_VERSION": "policy-1", "HUB_RUNTIME_GENERATION": "gen-1", "HUB_SESSION_SLOTS": "2"} {
		t.Setenv(key, value)
	}
	// The admission journal is keyed by JobID: it must land in a writable
	// state dir, not the default /state a bare test box does not have.
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	var admitted atomic.Int32
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			conversation, _ := body["input"].(string)
			admitted.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"run_id":"run-%s","status":"started"}`, conversation)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/run-"):
			<-release
			name := strings.TrimPrefix(r.URL.Path, "/v1/runs/run-")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": "reply-" + name})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	go func() {
		deadline := time.After(10 * time.Second)
		for admitted.Load() < 2 {
			select {
			case <-deadline:
				close(release)
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		close(release)
	}()

	handler := runtimeHandler()
	execute := func(sender int64) (int, string) {
		request := validExecuteRequest("alice", "personal", fmt.Sprint(sender), fmt.Sprint(sender))
		request.JobID = fmt.Sprintf("job-%d", sender)
		request.Envelope = identity.TelegramEnvelope("alice", sender, "alice", "policy-1")
		req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(mustJSON(t, request)))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	var wg sync.WaitGroup
	results := make([][2]any, 2)
	for i, sender := range []int64{11, 22} {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			code, body := execute(sender)
			results[slot] = [2]any{code, body}
		}(i)
	}
	wg.Wait()
	for i, result := range results {
		code, _ := result[0].(int)
		body, _ := result[1].(string)
		if code != http.StatusOK {
			t.Fatalf("session %d failed: %d %s", i, code, body)
		}
	}
	if !strings.Contains(results[0][1].(string), "reply-11") || !strings.Contains(results[1][1].(string), "reply-22") {
		t.Fatalf("sessions answered each other's runs: %v", results)
	}
}
