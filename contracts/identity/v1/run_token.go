package identity

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

	primitive "github.com/letya999/hermes-hub/internal/identity"
)

const RunTokenIssuer = "hub-control"
const RunTokenHeader = "X-Hub-Run-Token"
const MemoryRunTokenHeader = "X-Hub-Memory-Run-Token"
const CommunicationRunTokenHeader = "X-Hub-Communication-Run-Token"
const EgressRunTokenHeader = "X-Hub-Egress-Run-Token"

const (
	ToolHubAudience       = "tool-hub"
	MemoryAudience        = "memory-hub"
	ModelAudience         = "model-gateway"
	EgressAudience        = "egress-gateway"
	CommunicationAudience = "communication-hub"
)

type SigningPurpose string

const (
	SignRun     SigningPurpose = "run"
	SignLink    SigningPurpose = "link"
	SignService SigningPurpose = "service"
	SignPolicy  SigningPurpose = "policy"
)

func (p SigningPurpose) Type() string {
	if p == SignPolicy {
		return "hub-policy+jws"
	}
	return "hub-" + string(p) + "+jwt"
}

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

func SigningKeyID(public []byte) string {
	digest := sha256.Sum256(public)
	return hex.EncodeToString(digest[:])
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

var (
	claimID  = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,256}$`)
	digestID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

func (c RunClaims) Valid() bool {
	if c.Issuer != RunTokenIssuer || !primitive.ValidID(c.Audience) || !validOwnedAgent(c.Subject, c.Actor) ||
		!primitive.ValidID(c.Organization) || !primitive.ValidID(c.Space) ||
		(c.Environment != "dev" && c.Environment != "prod") ||
		(c.Conversation == "") == (c.Assignment == "") || (c.Assignment != "") != (c.AssignmentPlanRevision != 0) ||
		!claimID.MatchString(c.Conversation+c.Assignment) || !claimID.MatchString(c.Job) ||
		!claimID.MatchString(c.Task) || !claimID.MatchString(c.Run) || !claimID.MatchString(c.Generation) ||
		!digestID.MatchString(c.PolicyRevision) || !primitive.ValidID(c.Origin) || c.Expires <= 0 {
		return false
	}
	if c.Initiator != "" {
		service, system := strings.CutPrefix(c.Initiator, "system:")
		if !system || !primitive.ValidID(service) {
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

func validOwnedAgent(subject, actor string) bool {
	profile, ownsActor := strings.CutPrefix(actor, "agent:"+subject+":")
	return primitive.ValidID(subject) && ownsActor && primitive.ValidID(profile)
}

// VerifyRunToken verifies a compact Ed25519 JWS using only trusted public keys.
func VerifyRunToken(token, audience string, keys []PublicSigningKey, now time.Time) (RunClaims, error) {
	var claims RunClaims
	body, err := verifySignedPayload(token, SignRun, keys, now, 8192)
	if err != nil || !canonicalSignedJSON(body, &claims) || !claims.Valid() ||
		claims.Audience != audience || now.Unix() >= claims.Expires {
		return RunClaims{}, ErrForbidden
	}
	return claims, nil
}

func verifySignedPayload(token string, purpose SigningPurpose, keys []PublicSigningKey, now time.Time, maxSize int) ([]byte, error) {
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
	if !canonicalSignedJSON(headerBody, &header) || header.Algorithm != "EdDSA" || header.Type != purpose.Type() {
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
			!key.valid() ||
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

func canonicalSignedJSON(body []byte, out any) bool {
	if json.Unmarshal(body, out) != nil {
		return false
	}
	encoded, err := json.Marshal(out)
	return err == nil && bytes.Equal(encoded, body)
}
