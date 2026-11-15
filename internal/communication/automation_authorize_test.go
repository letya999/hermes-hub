package communication

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

func TestAutomationOwnerHomeUsesLiveOwner(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alice := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}}
	g := &Gateway{config: Config{ControlAuth: "service", OrganizationID: "personal", Users: []User{alice, bob}}, spool: spool, users: map[int64]User{11: alice, 22: bob}, now: time.Now}
	server := httptest.NewServer(g.Handler())
	defer server.Close()
	client := automation.HTTPCommunicationClient{URL: server.URL, Token: "service"}
	if home, lang, err := client.OwnerHome(context.Background(), "alice"); err != nil || home != 11 || lang == "" {
		t.Fatalf("owner home: chat=%d lang=%q err=%v", home, lang, err)
	}
	client.Token = "wrong"
	if _, _, err := client.OwnerHome(context.Background(), "alice"); err == nil {
		t.Fatal("wrong service bearer read an owner home")
	}
	client.Token = "service"
	g.users[11] = bob
	if home, _, err := client.OwnerHome(context.Background(), "alice"); err != nil || home != 0 {
		t.Fatalf("rebound home was still routable: chat=%d err=%v", home, err)
	}
}

func TestAutomationOwnerHomeRequiresAutomationServiceBearer(t *testing.T) {
	owner := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	g := &Gateway{config: Config{ControlAuth: "owner-runtime", AutomationAuth: "automation-service", Users: []User{owner}}, users: map[int64]User{11: owner}}
	server := httptest.NewServer(g.Handler())
	defer server.Close()
	client := automation.HTTPCommunicationClient{URL: server.URL, Token: "automation-service"}
	if home, _, err := client.OwnerHome(t.Context(), "alice"); err != nil || home != 11 {
		t.Fatalf("supervised service home: chat=%d err=%v", home, err)
	}
	for _, token := range []string{"owner-runtime", "wrong", ""} {
		client.Token = token
		if _, _, err := client.OwnerHome(t.Context(), "alice"); err == nil {
			t.Fatal("non-service bearer read owner home")
		}
	}
}
