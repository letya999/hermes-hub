package toolhub

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/audit"
)

func TestEndpointAdminRequiresDurableAttributedAudit(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-ledger", true: "durable-ledger"}[configured], func(t *testing.T) {
			path := ""
			if configured {
				path = filepath.Join(t.TempDir(), "audit.jsonl")
			}
			t.Setenv("HUB_AUDIT_LEDGER", path)
			g, _ := adminFixture(t, nil)
			token := "admin-bearer-token-0000000000000000"
			h, err := NewEndpointHandler(EndpointConfig{Token: token, Auth: g.Tokens[token], Backend: stubBackend{text: "unused"}}, g.Store)
			if err != nil {
				t.Fatal(err)
			}
			w := adminCall(h, token, "GET", "/v1/admin/diagnose/ob-1", "")
			if !configured {
				if w.Code != 503 || strings.Contains(w.Body.String(), "alice") {
					t.Errorf("unrecorded cross-principal read: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("durably audited diagnosis failed: %d %s", w.Code, w.Body.String())
			}
			ledger, err := audit.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			events, err := ledger.List("hermes-admin")
			if err != nil || len(events) != 1 {
				t.Fatalf("missing admin audit attribution: events=%d err=%v", len(events), err)
			}
			if events[0].Name != "ob-1" || events[0].Outcome != "read" || events[0].Kind != "tool-call" {
				t.Errorf("wrong admin target/action: %+v", events[0])
			}
		})
	}
}
