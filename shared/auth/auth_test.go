package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunTokenVerifiesOnlyForItsPurposeAudienceAndLifetime(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := PublicSigningKey{ID: SigningKeyID(public), Purpose: SignRun, Public: public}
	keys := []PublicSigningKey{key}
	claims := RunClaims{
		Issuer: RunTokenIssuer, Audience: "tool-hub", Subject: "alice", Actor: "agent:alice:default",
		Organization: "personal", Space: "alice", Environment: "dev", Conversation: "telegram-7",
		Job: "job-7", Task: "default", Run: "run-7", Generation: "gen-2",
		PolicyRevision: "sha256:" + strings.Repeat("a", 64), Origin: "telegram_bot",
		Mode: "ask-critical", Expires: now.Add(time.Minute).Unix(),
	}
	mint := func(purpose SigningPurpose, c RunClaims, private ed25519.PrivateKey) string {
		t.Helper()
		header, err := json.Marshal(SignatureHeader{Algorithm: "EdDSA", Type: purpose.Type(), KeyID: key.ID})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		encode := base64.RawURLEncoding.EncodeToString
		input := encode(header) + "." + encode(payload)
		return input + "." + encode(ed25519.Sign(private, []byte(input)))
	}
	token := mint(SignRun, claims, private)
	got, err := VerifyRunToken(token, claims.Audience, keys, now)
	if err != nil || got != claims {
		t.Fatalf("signed claims did not round-trip: %v", err)
	}
	_, other, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	foreignOwner := claims
	foreignOwner.Actor = "agent:bob:default"
	for _, tc := range []struct {
		name, token, audience string
		at                    time.Time
	}{
		{"wrong signature", mint(SignRun, claims, other), claims.Audience, now},
		{"wrong purpose", mint(SignService, claims, private), claims.Audience, now},
		{"wrong audience", token, "model-gateway", now},
		{"foreign actor", mint(SignRun, foreignOwner, private), claims.Audience, now},
		{"expired", token, claims.Audience, time.Unix(claims.Expires, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyRunToken(tc.token, tc.audience, keys, tc.at); !errors.Is(err, ErrForbidden) {
				t.Fatalf("invalid token passed verification: %v", err)
			}
		})
	}
	keys[0].VerifyUntil = now.Unix()
	if _, err := VerifyRunToken(token, claims.Audience, keys, now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("retired key passed verification: %v", err)
	}
}

func TestServiceTokenBindsExactRequest(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := PublicSigningKey{ID: SigningKeyID(public), Purpose: SignService, Public: public}
	request := ServiceRequest{Space: "alice", Environment: "dev", RequestDigest: "sha256:" + strings.Repeat("b", 64)}
	claims := ServiceClaims{Issuer: RunTokenIssuer, Audience: "tool-hub-automation", Service: "system:communication", ServiceRequest: request, Expires: now.Add(time.Minute).Unix()}
	header, _ := json.Marshal(SignatureHeader{Algorithm: "EdDSA", Type: SignService.Type(), KeyID: key.ID})
	payload, _ := json.Marshal(claims)
	encode := base64.RawURLEncoding.EncodeToString
	input := encode(header) + "." + encode(payload)
	token := input + "." + encode(ed25519.Sign(private, []byte(input)))
	if err := VerifyServiceToken(token, claims.Audience, claims.Service, request, []PublicSigningKey{key}, now); err != nil {
		t.Fatalf("valid service token: %v", err)
	}
	other := request
	other.RequestDigest = "sha256:" + strings.Repeat("c", 64)
	for name, check := range map[string]error{
		"other request":  VerifyServiceToken(token, claims.Audience, claims.Service, other, []PublicSigningKey{key}, now),
		"other audience": VerifyServiceToken(token, "model-gateway", claims.Service, request, []PublicSigningKey{key}, now),
		"other service":  VerifyServiceToken(token, claims.Audience, "system:toolhub", request, []PublicSigningKey{key}, now),
		"expired":        VerifyServiceToken(token, claims.Audience, claims.Service, request, []PublicSigningKey{key}, now.Add(time.Hour)),
		"run key":        VerifyServiceToken(token, claims.Audience, claims.Service, request, []PublicSigningKey{{ID: key.ID, Purpose: SignRun, Public: public}}, now),
	} {
		if !errors.Is(check, ErrForbidden) {
			t.Errorf("%s: want forbidden, got %v", name, check)
		}
	}
}
