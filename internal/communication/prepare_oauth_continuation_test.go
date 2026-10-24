package communication

import (
	"strings"
	"testing"
)

// The agent-facing continuation for an awaiting-oauth outcome must tell the
// agent the authorization link already arrived in the channel message — the
// /connect/ hop carries OAuth state (link material) and never enters a tool
// result, so without this guidance the agent parrots "cannot hand out the
// link" or reconstructs a URL itself.
func TestPrepareContinuationOAuthPointsAtNotice(t *testing.T) {
	text := prepareContinuation(prepareOutcomeRequest{
		OnboardingID: "onboard-oauth",
		Phase:        "awaiting-oauth",
		Repository:   "https://github.com/nspady/google-calendar-mcp",
		FormURL:      "http://127.0.0.1:8090/connect/onboard-oauth?state=flow-1",
	})
	if !strings.Contains(text, "уже отправлен") && !strings.Contains(text, "статусном сообщении") {
		t.Fatalf("continuation does not point at the delivered notice: %q", text)
	}
	if !strings.Contains(text, "resend=true") {
		t.Fatalf("continuation does not teach the resend path for link requests: %q", text)
	}
	if strings.Contains(text, "flow-1") {
		t.Fatalf("continuation echoed link material: %q", text)
	}
}

// A wizard link fronts every step on one page: the agent must point back at
// that single link instead of inventing a separate confirm message or button.
func TestPrepareContinuationWizardPointsAtSameLink(t *testing.T) {
	for _, phase := range []string{"awaiting-credentials", "awaiting-confirm", "awaiting-oauth"} {
		text := prepareContinuation(prepareOutcomeRequest{OnboardingID: "onboard-w", Phase: phase, Repository: "https://github.com/taylorwilsdon/google_workspace_mcp", FormURL: "http://127.0.0.1:8090/connect/onboard-w?k=wiz-1"})
		if !strings.Contains(text, "ту же ссылку") || !strings.Contains(text, "resend=true") || strings.Contains(text, "отдельным сообщением") || strings.Contains(text, "wiz-1") {
			t.Fatalf("%s: wizard continuation misdirects the agent: %q", phase, text)
		}
	}
}

func TestPrepareContinuationConfirmPointsAtNotice(t *testing.T) {
	text := prepareContinuation(prepareOutcomeRequest{
		OnboardingID: "onboard-confirm",
		Phase:        "awaiting-confirm",
		Repository:   "https://github.com/taylorwilsdon/google_workspace_mcp",
		FormURL:      "http://127.0.0.1:8090/confirm/onboard-confirm?t=flow-1",
	})
	if !strings.Contains(text, "уже отправлена") || !strings.Contains(text, "resend=true") {
		t.Fatalf("continuation does not direct the agent to the delivered confirmation link: %q", text)
	}
	if strings.Contains(text, "flow-1") {
		t.Fatalf("continuation echoed link material: %q", text)
	}
}
