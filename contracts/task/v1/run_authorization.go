package task

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"

	identity "github.com/letya999/hermes-hub/contracts/identity/v1"
)

// VerificationCredential projects only read authority for signing keys,
// signed policy and live runs. Peers cannot derive the control credential.
func VerificationCredential(controlCredential string) string {
	if controlCredential == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(controlCredential))
	_, _ = io.WriteString(mac, "hub-run-verification-read/v1")
	return hex.EncodeToString(mac.Sum(nil))
}

// ClaimsForRun projects a durable admission into an audience-specific token.
// Only the control issuer sets the expiry and signs it.
func ClaimsForRun(record RunRecord, audience, mode string) identity.RunClaims {
	org := record.ActingIdentity.Organization
	if org == "" {
		org = "personal"
	}
	claims := identity.RunClaims{
		Issuer: identity.RunTokenIssuer, Audience: audience, Subject: record.ActingIdentity.Subject,
		Actor: record.ActingIdentity.Actor, Organization: org, Space: record.Envelope.ContextID,
		Environment: record.Environment, Job: record.JobID, Task: record.TaskID, Run: record.RunID,
		Generation: record.RuntimeGeneration, PolicyRevision: record.PolicyRevision, Origin: record.Channel,
		Mode: mode, Initiator: record.Initiator,
	}
	if record.Assignment {
		claims.Assignment = record.AssignmentID
		claims.AssignmentPlanRevision = record.AssignmentPlanRevision
	} else {
		claims.Conversation = record.Envelope.ConversationID
	}
	return claims
}

// AuthorizeRun verifies the signature and live authority before returning the
// signed policy and the record that supplies internal routing data.
func AuthorizeRun(ctx context.Context, identities identity.Client, runs Client, token, audience, environment, runID string, now time.Time) (RunRecord, identity.PolicyBundle, error) {
	deny := func() (RunRecord, identity.PolicyBundle, error) {
		return RunRecord{}, identity.PolicyBundle{}, ErrForbidden
	}
	if token == "" || len(token) > 8192 || identities == nil || runs == nil {
		return deny()
	}
	keys, err := identities.SigningKeys(ctx)
	if err != nil {
		return deny()
	}
	claims, err := identity.VerifyRunToken(token, audience, keys, now)
	if err != nil || claims.Environment != environment || claims.Run != runID {
		return deny()
	}
	record, err := runs.Run(ctx, claims.Job)
	if err != nil || !record.LiveAt(now) || claims.Expires > record.Deadline.Unix() ||
		record.Assignment != (claims.Assignment != "") {
		return deny()
	}
	want := ClaimsForRun(record, audience, claims.Mode)
	want.Expires = claims.Expires
	if claims != want {
		return deny()
	}
	policy, err := verifyRecordPolicy(ctx, identities, record, keys, now)
	if err != nil || policy.Identity.Restrictions.Mode != claims.Mode {
		return deny()
	}
	return record, policy, nil
}

// AdmissionPolicy loads the signed policy for a trusted supervisor admission.
// The control-plane caller must separately check the admission and its liveness.
func AdmissionPolicy(ctx context.Context, identities identity.Client, record RunRecord, now time.Time) (identity.PolicyBundle, error) {
	if identities == nil || !identity.ValidPolicyDigest(record.PolicyRevision) {
		return identity.PolicyBundle{}, ErrForbidden
	}
	keys, err := identities.SigningKeys(ctx)
	if err != nil {
		return identity.PolicyBundle{}, ErrForbidden
	}
	return verifyRecordPolicy(ctx, identities, record, keys, now)
}

func verifyRecordPolicy(ctx context.Context, identities identity.Client, record RunRecord, keys []identity.PublicSigningKey, now time.Time) (identity.PolicyBundle, error) {
	signed, err := identities.Policy(ctx, record.PolicyRevision)
	if err != nil || signed.Digest != record.PolicyRevision {
		return identity.PolicyBundle{}, ErrForbidden
	}
	policy, err := identity.VerifyPolicyBundle(signed.JWS, record.PolicyRevision, record.Envelope.ContextID, record.Environment, keys, now)
	if err != nil || policy.Identity == nil || policy.PassportDigest != record.PassportDigest {
		return identity.PolicyBundle{}, ErrForbidden
	}
	revision, err := policy.Identity.Revision()
	if err != nil || revision != record.ActingIdentity.TemplateRevision || policy.Identity.Subject.ID != record.ActingIdentity.Subject ||
		policy.Identity.Actor.ID != record.ActingIdentity.Actor {
		return identity.PolicyBundle{}, ErrForbidden
	}
	return policy, nil
}

func (r RunRecord) LiveAt(now time.Time) bool {
	return r.Status == "running" && !r.Revoked && r.RunID != "" && now.Before(r.Deadline) &&
		r.RuntimeGeneration != "" && r.RuntimeGeneration == r.CurrentGeneration
}
