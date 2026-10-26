package exposure

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
)

type fakeProvider struct {
	opened   []Spec
	closed   []string
	failErr  error
	closeErr error
	next     int
}

func (f *fakeProvider) Open(_ context.Context, spec Spec) (string, error) {
	if f.failErr != nil {
		return "", f.failErr
	}
	f.next++
	f.opened = append(f.opened, spec)
	return "handle-" + spec.Endpoint, nil
}

func (f *fakeProvider) Close(_ context.Context, handle string) error {
	f.closed = append(f.closed, handle)
	return f.closeErr
}

func testManager(t *testing.T, p Provider) (*Manager, *audit.Ledger) {
	t.Helper()
	cfg, err := Load(writeConfig(t, validSishYAML))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg, ManagerOptions{
		Provider: p,
		Auth:     identity.Envelope{PrincipalID: "alice", ContextID: "user:alice", RuntimeID: "rt-1", PolicyVersion: "pol-1"},
		Ledger:   ledger,
	})
	return m, ledger
}

func TestManagerOpenClose(t *testing.T) {
	p := &fakeProvider{}
	m, ledger := testManager(t, p)
	ctx := context.Background()

	tw, err := m.Open(ctx, "demo")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if tw.PublicName != "demo.alice.hub.example.com" || tw.Target != "service:mysite:8080" {
		t.Fatalf("tunnel: %+v", tw)
	}
	if len(p.opened) != 1 || p.opened[0].Bind != "demo.alice" || p.opened[0].Provider != "sish" {
		t.Fatalf("spec: %+v", p.opened)
	}
	if got := len(m.List()); got != 1 {
		t.Fatalf("list: %d", got)
	}
	// Re-open is idempotent, not a second workload.
	if _, err := m.Open(ctx, "demo"); err != nil || len(p.opened) != 1 {
		t.Fatalf("re-open: %v opened=%d", err, len(p.opened))
	}
	if err := m.Close(ctx, "demo"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(p.closed) != 1 || p.closed[0] != "handle-demo" {
		t.Fatalf("closed: %v", p.closed)
	}
	if got := len(m.List()); got != 0 {
		t.Fatalf("list after close: %d", got)
	}
	events, err := ledger.List("alice")
	if err != nil || len(events) != 2 {
		t.Fatalf("audit: %v %d", err, len(events))
	}
	for i, outcome := range []string{"open", "close"} {
		if events[i].Kind != "exposure" || events[i].Outcome != outcome || events[i].Name != "demo.alice.hub.example.com" {
			t.Fatalf("event %d: %+v", i, events[i])
		}
	}
}

func TestManagerOpenCapBusy(t *testing.T) {
	cfg, err := Load(writeConfig(t, `schema: 1
provider: sish
relay: {host: v.example.com, user: u, domain: hub.example.com, host_keys: ["sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"]}
zone: alice
key_ref: "broker:grant-01"
max_exposures: 1
endpoints:
  a: {target: "hub_public_listener"}
  b: {target: "hub_public_listener"}
`))
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{}
	m := NewManager(cfg, ManagerOptions{Provider: p})
	ctx := context.Background()
	if _, err := m.Open(ctx, "a"); err != nil {
		t.Fatalf("open a: %v", err)
	}
	if _, err := m.Open(ctx, "b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("open b over cap: %v", err)
	}
	if len(p.opened) != 1 {
		t.Fatalf("provider opened over the cap: %d", len(p.opened))
	}
	if err := m.Close(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(ctx, "b"); err != nil {
		t.Fatalf("open after close frees the slot: %v", err)
	}
}

func TestManagerNoProviderFailsClosed(t *testing.T) {
	m, ledger := testManager(t, nil)
	if _, err := m.Open(context.Background(), "demo"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no provider must fail closed: %v", err)
	}
	events, _ := ledger.List("alice")
	if len(events) != 1 || events[0].Outcome != "denied" {
		t.Fatalf("audit: %+v", events)
	}
}

func TestManagerProviderFailureAudited(t *testing.T) {
	p := &fakeProvider{failErr: errors.New("admission refused")}
	m, ledger := testManager(t, p)
	if _, err := m.Open(context.Background(), "demo"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("provider failure: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatal("failed open left a tunnel")
	}
	events, _ := ledger.List("alice")
	if len(events) != 1 || events[0].Outcome != "failed" {
		t.Fatalf("audit: %+v", events)
	}
}

func TestManagerStatus(t *testing.T) {
	p := &fakeProvider{}
	m, _ := testManager(t, p)
	ctx := context.Background()
	if _, err := m.Status("demo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("status closed endpoint: %v", err)
	}
	if _, err := m.Open(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	st, err := m.Status("demo")
	if err != nil || st.PublicName != "demo.alice.hub.example.com" || st.ExpiresAt.IsZero() {
		t.Fatalf("status: %v %+v", err, st)
	}
}

func TestManagerReapLifetime(t *testing.T) {
	p := &fakeProvider{}
	now := time.Now()
	current := now
	cfg, err := Load(writeConfig(t, validSishYAML))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg, ManagerOptions{
		Provider: p,
		Auth:     identity.Envelope{PrincipalID: "alice"},
		Ledger:   ledger,
		Now:      func() time.Time { return current },
	})
	ctx := context.Background()
	if _, err := m.Open(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if got := m.Reap(ctx); len(got) != 0 {
		t.Fatalf("reaped early: %v", got)
	}
	current = now.Add(cfg.Lifetime() + time.Second)
	if got := m.Reap(ctx); len(got) != 1 || got[0] != "demo" {
		t.Fatalf("reap: %v", got)
	}
	if len(p.closed) != 1 || len(m.List()) != 0 {
		t.Fatalf("post-reap: closed=%v list=%v", p.closed, m.List())
	}
	events, _ := ledger.List("alice")
	last := events[len(events)-1]
	if last.Outcome != "close" {
		t.Fatalf("reap receipt: %+v", last)
	}
}

func TestManagerReapsExpiredInBackground(t *testing.T) {
	p := &fakeProvider{}
	now := time.Now()
	current := now
	cfg, err := Load(writeConfig(t, validSishYAML))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg, ManagerOptions{
		Provider:     p,
		Now:          func() time.Time { return current },
		ReapInterval: 20 * time.Millisecond,
	})
	defer m.Shutdown()
	if _, err := m.Open(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	current = now.Add(cfg.Lifetime() + time.Second)
	deadline := time.Now().Add(3 * time.Second)
	for len(m.List()) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("expired tunnel was not reaped in the background")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(p.closed) != 1 {
		t.Fatalf("provider close: %v", p.closed)
	}
}

func TestManagerReapCloseFailure(t *testing.T) {
	p := &fakeProvider{closeErr: errors.New("relay down")}
	now := time.Now()
	current := now
	cfg, err := Load(writeConfig(t, validSishYAML))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg, ManagerOptions{
		Provider: p,
		Auth:     identity.Envelope{PrincipalID: "alice"},
		Ledger:   ledger,
		Now:      func() time.Time { return current },
	})
	ctx := context.Background()
	if _, err := m.Open(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	current = now.Add(cfg.Lifetime() + time.Second)
	if got := m.Reap(ctx); len(got) != 0 {
		t.Fatalf("failed close reported closed: %v", got)
	}
	if _, err := m.Status("demo"); err != nil {
		t.Fatalf("record must stay visible on incomplete cleanup: %v", err)
	}
	events, _ := ledger.List("alice")
	last := events[len(events)-1]
	if last.Outcome != "failed" {
		t.Fatalf("close-failure receipt: %+v", last)
	}
}

func TestManagerOpenUnknownDenied(t *testing.T) {
	p := &fakeProvider{}
	m, ledger := testManager(t, p)
	if _, err := m.Open(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open unknown: %v", err)
	}
	if len(p.opened) != 0 {
		t.Fatalf("provider invoked: %v", p.opened)
	}
	events, err := ledger.List("alice")
	if err != nil || len(events) != 1 || events[0].Outcome != "denied" {
		t.Fatalf("audit: %v %+v", err, events)
	}
}
