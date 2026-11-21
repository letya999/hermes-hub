package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/passportview"
)

func TestApplyRespawnBoundToLiveDigest(t *testing.T) {
	m, space := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	effective, err := passportview.LoadEffective(space)
	if err != nil {
		t.Fatal(err)
	}
	// The v1 mirror points `current` at the effective digest and resolves
	// the emitted revision through the .policy indirection.
	digest := "sha256:" + strings.Repeat("ab", 32)
	passports := filepath.Join(space, "generated", "passports")
	if err := os.MkdirAll(filepath.Join(passports, ".policy"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(passports, ".policy", strings.TrimPrefix(digest, "sha256:")), []byte(effective.Header.Metadata.Revision), 0600); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(passports, "current")
	if err := os.WriteFile(current, []byte(digest+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
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
	}{{"apply", digest, http.StatusOK}, {"missing proof", "", http.StatusForbidden}, {"wrong digest", "sha256:" + strings.Repeat("cd", 32), http.StatusForbidden}} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.Handler().ServeHTTP(rec, request(tc.header))
			if rec.Code != tc.want {
				t.Fatalf("status=%d, want %d", rec.Code, tc.want)
			}
		})
	}
	// A mid-publish mirror pins current to pending — nothing is live to
	// bind the respawn to.
	if err := os.WriteFile(current, []byte("pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if m.applyRespawnAuthorized(request(digest), "alice", "alice", "") {
		t.Fatal("pending pointer admitted a respawn")
	}
	if err := os.WriteFile(current, []byte(digest+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if m.applyRespawnAuthorized(request(digest), "alice", "bob", "") {
		t.Fatal("foreign context admitted")
	}
}
