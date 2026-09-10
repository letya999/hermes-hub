package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeExecutor struct{ calls atomic.Int32 }

func (f *fakeExecutor) Run(context.Context, Job) (string, error) {
	f.calls.Add(1)
	return "reply", nil
}

func TestPrivateExecutionContractAuthorizesScopesAndReplays(t *testing.T) {
	executor := &fakeExecutor{}
	server := NewServer(Binding{UserID: "alice", OrganizationID: "acme"}, "runtime-secret", executor)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	client := NewHTTPClient(httpServer.URL, "runtime-secret")
	job := Job{ID: "job-1", OrganizationID: "acme", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", IdempotencyKey: "idem-1", Text: "hello"}
	if result, err := client.Run(context.Background(), job); err != nil || result != "reply" {
		t.Fatal(result, err)
	}
	if result, err := client.Run(context.Background(), job); err != nil || result != "reply" || executor.calls.Load() != 1 {
		t.Fatal(result, err, executor.calls.Load())
	}
	bad := job
	bad.ScopeID = "user:bob"
	if _, err := client.Run(context.Background(), bad); err == nil {
		t.Fatal("cross-user scope accepted")
	}
	bad = job
	bad.UserID = "bob"
	bad.IdempotencyKey = "different-user"
	if _, err := client.Run(context.Background(), bad); err == nil {
		t.Fatal("cross-user identity accepted")
	}
	bad = job
	bad.Text = "different"
	if _, err := client.Run(context.Background(), bad); err == nil {
		t.Fatal("idempotency replay with different job accepted")
	}
}

func TestPrivateExecutionContractRejectsMissingAuthAndMalformedRequests(t *testing.T) {
	server := NewServer(Binding{UserID: "alice", OrganizationID: "personal"}, "runtime-secret", &fakeExecutor{})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/jobs", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatal(response, err)
	}
	_ = response.Body.Close()
	request, _ = http.NewRequest(http.MethodPost, httpServer.URL+"/v1/jobs", strings.NewReader(`{"version":1,"job":{"id":"x"}}`))
	request.Header.Set("Authorization", "Bearer runtime-secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusForbidden {
		t.Fatal(response, err)
	}
	_ = response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(strings.NewReader(`{"version":1}`)).Decode(&payload); err != nil || payload["version"] != float64(1) {
		t.Fatal(err, payload)
	}
}
