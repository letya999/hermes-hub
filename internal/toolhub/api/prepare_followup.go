package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// DeliverPrepareOutcome asks communication-hub to tell the owner that a
// background prepare settled. The MCP log alone never becomes a channel
// reply. Missing configuration is a no-op so tests and installs without a
// gateway keep the store as the source of truth.
func DeliverPrepareOutcome(ctx context.Context, auth identity.Envelope, onboarding Onboarding) {
	PostPrepareNotice(ctx, auth, onboarding, "", 0)
}

// DeliverPrepareEvent reports an interim moment that is not a settled phase:
// the build kicking off, the credential probe starting, a rejected submit.
// The event rides the same channel as settle notices and dedupes by its own
// key, so a rejection shares no idempotency slot with the form link.
func DeliverPrepareEvent(ctx context.Context, auth identity.Envelope, onboarding Onboarding, event string) {
	PostPrepareNotice(ctx, auth, onboarding, event, 0)
}

func PostPrepareNotice(ctx context.Context, auth identity.Envelope, onboarding Onboarding, event string, tools int) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_CONTROL_URL")), "/")
	if base == "" || auth.PrincipalID == "" || onboarding.OnboardingID == "" {
		return
	}
	token := strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_AUTH"))
	if token == "" {
		if strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL")) != "" {
			token = os.Getenv("HUB_SUPERVISOR_AUTH")
		} else {
			token = os.Getenv("HUB_RUNTIME_AUTH")
		}
	}
	if token == "" {
		return
	}
	payload := map[string]any{
		"onboarding_id":     onboarding.OnboardingID,
		"phase":             onboarding.Phase,
		"definition_id":     onboarding.DefinitionID,
		"contract_id":       onboarding.BrokerContractID,
		"broker_request_id": onboarding.BrokerRequestID,
		"repository":        onboarding.SourceURL,
		"detail":            PublicPrepareError(ErrString(onboarding.Error)),
		"form_url":          credentialNoticeURL(onboarding),
	}
	if !onboarding.CreatedAt.IsZero() {
		// The gateway resolves the originating session from this instant; it
		// travels on every notice so a restarted gateway can still rebind.
		payload["created_at"] = onboarding.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if event != "" {
		payload["event"] = event
	}
	if tools > 0 {
		payload["tools"] = tools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, base+"/v1/prepare-outcome", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Hub-Principal", auth.PrincipalID)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("toolhub prepare outcome delivery failed onboarding=%s: %v", onboarding.OnboardingID, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		log.Printf("toolhub prepare outcome delivery onboarding=%s status=%d", onboarding.OnboardingID, response.StatusCode)
	}
}

func credentialNoticeURL(onboarding Onboarding) string {
	if origin := PublicOrigin(); origin != "" {
		return ActionURL(origin, onboarding)
	}
	return ActionURL(FormOriginForListen(os.Getenv("HUB_TOOLHUB_LISTEN")), onboarding)
}

// ActionURL resolves the user-facing link for the onboarding's pending action
// against an explicit form origin. Selection order matches the channel notice
// so elicitation and notices never disagree about which link is live.
func ActionURL(origin string, onboarding Onboarding) string {
	if onboarding.OnboardingID == "" {
		return ""
	}
	// A live remove link outranks other pending actions: the preview mints it
	// while the onboarding still sits in its previous phase.
	if onboarding.RemoveNonce != "" && onboarding.Phase != PhaseRemoved {
		return origin + "/remove/" + url.PathEscape(onboarding.OnboardingID) + "?t=" + url.QueryEscape(onboarding.RemoveNonce)
	}
	if onboarding.Phase == PhaseAwaitingConfirm && onboarding.ConfirmationNonce != "" && !onboarding.ConfirmationUsed {
		return origin + "/confirm/" + url.PathEscape(onboarding.OnboardingID) + "?t=" + url.QueryEscape(onboarding.ConfirmationNonce)
	}
	// External action links (provider OAuth, broker connect) hop through
	// /connect/ so the destination URL and its state never sit in chat text.
	if onboarding.Phase == PhaseAwaitingOAuth && onboarding.ProviderAuthorizationURL != "" {
		if parsed, err := url.Parse(onboarding.ProviderAuthorizationURL); err == nil {
			if state := parsed.Query().Get("state"); state != "" {
				return origin + "/connect/" + url.PathEscape(onboarding.OnboardingID) + "?state=" + url.QueryEscape(state)
			}
		}
	}
	if onboarding.Phase == PhaseAwaitingCreds && onboarding.BrokerRequestID != "" && onboarding.BrokerAuthorizationURL != "" {
		return origin + "/connect/" + url.PathEscape(onboarding.OnboardingID) + "?request=" + url.QueryEscape(onboarding.BrokerRequestID)
	}
	if onboarding.Phase != PhaseAwaitingCreds || onboarding.FormNonce == "" {
		return ""
	}
	return origin + "/credentials/" + url.PathEscape(onboarding.OnboardingID) + "?t=" + url.QueryEscape(onboarding.FormNonce)
}

func ErrString(text string) error {
	if text == "" {
		return nil
	}
	return &plainError{text}
}

type plainError struct{ text string }

func (e *plainError) Error() string { return e.text }
