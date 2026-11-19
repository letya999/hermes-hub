package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/letya999/hermes-hub/contracts/internal/wire"
	primitive "github.com/letya999/hermes-hub/internal/identity"
)

const maxPolicyBundleBytes = 512 << 10

// PolicyBundle binds six effective passport documents and the narrowed
// registry template to one immutable policy revision.
type PolicyBundle struct {
	Space            string                     `json:"space"`
	Environment      string                     `json:"environment"`
	PassportRevision string                     `json:"passport_revision"`
	PassportDigest   string                     `json:"passport_digest,omitempty"`
	Identity         *AgentTemplate             `json:"identity,omitempty"`
	Passports        map[string]json.RawMessage `json:"passports"`
}

var policyPassports = [...]string{"stack", "config", "models", "workspace", "tools", "grants"}

type SignedPolicy struct {
	Digest string `json:"digest"`
	JWS    string `json:"jws"`
}

func ValidPolicyDigest(digest string) bool { return digestID.MatchString(digest) }

func PolicyDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// AllowsRight intersects a signed passport right with the agent ceiling.
func (b PolicyBundle) AllowsRight(domain, operation string) bool {
	if b.Identity == nil || !b.HasPassportRight(domain, operation) {
		return false
	}
	ceiling := b.Identity.Restrictions.Grants
	return ceiling == nil || slices.Contains(ceiling, domain+"."+operation)
}

// HasPassportRight checks an explicit operation in the signed publication.
func (b PolicyBundle) HasPassportRight(domain, operation string) bool {
	if domain == "" || operation == "" {
		return false
	}
	var grants struct {
		Allow map[string][]string `json:"allow"`
	}
	return json.Unmarshal(b.Passports["grants"], &grants) == nil && slices.Contains(grants.Allow[domain], operation)
}

func (b PolicyBundle) Canonical() ([]byte, string, error) {
	if (b.Environment != "dev" && b.Environment != "prod") || !claimID.MatchString(b.PassportRevision) ||
		!primitive.ValidID(b.Space) || len(b.Passports) != len(policyPassports) {
		return nil, "", ErrInvalid
	}
	if b.Identity != nil {
		query := TemplateRequest{Subject: b.Identity.Subject.ID, Actor: b.Identity.Actor.ID, Space: b.Space}
		if !ValidPolicyDigest(b.PassportDigest) || !validOwnedAgent(query.Subject, query.Actor) || !validTemplate(query, *b.Identity) {
			return nil, "", ErrInvalid
		}
	} else if b.PassportDigest != "" {
		return nil, "", ErrInvalid
	}
	documents := make(map[string]json.RawMessage, len(policyPassports))
	var inputSize int
	for _, name := range policyPassports {
		raw := b.Passports[name]
		inputSize += len(raw)
		if inputSize > maxPolicyBundleBytes {
			return nil, "", ErrInvalid
		}
		var document map[string]any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if d.Decode(&document) != nil || d.Decode(new(any)) != io.EOF {
			return nil, "", ErrInvalid
		}
		if kind, _ := document["kind"].(string); kind != name {
			return nil, "", ErrInvalid
		}
		metadata, ok := document["metadata"].(map[string]any)
		if !ok || metadata["space"] != b.Space || metadata["environment"] != b.Environment || metadata["revision"] != b.PassportRevision {
			return nil, "", ErrInvalid
		}
		normalized, err := json.Marshal(document)
		if err != nil {
			return nil, "", ErrInvalid
		}
		documents[name] = normalized
	}
	b.Passports = documents
	body, err := json.Marshal(b)
	if err != nil || len(body) > maxPolicyBundleBytes {
		return nil, "", ErrInvalid
	}
	return body, PolicyDigest(body), nil
}

func (c inProc) Policy(ctx context.Context, digest string) (SignedPolicy, error) {
	if !ValidPolicyDigest(digest) {
		return SignedPolicy{}, ErrInvalid
	}
	value, err := c.server.Policy(ctx, digest)
	if err != nil {
		return SignedPolicy{}, wire.Normalize(err)
	}
	return checkPolicyResponse(value, digest)
}

func (c httpClient) Policy(ctx context.Context, digest string) (SignedPolicy, error) {
	if !ValidPolicyDigest(digest) {
		return SignedPolicy{}, ErrInvalid
	}
	var value SignedPolicy
	if err := c.Do(ctx, http.MethodGet, "/v1/identity/policies/"+url.PathEscape(digest), nil, &value); err != nil {
		return SignedPolicy{}, err
	}
	return checkPolicyResponse(value, digest)
}

func checkPolicyResponse(value SignedPolicy, digest string) (SignedPolicy, error) {
	if value.Digest != digest || value.JWS == "" || len(value.JWS) > 1<<20 {
		return SignedPolicy{}, ErrUnavailable
	}
	return value, nil
}

func VerifyPolicyBundle(signed, digest, space, environment string, keys []PublicSigningKey, now time.Time) (PolicyBundle, error) {
	body, err := verifySignedPayload(signed, SignPolicy, keys, now, 1<<20)
	var policy PolicyBundle
	if err != nil || len(body) > maxPolicyBundleBytes || json.Unmarshal(body, &policy) != nil ||
		policy.Space != space || policy.Environment != environment {
		return PolicyBundle{}, ErrForbidden
	}
	canonical, actualDigest, err := policy.Canonical()
	if err != nil || actualDigest != digest || !bytes.Equal(body, canonical) {
		return PolicyBundle{}, ErrForbidden
	}
	return policy, nil
}
