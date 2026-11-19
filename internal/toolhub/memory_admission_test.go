package toolhub

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

func TestWaitingMemoryCallDoesNotHoldRegistryFence(t *testing.T) {
	store, _, _, auths, _ := memoryCardStore(t, "alice", "bob")
	passports, vaults := t.TempDir(), t.TempDir()
	for _, user := range []string{"alice", "bob"} {
		writeMemoryPassportEnv(t, passports, user, "dev", true)
		if err := vault.Ensure(filepath.Join(vaults, user)); err != nil {
			t.Fatal(err)
		}
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	u, err := g.Vaults.Resolve("alice", "dev")
	if err != nil {
		t.Fatal(err)
	}
	u.mu.Lock()
	locked := true
	defer func() {
		if locked {
			u.mu.Unlock()
		}
	}()
	admitted := make(chan struct{}, 1)
	g.AuditWrite = func(event string, fields map[string]string) error {
		if event == "admit" && fields["principal_id"] == "alice" {
			admitted <- struct{}{}
		}
		return nil
	}
	wrongRun := make(chan error, 1)
	go func() {
		_, err := memoryCardCall(t, g, auths["alice"], "job-alice", "foreign-run", map[string]any{"op": "remember", "name": "foreign", "text": "must not write"})
		wrongRun <- err
	}()
	select {
	case err := <-wrongRun:
		if !errors.Is(err, api.ErrUnauthorized) {
			t.Fatal("invalid run was not denied before touching the held vault")
		}
	case <-time.After(time.Second):
		t.Fatal("invalid run waited for vault access before authorization")
	}
	done := make(chan error, 1)
	go func() {
		out, err := memoryCardCall(t, g, auths["alice"], "job-alice", "run-alice-1", map[string]any{"op": "remember", "name": "waiting", "text": "alice only"})
		if err == nil && out["status"] != "ok" {
			err = errors.New("waiting owner write did not succeed")
		}
		done <- err
	}()
	select {
	case <-admitted:
		t.Error("memory dispatch holds the registry admission fence while waiting for its vault")
	case <-time.After(250 * time.Millisecond):
	}
	writer := make(chan struct{})
	go func() { store.mu.Lock(); close(writer); store.mu.Unlock() }()
	select {
	case <-writer:
	case <-time.After(time.Second):
		t.Error("blocked owner prevented a registry writer from progressing")
	}
	bob := make(chan error, 1)
	go func() {
		out, err := memoryCardCall(t, g, auths["bob"], "job-bob", "run-bob-1", map[string]any{"op": "remember", "name": "independent", "text": "bob only"})
		if err == nil && out["status"] != "ok" {
			err = errors.New("independent owner write did not succeed")
		}
		bob <- err
	}()
	select {
	case err := <-bob:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("blocked owner's vault prevented the other owner's memory call")
	}
	u.mu.Unlock()
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("memory call did not resume after its vault was released")
	}
}

func TestMemoryAuthorityRecheckedAfterVaultWait(t *testing.T) {
	store, gov, _, auths, _ := memoryCardStore(t, "alice")
	passports, vaults := t.TempDir(), t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	if err := vault.Ensure(filepath.Join(vaults, "alice")); err != nil {
		t.Fatal(err)
	}
	g, _ := memoryCardEnv(t, store, passports, vaults, auths, nil)
	u, err := g.Vaults.Resolve("alice", "dev")
	if err != nil {
		t.Fatal(err)
	}
	u.mu.Lock()
	locked := true
	defer func() {
		if locked {
			u.mu.Unlock()
		}
	}()
	verified := make(chan struct{}, 1)
	authority := g.Runs
	g.Runs = runAuthorityFunc(func(ctx context.Context, job string) (api.RunContext, error) {
		record, err := authority.Run(ctx, job)
		select {
		case verified <- struct{}{}:
		default:
		}
		return record, err
	})
	done := make(chan error, 1)
	go func() {
		_, err := memoryCardCall(t, g, auths["alice"], "job-alice", "run-alice-1", map[string]any{"op": "remember", "name": "revoked", "text": "must not write"})
		done <- err
	}()
	select {
	case <-verified:
	case <-time.After(2 * time.Second):
		t.Fatal("memory run was not verified before waiting for its vault")
	}
	if err := gov.RevokeRight(api.DeterministicID("rightgrant", "alice", "memory", "remember"), "platform", "revoke during vault wait", time.Now()); err != nil {
		t.Fatal(err)
	}
	u.mu.Unlock()
	locked = false
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("memory write used its pre-wait authority after revoke")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked memory call did not finish")
	}
	files, err := filepath.Glob(filepath.Join(vaults, "alice", "memory", "cards", "notes", "inbox", "*.md"))
	if err != nil || len(files) != 0 {
		t.Fatal("revoked waiting call created a memory card")
	}
}
