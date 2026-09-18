// Package credentialbroker is the small root-module adapter around the copied
// Credential Broker client. It owns key-file hygiene and identity conversion;
// it does not cache or expose plaintext credentials.
package credentialbroker

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	brokerclient "github.com/letya999/credential-broker/client"
	brokeridentity "github.com/letya999/credential-broker/identity"
	"github.com/letya999/hermes-hub/internal/identity"
)

type Config struct {
	URL     string
	KeyFile string
	KeyID   string
	Issuer  string
	// HTTPClient is optional and is intended for an operator-provided trust
	// store (for example a private CA). FromEnv leaves it nil, so production
	// keeps the platform CA policy by default.
	HTTPClient *http.Client
}

func (c Config) Enabled() bool { return strings.TrimSpace(c.URL) != "" }

func (c Config) New(auth identity.Envelope, audience string) (*brokerclient.Client, error) {
	if !c.Enabled() {
		return nil, errors.New("credential broker is not configured")
	}
	key, err := readPrivateKey(c.KeyFile)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	signingKey := append(ed25519.PrivateKey(nil), key...)
	actor := Actor(auth, "", "")
	return brokerclient.New(c.URL, brokerclient.Signer{
		KeyID: c.KeyID, Issuer: c.Issuer, Audience: audience, PrivateKey: signingKey,
	}, actor, c.HTTPClient)
}

func (c Config) NewForBinding(auth identity.Envelope, audience, bindingID, workloadID string) (*brokerclient.Client, error) {
	if !c.Enabled() {
		return nil, errors.New("credential broker is not configured")
	}
	key, err := readPrivateKey(c.KeyFile)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	signingKey := append(ed25519.PrivateKey(nil), key...)
	return brokerclient.New(c.URL, brokerclient.Signer{
		KeyID: c.KeyID, Issuer: c.Issuer, Audience: audience, PrivateKey: signingKey,
	}, Actor(auth, bindingID, workloadID), c.HTTPClient)
}

func Actor(auth identity.Envelope, bindingID, workloadID string) brokeridentity.Actor {
	return brokeridentity.Actor{
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		ExternalIdentityID: auth.ExternalIdentityID, ConversationID: auth.ConversationID,
		PolicyVersion: auth.PolicyVersion, BindingID: bindingID, WorkloadID: workloadID,
	}
}

func FromEnv(prefix string) (Config, error) {
	if prefix == "" {
		return Config{}, errors.New("credential broker env prefix is required")
	}
	c := Config{
		URL: os.Getenv(prefix + "URL"), KeyFile: os.Getenv(prefix + "KEY_FILE"),
		KeyID: os.Getenv(prefix + "KEY_ID"), Issuer: os.Getenv(prefix + "ISSUER"),
	}
	if !c.Enabled() {
		return Config{}, nil
	}
	if c.KeyFile == "" || c.KeyID == "" || c.Issuer == "" {
		return Config{}, errors.New("credential broker URL requires key file, key ID and issuer")
	}
	return c, nil
}

func readPrivateKey(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("credential broker signing key must be an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("credential broker signing key must be a private regular file")
	}
	key, err := os.ReadFile(path)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		clear(key)
		return nil, errors.New("credential broker signing key is invalid")
	}
	return key, nil
}
