package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

func TestAutomationDeliveryHandoffBindsOwnerAndRequestID(t *testing.T) {
	root := t.TempDir()
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	alice := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}}
	g := &Gateway{config: Config{ControlAuth: "service", Users: []User{alice, bob}}, spool: spool, users: map[int64]User{11: alice, 22: bob}, now: time.Now}
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	client := automation.HTTPCommunicationClient{URL: srv.URL, Token: "service"}
	delivery := Delivery{ID: "automation-1", Channel: "telegram_bot", ChatID: 11, DeliveryTargetID: "telegram-11", Text: "result"}
	for i := 0; i < 2; i++ {
		if err := client.PostDelivery(context.Background(), "alice", delivery); err != nil {
			t.Fatalf("handoff %d: %v", i, err)
		}
	}
	if err := client.PostDelivery(context.Background(), "bob", delivery); err == nil {
		t.Fatal("bob handed off a delivery to alice's chat")
	}
	wrongTarget := delivery
	wrongTarget.ID = "automation-wrong-target"
	wrongTarget.DeliveryTargetID = "telegram-22"
	if err := client.PostDelivery(context.Background(), "alice", wrongTarget); err == nil {
		t.Fatal("delivery target did not match the routed chat")
	}
	client.Token = "wrong"
	if err := client.PostDelivery(context.Background(), "alice", Delivery{ID: "automation-2", Channel: "telegram_bot", ChatID: 11, DeliveryTargetID: "telegram-11"}); err == nil {
		t.Fatal("wrong service bearer was accepted")
	}
	claimed, err := spool.ClaimDelivery()
	if err != nil || claimed == nil || claimed.ID != delivery.ID || claimed.OwnerPrincipalID != "alice" || claimed.IdempotencyKey != delivery.ID {
		t.Fatalf("owner or request ID not durably bound: %+v, %v", claimed, err)
	}
	if duplicate, err := spool.ClaimDelivery(); err != nil || duplicate != nil {
		t.Fatalf("duplicate handoff created another delivery: %+v, %v", duplicate, err)
	}
}

func TestAutomationDeliveryUsesLiveSendRight(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_AUTH", "service")
	root := t.TempDir()
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	alice := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	api := &fakeAPI{}
	decisions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		if r.URL.Path != "/v1/rights/decide" || r.Header.Get("Authorization") != "Bearer service" || json.NewDecoder(r.Body).Decode(&request) != nil || request["principal_id"] != "alice" || request["capability"] != "channels" || request["operation"] != "send" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"allow":true}`))
	}))
	defer decisions.Close()
	g := &Gateway{config: Config{ControlAuth: "service", ToolHubURL: decisions.URL, Users: []User{alice}}, spool: spool, users: map[int64]User{11: alice}, api: api, now: time.Now}
	server := httptest.NewServer(g.Handler())
	defer server.Close()
	client := automation.HTTPCommunicationClient{URL: server.URL, Token: "service"}
	if err := client.PostDelivery(context.Background(), "alice", Delivery{ID: "automation-allowed", Channel: "telegram_bot", ChatID: 11, DeliveryTargetID: "telegram-11", Text: "result"}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	api.mu.Lock()
	sent := len(api.sent)
	api.mu.Unlock()
	if sent != 1 {
		t.Fatalf("live right did not allow one delivery: %d", sent)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "done", spoolFileID("automation-allowed")+".json")); err != nil {
		t.Fatalf("accepted delivery lacked a durable receipt: %v", err)
	}
}

func TestAutomationDeliveryRechecksOwnerBeforeSend(t *testing.T) {
	root := t.TempDir()
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	alice := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	api := &fakeAPI{}
	g := &Gateway{config: Config{Users: []User{alice}}, spool: spool, users: map[int64]User{11: alice}, api: api, now: time.Now}
	if err := spool.EnqueueDelivery(Delivery{ID: "automation-revoked", OwnerPrincipalID: "alice", Channel: "telegram_bot", ChatID: 11, Text: "private"}); err != nil {
		t.Fatal(err)
	}
	g.config.Users[0].Enabled = false
	g.deliverOne(context.Background())
	api.mu.Lock()
	sent := len(api.sent)
	api.mu.Unlock()
	if sent != 0 {
		t.Fatal("revoked owner delivery was sent")
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "failed", spoolFileID("automation-revoked")+".json")); err != nil {
		t.Fatalf("revoked delivery was not durably fenced: %v", err)
	}
}
