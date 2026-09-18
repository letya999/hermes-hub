// Package broker owns credential requests, verified enrollment, explicit grants,
// short leases and a durable ledger. It does not own Hermes or MCP workloads.
package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/journal"
	"github.com/letya999/credential-broker/internal/seal"
	"github.com/letya999/credential-broker/materialize"
	"github.com/letya999/credential-broker/provider"
)

var (
	ErrDenied      = errors.New("access denied")
	ErrNotFound    = errors.New("not found")
	ErrConflict    = errors.New("state conflict")
	ErrExpired     = errors.New("expired")
	ErrInvalid     = errors.New("invalid request")
	ErrUnavailable = errors.New("broker unavailable")
	ErrReauthorize = errors.New("reauthorization required")
)

type ExternalAlias struct {
	Ref                                 provider.Ref
	PrincipalID, ContextID, ContractKey string
}
type Config struct {
	Contracts       []contract.Contract
	Providers       provider.Registry
	ExternalAliases map[string]ExternalAlias
	Journal         *journal.Journal
	Materializer    *materialize.Manager
	PublicOrigin    string
	SessionKey      []byte
	Now             func() time.Time
	OAuth           map[string]*OAuthProvider
}

type requestRecord struct {
	View             v1.Request     `json:"view"`
	Actor            identity.Actor `json:"actor"`
	OwnerKind        string         `json:"owner_kind"`
	ContractKey      string         `json:"contract_key"`
	ContractDigest   string         `json:"contract_digest"`
	Idempotency      string         `json:"idempotency"`
	Fingerprint      string         `json:"fingerprint"`
	RotateID         string         `json:"rotate_id,omitempty"`
	ExpectedRevision uint64         `json:"expected_revision"`
	ExternalAlias    string         `json:"external_alias,omitempty"`
	OAuthStateHash   string         `json:"oauth_state_hash,omitempty"`
	DraftRef         *provider.Ref  `json:"draft_ref,omitempty"`
}
type sessionRecord struct {
	Hash      string    `json:"hash"`
	CodeHash  string    `json:"code_hash"`
	RequestID string    `json:"request_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Approved  bool      `json:"approved"`
	Consumed  bool      `json:"consumed"`
}
type credentialRecord struct {
	View           v1.Credential  `json:"view"`
	ContractKey    string         `json:"contract_key"`
	ContractDigest string         `json:"contract_digest"`
	Ref            provider.Ref   `json:"ref"`
	Managed        bool           `json:"managed"`
	OwnedRefs      []provider.Ref `json:"owned_refs,omitempty"`
	TokenRevision  uint64         `json:"token_revision"`
}
type grantRecord struct {
	View           v1.Grant `json:"view"`
	ContractKey    string   `json:"contract_key"`
	ContractDigest string   `json:"contract_digest"`
	Idempotency    string   `json:"idempotency"`
	Fingerprint    string   `json:"fingerprint"`
}
type leaseRecord struct {
	View         v1.Lease
	Materialized *v1.Materialized
	StateKey     string
	Active       map[string]context.CancelFunc
}
type stateRecord struct {
	OwnedRefs []provider.Ref `json:"owned_refs,omitempty"`
	Key       string         `json:"key"`
	Ref       provider.Ref   `json:"ref"`
	Revision  uint64         `json:"revision"`
}
type oauthRecord struct {
	StateHash   string       `json:"state_hash"`
	RequestID   string       `json:"request_id"`
	SessionHash string       `json:"session_hash"`
	VerifierRef provider.Ref `json:"verifier_ref"`
	ExpiresAt   time.Time    `json:"expires_at"`
	Consumed    bool         `json:"consumed"`
}
type mutation struct {
	Audit      v1.Event          `json:"audit"`
	Request    *requestRecord    `json:"request,omitempty"`
	Session    *sessionRecord    `json:"session,omitempty"`
	Credential *credentialRecord `json:"credential,omitempty"`
	Grant      *grantRecord      `json:"grant,omitempty"`
	State      *stateRecord      `json:"state,omitempty"`
	OAuth      *oauthRecord      `json:"oauth,omitempty"`
}
type Broker struct {
	mu           sync.Mutex
	cfg          Config
	catalog      map[string]contract.Contract
	requests     map[string]requestRecord
	sessions     map[string]sessionRecord
	credentials  map[string]credentialRecord
	grants       map[string]grantRecord
	states       map[string]stateRecord
	oauth        map[string]oauthRecord
	leases       map[string]*leaseRecord
	events       []v1.Event
	refreshLocks map[string]*sync.Mutex
	closed       bool
}

func key(id string, rev int) string { return fmt.Sprintf("%s@%d", id, rev) }
func newID(prefix string) string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("OS randomness unavailable")
	}
	return prefix + hex.EncodeToString(b[:])
}
func (b *Broker) now() time.Time {
	if b.cfg.Now != nil {
		return b.cfg.Now().UTC()
	}
	return time.Now().UTC()
}
func (b *Broker) live() bool { return !b.closed && b.cfg.Journal.Healthy() }
func (b *Broker) audit(kind string, a identity.Actor) v1.Event {
	return v1.Event{At: b.now(), Kind: kind, PrincipalID: a.PrincipalID, ContextID: a.ContextID, PolicyVersion: a.PolicyVersion}
}
func managedBy(a identity.Actor, c credentialRecord) bool {
	if a.ContextID != c.View.ContextID {
		return false
	}
	return c.View.OwnerKind == "context" && a.ContextManager || c.View.OwnerKind == "user" && a.PrincipalID == c.View.PrincipalID
}
func requestBy(a identity.Actor, r requestRecord) bool {
	return a.PrincipalID == r.Actor.PrincipalID && a.ContextID == r.Actor.ContextID
}
func requestKey(a identity.Actor, s string) string {
	return seal.Digest(a.PrincipalID + "/" + a.ContextID + "/" + s)
}
