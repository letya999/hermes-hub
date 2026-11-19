package identity

import (
	"time"

	"github.com/letya999/hermes-hub/shared/auth"
)

// Verification lives in shared/auth (verify only); these aliases keep the
// contract's wire vocabulary. Issuing stays in identity-hub.

const RunTokenIssuer = auth.RunTokenIssuer
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

type SigningPurpose = auth.SigningPurpose

const (
	SignRun     = auth.SignRun
	SignLink    = auth.SignLink
	SignService = auth.SignService
	SignPolicy  = auth.SignPolicy
)

type SignatureHeader = auth.SignatureHeader

// PublicSigningKey contains the verification material for one purpose.
type PublicSigningKey = auth.PublicSigningKey

func SigningKeyID(public []byte) string { return auth.SigningKeyID(public) }

// RunClaims identifies an admitted action. Policy and live run authority
// must be checked separately for every call.
type RunClaims = auth.RunClaims

// VerifyRunToken verifies a compact Ed25519 JWS using only trusted public keys.
func VerifyRunToken(token, audience string, keys []PublicSigningKey, now time.Time) (RunClaims, error) {
	return auth.VerifyRunToken(token, audience, keys, now)
}
