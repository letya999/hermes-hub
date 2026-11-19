package communication

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type agentRunRecord struct{ value task.RunRecord }

func (a agentRunRecord) Run(_ context.Context, job string) (task.RunRecord, error) {
	if job != a.value.JobID {
		return task.RunRecord{}, task.ErrNotFound
	}
	return a.value, nil
}

func TestDelegatedGoalRejectsBodyConversationAndAssertedPrincipal(t *testing.T) {
	ctx := t.Context()
	registry, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	users := []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}}
	if _, err := registry.ImportLegacy(ctx, "fixture", "personal", users); err != nil {
		t.Fatal(err)
	}
	if err := registry.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	policy := contract.PolicyBundle{Space: "alice", Environment: "dev", PassportRevision: "r-1", Passports: map[string]json.RawMessage{}}
	for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
		policy.Passports[name] = json.RawMessage(fmt.Sprintf(`{"kind":%q,"metadata":{"space":"alice","environment":"dev","revision":"r-1"},"allow":{"goals":["read"]}}`, name))
	}
	base, err := registry.StagePassports(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivatePassports(ctx, policy.Space, policy.Environment, "", base.Digest); err != nil {
		t.Fatal(err)
	}
	const actor = "agent:alice:default"
	signed, bundle, err := registry.BindPassports(ctx, contract.TemplateRequest{Subject: "alice", Actor: actor, Space: "alice"}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	revision, err := bundle.Identity.Revision()
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 7, "alice", "policy-1")
	record := task.RunRecord{JobID: "job-one", RunID: "run-one", TaskID: "default", Status: "running", Channel: "telegram_bot", Envelope: caller,
		Environment: "dev", RuntimeGeneration: "gen-one", CurrentGeneration: "gen-one", Deadline: time.Now().Add(time.Minute),
		ActingIdentity: task.ActingIdentity{Subject: "alice", Actor: actor, TemplateRevision: revision}, PolicyRevision: signed.Digest, PassportDigest: base.Digest}
	token, err := registry.IssueRun(ctx, task.ClaimsForRun(record, contract.CommunicationAudience, bundle.Identity.Restrictions.Mode), record.Deadline, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{config: Config{Identity: registry, Runs: agentRunRecord{record}, ControlAuth: "synthetic-runtime", Users: users, Environment: "dev"}}
	request := func(bearer, principal, conversation string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/goals", strings.NewReader(`{"op":"status","conversation_id":"`+conversation+`"}`))
		r.Header.Set("Authorization", "Bearer synthetic-runtime")
		r.Header.Set(contract.RunTokenHeader, bearer)
		r.Header.Set(api.HeaderRunID, record.RunID)
		r.Header.Set("X-Hub-Principal", principal)
		return r
	}
	if auth, _, ok := g.authorizeAgentControl(request(token, "", caller.ConversationID)); !ok || !auth.Equal(caller) {
		t.Fatal("signed delegation did not bind the durable caller")
	}
	for _, test := range []struct{ token, principal, conversation string }{
		{"", caller.PrincipalID, caller.ConversationID},
		{token, "bob", caller.ConversationID},
		{token, "", "telegram-other"},
	} {
		w := httptest.NewRecorder()
		g.handleGoals(w, request(test.token, test.principal, test.conversation))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unverified goal request reached handler, status=%d", w.Code)
		}
	}
}
