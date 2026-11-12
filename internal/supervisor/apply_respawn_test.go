package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passportview"
)

func TestApplyRespawnUsesExactJournalWhilePDPIsFenced(t *testing.T) {
	m, space := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	effective, err := passportview.LoadEffective(space)
	if err != nil {
		t.Fatal(err)
	}
	journal := map[string]any{
		"environment": "prod", "previous": effective.Header.Metadata.Revision,
		"revision": "r-next", "phase": "effects",
		"actions": []map[string]string{{"kind": "respawn", "target": "hermes-runtime"}},
	}
	path := filepath.Join(space, "generated", "passports", passportview.ApplyJournalFile)
	writeJournal := func() {
		t.Helper()
		body, err := json.Marshal(journal)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJournal()
	t.Setenv("HUB_TOOLHUB_URL_PATTERN", "http://127.0.0.1:1")
	request := func(header string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/respawn", strings.NewReader(`{"principal_id":"alice","context_id":"alice"}`))
		r.Header.Set("Authorization", "Bearer secret")
		if header != "" {
			r.Header.Set("X-Hub-Apply-Revision", header)
		}
		return r
	}
	for _, tc := range []struct {
		name, header string
		want         int
	}{{"apply", "r-next", http.StatusOK}, {"missing proof", "", http.StatusForbidden}, {"wrong revision", "r-other", http.StatusForbidden}} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.Handler().ServeHTTP(rec, request(tc.header))
			if rec.Code != tc.want {
				t.Fatalf("status=%d, want %d", rec.Code, tc.want)
			}
		})
	}
	current := filepath.Join(space, "generated", "passports", "current")
	if err := os.WriteFile(current, []byte("pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !m.applyRespawnAuthorized(request("r-next"), "alice", "alice", "") {
		t.Fatal("journaled respawn denied while current revision is pending")
	}
	journal["phase"] = "binding"
	writeJournal()
	if m.applyRespawnAuthorized(request("r-next"), "alice", "alice", "") {
		t.Fatal("non-effect journal admitted")
	}
	journal["phase"] = "effects"
	journal["actions"] = []map[string]string{{"kind": "recreate", "target": "hermes-runtime"}}
	writeJournal()
	if !m.applyRespawnAuthorized(request("r-next"), "alice", "alice", "") {
		t.Fatal("journaled runtime recreate denied")
	}
	journal["actions"] = []map[string]string{{"kind": "live", "target": "tool-policy"}}
	writeJournal()
	if m.applyRespawnAuthorized(request("r-next"), "alice", "alice", "") {
		t.Fatal("journal without respawn admitted")
	}
	if m.applyRespawnAuthorized(request("r-next"), "alice", "bob", "") {
		t.Fatal("foreign context admitted")
	}
}
