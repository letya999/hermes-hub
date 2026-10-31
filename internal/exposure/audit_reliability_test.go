package exposure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
)

type auditFaultProvider struct {
	fakeProvider
	afterOpen     func()
	effectError   error
	closeFailures int
}

func (p *auditFaultProvider) Open(ctx context.Context, spec Spec) (string, error) {
	handle, err := p.fakeProvider.Open(ctx, spec)
	if err == nil && p.afterOpen != nil {
		p.afterOpen()
	}
	if err == nil && p.effectError != nil {
		err = p.effectError
	}
	return handle, err
}

func (p *auditFaultProvider) Close(ctx context.Context, handle string) error {
	if p.closeFailures > 0 {
		p.closeFailures--
		return errors.New("fixture unconfirmed close")
	}
	return p.fakeProvider.Close(ctx, handle)
}

func auditManager(t *testing.T, p Provider) (*Manager, string) {
	t.Helper()
	cfg, err := Load(writeConfig(t, validSishYAML))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	ledger, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg, ManagerOptions{Provider: p, Ledger: ledger, Auth: identity.Envelope{PrincipalID: "alice", ContextID: "user:alice", RuntimeID: "test", PolicyVersion: "p1"}})
	t.Cleanup(m.Shutdown)
	return m, path
}

func TestAuditFailureBeforeEffectPreventsCall(t *testing.T) {
	p := &auditFaultProvider{}
	m, path := auditManager(t, p)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(context.Background(), "demo"); err == nil {
		t.Error("effect admitted without audit")
	}
	if len(p.opened) != 0 {
		t.Fatalf("provider called %d times", len(p.opened))
	}
}

func TestPostEffectAuditFailureNeverRepeatsAction(t *testing.T) {
	p := &auditFaultProvider{}
	m, path := auditManager(t, p)
	p.afterOpen = func() {
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Open(context.Background(), "demo"); err == nil {
		t.Error("post-effect audit failure reported as success")
	}
	if _, err := m.Open(context.Background(), "demo"); err == nil {
		t.Error("unverified effect reported as success on retry")
	}
	if len(p.opened) != 1 {
		t.Fatalf("provider effects=%d", len(p.opened))
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
}

func TestUnsettledOpenBlocksFreshManager(t *testing.T) {
	p := &auditFaultProvider{}
	m, path := auditManager(t, p)
	p.afterOpen = func() {
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Open(context.Background(), "demo"); !errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("post effect=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	secondProvider := &fakeProvider{}
	fresh := NewManager(m.cfg, ManagerOptions{Provider: secondProvider, Ledger: m.ledger, Auth: m.auth})
	t.Cleanup(fresh.Shutdown)
	if _, err := fresh.Open(context.Background(), "demo"); !errors.Is(err, audit.ErrUncertain) {
		t.Errorf("unsettled effect reopened: %v", err)
	}
	if len(secondProvider.opened) != 0 {
		t.Fatalf("effect repeated after reopen: %d", len(secondProvider.opened))
	}
}

func TestProviderErrorPreservesKnownHandleForClose(t *testing.T) {
	p := &auditFaultProvider{effectError: errors.New("fixture provider failed after effect")}
	m, _ := auditManager(t, p)
	if _, err := m.Open(context.Background(), "demo"); !errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("effect=%v", err)
	}
	status, err := m.Status("demo")
	if err != nil || !status.Uncertain {
		t.Fatalf("known effect lost: %+v %v", status, err)
	}
	// Stopping known access remains possible even with an unsettled audit intent.
	if err := m.Close(context.Background(), "demo"); err != nil && !errors.Is(err, audit.ErrUncertain) {
		t.Fatal(err)
	}
	if len(p.closed) != 1 || p.closed[0] != "handle-demo" {
		t.Fatalf("cleanup handle lost: %v", p.closed)
	}
}

func TestUnknownEffectStillConsumesExposureCapacity(t *testing.T) {
	p := &auditFaultProvider{fakeProvider: fakeProvider{failErr: errors.New("fixture uncertain")}}
	m, _ := auditManager(t, p)
	m.cfg.MaxExposures = 1
	m.cfg.Endpoints["other"] = m.cfg.Endpoints["demo"]
	if _, err := m.Open(context.Background(), "demo"); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal(err)
	}
	if _, err := m.Open(context.Background(), "other"); !errors.Is(err, ErrBusy) {
		t.Fatalf("unknown resource bypassed cap: %v", err)
	}
}

func TestAuditFailureNeverPreventsRequestedClose(t *testing.T) {
	p := &auditFaultProvider{}
	m, path := auditManager(t, p)
	if _, err := m.Open(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background(), "demo"); !errors.Is(err, audit.ErrUncertain) {
		t.Fatalf("missing uncertain receipt: %v", err)
	}
	if len(p.closed) != 1 || len(m.List()) != 0 {
		t.Fatalf("audit prevented cleanup: closed=%d remaining=%d", len(p.closed), len(m.List()))
	}
}

func TestVerifiedCleanupPreservesUnknownOpenAndAllowsFreshResource(t *testing.T) {
	p := &auditFaultProvider{effectError: errors.New("fixture unknown open")}
	m, _ := auditManager(t, p)
	if _, err := m.Open(context.Background(), "demo"); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal(err)
	}
	if err := m.Close(context.Background(), "demo"); err != nil {
		t.Fatalf("known resource cleanup: %v", err)
	}
	pending, err := m.ledger.Pending(audit.Scope{PrincipalID: "alice"}, 10)
	if err != nil || len(pending) != 1 || pending[0].Execution != "unknown" {
		t.Fatalf("invented open outcome: %+v %v", pending, err)
	}
	freshProvider := &fakeProvider{}
	fresh := NewManager(m.cfg, ManagerOptions{Provider: freshProvider, Ledger: m.ledger, Auth: m.auth})
	t.Cleanup(fresh.Shutdown)
	if _, err := fresh.Open(context.Background(), "demo"); err != nil {
		t.Fatalf("verified closed target stayed fenced: %v", err)
	}
	if len(p.opened) != 1 || len(p.closed) != 1 || len(freshProvider.opened) != 1 {
		t.Fatal("unexpected resource effect count")
	}
}

func TestCleanupReceiptBindsOpeningAdmission(t *testing.T) {
	m, _ := auditManager(t, &fakeProvider{})
	if _, err := m.Open(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	events, err := m.ledger.List("alice")
	if err != nil {
		t.Fatal(err)
	}
	opening := ""
	for _, event := range events {
		if event.Phase == "admitted" && event.Action == "exposure.open" && event.CleanupAction == "exposure.close" {
			opening = event.OperationID
		}
		if event.Phase == "completed" && event.Action == "exposure.close" && opening != "" && event.CleanupOf == opening {
			return
		}
	}
	t.Fatal("cleanup omitted its durable opening reservation")
}

func TestRequestedCloseReusesUnsettledCleanupAdmission(t *testing.T) {
	p := &auditFaultProvider{closeFailures: 1}
	m, _ := auditManager(t, p)
	if _, err := m.Open(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background(), "demo"); !errors.Is(err, audit.ErrUncertain) {
		t.Fatal("unconfirmed close reported success")
	}
	if err := m.Close(context.Background(), "demo"); err != nil {
		t.Fatalf("requested stop could not finish its reserved receipt: %v", err)
	}
	events, err := m.ledger.List("alice")
	if err != nil {
		t.Fatal(err)
	}
	admitted, completed := 0, 0
	for _, event := range events {
		if event.CleanupOf != "" && event.Phase == "admitted" {
			admitted++
		}
		if event.CleanupOf != "" && event.Phase == "completed" {
			completed++
		}
	}
	if admitted != 1 || completed != 1 || len(p.opened) != 1 || len(p.closed) != 1 {
		t.Fatal("cleanup was readmitted or resource opening repeated")
	}
}
