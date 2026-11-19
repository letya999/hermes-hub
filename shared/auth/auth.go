// Package auth verifies the hub's signed tokens: run tokens and service
// tokens (compact Ed25519 JWS over canonical JSON) against published public
// signing keys. It only verifies; identity-hub alone issues tokens and keys.
// contracts/identity/v1 re-exports these types for its wire shapes.
package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/shared/httpapi"
)

// ErrForbidden is the only verification failure: callers learn nothing about
// which check refused the token.
var ErrForbidden = httpapi.ErrForbidden

// RunTokenIssuer is the issuer of every hub token.
const RunTokenIssuer = "hub-control"

// SigningPurpose separates key and token families; a key signs one purpose.
type SigningPurpose string

const (
	SignRun     SigningPurpose = "run"
	SignLink    SigningPurpose = "link"
	SignService SigningPurpose = "service"
	SignPolicy  SigningPurpose = "policy"
)

// Type is the JWS typ header for the purpose.
func (p SigningPurpose) Type() string {
	if p == SignPolicy {
		return "hub-policy+jws"
	}
	return "hub-" + string(p) + "+jwt"
}

// SignatureHeader is the JWS protected header.
type SignatureHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

// PublicSigningKey contains the verification material for one purpose.
// VerifyUntil is zero for the current key and bounds every cached copy of
// a retired key once its retirement is published.
type PublicSigningKey struct {
	ID          string         `json:"id"`
	Purpose     SigningPurpose `json:"purpose"`
	Public      []byte         `json:"public_key"`
	VerifyUntil int64          `json:"verify_until,omitempty"`
}

// SigningKeyID is the hex SHA-256 of the public key.
func SigningKeyID(public []byte) string {
	digest := sha256.Sum256(public)
	return hex.EncodeToString(digest[:])
}

// Valid reports a known purpose, an Ed25519 key and an ID bound to it.
func (key PublicSigningKey) Valid() bool {
	switch key.Purpose {
	case SignRun, SignLink, SignService, SignPolicy:
		return key.VerifyUntil >= 0 && len(key.Public) == ed25519.PublicKeySize && SigningKeyID(key.Public) == key.ID
	default:
		return false
	}
}

var (
	// idPattern mirrors internal/identity.ValidID (parity tested in
	// contracts/identity/v1); shared packages never import internal/.
	idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	claimID   = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,256}$`)
	digestID  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ValidID is the principal, organization and space identifier rule.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// ValidClaimID is the job, run, task and challenge identifier rule.
func ValidClaimID(id string) bool { return claimID.MatchString(id) }

// ValidDigest accepts sha256:<64 lowercase hex>.
func ValidDigest(digest string) bool { return digestID.MatchString(digest) }

// ValidOwnedAgent reports that actor is an agent profile owned by subject.
func ValidOwnedAgent(subject, actor string) bool {
	profile, ownsActor := strings.CutPrefix(actor, "agent:"+subject+":")
	return ValidID(subject) && ownsActor && ValidID(profile)
}

// RunClaims identifies an admitted action. Policy and live run authority
// must be checked separately for every call.
type RunClaims struct {
	Issuer                 string `json:"iss"`
	Audience               string `json:"aud"`
	Subject                string `json:"sub"`
	Actor                  string `json:"act"`
	Organization           string `json:"org"`
	Space                  string `json:"space"`
	Environment            string `json:"env"`
	Conversation           string `json:"conversation,omitempty"`
	Assignment             string `json:"assignment,omitempty"`
	AssignmentPlanRevision uint64 `json:"assignment_plan_revision,omitempty"`
	Job                    string `json:"job"`
	Task                   string `json:"task"`
	Run                    string `json:"run"`
	Generation             string `json:"gen"`
	PolicyRevision         string `json:"policy_rev"`
	Origin                 string `json:"origin"`
	Initiator              string `json:"initiator,omitempty"`
	Mode                   string `json:"mode"`
	Expires                int64  `json:"exp"`
}

// Valid checks the claim shape (not the signature or lifetime).
func (c RunClaims) Valid() bool {
	if c.Issuer != RunTokenIssuer || !ValidID(c.Audience) || !ValidOwnedAgent(c.Subject, c.Actor) ||
		!ValidID(c.Organization) || !ValidID(c.Space) ||
		(c.Environment != "dev" && c.Environment != "prod") ||
		(c.Conversation == "") == (c.Assignment == "") || (c.Assignment != "") != (c.AssignmentPlanRevision != 0) ||
		!claimID.MatchString(c.Conversation+c.Assignment) || !claimID.MatchString(c.Job) ||
		!claimID.MatchString(c.Task) || !claimID.MatchString(c.Run) || !claimID.MatchString(c.Generation) ||
		!digestID.MatchString(c.PolicyRevision) || !ValidID(c.Origin) || c.Expires <= 0 {
		return false
	}
	if c.Initiator != "" {
		service, system := strings.CutPrefix(c.Initiator, "system:")
		if !system || !ValidID(service) {
			return false
		}
	}
	switch c.Mode {
	case "read-only", "plan", "ask", "ask-critical", "full-access":
		return true
	default:
		return false
	}
}

// VerifyRunToken verifies a run token for audience using only trusted public
// keys.
func VerifyRunToken(token, audience string, keys []PublicSigningKey, now time.Time) (RunClaims, error) {
	var claims RunClaims
	body, err := VerifySigned(token, SignRun, keys, now, 8192)
	if err != nil || !CanonicalJSON(body, &claims) || !claims.Valid() ||
		claims.Audience != audience || now.Unix() >= claims.Expires {
		return RunClaims{}, ErrForbidden
	}
	return claims, nil
}

// ServiceRequest limits one service bearer to the exact request bytes.
type ServiceRequest struct {
	Space          string `json:"space"`
	Environment    string `json:"environment"`
	RequestDigest  string `json:"request_digest"`
	PolicyRevision string `json:"policy_revision,omitempty"`
}

// ServiceClaims is the signed body of a service token.
type ServiceClaims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Service  string `json:"sub"`
	ServiceRequest
	Expires int64 `json:"exp"`
}

// Valid checks the request shape.
func (r ServiceRequest) Valid() bool {
	return ValidID(r.Space) && (r.Environment == "dev" || r.Environment == "prod") && ValidDigest(r.RequestDigest) &&
		(r.PolicyRevision == "" || ValidDigest(r.PolicyRevision))
}

// VerifyServiceToken verifies a service token for audience and service bound
// to exactly request.
func VerifyServiceToken(token, audience, service string, request ServiceRequest, keys []PublicSigningKey, now time.Time) error {
	claims, err := VerifyServiceRequest(token, request, keys, now)
	if err != nil || claims.Audience != audience || claims.Service != service {
		return ErrForbidden
	}
	return nil
}

// VerifyServiceRequest verifies a service token bound to exactly request and
// returns its claims for an audience/service decision by the caller.
func VerifyServiceRequest(token string, request ServiceRequest, keys []PublicSigningKey, now time.Time) (ServiceClaims, error) {
	var claims ServiceClaims
	body, err := VerifySigned(token, SignService, keys, now, 4096)
	if err != nil || !CanonicalJSON(body, &claims) || !claims.ServiceRequest.Valid() ||
		claims.Issuer != RunTokenIssuer ||
		claims.ServiceRequest != request || now.Unix() >= claims.Expires {
		return ServiceClaims{}, ErrForbidden
	}
	return claims, nil
}

// VerifySigned checks a compact EdDSA JWS of purpose against keys and returns
// its payload bytes.
func VerifySigned(token string, purpose SigningPurpose, keys []PublicSigningKey, now time.Time, maxSize int) ([]byte, error) {
	if len(token) > maxSize || strings.ContainsAny(token, " \r\n\t") {
		return nil, ErrForbidden
	}
	headerPart, tail, ok := strings.Cut(token, ".")
	if !ok {
		return nil, ErrForbidden
	}
	bodyPart, sigPart, ok := strings.Cut(tail, ".")
	if !ok {
		return nil, ErrForbidden
	}
	decode := base64.RawURLEncoding.Strict().DecodeString
	headerBody, err := decode(headerPart)
	if err != nil {
		return nil, ErrForbidden
	}
	var header SignatureHeader
	if !CanonicalJSON(headerBody, &header) || header.Algorithm != "EdDSA" || header.Type != purpose.Type() {
		return nil, ErrForbidden
	}
	signature, err := decode(sigPart)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, ErrForbidden
	}
	signed := token[:len(headerPart)+1+len(bodyPart)]
	for _, key := range keys {
		if key.ID != header.KeyID || key.Purpose != purpose ||
			(key.VerifyUntil != 0 && now.Unix() >= key.VerifyUntil) ||
			!key.Valid() ||
			!ed25519.Verify(key.Public, []byte(signed), signature) {
			continue
		}
		body, err := decode(bodyPart)
		if err == nil {
			return body, nil
		}
		break
	}
	return nil, ErrForbidden
}

// CanonicalJSON decodes body into out and accepts it only when re-encoding
// reproduces the exact bytes (no duplicate, unknown or reordered fields).
func CanonicalJSON(body []byte, out any) bool {
	if json.Unmarshal(body, out) != nil {
		return false
	}
	encoded, err := json.Marshal(out)
	return err == nil && bytes.Equal(encoded, body)
}
