package communication

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/locale"
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
	}, locale.LangEN)
	if !strings.Contains(text, "already sent") && !strings.Contains(text, "status message") {
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
		text := prepareContinuation(prepareOutcomeRequest{OnboardingID: "onboard-w", Phase: phase, Repository: "https://github.com/taylorwilsdon/google_workspace_mcp", FormURL: "http://127.0.0.1:8090/connect/onboard-w?k=wiz-1"}, locale.LangEN)
		if !strings.Contains(text, "same link") || !strings.Contains(text, "resend=true") || strings.Contains(text, "separate message") || strings.Contains(text, "wiz-1") {
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
	}, locale.LangEN)
	if !strings.Contains(text, "already sent") || !strings.Contains(text, "resend=true") {
		t.Fatalf("continuation does not direct the agent to the delivered confirmation link: %q", text)
	}
	if strings.Contains(text, "flow-1") {
		t.Fatalf("continuation echoed link material: %q", text)
	}
}

// Every continuation branch is model-facing English — the generic phase
// fallback and the form-link variant included — so instruction language can
// never bleed into the agent's reply.
func TestPrepareContinuationIsEnglishEverywhere(t *testing.T) {
	cyrillic := func(s string) bool {
		for _, r := range s {
			if r >= 'А' && r <= 'я' || r == 'Ё' || r == 'ё' {
				return true
			}
		}
		return false
	}
	cases := []prepareOutcomeRequest{
		{OnboardingID: "onboard-gen", Phase: "enabled"},
		{OnboardingID: "onboard-creds", Phase: "awaiting-credentials", FormURL: "http://127.0.0.1:8090/credentials/onboard-creds?nonce=n1"},
	}
	for _, request := range cases {
		text := prepareContinuation(request, locale.LangRU)
		body, _, _ := strings.Cut(text, " The user's language is")
		if cyrillic(body) {
			t.Fatalf("continuation carries instruction-language text: %q", text)
		}
	}
}

// The model-facing instruction is English; the owner's language travels as an
// explicit hint so the reply lands in the user's own language instead of
// drifting to the instruction language.
func TestPrepareContinuationCarriesLangHint(t *testing.T) {
	for lang, want := range map[locale.Lang]string{
		locale.LangEN: "English (en)",
		locale.LangRU: "Русский (ru)",
		locale.LangKK: "Қазақша (kk)",
		locale.LangES: "Español (es)",
	} {
		text := prepareContinuation(prepareOutcomeRequest{OnboardingID: "onboard-hint", Phase: "enabled"}, lang)
		if !strings.Contains(text, want) {
			t.Fatalf("%s: continuation misses the language hint %q: %q", lang, want, text)
		}
	}
}
