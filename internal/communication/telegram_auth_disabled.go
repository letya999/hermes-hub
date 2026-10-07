//go:build !telegramauth

package communication

import (
	"errors"
	"net/http"

	"github.com/letya999/hermes-hub/internal/identity"
)

type telegramAuthState struct{}

func newTelegramAuthState(Config) (*telegramAuthState, error) {
	return nil, errors.New("telegram authentication requires the opt-in Communication Hub image")
}

func (g *Gateway) registerTelegramAuth(*http.ServeMux) {}

func (g *Gateway) telegramAuthInvite(identity.Envelope, prepareOutcomeRequest) string { return "" }
