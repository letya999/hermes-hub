package agenttools

import (
	"context"
	"net/http"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type runDelegationKey struct{}

func WithRunDelegation(ctx context.Context, run api.RunDelegation) context.Context {
	return context.WithValue(ctx, runDelegationKey{}, run)
}

func (t *Tools) communicationAuthority(r *http.Request, principal string) {
	run, ok := r.Context().Value(runDelegationKey{}).(api.RunDelegation)
	if ok {
		r.Header.Set(contract.RunTokenHeader, run.Communication)
		r.Header.Set(api.HeaderRunID, run.RunID)
		return
	}
	if t.CommunicationAuth != "" {
		r.Header.Set("Authorization", "Bearer "+t.CommunicationAuth)
	}
	r.Header.Set("X-Hub-Principal", principal)
}
