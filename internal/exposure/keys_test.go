package exposure

import (
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
