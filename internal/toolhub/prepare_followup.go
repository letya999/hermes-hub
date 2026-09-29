package toolhub

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

// deliverPrepareOutcome asks communication-hub to tell the owner that a
// background prepare settled. The MCP log alone never becomes a channel
// reply. Missing configuration is a no-op so tests and installs without a
// gateway keep the store as the source of truth.
func deliverPrepareOutcome(ctx context.Context, auth identity.Envelope, onboarding Onboarding) {
	postPrepareNotice(ctx, auth, onboarding, "", 0)
}

// deliverPrepareEvent reports an interim moment that is not a settled phase:
// the build kicking off, the credential probe starting, a rejected submit.
// The event rides the same channel as settle notices and dedupes by its own
// key, so a rejection shares no idempotency slot with the form link.
func deliverPrepareEvent(ctx context.Context, auth identity.Envelope, onboarding Onboarding, event string) {
	postPrepareNotice(ctx, auth, onboarding, event, 0)
}

func postPrepareNotice(ctx context.Context, auth identity.Envelope, onboarding Onboarding, event string, tools int) {
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
		"onboarding_id": onboarding.OnboardingID,
		"phase":         onboarding.Phase,
		"definition_id": onboarding.DefinitionID,
		"repository":    onboarding.SourceURL,
		"detail":        publicPrepareError(errString(onboarding.Error)),
		"form_url":      credentialNoticeURL(onboarding),
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
	if onboarding.Phase != PhaseAwaitingCreds || onboarding.FormNonce == "" || onboarding.OnboardingID == "" {
		return ""
	}
	return formOriginForListen(os.Getenv("HUB_TOOLHUB_LISTEN")) + "/credentials/" + url.PathEscape(onboarding.OnboardingID) + "?nonce=" + url.QueryEscape(onboarding.FormNonce)
}

func errString(text string) error {
	if text == "" {
		return nil
	}
	return &plainError{text}
}

type plainError struct{ text string }

func (e *plainError) Error() string { return e.text }
