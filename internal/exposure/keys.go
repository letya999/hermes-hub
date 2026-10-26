package exposure

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// GenerateKeypair returns an ed25519 OpenSSH private key (unencrypted PEM)
// and its authorized_keys line. The private half is credential material —
// it travels only through Broker delivery, never argv, logs or tool results.
func GenerateKeypair() (privatePEM, publicLine []byte, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("keypair: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("keypair: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, nil, fmt.Errorf("keypair: %w", err)
	}
	return pem.EncodeToMemory(block), ssh.MarshalAuthorizedKey(signer.PublicKey()), nil
}
