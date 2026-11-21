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

type RelayRunAuthority struct {
	identities       identity.Client
	runs             task.Client
	assignments      task.AssignmentAuthority
	servicePassports identity.ServicePassportsReader
	environment      string
}

func RelayRunAuthorityFromEnv() *RelayRunAuthority {
	origin := strings.TrimSpace(os.Getenv("HUB_RUN_VERIFY_URL"))
	key := strings.TrimSpace(os.Getenv("HUB_RUN_VERIFY_AUTH"))
	if origin == "" && key == "" {
		return nil
	}
	a := &RelayRunAuthority{environment: os.Getenv("HUB_ENVIRONMENT")}
	if origin != "" && key != "" {
		client := &http.Client{Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		a.identities = identity.NewHTTPClient(origin, key, client)
		a.runs = task.NewHTTPClient(origin, key, client)
		a.servicePassports = identity.NewServicePassportsReader(origin, key, client)
		if toolhub := strings.TrimSpace(os.Getenv("HUB_TOOLHUB_URL")); toolhub != "" {
			a.assignments = task.NewAssignmentHTTPClient(toolhub, key, client)
		}
	}
	return a
}

func (a *RelayRunAuthority) authorizeModelService(r *http.Request, space string, body []byte) (string, error) {
	values := r.Header.Values(identity.ModelServiceTokenHeader)
	if a == nil || a.identities == nil || a.servicePassports == nil || len(values) != 1 ||
		r.Header.Get("Origin") != "" || len(r.Header.Values(identity.RunTokenHeader)) != 0 ||
		len(r.Header.Values(api.HeaderRunID)) != 0 || len(r.Header.Values(api.HeaderJobID)) != 0 {
		return "", identity.ErrForbidden
	}
	ctx := r.Context()
	digest, err := a.servicePassports.ActiveServicePassports(ctx, space, a.environment)
	if err != nil {
		return "", identity.ErrForbidden
	}
	keys, err := a.identities.SigningKeys(ctx)
	now := time.Now()
	claim := identity.ServiceRequest{Space: space, Environment: a.environment,
		RequestDigest: identity.PolicyDigest(body), PolicyRevision: digest}
	if err != nil {
		return "", identity.ErrForbidden
	}
	audience, err := identity.VerifyModelServiceToken(values[0], claim, keys, now)
	if err != nil {
		return "", identity.ErrForbidden
	}
	signed, err := a.identities.Policy(ctx, digest)
	if err != nil || signed.Digest != digest {
		return "", identity.ErrForbidden
	}
	policy, err := identity.VerifyPolicyBundle(signed.JWS, digest, space, a.environment, keys, now)
	if err != nil || !policy.AllowsModelService(audience) {
		return "", identity.ErrForbidden
	}
	return audience, nil
}

func (a *RelayRunAuthority) authorize(r *http.Request, audience, space string) (identity.PolicyBundle, error) {
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
