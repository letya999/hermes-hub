package communication

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrimaryRuntimeControlBearerPinsPrincipal(t *testing.T) {
	alice := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}}
	for _, tc := range []struct {
		name, owner, token, want string
		supervised               bool
	}{
		{"owner cannot claim sibling", "alice", "owner-token", "alice", true},
		{"unknown owner denies", "", "owner-token", "", true},
		{"service peer retains routing", "alice", "service-token", "bob", true},
		{"legacy service retains routing", "", "owner-token", "bob", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &Gateway{config: Config{Supervised: tc.supervised, ControlAuth: "owner-token", ControlPrincipal: tc.owner, AutomationAuth: "service-token", controlAuthExtra: []string{"service-token"}, Users: []User{alice, bob}}}
			provisionTestIdentity(t, &g.config)

			r := httptest.NewRequest(http.MethodGet, "/v1/routines", nil)
			r.Header.Set("Authorization", "Bearer "+tc.token)
			r.Header.Set("X-Hub-Principal", "bob")
			caller, allowed := g.authorizeControl(r)
			if tc.want == "" {
				if allowed {
					t.Fatal("unbound runtime bearer borrowed a requested principal")
				}
			} else if !allowed || caller.PrincipalID != tc.want {
				t.Fatalf("control principal=%s allowed=%t expected=%s", caller.PrincipalID, allowed, tc.want)
			}
		})
	}
}

func TestPrimaryControlPrincipalComesFromRenderedIdentity(t *testing.T) {
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_COMMUNICATION_AUTH", "owner-token")
	t.Setenv("HUB_AUTOMATION_AUTH", "service-token")
	t.Setenv("HUB_SUPERVISOR_AUTH", "service-token")
	cfg := Config{Supervised: true, RuntimeAuth: "service-token"}
	fillChannelSecrets(&cfg)
	if cfg.ControlPrincipal != "alice" || cfg.ControlAuth == cfg.AutomationAuth {
		t.Fatal("rendered runtime/service identities were not kept separate")
	}
}
