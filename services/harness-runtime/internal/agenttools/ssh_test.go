package agenttools

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/ssh"
)

func sshTestConfig(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.MkdirAll(filepath.Join(dir, "keys"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "keys", "id"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	hostKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	body := "schema: 1\nhosts:\n  box:\n    host: 127.0.0.1\n    user: deploy\n    host_keys: [\"" + hostKey + "\"]\n    key_ref: \"file:keys/id\"\n    commands: [\"uptime\"]\n"
	path := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func toolNames(t *testing.T, v *Tools) map[string]bool {
	t.Helper()
	ctx := context.Background()
	a, b := mcp.NewInMemoryTransports()
	server, err := v.Server().Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	list, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, tool := range list.Tools {
		out[tool.Name] = true
	}
	return out
}

// Catalog r5 publishes the ssh family unconditionally — the catalog is the
// model surface; the capability itself stays opt-in and grant-gated at call
// time: no config fails closed, a write/shell/tunnel without its grant names
// the missing grant.
func TestSSHToolsAreOptInAndGrantGated(t *testing.T) {
	base := fixture(t)
	for _, want := range []string{"ssh_list", "ssh_read", "ssh_write", "ssh_exec", "ssh_shell", "ssh_tunnel"} {
		if !toolNames(t, base)[want] {
			t.Fatal("catalog ssh tool missing from surface:", want)
		}
	}
	for _, name := range []string{"ssh_exec", "ssh_list", "ssh_read"} {
		if _, err := base.Call(context.Background(), name, Input{Host: "box", Command: "uptime", Path: "/etc/motd"}); err == nil ||
			!strings.Contains(err.Error(), "not configured") {
			t.Fatalf("%s without config must fail closed: %v", name, err)
		}
	}

	config := sshTestConfig(t)
	t.Setenv("HUB_SSH_CONFIG", config)
	t.Setenv("HUB_STATE", t.TempDir())
	ro := fixture(t)
	if ro.SSH == nil {
		t.Fatal("service did not open with a valid config")
	}
	for _, denied := range []struct {
		name string
		in   Input
	}{
		{"ssh_write", Input{Host: "box", Path: "/etc/motd", Text: "x"}},
		{"ssh_shell", Input{Action: "open", Host: "box"}},
		{"ssh_tunnel", Input{Action: "open", Host: "box", Name: "t"}},
	} {
		if _, err := ro.Call(context.Background(), denied.name, denied.in); err == nil ||
			!strings.Contains(err.Error(), "grant is not active") {
			t.Fatalf("%s without grant: %v", denied.name, err)
		}
	}

	t.Setenv("HUB_SSH_WRITE", "true")
	t.Setenv("HUB_SSH_SHELL", "true")
	t.Setenv("HUB_SSH_TUNNEL", "true")
	full := fixture(t)
	if !full.SSHGrants.Write || !full.SSHGrants.Shell || !full.SSHGrants.Tunnel {
		t.Fatal("grants not picked up:", full.SSHGrants)
	}
	// Granted calls now reach the service and fail on the unreachable host —
	// never on the grant check.
	if _, err := full.Call(context.Background(), "ssh_write", Input{Host: "box", Path: "/etc/motd", Text: "x"}); err == nil ||
		strings.Contains(err.Error(), "grant is not active") {
		t.Fatal("granted ssh_write must pass the grant check:", err)
	}
}

func TestSSHBrokenConfigStillRegistersTools(t *testing.T) {
	broken := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(broken, []byte("schema: 9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_SSH_CONFIG", broken)
	t.Setenv("HUB_STATE", t.TempDir())
	v := fixture(t)
	names := toolNames(t, v)
	if !names["ssh_exec"] || v.SSH != nil || v.sshErr == nil {
		t.Fatal("broken config must keep tools visible and fail closed")
	}
	if _, err := v.Call(context.Background(), "ssh_exec", Input{Host: "box", Command: "uptime"}); err == nil {
		t.Fatal("broken config must fail the call, not panic")
	}
}
