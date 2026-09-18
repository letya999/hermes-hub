package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/securefs"
)

// Init creates an explicit loopback-only development installation. It neither
// authorizes an external account nor starts a service. Existing keys are never overwritten.
func Init(root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("init requires an absolute directory")
	}
	for _, dir := range []string{root, filepath.Join(root, "keys"), filepath.Join(root, "contracts"), filepath.Join(root, "state"), filepath.Join(root, "runtime"), filepath.Join(root, "secrets")} {
		if e := securefs.Dir(dir); e != nil {
			return e
		}
	}
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		return e
	}
	defer clear(key)
	if e := securefs.Create(filepath.Join(root, "keys"), "master.key", key, 0600); e != nil {
		return e
	}
	c := Config{Listen: "127.0.0.1:8787", PublicOrigin: "http://127.0.0.1:8787", DataDir: filepath.Join(root, "state"), RuntimeDir: filepath.Join(root, "runtime"), MasterKeyFile: filepath.Join(root, "keys/master.key"), ContractsDir: filepath.Join(root, "contracts"), DevHTTP: true, MaxLedgerBytes: 64 << 20, Providers: []ProviderConfig{{ID: "local", Type: "local", Directory: filepath.Join(root, "secrets")}}}
	for _, v := range []struct{ id, issuer, audience string }{{"toolhub", "hermes-toolhub", "broker:control"}, {"communication", "hermes-communication", "broker:approve"}, {"runtime", "hermes-runtime-adapter", "broker:runtime"}} {
		pub, priv, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		if e = securefs.Create(filepath.Join(root, "keys"), v.id+".private", priv, 0600); e != nil {
			return e
		}
		if e = securefs.Create(filepath.Join(root, "keys"), v.id+".public", pub, 0600); e != nil {
			return e
		}
		clear(priv)
		c.TrustedKeys = append(c.TrustedKeys, KeyConfig{ID: v.id, Issuer: v.issuer, PublicKeyFile: filepath.Join(root, "keys", v.id+".public"), Audiences: []string{v.audience}})
	}
	cc := contract.Contract{ID: "github-pat", Revision: 1, Title: "GitHub: персональный токен", Storage: "local", Fields: []contract.Field{{ID: "token", Label: "GitHub token", Kind: "secret", Required: true, MaxBytes: 4096}}, Deliveries: []contract.Delivery{{Type: "env", Field: "token", Target: "GITHUB_PERSONAL_ACCESS_TOKEN"}}, AllowShared: false}
	if e := WriteJSON(filepath.Join(root, "contracts/github-pat.json"), cc); e != nil {
		return e
	}
	if e := WriteJSON(filepath.Join(root, "actor.json"), identity.Actor{PrincipalID: "artem", ContextID: "artem", RuntimeID: "hermes-artem", ExternalIdentityID: "telegram-artem", ConversationID: "conversation-demo", PolicyVersion: "demo-policy-v1"}); e != nil {
		return e
	}
	return WriteJSON(filepath.Join(root, "config.json"), c)
}
