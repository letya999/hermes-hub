package toolhub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The loopback form marks required vs optional fields, offers optional inputs
// discovered upstream, and lets the owner demote a required field at submit.
func TestLoopbackFormOptionalFieldsAndDemote(t *testing.T) {
	store := NewStore()
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := credstore.Open(credstore.Options{Path: filepath.Join(t.TempDir(), "form.enc"), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, Secrets: secrets, Now: time.Now, ConfirmationTTL: time.Hour, FormOrigin: "http://127.0.0.1:8090"}
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "onboard-opt1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: api.OnboardingSelfInstall, Phase: api.PhaseAwaitingCreds, SourceURL: "https://github.com/zereight/gitlab-mcp",
		DefinitionID: "gitlab", DefinitionVersion: "1.0.0", Revision: 1, CreatedAt: time.Now(),
		FormNonce:   api.RandomNonce(),
		FormExpires: time.Now().Add(time.Hour),
		Required:    []api.CredentialHint{{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Secret: true, Delivery: "env"}},
		Optional:    []api.CredentialHint{{Name: "GITLAB_PROJECT_ID", Delivery: "env"}},
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	definition := userMCPDefinition()
	definition.DefinitionID = "gitlab"
	definition.Version = "1.0.0"
	definition.Credentials = []api.CredentialInput{
		{Name: "GITLAB_PERSONAL_ACCESS_TOKEN", Required: true},
		{Name: "GITLAB_PROJECT_ID", Required: false},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Control: control}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/credentials/"+onboarding.OnboardingID+"?nonce="+onboarding.FormNonce, nil)
	req.Host = "127.0.0.1"
	gateway.serveCredentials(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"обязательное", "необязательное", `name="opt:GITLAB_PERSONAL_ACCESS_TOKEN"`, `name="GITLAB_PROJECT_ID"`, "formnovalidate"} {
		if rec.Code != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("form misses %q status=%d body=%s", want, rec.Code, body)
		}
	}
	// Owner demotes the required token and fills only the optional input.
	form := url.Values{"nonce": {onboarding.FormNonce}, "opt:GITLAB_PERSONAL_ACCESS_TOKEN": {"1"}, "GITLAB_PROJECT_ID": {"42"}}
	post := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/credentials/"+onboarding.OnboardingID, strings.NewReader(form.Encode()))
	post.Host = "127.0.0.1"
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	gateway.serveCredentials(rec, post)
	// A live wizard nonce turns the accepted submit into a redirect onto the
	// stable /connect link; without one the legacy result page still serves.
	if rec.Code != http.StatusOK && rec.Code != http.StatusSeeOther {
		t.Fatalf("demoted submit rejected: %d %s", rec.Code, rec.Body.String())
	}
	stored, err := store.onboarding(onboarding.OnboardingID)
	if err != nil || stored.Phase == api.PhaseAwaitingCreds || stored.Locator == "" {
		t.Fatalf("phase=%s locator=%q err=%v", stored.Phase, stored.Locator, err)
	}
	bundle, err := secrets.Get(stored.Locator, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if bundle["GITLAB_PROJECT_ID"] != "42" || bundle["GITLAB_PERSONAL_ACCESS_TOKEN"] != "" {
		t.Fatalf("stored bundle: %v", bundle)
	}
}
