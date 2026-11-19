package communication

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestPrepareDeliveryStatusRequiresOwnerAndReportsReceipt(t *testing.T) {
	cfg := testConfig(t)
	cfg.Supervised = true
	cfg.RuntimeURL = "http://127.0.0.1:1"
	cfg.ControlAuth = "runtime-bearer"
	cfg.RuntimeAuth = cfg.ControlAuth
	cfg.AutomationAuth = "service-bearer"
	g, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	actor := cfg.Users[0].Envelope(11)
	id := "onboard-receipt"
	check := func(token, origin string, wantCode int, wantStatus, wantChannel string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v1/prepare-delivery-status?onboarding_id="+id+"&notice_version=1", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Hub-Principal", actor.PrincipalID)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != wantCode {
			t.Fatalf("status code=%d want=%d", w.Code, wantCode)
		}
		if wantStatus != "" {
			var body struct {
				DeliveryStatus string `json:"delivery_status"`
				Channel        string `json:"channel"`
			}
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.DeliveryStatus != wantStatus {
				t.Fatalf("delivery status=%q want=%q", body.DeliveryStatus, wantStatus)
			}
			if body.Channel != wantChannel {
				t.Fatalf("delivery channel=%q want=%q", body.Channel, wantChannel)
			}
		}
	}
	check(cfg.ControlAuth, "", http.StatusUnauthorized, "", "")
	check(cfg.AutomationAuth, "https://untrusted.example", http.StatusUnauthorized, "", "")
	check(cfg.AutomationAuth, "", http.StatusOK, api.PrepareDeliveryUnknown, "")
	delivery := Delivery{ID: "prepare-" + id + "-awaiting-credentials-v1-notice", Channel: "telegram_bot", ChatID: 11, Text: "test", CreatedAt: time.Now(), PrepareKey: prepareDeliveryIdentity(actor, id), PrepareVersion: 1}
	if _, err := g.spool.enqueuePrepareDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	check(cfg.AutomationAuth, "", http.StatusOK, api.PrepareDeliveryQueued, "telegram_bot")
	foreign := actor
	foreign.PrincipalID = "other"
	if got, _, err := g.spool.prepareDeliveryStatus(foreign, id, 1); err != nil || got != api.PrepareDeliveryUnknown {
		t.Fatalf("foreign owner saw notice: status=%q err=%v", got, err)
	}
	claimed, err := g.spool.ClaimDelivery()
	if err != nil || claimed == nil {
		t.Fatalf("claim: delivery=%v err=%v", claimed, err)
	}
	claimed.TelegramMessageID = 42
	if err := g.spool.UpdateSending(*claimed); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.CompleteDelivery(claimed.ID); err != nil {
		t.Fatal(err)
	}
	check(cfg.AutomationAuth, "", http.StatusOK, api.PrepareDeliveryDelivered, "telegram_bot")
	failed := delivery
	failed.ID = "prepare-" + id + "-awaiting-credentials-v2-notice"
	failed.PrepareVersion = 2
	if _, err := g.spool.enqueuePrepareDelivery(failed); err != nil {
		t.Fatal(err)
	}
	claimed, err = g.spool.ClaimDelivery()
	if err != nil || claimed == nil || claimed.ID != failed.ID {
		t.Fatalf("second claim: delivery=%v err=%v", claimed, err)
	}
	if err := g.spool.UncertainDelivery(claimed.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, err := g.spool.prepareDeliveryStatus(actor, id, 2); err != nil || got != api.PrepareDeliveryUncertain {
		t.Fatalf("failed notice: status=%q err=%v", got, err)
	}
}
