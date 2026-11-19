package main

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"github.com/letya999/hermes-hub/internal/passport"
	"github.com/letya999/hermes-hub/internal/reconcile"
	"github.com/letya999/hermes-hub/internal/supervisor"
)

func TestSpaceApplySignedPolicyPointerAndCrashRecovery(t *testing.T) {
	ctx := t.Context()
	root, dir, revision, previous := recoveryRevisions(t, nil)
	const space, environment = "alice", "dev"
	const admin, reader = "synthetic-admin", "synthetic-reader"
	registry, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	manager, err := supervisor.New(supervisor.Config{Identity: registry, SpacesRoot: filepath.Dir(dir), Image: "fixture", Environment: environment, RuntimeAuth: reader, AdminAuth: admin})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(manager.Handler())
	defer server.Close()
	t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", admin)
	t.Setenv("HUB_PASSPORT_PUBLISHER_URL", server.URL)
	bundle, err := revision.PolicyBundle()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contract.NewPassportPublisherHTTPClient(server.URL, reader, server.Client()).StagePassports(ctx, bundle); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("read bearer reached publication: %v", err)
	}
	if _, err := applyConfirm(ctx, dir, root, environment, revision, previous, nil, reconcile.Handlers{}); err != nil {
		t.Fatal(err)
	}
	digest, err := registry.ActivePassports(ctx, space, environment)
	if err != nil || !contract.ValidPolicyDigest(digest) {
		t.Fatalf("space apply did not activate the signed policy: %v", err)
	}
	assertPointer := func() {
		t.Helper()
		active, err := registry.ActivePassports(ctx, space, environment)
		if err != nil || active != digest {
			t.Fatalf("active signed policy diverged: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "generated", "passports", "current"))
		if err != nil || strings.TrimSpace(string(raw)) != digest {
			t.Fatalf("generated pointer does not name the signed digest: %v", err)
		}
		cached, err := passport.Current(dir)
		if err != nil || cached != revision.ID {
			t.Fatalf("cache cannot load the signed revision: %v", err)
		}
	}
	assertPointer()

	next := passport.Revision{ID: "r-0123456789abcded", Provenance: revision.Provenance, Docs: map[string][]byte{}}
	for name, body := range revision.Docs {
		next.Docs[name] = []byte(strings.ReplaceAll(string(body), revision.ID, next.ID))
	}
	original := applyStep
	t.Cleanup(func() { applyStep = original })
	stop := errors.New("synthetic crash after signed activation")
	applyStep = func(step string) {
		if step == "policy" {
			panic(stop)
		}
	}
	var stopped any
	func() {
		defer func() { stopped = recover() }()
		if _, err := applyConfirm(ctx, dir, root, environment, next, revision.ID, nil, reconcile.Handlers{}); err != nil {
			t.Fatal(err)
		}
	}()
	applyStep = original
	if stopped != stop {
		t.Fatalf("apply did not stop after control activated the policy: %v", stopped)
	}
	if active, err := registry.ActivePassports(ctx, space, environment); err != nil || active == digest {
		t.Fatalf("crash fixture did not replace the active policy: %v", err)
	}
	if err := recoverApply(ctx, dir, root, environment, reconcile.Handlers{}); err != nil {
		t.Fatal(err)
	}
	assertPointer()
	t.Setenv("HUB_SUPERVISOR_ADMIN_AUTH", "")
	if _, err := applyConfirm(ctx, dir, root, environment, next, revision.ID, nil, reconcile.Handlers{}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("missing publisher credential could replace a signed policy: %v", err)
	}
	assertPointer()
}
