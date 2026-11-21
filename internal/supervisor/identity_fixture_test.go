package supervisor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identityhub"
)

// Old lifecycle fixtures explicitly provision synthetic principals; production
// New leaves registry ownership to Serve and never trusts fixture defaults.
func newTestManager(t testing.TB, cfg Config) (*Manager, error) {
	t.Helper()
	// Managed claims move through contracts/spawn/v1; tests get the stub so
	// they exercise supervisor bookkeeping, not a live spawn-controller.
	if cfg.Spawn == nil {
		cfg.Spawn = &claimStub{}
	}
	m, err := New(cfg)
	if err != nil || cfg.Identity != nil {
		return m, err
	}
	r, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = r.Close() })
	_, err = r.ImportLegacy(context.Background(), "fixture", "personal", []commodel.User{
		{ID: "alice", Enabled: true, TelegramIDs: []int64{1, 7, 11}},
		{ID: "bob", Enabled: true, TelegramIDs: []int64{2, 8, 22}},
	})
	if err != nil {
		return nil, err
	}
	m.cfg.Identity = r
	return m, nil
}

func testSetIdentity(t *testing.T, m *Manager, organization string, users ...commodel.User) *identityhub.Registry {
	t.Helper()
	r, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := r.ImportLegacy(context.Background(), "fixture", organization, users); err != nil {
		t.Fatal(err)
	}
	m.cfg.Identity = r
	return r
}

func testWarmBinding(t *testing.T, m *Manager, input Binding) Binding {
	t.Helper()
	bound, err := m.normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	_, revision, err := m.identityTemplate(t.Context(), bound)
	if err != nil {
		t.Fatal(err)
	}
	bound.IdentityRevision = revision
	return bound
}
