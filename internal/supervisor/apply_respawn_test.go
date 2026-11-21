package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

func TestApplyRespawnBoundToLiveDigest(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	// The live digest is the contract's effective digest — the file mirror
	// and its .policy indirection are gone (S7d).
	digest := "sha256:" + strings.Repeat("ab", 32)
	m.cfg.Spawn = &claimStub{spec: spawnv1.EffectiveSpec{Space: "alice", Digest: digest}}
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
	// No live spec means nothing to bind the respawn to.
	m.cfg.Spawn = &claimStub{specErr: spawnv1.ErrNotFound}
	if m.applyRespawnAuthorized(request(digest), "alice", "alice", "") {
		t.Fatal("missing spec admitted a respawn")
	}
	m.cfg.Spawn = &claimStub{spec: spawnv1.EffectiveSpec{Space: "alice", Digest: digest}}
	if m.applyRespawnAuthorized(request(digest), "alice", "bob", "") {
		t.Fatal("foreign context admitted")
	}
}
