package runtime

import (
	"net/http"
	"os"
	"strings"
	"time"

	identity "github.com/letya999/hermes-hub/contracts/identity/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type relayRunAuthority struct {
	identities  identity.Client
	runs        task.Client
	assignments task.AssignmentAuthority
	environment string
}

func relayRunAuthorityFromEnv() *relayRunAuthority {
	origin := strings.TrimSpace(os.Getenv("HUB_RUN_VERIFY_URL"))
	key := strings.TrimSpace(os.Getenv("HUB_RUN_VERIFY_AUTH"))
	if origin == "" && key == "" {
		return nil
	}
	a := &relayRunAuthority{environment: os.Getenv("HUB_ENVIRONMENT")}
	if origin != "" && key != "" {
		client := &http.Client{Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		a.identities = identity.NewHTTPClient(origin, key, client)
		a.runs = task.NewHTTPClient(origin, key, client)
		if toolhub := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_URL")); toolhub != "" {
			a.assignments = task.NewAssignmentHTTPClient(toolhub, key, client)
		}
	}
	return a
}

func (a *relayRunAuthority) authorize(r *http.Request, audience, space string) (identity.PolicyBundle, error) {
	header := identity.RunTokenHeader
	if audience == identity.EgressAudience {
		header = identity.EgressRunTokenHeader
	}
	if a == nil || len(r.Header.Values(header)) != 1 || len(r.Header.Values(api.HeaderRunID)) != 1 {
		return identity.PolicyBundle{}, task.ErrForbidden
	}
	record, policy, err := task.AuthorizeRun(r.Context(), a.identities, a.runs, r.Header.Get(header),
		audience, a.environment, r.Header.Get(api.HeaderRunID), time.Now())
	if err != nil || record.Envelope.ContextID != space {
		return identity.PolicyBundle{}, task.ErrForbidden
	}
	if record.Assignment && (a.assignments == nil || a.assignments.AuthorizeAssignmentJob(r.Context(), record.JobID) != nil) {
		return identity.PolicyBundle{}, task.ErrForbidden
	}
	return policy, nil
}
