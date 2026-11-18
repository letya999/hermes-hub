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
	g := &Gateway{config: Config{ControlAuth: "service", AutomationAuth: "automation-service", Users: []User{alice, bob}}, spool: spool, users: map[int64]User{11: alice, 22: bob}, now: time.Now}
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	client := automation.HTTPCommunicationClient{URL: srv.URL, Token: "automation-service"}
	if err := (&automation.HTTPCommunicationClient{URL: srv.URL, Token: "service"}).PostDelivery(context.Background(), "alice", "alice", Delivery{ID: "automation-control-token", Channel: "telegram_bot", ChatID: 11, DeliveryTargetID: "telegram-11", Text: "denied"}); err == nil {
		t.Fatal("ordinary control token admitted a service-only delivery")
	}
	delivery := Delivery{ID: "automation-1", Channel: "telegram_bot", ChatID: 11, DeliveryTargetID: "telegram-11", Text: "result"}
	for i := 0; i < 2; i++ {
		if err := client.PostDelivery(context.Background(), "alice", "alice", delivery); err != nil {
			t.Fatalf("handoff %d: %v", i, err)
		}
	}
	if err := client.PostDelivery(context.Background(), "bob", "bob", delivery); err == nil {
		t.Fatal("bob handed off a delivery to alice's chat")
	}
	wrongTarget := delivery
	wrongTarget.ID = "automation-wrong-target"
	wrongTarget.DeliveryTargetID = "telegram-22"
	if err := client.PostDelivery(context.Background(), "alice", "alice", wrongTarget); err == nil {
		t.Fatal("delivery target did not match the routed chat")
	}
	if err := client.PostDelivery(context.Background(), "alice", "", delivery); err == nil {
		t.Fatal("delivery without an owner context was accepted")
	}
	// A forged scope inside the body never reaches the spool: the service
	// contract carries principal/context in headers only.
	forged := delivery
	forged.ID = "automation-forged"
	forged.OwnerPrincipalID, forged.OwnerContextID = "mallory", "mallory"
	if err := client.PostDelivery(context.Background(), "alice", "alice", forged); err != nil {
		t.Fatalf("client must strip body owner fields before posting: %v", err)
	}
	client.Token = "wrong"
	if err := client.PostDelivery(context.Background(), "alice", "alice", Delivery{ID: "automation-2", Channel: "telegram_bot", ChatID: 11, DeliveryTargetID: "telegram-11"}); err == nil {
		t.Fatal("wrong service bearer was accepted")
	}
	claimed, err := spool.ClaimDelivery()
	if err != nil || claimed == nil || claimed.ID != delivery.ID || claimed.OwnerPrincipalID != "alice" || claimed.OwnerContextID != "alice" || claimed.IdempotencyKey != delivery.ID {
		t.Fatalf("owner or request ID not durably bound: %+v, %v", claimed, err)
	}
	forgedClaim, err := spool.ClaimDelivery()
	if err != nil || forgedClaim == nil || forgedClaim.ID != "automation-forged" || forgedClaim.OwnerPrincipalID != "alice" || forgedClaim.OwnerContextID != "alice" {
		t.Fatalf("body-forged owner reached the spool: %+v, %v", forgedClaim, err)
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
	if err := client.PostDelivery(context.Background(), "alice", "alice", Delivery{ID: "automation-allowed", Channel: "telegram_bot", ChatID: 11, DeliveryTargetID: "telegram-11", Text: "result"}); err != nil {
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

func TestDeliveryWithBareOccurrenceRefFailsClosed(t *testing.T) {
	root := t.TempDir()
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	alice := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	api := &fakeAPI{}
	g := &Gateway{config: Config{Users: []User{alice}}, spool: spool, users: map[int64]User{11: alice}, api: api, now: time.Now}
	// A delivery that references an occurrence without an owner principal is a
	// legacy communication-owner record: post-cutover nothing may send it.
	if err := spool.EnqueueDelivery(Delivery{ID: "legacy-watch", OccurrenceID: "occ-1", Channel: "telegram_bot", ChatID: 11, Text: "stale"}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	api.mu.Lock()
	sent := len(api.sent)
	api.mu.Unlock()
	if sent != 0 {
		t.Fatal("bare occurrence delivery was sent")
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "failed", spoolFileID("legacy-watch")+".json")); err != nil {
		t.Fatalf("bare occurrence delivery was not settled unconfirmed: %v", err)
	}
}

// An owner-bound occurrence delivery re-checks the occurrence with ToolHub
// before the channel send: allow sends, a denial retires the record
// unconfirmed, and an unreachable owner keeps it retryable.
func TestOccurrenceDeliveryFenceRechecksOwner(t *testing.T) {
	t.Setenv("HUB_SUPERVISOR_AUTH", "service")
	root := t.TempDir()
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	alice := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	api := &fakeAPI{}
	allow := false
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/rights/decide" {
			_, _ = w.Write([]byte(`{"allow":true}`))
			return
		}
		var req struct {
			Operation string `json:"operation"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Operation != "occurrence.authorize" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !allow {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"result":{"allow":true}}`))
	}))
	defer control.Close()
	g := &Gateway{config: Config{ControlAuth: "service", ToolHubURL: control.URL, AutomationAuth: "automation-test", Users: []User{alice}}, spool: spool, users: map[int64]User{11: alice}, api: api, now: time.Now}
	put := func(id, occ string) {
		if err := spool.EnqueueDelivery(Delivery{ID: id, OccurrenceID: occ, OwnerPrincipalID: "alice", OwnerContextID: "alice", Channel: "telegram_bot", ChatID: 11, DeliveryTargetID: "telegram-11", Text: "note"}); err != nil {
			t.Fatal(err)
		}
	}
	put("occ-denied", "occ-1")
	g.deliverOne(context.Background())
	api.mu.Lock()
	if len(api.sent) != 0 {
		t.Fatal("denied occurrence delivery was sent")
	}
	api.mu.Unlock()
	if _, err := os.Stat(filepath.Join(root, "outbox", "failed", spoolFileID("occ-denied")+".json")); err != nil {
		t.Fatalf("denied occurrence delivery not settled unconfirmed: %v", err)
	}
	allow = true
	put("occ-allowed", "occ-2")
	g.deliverOne(context.Background())
	api.mu.Lock()
	if len(api.sent) != 1 {
		t.Fatal("authorized occurrence delivery was not sent")
	}
	api.mu.Unlock()
	// An unreachable owner pauses the delivery instead of settling it
	// unconfirmed: the outage must not silently drop a live notice.
	g.config.ToolHubURL = "http://127.0.0.1:1"
	put("occ-outage", "occ-3")
	g.deliverOne(context.Background())
	api.mu.Lock()
	if len(api.sent) != 1 {
		t.Fatal("outage delivered to the channel")
	}
	api.mu.Unlock()
	if _, err := os.Stat(filepath.Join(root, "outbox", "failed", spoolFileID("occ-outage")+".json")); err == nil {
		t.Fatal("outage settled the delivery unconfirmed")
	}
	g.config.ToolHubURL = control.URL
	path := filepath.Join(root, "outbox", "pending", spoolFileID("occ-outage")+".json")
	body, err := spool.fsys.ReadFile(path)
	if err != nil {
		t.Fatalf("outage delivery not durable in pending: %v", err)
	}
	var recovered Delivery
	if err := json.Unmarshal(body, &recovered); err != nil || recovered.ID != "occ-outage" {
		t.Fatalf("outage delivery record unreadable: %v", err)
	}
	recovered.NotBefore = time.Time{}
	if err := spool.writeJSON(path, recovered); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	api.mu.Lock()
	if len(api.sent) != 2 {
		t.Fatal("recovered outage delivery was not sent")
	}
	api.mu.Unlock()
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
	if err := spool.EnqueueDelivery(Delivery{ID: "automation-revoked", OwnerPrincipalID: "alice", OwnerContextID: "alice", Automation: true, Channel: "telegram_bot", ChatID: 11, Text: "private"}); err != nil {
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
