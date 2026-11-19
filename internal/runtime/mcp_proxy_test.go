package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type proxyBearerTransport struct {
	http.RoundTripper
	token string
}

func (t proxyBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	token := t.token
	if token == "" {
		token = "synthetic"
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set(api.HeaderJobID, "forged-job")
	r.Header.Set(api.HeaderRunID, "forged-run")
	r.Header.Set(contract.RunTokenHeader, "caller-supplied")
	return t.RoundTripper.RoundTrip(r)
}

func TestRunBindingWaitsForAdmission(t *testing.T) {
	s := &runtimeHTTP{}
	s.beginAdmission()
	defer s.finishAdmission()
	result := make(chan string, 1)
	go func() {
		job, err := s.jobForRun(t.Context(), "run-later")
		if err != nil {
			result <- ""
			return
		}
		result <- job
	}()
	deadline := time.After(time.Second)
	for {
		s.runMu.Lock()
		waiting := s.runChange != nil
		s.runMu.Unlock()
		if waiting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("tool call did not wait for admission")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := s.bindRun("job-later", "run-later"); err != nil {
		t.Fatal(err)
	}
	if got := <-result; got != "job-later" {
		t.Fatalf("early tool call resolved to %q", got)
	}
}

func TestConcurrentRunBindings(t *testing.T) {
	s := &runtimeHTTP{}
	for _, pair := range [][2]string{{"job-one", "run-one"}, {"job-two", "run-two"}} {
		if err := s.bindRun(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range [][2]string{{"job-one", "run-one"}, {"job-two", "run-two"}} {
		job, err := s.jobForRun(t.Context(), pair[1])
		if err != nil || job != pair[0] {
			t.Fatalf("run %s mapped to %s: %v", pair[1], job, err)
		}
	}
	if s.bindRun("foreign", "run-one") == nil || s.bindRun("job-one", "run-foreign") == nil {
		t.Fatal("conflicting run binding accepted")
	}
	s.releaseRun("job-one", "run-one", "uncertain")
	if job, err := s.jobForRun(t.Context(), "run-one"); err != nil || job != "job-one" {
		t.Fatal("uncertain run lost its binding")
	}
	s.releaseRun("job-one", "run-one", "completed")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.jobForRun(ctx, "run-one"); err == nil {
		t.Fatal("terminal run retained its binding")
	}
	if job, err := s.jobForRun(t.Context(), "run-two"); err != nil || job != "job-two" {
		t.Fatal("another active run lost its binding")
	}
}

func TestManagedMCPProxyRoutesEachCallToItsAdmittedRun(t *testing.T) {
	var mu sync.Mutex
	var pairs [][3]string
	tools := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	tools.AddTool(&mcp.Tool{Name: "probe", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mu.Lock()
		pairs = append(pairs, [3]string{r.GetExtra().Header.Get(api.HeaderJobID), r.GetExtra().Header.Get(api.HeaderRunID), r.GetExtra().Header.Get(contract.RunTokenHeader)})
		mu.Unlock()
		return &mcp.CallToolResult{}, nil
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return tools }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer upstream.Close()
	t.Setenv("HUB_RUNTIME_AUTH", "synthetic")
	t.Setenv("HUB_RUNTIME_GENERATION", "gen-1")
	t.Setenv("HUB_TOOLHUB_ENDPOINT", upstream.URL+"/mcp")
	handler := runtimeHandler().(*runtimeHTTP)
	for _, pair := range [][2]string{{"job-one", "run-one"}, {"job-two", "run-two"}} {
		proof := strings.Repeat("1", 64)
		if pair[0] == "job-two" {
			proof = strings.Repeat("2", 64)
		}
		if err := handler.bindAuthorizedRun(pair[0], pair[1], proof); err != nil {
			t.Fatal(err)
		}
		grant := RunAuthorization{JobID: pair[0], RunID: pair[1], Generation: "gen-1",
			Tokens: map[string]string{contract.ToolHubAudience: "synthetic-" + pair[1]}}
		raw, err := json.Marshal(grant)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			proof string
			want  int
		}{
			{strings.Repeat("3", 64), http.StatusUnauthorized},
			{proof, http.StatusNoContent},
		} {
			req := httptest.NewRequest(http.MethodPut, RunAuthorizationPath, strings.NewReader(string(raw)))
			req.Header.Set("Authorization", "Bearer synthetic")
			req.Header.Set(RunDeliveryHeader, tc.proof)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tc.want {
				t.Fatalf("credential delivery status=%d, want %d", response.Code, tc.want)
			}
		}
	}
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: proxy.URL + "/mcp", HTTPClient: &http.Client{Transport: proxyBearerTransport{RoundTripper: http.DefaultTransport}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, pair := range [][2]string{{"job-one", "run-one"}, {"job-two", "run-two"}} {
		if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Meta: mcp.Meta{"x-hermes-hub-run-id": pair[1]}, Name: "probe", Arguments: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	got := append([][3]string(nil), pairs...)
	mu.Unlock()
	if len(got) != 2 || got[0] != [3]string{"job-one", "run-one", "synthetic-run-one"} || got[1] != [3]string{"job-two", "run-two", "synthetic-run-two"} {
		t.Fatalf("MCP calls lost their run-specific authority: count=%d", len(got))
	}
	for _, meta := range []mcp.Meta{nil, {"x-hermes-hub-run-id": "run-foreign"}} {
		if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Meta: meta, Name: "probe", Arguments: map[string]any{}}); err == nil {
			t.Fatalf("unbound MCP call accepted with meta %v", meta)
		}
	}
	unauthorized := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"_meta":{"x-hermes-hub-run-id":"run-one"}}}`))
	unauthorized.Header.Set(api.HeaderJobID, "forged")
	unauthorized.Header.Set(api.HeaderRunID, "run-one")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, unauthorized)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP status=%d", w.Code)
	}
}
