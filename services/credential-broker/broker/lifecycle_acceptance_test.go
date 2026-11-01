package broker

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/provider"
)

// CHG-0220 acceptance (SPEC-0087 R4): 1000 synthetic refresh/checkpoint
// cycles under a live broker. Afterwards the retained set is bounded —
// only current versions, live pins, abandoned-but-owned intents awaiting
// sweep, and unknown provider objects survive — a fresh restore works,
// and no raw secret material appears anywhere in the ledger.
func TestLifecycleScaleAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("scale acceptance")
	}
	var refreshes atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/token" {
			w.WriteHeader(400)
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "authorization_code" {
			fmt.Fprint(w, `{"access_token":"access-0","refresh_token":"refresh-0","expires_in":60,"token_type":"bearer"}`)
			return
		}
		n := refreshes.Add(1)
		fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":"refresh-%d","expires_in":60,"token_type":"bearer"}`, n, n)
	}))
	defer up.Close()

	f := newFixture(t, oauthContract(), stateContract(), plainContract())
	f.cfg.OAuth["example"] = oauthProvider(up)
	f.start()

	// Two live credentials: one state-bearing, one OAuth-rotating.
	sc := f.enroll(alice, "session", "service")
	sg, sa := f.grant(alice, sc, "session", "state_binding", "state_workload")
	r, cookie, q := oauthBegin(t, f)
	if e := f.b.CompleteOAuth(testCtx, cookie, q.Get("state"), "one-code"); e != nil {
		t.Fatal(e)
	}
	rr, _ := f.b.GetRequest(alice, r.ID)
	oc, _ := f.b.GetCredential(alice, rr.CredentialID)
	og, oa := f.grant(alice, oc, "oauth", "oauth_binding", "oauth_workload")

	// An unknown provider object the broker never wrote — a conservative
	// keep that no lifecycle path may ever touch.
	unknown := strings.Repeat("u", 43)
	if e := os.WriteFile(filepath.Join(f.root, "secrets", unknown), []byte("foreign"), 0600); e != nil {
		t.Fatal(e)
	}

	// 500 checkpoint cycles: 8 rotating payloads, each written twice —
	// half dedupe against an identical predecessor, half supersede.
	for i := 0; i < 500; i++ {
		f.checkpoint(sa, sg, fmt.Sprintf(`{"session":"v%d"}`, i/2%8))
		if i%50 == 49 {
			f.b.Sweep()
		}
	}
	// 500 rotating OAuth cycles: lease → use → release, expiry forced.
	for i := 0; i < 500; i++ {
		l, e := f.b.Acquire(oa, v1.AcquireLease{GrantID: og.ID, TTLSeconds: 300})
		if e != nil {
			t.Fatal(e)
		}
		f.clock.add(61 * time.Second)
		u, e := f.b.BeginUse(testCtx, oa, l.ID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e := f.b.AccessToken(testCtx, u); e != nil {
			u.Finish()
			t.Fatal(e)
		}
		u.Finish()
		if e := f.b.Release(testCtx, oa, l.ID, v1.RuntimeRelease{}); e != nil {
			t.Fatal(e)
		}
		if i%50 == 49 {
			f.b.Sweep()
		}
	}
	if got := refreshes.Load(); got < 500 {
		t.Fatalf("refreshes = %d, want >= 500", got)
	}

	// Abandoned owned intent: crashed draft write + canceled request.
	w := &crashWriter{Provider: f.cfg.Providers["local"]}
	f.cfg.Providers["local"] = w
	w.armed.Store(true)
	dr := f.request(alice, "pat", "github", "scale_abandoned")
	dc := f.pair(alice, dr)
	_ = f.b.Submit(testCtx, dr.ID, dc, f.b.CSRF(dc, dr.ID), provider.Bundle{"token": []byte("x")}, nil)
	w.armed.Store(false)
	_ = f.b.CancelRequest(alice, dr.ID)

	// Drain the bounded worker until the queue is empty.
	for i := 0; i < 40; i++ {
		if n, _ := f.b.SweepObjects(testCtx, alice, 64); n == 0 {
			break
		}
	}

	// Retained set: exactly the live versions plus the unknown object.
	states := map[string]int{}
	for _, o := range f.b.objects {
		states[o.State]++
	}
	if states["current"] != 3 {
		t.Fatalf("retained current rows = %d, want 3 (session cred, oauth cred, state)", states["current"])
	}
	if states["intent"] != 0 || states["pending-delete"] != 0 {
		t.Fatalf("lifecycle left intents=%d pending=%d", states["intent"], states["pending-delete"])
	}
	entries, e := os.ReadDir(filepath.Join(f.root, "secrets"))
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 4 {
		names := []string{}
		for _, en := range entries {
			names = append(names, en.Name())
		}
		t.Fatalf("retained provider objects = %d, want 4 (3 current + unknown): %v", len(entries), names)
	}
	foundUnknown := false
	for _, en := range entries {
		if en.Name() == unknown {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Fatal("unknown provider object destroyed")
	}

	// Live restore: materialize the current state version and read it back.
	l := f.lease(alice, sg)
	m, e := f.b.Materialize(testCtx, sa, l.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(m.Mounts[1].Source, "runtime_session.json")); e != nil {
		t.Fatalf("live restore failed: %v", e)
	}
	if e := f.b.Release(testCtx, sa, l.ID, v1.RuntimeRelease{}); e != nil {
		t.Fatal(e)
	}

	// No raw material in the ledger: scan every ledger file for the
	// submitted sentinel and minted token values.
	for _, banned := range []string{"SENTINEL_NOT_A_REAL_SECRET", "access-", "refresh-"} {
		files, _ := filepath.Glob(filepath.Join(f.root, "ledger", "*"))
		for _, file := range files {
			raw, _ := os.ReadFile(file)
			if strings.Contains(string(raw), banned) {
				t.Fatalf("raw material %q found in %s", banned, file)
			}
		}
	}
	t.Logf("acceptance: %d cycles, object states %v, retained files %d", 1000, states, len(entries))
}
