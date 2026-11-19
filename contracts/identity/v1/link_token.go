package identity

import (
	"time"

	primitive "github.com/letya999/hermes-hub/internal/identity"
)

const ChannelLinkAudience = "identity-channel-link"

// LinkClaims authorizes one pending channel challenge. ChannelDigest binds
// the observed account without exposing its identifier in the bearer.
type LinkClaims struct {
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	Subject       string `json:"sub"`
	Challenge     string `json:"challenge"`
	ChannelDigest string `json:"channel_digest"`
	Expires       int64  `json:"exp"`
}

func VerifyLinkToken(token string, keys []PublicSigningKey, now time.Time) (LinkClaims, error) {
	var claims LinkClaims
	body, err := verifySignedPayload(token, SignLink, keys, now, 8192)
	if err != nil || !canonicalSignedJSON(body, &claims) || claims.Issuer != RunTokenIssuer || claims.Audience != ChannelLinkAudience ||
		!primitive.ValidID(claims.Subject) || !claimID.MatchString(claims.Challenge) || !ValidPolicyDigest(claims.ChannelDigest) ||
		now.Unix() >= claims.Expires {
		return LinkClaims{}, ErrForbidden
	}
	return claims, nil
}
