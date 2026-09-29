package sshcap

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func hostKeyLine(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validYAML(t *testing.T) string {
	t.Helper()
	return "schema: 1\nhosts:\n  box:\n    host: 127.0.0.1\n    user: deploy\n    host_keys: [\"" + hostKeyLine(t) + "\"]\n    key_ref: \"file:keys/id_ed25519\"\n    commands: [\"uptime\"]\n"
}

func TestLoadValidatesConfig(t *testing.T) {
	if _, err := Load(writeConfigFile(t, validYAML(t))); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"missing":      "",
		"schema":       strings.Replace(validYAML(t), "schema: 1", "schema: 2", 1),
		"no hosts":     "schema: 1\nhosts: {}\n",
		"no keys":      strings.Replace(validYAML(t), "    host_keys:", "    # host_keys:", 1),
		"no key ref":   strings.Replace(validYAML(t), "    key_ref: \"file:keys/id_ed25519\"\n", "", 1),
		"bad ref":      strings.Replace(validYAML(t), "file:keys/id_ed25519", "env:KEY", 1),
		"file escape":  strings.Replace(validYAML(t), "file:keys/id_ed25519", "file:../escape", 1),
		"bad grant":    strings.Replace(validYAML(t), "file:keys/id_ed25519", "broker:x", 1),
		"no caps":      strings.Replace(validYAML(t), "    commands: [\"uptime\"]\n", "", 1),
		"bad sudo":     validYAML(t) + "    sudo: always\n",
		"bad alias":    strings.Replace(validYAML(t), "  box:", "  Box!:", 1),
		"bad path":     strings.Replace(validYAML(t), "commands: [\"uptime\"]", "commands: [\"uptime\"]\n    paths: [\"relative/x\"]", 1),
		"rel path pat": strings.Replace(validYAML(t), "commands: [\"uptime\"]", "paths: [\"ok\"]", 1),
		"oversized":    strings.Replace(validYAML(t), "timeout_seconds", "x", 1) + "    timeout_seconds: 9999\n",
		"unknown fld":  validYAML(t) + "surprise: 1\n",
	}
	for name, body := range bad {
		if _, err := Load(writeConfigFile(t, body)); err == nil {
			t.Fatalf("%s config accepted", name)
		}
	}
	if _, err := Load(writeConfigFile(t, validYAML(t)+"---\nsecond: doc\n")); err == nil {
		t.Fatal("multi-document config accepted")
	}
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "absent.yaml")); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing config must report unavailable", err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("directory accepted as config")
	}
}

func TestFingerprintPinning(t *testing.T) {
	yaml := validYAML(t)
	cfg, err := Load(writeConfigFile(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.Hosts["box"]
	if len(h.pinned) != 1 {
		t.Fatal("pinned key not parsed")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	fp := strings.TrimPrefix(ssh.FingerprintSHA256(signer.PublicKey()), "SHA256:")
	fpYAML := "schema: 1\nhosts:\n  box:\n    host: 127.0.0.1\n    user: deploy\n    host_keys: [\"sha256:" + fp + "\"]\n    key_ref: \"file:keys/id_ed25519\"\n    commands: [\"uptime\"]\n"
	cfg, err = Load(writeConfigFile(t, fpYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Hosts["box"].hostKeyCallback()("h", nil, signer.PublicKey()); err != nil {
		t.Fatal("fingerprint pin rejected the real key", err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	if err := cfg.Hosts["box"].hostKeyCallback()("h", nil, otherSigner.PublicKey()); !errors.Is(err, ErrDenied) {
		t.Fatal("unpinned key accepted")
	}
}
