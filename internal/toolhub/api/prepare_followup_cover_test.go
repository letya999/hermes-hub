package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestPrepareRouteEnvMatrix(t *testing.T) {
	for _, k := range []string{"HUB_COMMUNICATION_CONTROL_URL", "HUB_COMMUNICATION_AUTH", "HUB_RUNTIME_SUPERVISOR_URL", "HUB_SUPERVISOR_AUTH", "HUB_RUNTIME_AUTH"} {
		t.Setenv(k, "")
	}
	if base, _ := prepareRoute(); base != "" {
		t.Fatal("unconfigured route resolved")
	}
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", "http://comm:9/")
	if base, _ := prepareRoute(); base != "" {
		t.Fatal("route without token resolved")
	}
	t.Setenv("HUB_RUNTIME_AUTH", "rt-token")
	base, token := prepareRoute()
	if base != "http://comm:9" || token != "rt-token" {
		t.Fatalf("runtime-auth route: %q %q", base, token)
	}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://sup:9")
	t.Setenv("HUB_SUPERVISOR_AUTH", "sup-token")
	if _, token = prepareRoute(); token != "sup-token" {
		t.Fatalf("supervisor fallback: %q", token)
	}
	t.Setenv("HUB_COMMUNICATION_AUTH", "comm-token")
	if _, token = prepareRoute(); token != "comm-token" {
		t.Fatalf("direct token: %q", token)
	}
}

func noticeServer(t *testing.T, status int, out any, seen *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "prepare-delivery-status") {
			w.WriteHeader(status)
			if out != nil {
				json.NewEncoder(w).Encode(out)
			}
			return
		}
		if seen != nil {
			json.NewDecoder(r.Body).Decode(seen)
		}
		w.WriteHeader(status)
		if out != nil {
			json.NewEncoder(w).Encode(out)
		}
	}))
}

func TestPostPrepareNoticeDelivery(t *testing.T) {
	auth := identity.Envelope{PrincipalID: "alice"}
	ob := Onboarding{OnboardingID: "ob-1", DefinitionID: "def", Stage: StagePending, CreatedAt: time.Now()}
	ob.ArmGate(GateConfirmSetup, "nonce", time.Now().Add(time.Hour))
	var seen map[string]any
	srv := noticeServer(t, http.StatusOK, map[string]string{"login_url": "http://login/x"}, &seen)
	defer srv.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", srv.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	url, queued := PostPrepareNoticeWithDelivery(context.Background(), auth, ob, "", 3)
	if !queued || url != "http://login/x" {
		t.Fatalf("notice: %q %v", url, queued)
	}
	if seen["onboarding_id"] != "ob-1" || seen["tools"] != float64(3) || seen["confirmation"] == nil || seen["created_at"] == nil {
		t.Fatalf("payload fields: %v", seen)
	}
	// Event notices dedupe on their own key; link-resent mints an event id.
	seen = nil
	if _, ok := PostPrepareNoticeWithDelivery(context.Background(), auth, ob, "link-resent", 0); !ok {
		t.Fatal("event notice not queued")
	}
	if seen["event"] != "link-resent" || seen["event_id"] == nil {
		t.Fatalf("event payload: %v", seen)
	}
	DeliverPrepareOutcome(context.Background(), auth, ob)
	DeliverPrepareEvent(context.Background(), auth, ob, "build-started")
	if u := PostPrepareNotice(context.Background(), auth, ob, "", 0); u != "http://login/x" {
		t.Fatalf("legacy wrapper: %q", u)
	}
	// Unconfigured or incomplete inputs are silent no-ops.
	if _, ok := PostPrepareNoticeWithDelivery(context.Background(), identity.Envelope{}, ob, "", 0); ok {
		t.Fatal("empty principal queued")
	}
	if _, ok := PostPrepareNoticeWithDelivery(context.Background(), auth, Onboarding{}, "", 0); ok {
		t.Fatal("empty onboarding queued")
	}
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", "")
	if _, ok := PostPrepareNoticeWithDelivery(context.Background(), auth, ob, "", 0); ok {
		t.Fatal("unconfigured route queued")
	}
}

func TestPreparePostResultBoundaries(t *testing.T) {
	auth := identity.Envelope{PrincipalID: "alice"}
	if _, ok := preparePostResult(context.Background(), auth, "http://127.0.0.1:1", "tok", map[string]any{"x": 1}); ok {
		t.Fatal("unreachable host queued")
	}
	srv := noticeServer(t, http.StatusBadGateway, nil, nil)
	defer srv.Close()
	if _, ok := preparePostResult(context.Background(), auth, srv.URL, "tok", map[string]any{}); ok {
		t.Fatal("non-2xx queued")
	}
	// A broken payload (unmarshalable value) fails before the call.
	if _, ok := preparePostResult(context.Background(), auth, srv.URL, "tok", map[string]any{"x": make(chan int)}); ok {
		t.Fatal("unmarshalable payload queued")
	}
	srv2 := noticeServer(t, http.StatusOK, nil, nil)
	defer srv2.Close()
	if u, ok := preparePostResult(context.Background(), auth, srv2.URL, "tok", map[string]any{}); !ok || u != "" {
		t.Fatalf("plain 2xx: %q %v", u, ok)
	}
}

func TestReadPrepareDeliveryStatusMapping(t *testing.T) {
	auth := identity.Envelope{PrincipalID: "alice"}
	if s, _ := ReadPrepareDeliveryStatus(context.Background(), auth, "ob", 0); s != PrepareDeliveryUnknown {
		t.Fatalf("zero version: %q", s)
	}
	srv := noticeServer(t, http.StatusOK, map[string]string{"delivery_status": "delivered", "channel": "telegram"}, nil)
	defer srv.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", srv.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	if s, ch := ReadPrepareDeliveryStatus(context.Background(), auth, "ob", 2); s != PrepareDeliveryDelivered || ch != "telegram" {
		t.Fatalf("delivered: %q %q", s, ch)
	}
	for _, tc := range []struct {
		status int
		body   any
		want   string
	}{
		{http.StatusInternalServerError, nil, PrepareDeliveryUnknown},
		{http.StatusOK, "garbage", PrepareDeliveryUnknown},
		{http.StatusOK, map[string]string{"delivery_status": "bogus"}, PrepareDeliveryUnknown},
		{http.StatusOK, map[string]string{"delivery_status": "queued"}, PrepareDeliveryQueued},
		{http.StatusOK, map[string]string{"delivery_status": "uncertain"}, PrepareDeliveryUncertain},
	} {
		s2 := noticeServer(t, tc.status, tc.body, nil)
		t.Setenv("HUB_COMMUNICATION_CONTROL_URL", s2.URL)
		if s, _ := ReadPrepareDeliveryStatus(context.Background(), auth, "ob", 2); s != tc.want {
			t.Fatalf("%v: %q want %q", tc.body, s, tc.want)
		}
		s2.Close()
	}
}

func TestViewerLinkDeliveryAndURL(t *testing.T) {
	auth := identity.Envelope{PrincipalID: "alice"}
	var seen map[string]any
	srv := noticeServer(t, http.StatusOK, nil, &seen)
	defer srv.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", srv.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	if !DeliverViewerLinkWithDelivery(context.Background(), auth, "conn-1", "http://view") {
		t.Fatal("viewer link not queued")
	}
	DeliverViewerLink(context.Background(), auth, "conn-1", "http://view")
	if seen["connection_id"] != "conn-1" || seen["viewer_url"] != "http://view" {
		t.Fatalf("viewer payload: %v", seen)
	}
	if DeliverViewerLinkWithDelivery(context.Background(), auth, "", "http://view") {
		t.Fatal("empty connection queued")
	}
	// ViewerLinkURL prefers the public origin, then the toolhub listener.
	t.Setenv("HUB_PUBLIC_ORIGIN", "https://hub.example/")
	if got := ViewerLinkURL("conn 1", "n&n"); got != "https://hub.example/viewer/conn%201?t=n%26n" {
		t.Fatalf("viewer url: %q", got)
	}
	t.Setenv("HUB_PUBLIC_ORIGIN", "")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:8443")
	if got := ViewerLinkURL("c", "n"); !strings.Contains(got, "/viewer/c?t=n") {
		t.Fatalf("listen-derived url: %q", got)
	}
	if ViewerLinkURL("c", "") != "" {
		t.Fatal("empty nonce rendered")
	}
}

func TestActionURLSelection(t *testing.T) {
	origin := "https://hub.example"
	if got := ActionURL(origin, Onboarding{}); got != "" {
		t.Fatalf("empty onboarding: %q", got)
	}
	if got := ActionURL(origin, Onboarding{OnboardingID: "o", Stage: StageGone, Gate: &Gate{Kind: GateRemove, Nonce: "n"}}); got != "" {
		t.Fatalf("gone record: %q", got)
	}
	cases := []struct {
		name string
		ob   Onboarding
		want string
	}{
		{"remove", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateRemove, Nonce: "rn"}}, origin + "/remove/o?t=rn"},
		{"wizard", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateCredentials, Nonce: "g"}, WizardNonce: "wz"}, origin + "/connect/o?k=wz"},
		{"confirm", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateConfirmSetup, Nonce: "cn"}}, origin + "/confirm/o?t=cn"},
		{"confirm-spent", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateConfirmSetup, Nonce: "cn"}, ConfirmationUsed: true}, ""},
		{"oauth-state", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateOAuth}, ProviderAuthorizationURL: "https://p.example/auth?state=st&x=1"}, origin + "/connect/o?state=st"},
		{"oauth-no-state", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateOAuth}, ProviderAuthorizationURL: "https://p.example/auth"}, ""},
		{"oauth-bad-url", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateOAuth}, ProviderAuthorizationURL: "://bad"}, ""},
		{"broker-request", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateCredentials, Nonce: "g"}, BrokerRequestID: "req-1", BrokerAuthorizationURL: "https://b.example"}, origin + "/connect/o?request=req-1"},
		{"creds-nonce", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateCredentials, Nonce: "cn"}}, origin + "/credentials/o?t=cn"},
		{"creds-no-link", Onboarding{OnboardingID: "o", Gate: &Gate{Kind: GateCredentials}}, ""},
	}
	for _, tc := range cases {
		if got := ActionURL(origin, tc.ob); got != tc.want {
			t.Fatalf("%s: %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestErrStringAndMemoryConfirmation(t *testing.T) {
	if ErrString("") != nil {
		t.Fatal("empty string wrapped")
	}
	if err := ErrString("boom"); err == nil || err.Error() != "boom" {
		t.Fatalf("plain error: %v", err)
	}
	var seen map[string]any
	srv := noticeServer(t, http.StatusOK, nil, &seen)
	defer srv.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", srv.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "tok")
	auth := identity.Envelope{PrincipalID: "alice"}
	DeliverMemoryConfirmation(context.Background(), auth, HumanConfirmation{OnboardingID: "ob-9"}, "memory edit")
	if seen["onboarding_id"] != "ob-9" || seen["phase"] != "awaiting-confirm" || seen["repository"] != "memory edit" || seen["confirmation"] == nil {
		t.Fatalf("memory notice: %v", seen)
	}
	DeliverMemoryConfirmation(context.Background(), auth, HumanConfirmation{}, "x")
}
