package toolhub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// writeMemoryPassportEnv publishes a six-passport revision for user in the
// given environment — the server-owned enrollment record the resolver reads.
func writeMemoryPassportEnv(t *testing.T, root, user, environment string, enabled bool) {
	t.Helper()
	writeMemoryPassportDecl(t, root, user, user, "r-0123456789abcdef", environment, enabled)
}

// writeMemoryPassportDecl publishes a revision under dirName declaring
// space — a directory naming one principal while claiming another is a
// forged enrollment, not a second binding.
func writeMemoryPassportDecl(t *testing.T, root, dirName, space, revision, environment string, enabled bool) {
	t.Helper()
	dir := filepath.Join(root, dirName, "passports", revision)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range spawnv1.Names {
		body := "apiVersion: six-passport/v2\nkind: " + name + "\nmetadata: {space: " + space + ", environment: " + environment + ", revision: " + revision + "}\n"
		if name == "config" {
			body += "memory: {enabled: " + map[bool]string{true: "true", false: "false"}[enabled] + "}\n"
		}
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, dirName, "passports", "current"), []byte(revision+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// vaultDirWithMarker materializes a real vault and seeds a distinct marker
// the test can read back to prove which vault was resolved.
func vaultDirWithMarker(t *testing.T, root, principal, marker string) string {
	t.Helper()
	dir := filepath.Join(root, principal)
	if err := vault.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory", "CORE.md"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readMarker(t *testing.T, u *UserVault) string {
	t.Helper()
	var marker string
	if err := u.Locked(func(root string) error {
		body, err := os.ReadFile(filepath.Join(root, "memory", "CORE.md"))
		if err != nil {
			return err
		}
		marker = string(body)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return marker
}

// TestToolHubVaultResolverIsolatesUsers proves two synthetic users and the
// dev/prod environments map independently, that identity/traversal/symlink
// and wrong-environment resolves fail before the vault is read, and that a
// blocked operation on one user never holds the other user's lock.
func TestToolHubVaultResolverIsolatesUsers(t *testing.T) {
	passports := t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	writeMemoryPassportEnv(t, passports, "bob", "prod", true)
	writeMemoryPassportDecl(t, passports, "mallory", "alice", "r-0123456789abcdef", "dev", true)
	root := t.TempDir()
	vaultDirWithMarker(t, root, "alice", "alice-vault-marker")
	vaultDirWithMarker(t, root, "bob", "bob-vault-marker")
	vaultDirWithMarker(t, root, "mallory", "mallory-vault-marker")
	resolver, err := NewVaultResolver(root, dirTreeReader{root: passports})
	if err != nil {
		t.Fatal(err)
	}

	alice, err := resolver.Resolve("alice", "dev")
	if err != nil {
		t.Fatalf("alice's enrolled vault denied: %v", err)
	}
	if got := readMarker(t, alice); got != "alice-vault-marker" {
		t.Fatalf("alice resolved a foreign vault: %q", got)
	}
	bob, err := resolver.Resolve("bob", "prod")
	if err != nil {
		t.Fatalf("bob's enrolled vault denied: %v", err)
	}
	if got := readMarker(t, bob); got != "bob-vault-marker" {
		t.Fatalf("bob resolved a foreign vault: %q", got)
	}
	// Wrong environment never opens the vault, even with a rendered binding.
	for _, denied := range [][2]string{{"alice", "prod"}, {"bob", "dev"}, {"alice", "staging"}, {"alice", ""}} {
		if _, err := resolver.Resolve(denied[0], denied[1]); err == nil {
			t.Fatalf("resolve(%q,%q) opened a vault across environments", denied[0], denied[1])
		}
	}
	// Unknown identity, traversal and a forged enrollment claim fail before open.
	for _, denied := range []string{"carol", "../alice", "alice/../bob", "mallory", "a b", ""} {
		if _, err := resolver.Resolve(denied, "dev"); err == nil {
			t.Fatalf("resolve(%q) opened a vault", denied)
		}
	}
	// A caller-supplied path is never consulted: a vault outside the binding
	// root is unreachable — there is no resolver input that can name it.
	outside := filepath.Join(t.TempDir(), "outside-vault")
	if err := vault.Ensure(outside); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(".."+string(filepath.Separator)+filepath.Base(outside), "dev"); err == nil {
		t.Fatal("traversal reached an outside vault")
	}

	// Lock isolation: alice's blocked operation never stalls bob.
	entered := make(chan struct{})
	release := make(chan struct{})
	aliceDone := make(chan error, 1)
	go func() {
		aliceDone <- alice.Locked(func(string) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	bobDone := make(chan error, 1)
	go func() { bobDone <- bob.Locked(func(string) error { return nil }) }()
	select {
	case err := <-bobDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("alice's blocked operation held bob's lock")
	}
	close(release)
	if err := <-aliceDone; err != nil {
		t.Fatal(err)
	}
}

// Unsafe bindings fail before open: a regular file and a symlinked entry are
// poisoned at scan time so they can never resolve as a vault.
func TestVaultResolverRejectsUnsafeBindings(t *testing.T) {
	passports := t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	writeMemoryPassportEnv(t, passports, "plain", "dev", true)
	root := t.TempDir()
	vaultDirWithMarker(t, root, "alice", "alice-vault-marker")
	if err := os.WriteFile(filepath.Join(root, "plain"), []byte("not a vault"), 0600); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewVaultResolver(root, dirTreeReader{root: passports})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve("plain", "dev"); err == nil {
		t.Fatal("a non-directory binding resolved as a vault")
	}
	target := t.TempDir()
	link := filepath.Join(root, "sneaky")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeMemoryPassportEnv(t, passports, "sneaky", "dev", true)
	resolver, err = NewVaultResolver(root, dirTreeReader{root: passports})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve("sneaky", "dev"); err == nil {
		t.Fatal("a symlinked binding escaped the vault root")
	}
}

// The resolver only trusts the startup-scanned table: a binding created
// after startup is not enrolled, and a malformed or escaped root fails
// construction.
func TestVaultResolverStartupBoundaries(t *testing.T) {
	passports := t.TempDir()
	writeMemoryPassportEnv(t, passports, "alice", "dev", true)
	root := t.TempDir()
	resolver, err := NewVaultResolver(root, dirTreeReader{root: passports})
	if err != nil {
		t.Fatal(err)
	}
	vaultDirWithMarker(t, root, "alice", "alice-vault-marker")
	if _, err := resolver.Resolve("alice", "dev"); err == nil {
		t.Fatal("a post-startup binding resolved without a render")
	}
	if _, err := NewVaultResolver(filepath.Join(root, "missing"), dirTreeReader{root: passports}); err == nil {
		t.Fatal("missing bindings root accepted")
	}
	file := filepath.Join(t.TempDir(), "file-root")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewVaultResolver(file, dirTreeReader{root: passports}); err == nil {
		t.Fatal("non-directory bindings root accepted")
	}
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(root, link); err == nil {
		if _, err := NewVaultResolver(link, dirTreeReader{root: passports}); err == nil {
			t.Fatal("symlinked bindings root accepted")
		}
	}
}

// The production startup path consumes the rendered env: with
// HUB_VAULTS_BY_PRINCIPAL set, NewEndpointHandler must expose a working
// resolver — an unreadable one fails the endpoint before serving.
func TestEndpointHandlerWiresVaultResolver(t *testing.T) {
	store, auth, _, _ := managedStore(t)
	passports := t.TempDir()
	writeMemoryPassportEnv(t, passports, auth.PrincipalID, "dev", true)
	stubSpawnClient(t, passports)
	root := t.TempDir()
	vaultDirWithMarker(t, root, auth.PrincipalID, "prod-wiring-marker")
	t.Setenv("HUB_VAULTS_BY_PRINCIPAL", root)
	config := EndpointConfig{Token: strings.Repeat("t", 32), Auth: auth,
		Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
			return api.BackendResult{Text: "ok"}, nil
		})}
	handler, err := NewEndpointHandler(config, store)
	if err != nil {
		t.Fatal(err)
	}
	resolver := handler.(*projectionEndpoint).gateway.Vaults
	if resolver == nil {
		t.Fatal("production wiring dropped the vault resolver")
	}
	u, err := resolver.Resolve(auth.PrincipalID, "dev")
	if err != nil {
		t.Fatalf("wired resolver denied the enrolled principal: %v", err)
	}
	if got := readMarker(t, u); got != "prod-wiring-marker" {
		t.Fatalf("wired resolver read %q", got)
	}
	t.Setenv("HUB_VAULTS_BY_PRINCIPAL", filepath.Join(root, "missing"))
	if _, err := NewEndpointHandler(config, store); err == nil {
		t.Fatal("endpoint accepted an unreadable vault bindings root")
	}
}
