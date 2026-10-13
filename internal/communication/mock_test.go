package communication

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The mock channel is the local test bench: messages enter through the
// control API and travel the real pipeline (task binding -> job -> outbox),
// while outbound deliveries are captured durably instead of calling
// Telegram. Phase A/B behaviour is verifiable end to end, offline.
func TestMockChannelEndToEnd(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	root := filepath.Dir(c.SpoolDir)
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: filepath.Join(root, "bob", "state"), WorkspaceDir: filepath.Join(root, "bob", "workspace")}
	for _, d := range []string{bob.StateDir, bob.WorkspaceDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	c.Users = append(c.Users, bob)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	callAs := func(principal, method, path string, body io.Reader) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+c.ControlAuth)
		req.Header.Set("X-Hub-Principal", principal)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	call := func(method, path string, body io.Reader) (int, string) {
		return callAs("alice", method, path, body)
	}
	if code, _ := call(http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":42,"text":"hello agent"}`)); code != http.StatusOK {
		t.Fatalf("mock inject: %d", code)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("mock message produced no job: %v %v", job, err)
	}
	if job.Channel != "mock" || job.UserID != "alice" || job.Text != "hello agent" {
		t.Fatalf("mock job: %+v", job)
	}
	if job.Envelope.ExternalIdentityID != "mock-42" || job.Envelope.DeliveryTargetID != "mock-42" {
		t.Fatalf("mock envelope: %+v", job.Envelope)
	}
	// Repeating the same client message id must not enqueue twice.
	if code, _ := call(http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":42,"text":"hello agent","client_id":"m-1"}`)); code != http.StatusOK {
		t.Fatalf("mock inject 2: %d", code)
	}
	if code, _ := call(http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":42,"text":"hello agent","client_id":"m-1"}`)); code != http.StatusOK {
		t.Fatalf("mock inject 3: %d", code)
	}
	dup, err := g.spool.ClaimJob()
	if err != nil {
		t.Fatal(err)
	}
	if extra, err := g.spool.ClaimJob(); err != nil || extra != nil {
		t.Fatalf("duplicate client_id enqueued a second job: %+v %v", extra, err)
	}
	_ = dup
	// Unknown principals can never inject.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/mock/message", strings.NewReader(`{"chat_id":42,"text":"x"}`))
	req.Header.Set("Authorization", "Bearer "+c.ControlAuth)
	req.Header.Set("X-Hub-Principal", "mallory")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("foreign mock inject accepted")
	}
	// An outbound delivery on the mock channel is captured durably — the
	// real outbox machinery still dedupes, retries and orders it.
	if err := g.spool.EnqueueDelivery(Delivery{ID: "mock-out-1", IdempotencyKey: "mock-out-1", Channel: "mock", ChatID: 42, Text: "ответ агента", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(t.Context())
	code, body := call(http.MethodGet, "/v1/mock/outbox", nil)
	if code != http.StatusOK || !strings.Contains(body, "ответ агента") {
		t.Fatalf("mock outbox: %d %s", code, body)
	}
	// The capture survives a gateway restart — it is a spool record, not RAM.
	if _, err := os.Stat(filepath.Join(g.spool.root, "mock-sent")); err != nil {
		t.Fatalf("mock-sent dir missing: %v", err)
	}
	// A mock chat binds to the first principal that uses it, like a DM binds
	// to an account: bob can neither inject into alice's chat 42 nor read its
	// captured outbox.
	if code, _ := callAs("bob", http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":42,"text":"hijack"}`)); code != http.StatusForbidden {
		t.Fatalf("foreign chat inject: %d", code)
	}
	if code, body := callAs("bob", http.MethodGet, "/v1/mock/outbox", nil); code != http.StatusOK || strings.Contains(body, "ответ агента") {
		t.Fatalf("bob saw alice's outbox: %d %s", code, body)
	}
	// Bob's own chat works for him and stays invisible to alice.
	if code, _ := callAs("bob", http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":77,"text":"hi"}`)); code != http.StatusOK {
		t.Fatalf("bob inject: %d", code)
	}
	_, _ = g.spool.ClaimJob()
	if err := g.spool.EnqueueDelivery(Delivery{ID: "mock-out-2", IdempotencyKey: "mock-out-2", Channel: "mock", ChatID: 77, Text: "ответ для боба", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(t.Context())
	if code, body := callAs("bob", http.MethodGet, "/v1/mock/outbox", nil); code != http.StatusOK || !strings.Contains(body, "ответ для боба") {
		t.Fatalf("bob outbox: %d %s", code, body)
	}
	if code, body := call(http.MethodGet, "/v1/mock/outbox", nil); code != http.StatusOK || strings.Contains(body, "ответ для боба") {
		t.Fatalf("alice saw bob's outbox: %d %s", code, body)
	}
	// A routine on channel=mock fires through the same occurrence pipeline
	// and lands in the captured outbox — the whole Phase B surface is
	// end-to-end testable offline.
	due := time.Now().UTC().Add(time.Minute)
	code, _ = call(http.MethodPost, "/v1/routines", strings.NewReader(`{"schedule_id":"mock-rem","channel":"mock","chat_id":42,"timezone":"UTC","expression":"once:`+due.Format(time.RFC3339)+`","input":"разбудить","job_kind":"reminder"}`))
	if code != http.StatusOK {
		t.Fatalf("mock routine create: %d", code)
	}
	g.tickProactive(due.Add(2 * time.Minute))
	g.deliverOne(t.Context())
	if code, body := call(http.MethodGet, "/v1/mock/outbox", nil); code != http.StatusOK || !strings.Contains(body, "разбудить") {
		t.Fatalf("mock routine delivery: %d %s", code, body)
	}
	_ = bytes.MinRead
}
