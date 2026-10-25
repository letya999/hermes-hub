package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A single principal may own several contexts, but the host must bound how
// many containers that one principal runs concurrently: HUB_MAX_RUNTIMES_PER_PRINCIPAL
// caps live runtimes per principal so one user cannot exhaust host capacity.
func TestRuntimePerPrincipalCeiling(t *testing.T) {
	t.Setenv("HUB_MAX_RUNTIMES_PER_PRINCIPAL", "1")
	m, aliceRoot := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	root := filepath.Dir(aliceRoot)
	first, err := m.Ensure(context.Background(), binding(aliceRoot))
	if err != nil || first.RuntimeID == "" {
		t.Fatalf("first ensure: %v", err)
	}
	secondCtx := filepath.Join(root, "ctx2")
	if err := os.MkdirAll(secondCtx, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondCtx, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, secondCtx)
	second := binding(secondCtx)
	second.ContextID = "ctx2"
	if _, err := m.Ensure(context.Background(), second); err == nil {
		t.Fatal("per-principal runtime ceiling not enforced")
	}
	bobCtx := filepath.Join(root, "bob")
	if err := os.MkdirAll(bobCtx, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bobCtx, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writeHermesSources(t, bobCtx)
	bob := binding(bobCtx)
	bob.PrincipalID, bob.ContextID, bob.RuntimeID, bob.UserID = "bob", "bob", "bob", "bob"
	if _, err := m.Ensure(context.Background(), bob); err != nil {
		t.Fatalf("other principal refused: %v", err)
	}
}
