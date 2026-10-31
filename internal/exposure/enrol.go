package exposure

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// EnrolResult is what `hubctl exposure enrol` reports — it never carries
// the private half, only where that half was materialized.
type EnrolResult struct {
	PublicLine  string // authorized_keys line for the sish server
	KeyPath     string // where the private key landed (file ref or staging)
	BrokerGrant string // grant id when key_ref is broker:
	Generated   bool   // true when fresh key material was created
}

// Enrol ensures key material for the reviewed config exists under keyDir
// and returns the enrolment material. Existing keys are reused — enrolment
// never rotates silently, because the sish server pins the public half.
func (c *Config) Enrol(keyDir string) (*EnrolResult, error) {
	if c.Provider != "sish" {
		return nil, fmt.Errorf("%w: keypair enrolment is sish-only", ErrInvalid)
	}
	scheme, rest, _ := strings.Cut(c.KeyRef, ":")
	res := &EnrolResult{}
	switch scheme {
	case "file":
		res.KeyPath = filepath.Join(keyDir, filepath.FromSlash(rest))
	default: // broker: — stage for the operator to push into the grant
		res.KeyPath = filepath.Join(keyDir, "sish-enrol-key.pem")
		res.BrokerGrant = rest
	}
	if priv, err := os.ReadFile(res.KeyPath); err == nil { // #nosec G304 -- path composed from validated ref.
		signer, err := ssh.ParsePrivateKey(priv)
		if err != nil {
			return nil, fmt.Errorf("%w: existing key at %s unreadable", ErrInvalid, res.KeyPath)
		}
		res.PublicLine = string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
		return res, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	priv, pub, err := GenerateKeypair()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(res.KeyPath, priv, 0600); err != nil {
		return nil, err
	}
	res.PublicLine = string(pub)
	res.Generated = true
	return res, nil
}
