package communication

import (
	"net/http"
	"slices"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func (g *Gateway) authorizeAgentControl(r *http.Request) (identity.Envelope, *contract.PolicyBundle, bool) {
	if g.config.Runs == nil && len(r.Header.Values(contract.RunTokenHeader)) == 0 {
		auth, ok := g.authorizeControl(r)
		return auth, nil, ok
	}
	record, policy, ok := g.authorizeAgentRun(r)
	return record.Envelope, policy, ok
}

func (g *Gateway) authorizeAgentRun(r *http.Request) (task.RunRecord, *contract.PolicyBundle, bool) {
	if r.Header.Get("Origin") != "" || len(r.Header.Values(contract.RunTokenHeader)) != 1 || len(r.Header.Values(api.HeaderRunID)) != 1 {
		return task.RunRecord{}, nil, false
	}
	authority := g.config.Runs
	if authority == nil && g.config.Supervised && g.config.RuntimeURL != "" {
		authority = task.NewHTTPClient(g.config.RuntimeURL, g.config.RuntimeAuth, nil)
	}
	record, policy, err := task.AuthorizeRun(r.Context(), g.config.Identity, authority, r.Header.Get(contract.RunTokenHeader), contract.CommunicationAudience,
		g.config.Environment, r.Header.Get(api.HeaderRunID), time.Now())
	if err != nil || !g.user(record.Envelope.PrincipalID).Enabled {
		return task.RunRecord{}, nil, false
	}
	if record.Assignment && (g.config.AssignmentAuthority == nil || g.config.AssignmentAuthority.AuthorizeAssignmentJob(r.Context(), record.JobID) != nil) {
		return task.RunRecord{}, nil, false
	}
	if principal := r.Header.Get("X-Hub-Principal"); principal != "" && principal != record.Envelope.PrincipalID {
		return task.RunRecord{}, nil, false
	}
	return record, &policy, true
}

func agentOperationAllowed(policy *contract.PolicyBundle, capability, operation string, write bool) bool {
	if policy == nil {
		return true
	}
	return policy.AllowsRight(capability, operation) &&
		(!write || policy.Identity.Restrictions.AllowsUnattendedWrite())
}

func agentSkillAllowed(policy *contract.PolicyBundle, skill string) bool {
	if policy == nil {
		return true
	}
	skills := policy.Identity.Restrictions.Skills
	return skills == nil || slices.Contains(skills, skill)
}

func denyAgentOperation(w http.ResponseWriter, r *http.Request) {
	lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
	http.Error(w, locale.T(lang, "run.authorization-denied"), http.StatusUnauthorized)
}
