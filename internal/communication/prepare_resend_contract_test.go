package communication

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/locale"
)

func TestStableWizardResendHasFreshOutcomeKey(t *testing.T) {
	request := prepareOutcomeRequest{
		OnboardingID: "onboard-test", Phase: "awaiting-confirm", Event: "link-resent",
		FormURL: "http://127.0.0.1:8090/connect/onboard-test?k=stable", EventID: "notice-one",
	}
	first := prepareOutcomeKey(request, "")
	request.EventID = "notice-two"
	if second := prepareOutcomeKey(request, ""); first == second {
		t.Fatal("explicit resend of a stable wizard link was deduplicated")
	}
	text := prepareContinuation(request, locale.LangEN)
	if !strings.Contains(text, "protected confirmation button") || !strings.Contains(text, "connector_edit action=resend") {
		t.Fatalf("wizard continuation omitted the human confirmation path: %q", text)
	}
}
