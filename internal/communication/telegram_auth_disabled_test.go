//go:build !telegramauth

package communication

import (
	"net/http"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestTelegramAuthRequiresOptionalImage(t *testing.T) {
	if _, err := newTelegramAuthState(Config{}); err == nil {
		t.Fatal("base image accepted Telegram auth")
	}
	g := &Gateway{}
	mux := http.NewServeMux()
	g.registerTelegramAuth(mux)
	if got := g.telegramAuthInvite(identity.Envelope{}, prepareOutcomeRequest{}); got != "" {
		t.Fatalf("unexpected invite %q", got)
	}
	request := prepareOutcomeRequest{Phase: "awaiting-credentials", ContractID: "telegram-session", FormURL: "http://localhost:8081/connect/abc"}
	if got := prepareOutcomeNotice(request, "http://localhost:8081/telegram-auth/abc"); !strings.Contains(got, "/telegram-auth/abc") || !strings.Contains(got, "/connect/abc") {
		t.Fatalf("missing QR invitation: %q", got)
	}
}
