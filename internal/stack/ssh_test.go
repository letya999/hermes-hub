package stack

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// writeSSHConfig gives Render a valid per-owner SSH capability config.
func writeSSHConfig(t *testing.T, dir string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	hostKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	sshDir := filepath.Join(dir, "connections", "ssh")
	if err = os.MkdirAll(filepath.Join(sshDir, "keys"), 0750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(sshDir, "keys", "id"), []byte("x"), 0640); err != nil {
		t.Fatal(err)
	}
	body := "schema: 1\nhosts:\n  box:\n    host: 127.0.0.1\n    user: deploy\n    host_keys: [\"" + hostKey + "\"]\n    key_ref: \"file:keys/id\"\n    commands: [\"uptime\"]\n"
	if err = os.WriteFile(filepath.Join(sshDir, "config.yaml"), []byte(body), 0640); err != nil {
		t.Fatal(err)
	}
}

func TestSSHFeatureValidationAndDoctor(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	for _, sub := range []string{"write", "shell", "tunnel"} {
		copy := s
		copy.Tools = map[string]ToolEntry{"ssh": {Via: "off", Tools: map[string]bool{sub: true}}}
		if copy.Validate() == nil {
			t.Fatal("ssh_" + sub + " accepted on a denied ssh")
		}
		copy.Tools = map[string]ToolEntry{"ssh": {Via: "toolhub", Tools: map[string]bool{"bogus": true}}}
		if copy.Validate() == nil {
			t.Fatal("unknown ssh toggle accepted")
		}
	}
	s.Tools, s.Ingress = testTools("ssh"), testIngress("ssh")
	if err := s.Validate(); err != nil {
		t.Fatal("ssh alone should validate:", err)
	}
	s.SpaceDir = t.TempDir()
	if !strings.Contains(strings.Join(Doctor(s, nil), " "), "connections/ssh/config.yaml") {
		t.Fatal("doctor must flag a missing ssh config")
	}
	writeSSHConfig(t, s.SpaceDir)
	for _, issue := range Doctor(s, nil) {
		if strings.Contains(issue, "connections/ssh") {
			t.Fatal("doctor still flags ssh config after creation:", issue)
		}
	}
}

func TestSSHRenderMountsConfigAndWiresEnv(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "me", "r-0000000000000001")
	writeSSHConfig(t, d)
	s, err := ReadEffective(d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.Model = "test"
	s.ModelURL = "http://host.docker.internal:8317/v1"
	s.Tools, s.Ingress = testTools("ssh", "ssh_write", "ssh_tunnel"), testIngress("ssh", "ssh_write", "ssh_tunnel")
	if err = saveSpace(t, d, s); err != nil {
		t.Fatal(err)
	}
	if err = RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	// Managed runtimes are supervisor-spawned: the ssh state dir is a
	// read-only mount in the RuntimeService spawn contract, not the compose
	// file.
	found := false
	for _, v := range RuntimeService(s, root, d)["volumes"].([]any) {
		m := v.(M)
		if m["target"] == "/state/ssh" {
			if m["read_only"] != true {
				t.Fatal("ssh config mount must be read-only")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("ssh config mount missing from RuntimeService")
	}
	managedEnv := RuntimeService(s, root, d)["environment"].(M)
	if managedEnv["HUB_SSH_CONFIG"] != "/state/ssh/config.yaml" || managedEnv["HUB_SSH_WRITE"] != "true" || managedEnv["HUB_SSH_TUNNEL"] != "true" || managedEnv["HUB_SSH_SHELL"] != "false" {
		t.Fatal("managed SSH config environment missing")
	}
	// The unmanaged `hub` stdio server carries the same reviewed settings.
	unmanaged := s
	unmanaged.CapabilityMode = ""
	hub := Config(unmanaged)["mcp_servers"].(M)["hub"].(M)
	env := hub["env"].(M)
	if env["HUB_SSH_CONFIG"] != "/state/ssh/config.yaml" || env["HUB_SSH_WRITE"] != "true" || env["HUB_SSH_TUNNEL"] != "true" || env["HUB_SSH_SHELL"] != "false" {
		t.Fatalf("ssh env wrong: %v", env)
	}
	if env["HUB_CREDENTIAL_BROKER_RUNTIME_URL"] != "${HUB_CREDENTIAL_BROKER_RUNTIME_URL}" {
		t.Fatal("broker runtime env not wired through")
	}
}

func TestSSHRenderFailsClosedOnBrokenConfig(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "me", "r-0000000000000001")
	s, err := ReadEffective(d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.Tools, s.Ingress = testTools("ssh"), testIngress("ssh")
	if err = saveSpace(t, d, s); err != nil {
		t.Fatal(err)
	}
	if err = RenderEnvironment(d, root, "prod"); err == nil {
		t.Fatal("render accepted ssh without a config")
	}
}
