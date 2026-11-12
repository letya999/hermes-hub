package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
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

// prepareRoute resolves the channel's prepare-outcome endpoint and its
// shared control credential from the environment; "" when unconfigured.
func prepareRoute() (string, string) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_CONTROL_URL")), "/")
	if base == "" {
		return "", ""
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
		return "", ""
	}
	return base, token
}

func PostPrepareNotice(ctx context.Context, auth identity.Envelope, onboarding Onboarding, event string, tools int) string {
	url, _ := PostPrepareNoticeWithDelivery(ctx, auth, onboarding, event, tools)
	return url
}

// PostPrepareNoticeWithDelivery distinguishes a queued notice from a channel
// sign-in link: ordinary confirmation notices have no login_url.
func PostPrepareNoticeWithDelivery(ctx context.Context, auth identity.Envelope, onboarding Onboarding, event string, tools int) (string, bool) {
	base, token := prepareRoute()
	if base == "" || auth.PrincipalID == "" || onboarding.OnboardingID == "" {
		return "", false
	}
	payload := map[string]any{
		"onboarding_id":     onboarding.OnboardingID,
		"notice_version":    onboarding.NoticeVersion,
		"phase":             onboarding.Phase,
		"definition_id":     onboarding.DefinitionID,
		"contract_id":       onboarding.BrokerContractID,
		"broker_request_id": onboarding.BrokerRequestID,
		"repository":        onboarding.SourceURL,
		"detail":            PublicPrepareError(ErrString(onboarding.Error)),
		"form_url":          credentialNoticeURL(onboarding),
	}
	if onboarding.Phase == PhaseAwaitingConfirm && onboarding.RemoveNonce == "" {
		payload["confirmation"] = HumanConfirmation{OnboardingID: onboarding.OnboardingID, Actor: auth, Nonce: onboarding.ConfirmationNonce, Revision: ConfirmationRevision(onboarding), ActionDigest: ConfirmationActionDigest(onboarding), ExpiresAt: onboarding.ConfirmationExpires}
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
		if event == "link-resent" {
			id := onboarding.NoticeEventID
			if id == "" {
				id = RandomNonce()
			}
			payload["event_id"] = id
		}
	}
	if tools > 0 {
		payload["tools"] = tools
	}
	return preparePostResult(ctx, auth, base, token, payload)
}

// ReadPrepareDeliveryStatus checks the channel's persisted outcome, never the
// mere acknowledgement that a notice entered its queue.
func ReadPrepareDeliveryStatus(ctx context.Context, auth identity.Envelope, onboardingID string, version uint64) string {
	base, token := prepareRoute()
	if base == "" || auth.PrincipalID == "" || onboardingID == "" || version == 0 {
		return PrepareDeliveryUnknown
	}
	query := url.Values{"onboarding_id": {onboardingID}, "notice_version": {strconv.FormatUint(version, 10)}}
	callCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, base+"/v1/prepare-delivery-status?"+query.Encode(), nil) // #nosec G704 -- internal control endpoint
	if err != nil {
		return PrepareDeliveryUnknown
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Hub-Principal", auth.PrincipalID)
	response, err := http.DefaultClient.Do(req) // #nosec G704 -- internal control endpoint
	if err != nil {
		return PrepareDeliveryUnknown
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return PrepareDeliveryUnknown
	}
	var result struct {
		DeliveryStatus string `json:"delivery_status"`
	}
	if json.NewDecoder(response.Body).Decode(&result) != nil {
		return PrepareDeliveryUnknown
	}
	switch result.DeliveryStatus {
	case PrepareDeliveryUnknown, PrepareDeliveryQueued, PrepareDeliveryDelivered, PrepareDeliveryUncertain:
		return result.DeliveryStatus
	default:
		return PrepareDeliveryUnknown
	}
}

// preparePost ships one prepared payload to the channel's prepare-outcome
// endpoint and returns the channel-minted login URL when the notice carries
// one. A missing route or a failed post is silent — callers stay honest in
// their own result instead.
func preparePost(ctx context.Context, auth identity.Envelope, base, token string, payload map[string]any) string {
	url, _ := preparePostResult(ctx, auth, base, token, payload)
	return url
}

func preparePostResult(ctx context.Context, auth identity.Envelope, base, token string, payload map[string]any) (string, bool) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, base+"/v1/prepare-outcome", bytes.NewReader(body)) // #nosec G704 -- internal control endpoint
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Hub-Principal", auth.PrincipalID)
	response, err := http.DefaultClient.Do(req) // #nosec G704 -- internal control endpoint
	if err != nil {
		log.Printf("toolhub prepare outcome delivery failed onboarding=%v: %v", payload["onboarding_id"], err)
		return "", false
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		log.Printf("toolhub prepare outcome delivery onboarding=%v status=%d", payload["onboarding_id"], response.StatusCode) // #nosec G706 -- ids are store-generated IDPattern strings
		return "", false
	}
	// The channel returns the sign-in link it minted for this request (QR
	// invite and friends); callers may surface it in the tool result.
	var outcome struct {
		LoginURL string `json:"login_url"`
	}
	_ = json.NewDecoder(response.Body).Decode(&outcome)
	return outcome.LoginURL, true
}

// DeliverViewerLink posts the one-time browser viewer link to the owner's
// channel. The URL never enters a tool result — only the notice carries it.
func DeliverViewerLink(ctx context.Context, auth identity.Envelope, connectionID, viewerURL string) {
	_ = DeliverViewerLinkWithDelivery(ctx, auth, connectionID, viewerURL)
}

func DeliverViewerLinkWithDelivery(ctx context.Context, auth identity.Envelope, connectionID, viewerURL string) bool {
	base, token := prepareRoute()
	if base == "" || auth.PrincipalID == "" || connectionID == "" || viewerURL == "" {
		return false
	}
	_, queued := preparePostResult(ctx, auth, base, token, map[string]any{"connection_id": connectionID, "viewer_url": viewerURL})
	return queued
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
	if onboarding.Phase == PhaseCleanupIncomplete {
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

// DeliverMemoryConfirmation sends a memory edit proposal's confirmation
// proof to the owner's channel through the same prepare-outcome lane the
// connector confirmations use — the channel renders the verified button and
// posts the tap back to /v1/confirmations. The label gives the notice text
// its subject; delivery itself is best-effort like every prepare notice.
func DeliverMemoryConfirmation(ctx context.Context, auth identity.Envelope, proof HumanConfirmation, label string) {
	base, token := prepareRoute()
	if base == "" || auth.PrincipalID == "" || proof.OnboardingID == "" {
		return
	}
	payload := map[string]any{
		"onboarding_id": proof.OnboardingID,
		"phase":         "awaiting-confirm",
		"repository":    label,
		"created_at":    time.Now().UTC().Format(time.RFC3339Nano),
		"confirmation":  proof,
	}
	preparePost(ctx, auth, base, token, payload)
}
