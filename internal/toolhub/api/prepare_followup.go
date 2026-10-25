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

func PostPrepareNotice(ctx context.Context, auth identity.Envelope, onboarding Onboarding, event string, tools int) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_CONTROL_URL")), "/")
	if base == "" || auth.PrincipalID == "" || onboarding.OnboardingID == "" {
		return ""
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
		return ""
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
	// The reviewed contract declares how credentials may be collected; the
	// channel offers that intake (e.g. QR login) instead of sniffing ids.
	if reviewed, ok, rerr := PreparedContractFor(onboarding.BrokerContractID, onboarding.BrokerContractRevision); rerr == nil && ok && reviewed.Intake != "" {
		payload["credential_intake"] = reviewed.Intake
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
		return ""
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, base+"/v1/prepare-outcome", bytes.NewReader(body)) // #nosec G704 -- internal control endpoint
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Hub-Principal", auth.PrincipalID)
	response, err := http.DefaultClient.Do(req) // #nosec G704 -- internal control endpoint
	if err != nil {
		log.Printf("toolhub prepare outcome delivery failed onboarding=%s: %v", onboarding.OnboardingID, err)
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		log.Printf("toolhub prepare outcome delivery onboarding=%s status=%d", onboarding.OnboardingID, response.StatusCode) // #nosec G706 -- ids are store-generated IDPattern strings
		return ""
	}
	// The channel returns the sign-in link it minted for this request (QR
	// invite and friends); callers may surface it in the tool result.
	var outcome struct {
		LoginURL string `json:"login_url"`
	}
	_ = json.NewDecoder(response.Body).Decode(&outcome)
	return outcome.LoginURL
}

// DeliverViewerLink posts the one-time browser viewer link to the owner's
// channel. The URL never enters a tool result — only the notice carries it.
func DeliverViewerLink(ctx context.Context, auth identity.Envelope, connectionID, viewerURL string) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_CONTROL_URL")), "/")
	if base == "" || auth.PrincipalID == "" || connectionID == "" || viewerURL == "" {
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
	body, err := json.Marshal(map[string]any{"connection_id": connectionID, "viewer_url": viewerURL})
	if err != nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, base+"/v1/prepare-outcome", bytes.NewReader(body)) // #nosec G704 -- internal control endpoint
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Hub-Principal", auth.PrincipalID)
	response, err := http.DefaultClient.Do(req) // #nosec G704 -- internal control endpoint
	if err != nil {
		log.Printf("toolhub viewer link delivery failed connection=%s: %v", connectionID, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		log.Printf("toolhub viewer link delivery connection=%s status=%d", connectionID, response.StatusCode) // #nosec G706 -- ids are store-generated IDPattern strings
	}
}

// ViewerLinkURL renders the owner-only viewer address on the public form
// origin — the same selection order the channel notice machinery uses.
func ViewerLinkURL(connectionID, nonce string) string {
	origin := PublicOrigin()
	if origin == "" {
		origin = FormOriginForListen(os.Getenv("HUB_TOOLHUB_LISTEN"))
	}
	if origin == "" || nonce == "" {
		return ""
	}
	return strings.TrimRight(origin, "/") + "/viewer/" + url.PathEscape(connectionID) + "?t=" + url.QueryEscape(nonce)
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
	// An armed wizard collapses the interactive phases into one stable link:
	// the page renders the live step itself, so a rotated OAuth state or a
	// reminted broker request never strands the URL the channel delivered.
	if onboarding.WizardNonce != "" {
		switch onboarding.Phase {
		case PhaseAwaitingCreds, PhaseAwaitingConfirm, PhaseAwaitingOAuth:
			return origin + "/connect/" + url.PathEscape(onboarding.OnboardingID) + "?k=" + url.QueryEscape(onboarding.WizardNonce)
		}
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
