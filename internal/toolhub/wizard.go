package toolhub

import (
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// armWizard attaches the stable single-link capability to an onboarding that
// just minted a user-facing step (form nonce, broker request, confirmation
// nonce). The nonce is minted once and kept: re-mints renew only the expiry,
// so the link the channel already delivered never strands. Callers persist
// the record themselves — the helper mutates the in-memory copy only.
func (c *ControlPlane) armWizard(onboarding *api.Onboarding) {
	if onboarding.WizardNonce == "" {
		onboarding.WizardNonce = api.RandomNonce()
	}
	onboarding.WizardExpires = c.now().Add(wizardTTL)
}

// wizardTTL outlives every step TTL: the link is only a capability to view
// and drive the owner's onboarding; each write still checks its own nonce.
const wizardTTL = 24 * time.Hour
