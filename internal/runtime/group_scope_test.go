package runtime

import "testing"

func TestRuntimeSeparatesGroupAndPersonalScopeAdmission(t *testing.T) {
	t.Setenv("HUB_USER_ID", "team")
	t.Setenv("HUB_ORGANIZATION_ID", "personal")
	t.Setenv("HUB_RUNTIME_ID", "team")
	t.Setenv("HUB_POLICY_VERSION", "policy-1")
	r := validExecuteRequest("team", "personal", "group-key", "hello")
	r.ScopeID = "group:team"
	t.Setenv("HUB_SCOPE_KIND", "group")
	if err := validateExecuteRequest(r); err != nil {
		t.Errorf("dedicated group runtime rejected its own group: %v", err)
	}
	r.ScopeID = "user:team"
	if err := validateExecuteRequest(r); err == nil {
		t.Error("group runtime accepted a private conversation")
	}
	t.Setenv("HUB_SCOPE_KIND", "user")
	r.ScopeID = "group:team"
	if err := validateExecuteRequest(r); err == nil {
		t.Error("personal runtime accepted group authority")
	}
}
