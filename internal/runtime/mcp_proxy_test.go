package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type proxyBearerTransport struct{ http.RoundTripper }

func TestRunReservationQueuesDifferentJobs(t *testing.T) {
	s := &runtimeHTTP{}
	if reserved, err := s.reserveRun(t.Context(), "first"); err != nil || !reserved {
		t.Fatalf("first reservation: reserved=%v err=%v", reserved, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		reserved, err := s.reserveRun(ctx, "second")
		if err == nil && !reserved {
			err = context.Canceled
		}
		result <- err
	}()
	<-started
	select {
	case err := <-result:
		t.Fatalf("second run bypassed the active job: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	s.releaseAdmission("first")
	if err := <-result; err != nil {
		t.Fatalf("second reservation after release: %v", err)
	}
	s.releaseAdmission("second")
}

func TestRunReservationWaitsForSameJobAdmission(t *testing.T) {
	s := &runtimeHTTP{}
	if reserved, err := s.reserveRun(t.Context(), "first"); err != nil || !reserved {
		t.Fatalf("first reservation: reserved=%v err=%v", reserved, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	result := make(chan bool, 1)
	go func() {
		reserved, err := s.reserveRun(ctx, "first")
		result <- err == nil && !reserved
	}()
	select {
	case <-result:
		t.Fatal("readmission bypassed pending admission")
	case <-time.After(20 * time.Millisecond):
	}
	s.runMu.Lock()
	s.activeRun = "run-one"
	s.signalRunChangeLocked()
	s.runMu.Unlock()
	if !<-result {
		t.Fatal("readmission did not attach to the admitted run")
	}
	s.releaseRun("first", "run-one", "completed")
}

func (t proxyBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer synthetic")
	return t.RoundTripper.RoundTrip(r)
}

func TestManagedMCPProxyBindsOnlyTheAdmittedRun(t *testing.T) {
	var job, run string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		job, run = r.Header.Get(api.HeaderJobID), r.Header.Get(api.HeaderRunID)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	t.Setenv("HUB_RUNTIME_AUTH", "synthetic")
	t.Setenv("HUB_TOOLHUB_ENDPOINT", upstream.URL+"/mcp")
	server := runtimeHandler()
	call := func(auth bool) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.Header.Set(api.HeaderJobID, "forged-job")
		r.Header.Set(api.HeaderRunID, "forged-run")
		if auth {
			r.Header.Set("Authorization", "Bearer synthetic")
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w.Code
	}
	if code := call(false); code != http.StatusUnauthorized {
		t.Fatalf("unauthorized MCP status=%d", code)
	}
	if code := call(true); code != http.StatusOK || job != "" || run != "" {
		t.Fatalf("unadmitted MCP: status=%d job=%q run=%q", code, job, run)
	}
	s := server.(*runtimeHTTP)
	s.runMu.Lock()
	s.activeJob, s.activeRun = "real-job", "real-run"
	s.runMu.Unlock()
	if code := call(true); code != http.StatusOK || job != "real-job" || run != "real-run" {
		t.Fatalf("admitted MCP: status=%d job=%q run=%q", code, job, run)
	}
	s.releaseRun("real-job", "real-run", "uncertain")
	if code := call(true); code != http.StatusOK || job != "real-job" || run != "real-run" {
		t.Fatalf("uncertain run lost binding: status=%d job=%q run=%q", code, job, run)
	}
	s.releaseRun("real-job", "real-run", "completed")
	if code := call(true); code != http.StatusOK || job != "" || run != "" {
		t.Fatalf("terminal run kept binding: status=%d job=%q run=%q", code, job, run)
	}
}

func TestManagedMCPProxyRefreshesHeadersOnPersistentSession(t *testing.T) {
	var pairs [][2]string
	tools := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	tools.AddTool(&mcp.Tool{Name: "probe", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pairs = append(pairs, [2]string{r.GetExtra().Header.Get(api.HeaderJobID), r.GetExtra().Header.Get(api.HeaderRunID)})
		return &mcp.CallToolResult{}, nil
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return tools }, &mcp.StreamableHTTPOptions{}))
	defer upstream.Close()
	t.Setenv("HUB_CAPABILITY_MODE", "managed")
	t.Setenv("HUB_RUNTIME_AUTH", "synthetic")
	t.Setenv("HUB_TOOLHUB_ENDPOINT", upstream.URL+"/mcp")
	handler := runtimeHandler().(*runtimeHTTP)
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: proxy.URL + "/mcp", HTTPClient: &http.Client{Transport: proxyBearerTransport{http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, pair := range [][2]string{{"job-one", "run-one"}, {"job-two", "run-two"}} {
		handler.runMu.Lock()
		handler.activeJob, handler.activeRun = pair[0], pair[1]
		handler.runMu.Unlock()
		if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "probe", Arguments: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(pairs) != 2 || pairs[0] != [2]string{"job-one", "run-one"} || pairs[1] != [2]string{"job-two", "run-two"} {
		t.Fatalf("persistent MCP headers were stale: %v", pairs)
	}
}
