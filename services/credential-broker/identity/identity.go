// Package identity is the versioned Hermes identity-assertion boundary.
// Assertions are purpose-separated Ed25519 signatures, not general-purpose JWTs.
package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/letya999/credential-broker/internal/strictjson"
)

var ErrUnauthorized = errors.New("unauthorized")
var rawID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

func ValidID(s string) bool { return rawID.MatchString(s) }

// Actor carries canonical IDs, never Telegram/Slack names. ContextManager is
// an assertion by the trusted Hub, NOT a user-editable browser field.
type Actor struct {
	PrincipalID        string `json:"principal_id"`
	ContextID          string `json:"context_id"`
	RuntimeID          string `json:"runtime_id"`
	ExternalIdentityID string `json:"external_identity_id,omitempty"`
	ConversationID     string `json:"conversation_id,omitempty"`
	PolicyVersion      string `json:"policy_version"`
	ContextManager     bool   `json:"context_manager,omitempty"`
	BindingID          string `json:"binding_id,omitempty"`
	WorkloadID         string `json:"workload_id,omitempty"`
}

func (a Actor) Valid() bool {
	if !ValidID(a.PrincipalID) || !ValidID(a.ContextID) || !ValidID(a.RuntimeID) || a.PolicyVersion == "" || len(a.PolicyVersion) > 128 {
		return false
	}
	for _, s := range []string{a.ExternalIdentityID, a.ConversationID, a.BindingID, a.WorkloadID} {
		if s != "" && !ValidID(s) {
			return false
		}
	}
	return true
}

type Claims struct {
	Version    int    `json:"version"`
	KeyID      string `json:"key_id"`
	Issuer     string `json:"issuer"`
	Audience   string `json:"audience"`
	IssuedAt   int64  `json:"issued_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Method     string `json:"method"`
	RequestURI string `json:"request_uri"`
	BodySHA256 string `json:"body_sha256"`
	Actor      Actor  `json:"actor"`
}

type TrustedKey struct {
	PublicKey ed25519.PublicKey
	Issuer    string
	Audiences []string
}
type Verifier struct {
	Keys map[string]TrustedKey
	Now  func() time.Time
}

func Hash(body []byte) string { h := sha256.Sum256(body); return hex.EncodeToString(h[:]) }
func Sign(key ed25519.PrivateKey, c Claims) (string, error) {
	if len(key) != ed25519.PrivateKeySize || !c.Actor.Valid() {
		return "", ErrUnauthorized
	}
	c.Version = 1
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	sig := ed25519.Sign(key, []byte("hermes-credential-broker/assertion/v1."+payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
func (v Verifier) Verify(token, audience, method, uri string, body []byte) (Actor, error) {
	bad := func() (Actor, error) { return Actor{}, ErrUnauthorized }
	if len(token) > 4096 {
		return bad()
	}
	p := strings.Split(token, ".")
	if len(p) != 2 {
		return bad()
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(p[0])
	if err != nil {
		return bad()
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(p[1])
	if err != nil {
		return bad()
	}
	var c Claims
	if strictjson.Decode(payload, &c) != nil {
		return bad()
	}
	k, ok := v.Keys[c.KeyID]
	if !ok || len(k.PublicKey) != ed25519.PublicKeySize || !slices.Contains(k.Audiences, audience) {
		return bad()
	}
	if !ed25519.Verify(k.PublicKey, []byte("hermes-credential-broker/assertion/v1."+p[0]), sig) {
		return bad()
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	if c.Version != 1 || c.Issuer != k.Issuer || c.Audience != audience || c.ExpiresAt <= now.Unix() || c.IssuedAt > now.Unix()+5 || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt > 120 || c.IssuedAt < now.Unix()-120 {
		return bad()
	}
	if c.Method != method || c.RequestURI != uri || c.BodySHA256 != Hash(body) || !c.Actor.Valid() {
		return bad()
	}
	if audience == "broker:runtime" && (!ValidID(c.Actor.BindingID) || !ValidID(c.Actor.WorkloadID)) {
		return bad()
	}
	return c.Actor, nil
}
