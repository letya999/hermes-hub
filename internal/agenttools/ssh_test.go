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

func TestSSHToolsAreOptInAndGrantGated(t *testing.T) {
	base := fixture(t)
	for name := range toolNames(t, base) {
		if strings.HasPrefix(name, "ssh_") {
			t.Fatal("ssh tool registered without the feature:", name)
		}
	}

	config := sshTestConfig(t)
	t.Setenv("HUB_SSH_CONFIG", config)
	t.Setenv("HUB_STATE", t.TempDir())
	ro := fixture(t)
	names := toolNames(t, ro)
	for _, want := range []string{"ssh_hosts", "ssh_exec", "ssh_read"} {
		if !names[want] {
			t.Fatal("base ssh tool missing:", want)
		}
	}
	for _, deny := range []string{"ssh_write", "ssh_shell_open", "ssh_tunnel_open"} {
		if names[deny] {
			t.Fatal("privileged ssh tool registered without grant:", deny)
		}
	}
	if ro.SSH == nil {
		t.Fatal("service did not open with a valid config")
	}

	t.Setenv("HUB_SSH_WRITE", "true")
	t.Setenv("HUB_SSH_SHELL", "true")
	t.Setenv("HUB_SSH_TUNNEL", "true")
	full := fixture(t)
	names = toolNames(t, full)
	for _, want := range []string{"ssh_write", "ssh_shell_open", "ssh_shell_send", "ssh_shell_read", "ssh_shell_close", "ssh_tunnel_open", "ssh_tunnel_list", "ssh_tunnel_close"} {
		if !names[want] {
			t.Fatal("granted ssh tool missing:", want)
		}
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
}
