package exposure

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestGenerateKeypair(t *testing.T) {
	priv, pub, err := GenerateKeypair()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatalf("private half not parseable: %v", err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(pub)
	if err != nil {
		t.Fatalf("public half not authorized_key: %v", err)
	}
	if ssh.FingerprintSHA256(parsed) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatal("halves disagree")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(pub)), "ssh-ed25519 ") {
		t.Fatalf("public key type: %q", pub)
	}
}

func TestEnrolFileRef(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(validSishYAML, `key_ref: "broker:grant-01"`, `key_ref: "file:sish-key.pem"`, 1)
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	res, err := cfg.Enrol(dir)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if !res.Generated || res.BrokerGrant != "" {
		t.Fatalf("result: %+v", res)
	}
	priv, err := os.ReadFile(res.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(res.KeyPath)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("key mode: %v", info.Mode())
		}
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(res.PublicLine))
	if err != nil || ssh.FingerprintSHA256(pub) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Fatalf("public line mismatch: %v", err)
	}
	again, err := cfg.Enrol(dir)
	if err != nil || again.Generated || again.PublicLine != res.PublicLine {
		t.Fatalf("enrol must reuse existing key: %+v %v", again, err)
	}
}

func TestEnrolBrokerRef(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(writeConfig(t, validSishYAML))
	if err != nil {
		t.Fatal(err)
	}
	res, err := cfg.Enrol(dir)
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if res.BrokerGrant != "grant-01" || !res.Generated {
		t.Fatalf("result: %+v", res)
	}
	priv, err := os.ReadFile(filepath.Join(dir, "sish-enrol-key.pem"))
	if err != nil {
		t.Fatalf("staging key: %v", err)
	}
	if _, err := ssh.ParsePrivateKey(priv); err != nil {
		t.Fatalf("staging key unreadable: %v", err)
	}
	if res.KeyPath != filepath.Join(dir, "sish-enrol-key.pem") {
		t.Fatalf("key path: %q", res.KeyPath)
	}
}

func TestEnrolRejectsCloudflared(t *testing.T) {
	cfg, err := Load(writeConfig(t, `schema: 1
provider: cloudflared
key_ref: "broker:grant-01"
endpoints: {a: {target: hub_public_listener}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Enrol(t.TempDir()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cloudflared keypair enrolment must fail: %v", err)
	}
}
