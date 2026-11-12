package toolhub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestConnectorStatusReportsChannelDelivery(t *testing.T) {
	auth := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	status := api.PrepareDeliveryUncertain
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/prepare-delivery-status" || r.URL.Query().Get("onboarding_id") != "onboard-test" || r.URL.Query().Get("notice_version") != "3" || r.Header.Get("Authorization") != "Bearer service-bearer" || r.Header.Get("X-Hub-Principal") != "alice" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"delivery_status": status})
	}))
	defer server.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", server.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "service-bearer")
	onboarding := api.Onboarding{OnboardingID: "onboard-test", NoticeVersion: 3, NoticeQueuedVersion: 3}
	control := &ControlPlane{}
	result := api.OperationResult{NextAction: "submit_credentials"}
	control.observeNoticeDelivery(t.Context(), auth, onboarding, &result)
	if result.DeliveryStatus != api.PrepareDeliveryUncertain || result.Code != api.OpCodeDeliveryUnavailable || result.NextAction != "resend" || !result.LinkSent {
		t.Fatalf("uncertain delivery not actionable: %+v", result)
	}
	status = api.PrepareDeliveryDelivered
	result = api.OperationResult{}
	control.observeNoticeDelivery(t.Context(), auth, onboarding, &result)
	if result.DeliveryStatus != api.PrepareDeliveryDelivered || result.Code != "" || !result.LinkSent {
		t.Fatalf("delivered receipt not exposed: %+v", result)
	}
	onboarding.NoticeVersion++
	result = api.OperationResult{}
	control.observeNoticeDelivery(t.Context(), auth, onboarding, &result)
	if result.DeliveryStatus != api.PrepareDeliveryNotQueued || result.LinkSent {
		t.Fatalf("unqueued notice misreported: %+v", result)
	}
}
