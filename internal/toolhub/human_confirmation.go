package toolhub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type humanConfirmationKey struct{}

func (g *Gateway) serveHumanConfirmation(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" || g.Control == nil || g.ControlToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(g.ControlToken)) != 1 {
		http.Error(w, "confirmation denied", http.StatusForbidden)
		return
	}
	if _, enrolled := g.matchToken("Bearer " + token); enrolled {
		http.Error(w, "confirmation denied", http.StatusForbidden)
		return
	}
	var proof api.HumanConfirmation
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&proof) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "confirmation denied", http.StatusForbidden)
		return
	}
	g.mu.Lock()
	enrolled := false
	for _, auth := range g.Tokens {
		if proof.Actor.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion) == nil {
			proof.Actor.CapabilityProfile, proof.Actor.Environment, proof.Actor.Generation, proof.Actor.Organization = auth.CapabilityProfile, auth.Environment, auth.Generation, auth.Organization
			enrolled = true
			break
		}
	}
	g.mu.Unlock()
	if !enrolled {
		http.Error(w, "confirmation denied", http.StatusForbidden)
		return
	}
	ctx := context.WithValue(r.Context(), humanConfirmationKey{}, proof)
	onboarding, err := g.Control.Store.onboarding(proof.OnboardingID)
	if err == nil && onboarding.PendingWrites {
		_, err = g.Control.Store.ConsumeHumanConfirmation(proof, g.Control.now())
		if err != nil && !errors.Is(err, api.ErrUnauthorized) {
			// A store that cannot persist the consumed proof is unavailable
			// state, not a denied proof — answering 403 would mask a
			// durability failure as a rejected confirmation.
			http.Error(w, "confirmation unavailable", http.StatusServiceUnavailable)
			return
		}
	} else if err == nil {
		err = g.Control.finishAuthorization(ctx, proof.OnboardingID)
	}
	if err != nil {
		http.Error(w, "confirmation failed", http.StatusForbidden)
		return
	}
	_ = g.audit("human_confirmation", map[string]string{
		"principal_id": proof.Actor.PrincipalID, "context_id": proof.Actor.ContextID, "runtime_id": proof.Actor.RuntimeID,
		"policy_version": proof.Actor.PolicyVersion, "sender_id": proof.Actor.ExternalIdentityID,
		"onboarding_id": proof.OnboardingID, "outcome": "allow",
	})
	w.WriteHeader(http.StatusOK)
}

func (s *Store) ConsumeHumanConfirmation(proof api.HumanConfirmation, now time.Time) (api.Onboarding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.onboardings[proof.OnboardingID]
	a := proof.Actor
	if !ok || a.Validate(o.PrincipalID, o.ContextID, o.RuntimeID, o.PolicyVersion) != nil || o.Phase != api.PhaseAwaitingConfirm || o.ConfirmationUsed || proof.Revision != o.Revision || proof.Nonce == "" || proof.Nonce != o.ConfirmationNonce || !now.Before(o.ConfirmationExpires) || !proof.ExpiresAt.Equal(o.ConfirmationExpires) || proof.ActionDigest != api.ConfirmationActionDigest(o) {
		return api.Onboarding{}, api.ErrUnauthorized
	}
	if a.CapabilityProfile != "" {
		profile, _, err := s.capabilityProfileLocked(a)
		if err != nil || profile.Connectors == api.ConnectorsNone || profile.Connectors == "" && !slices.Contains(profile.ControlOperations, "confirm") || o.Mode == api.OnboardingSelfInstall && !profile.SelfInstall {
			return api.Onboarding{}, api.ErrUnauthorized
		}
	} else if !s.controlAllowedLocked(a, "confirm") {
		return api.Onboarding{}, api.ErrUnauthorized
	}
	return s.consumeConfirmationLocked(o, o.PendingWrites, &api.HumanConsent{ActorID: a.ExternalIdentityID, ActionDigest: proof.ActionDigest, Revision: proof.Revision, ConfirmedAt: now.UTC()})
}
