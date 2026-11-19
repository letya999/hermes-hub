package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestHumanConfirmationCallbackVerifiesSenderChatExpiryAndRestart(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "synthetic-service-confirmation-bearer"
	cfg.Users[0].RuntimeID = "runtime"
	actor := cfg.Users[0].Envelope(11)
	proof := api.HumanConfirmation{OnboardingID: "onboard-human", Actor: actor, Nonce: "opaque-human-link", Revision: 3, ActionDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var got api.HumanConfirmation
		if r.URL.Path != "/v1/confirmations" || r.Header.Get("Authorization") != "Bearer "+cfg.ControlAuth || json.NewDecoder(r.Body).Decode(&got) != nil || got.Actor.ExternalIdentityID != "telegram-11" || got.Actor.PrincipalID != actor.PrincipalID || got.Nonce != proof.Nonce || got.ActionDigest != proof.ActionDigest || got.Revision != proof.Revision {
			t.Error("verified actor/action lost in channel callback")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg.ToolHubURL = server.URL
	g, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"onboarding_id": proof.OnboardingID, "phase": "awaiting-confirm", "definition_id": "connector", "confirmation": proof})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cfg.ControlAuth)
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmation notice status=%d body=%s", rec.Code, rec.Body.String())
	}
	if job, err := g.spool.ClaimJob(); err != nil || job != nil {
		t.Errorf("agent resumed before human confirmation: job=%v err=%v", job, err)
	}
	fake := &fakeAPI{}
	g.api = fake
	g.deliverOne(context.Background())
	if len(fake.keyboards) != 1 || len(fake.keyboards[0]) != 1 {
		t.Fatalf("human callback button was not delivered: %+v", fake.keyboards)
	}
	data := fake.keyboards[0][0].Data
	if !strings.HasPrefix(data, "h:") || strings.Contains(data, proof.Nonce) {
		t.Fatal("callback must be opaque and bounded")
	}
	g, err = newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	tap := func(sender, chat int64) {
		t.Helper()
		if err := g.handleCallback(context.Background(), &CallbackQuery{ID: "human-callback", From: &TGUser{ID: sender}, Message: &Message{Chat: TGChat{ID: chat, Type: "private"}}, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	tap(99, 11)
	tap(11, 22)
	g.now = func() time.Time { return proof.ExpiresAt.Add(time.Second) }
	tap(11, 11)
	if calls != 0 {
		t.Fatalf("unverified/expired callback reached ToolHub: %d", calls)
	}
	g.now = time.Now
	fresh, err := g.spool.humanConfirmationButton(api.HumanConfirmation{OnboardingID: proof.OnboardingID, Actor: actor, Nonce: proof.Nonce, Revision: proof.Revision, ActionDigest: proof.ActionDigest, ExpiresAt: time.Now().Add(time.Minute)}, "telegram_bot", 11)
	if err != nil {
		t.Fatal(err)
	}
	data = fresh.Data
	tap(11, 11)
	if calls != 1 {
		t.Fatalf("exact callback after restart calls=%d", calls)
	}
	tap(11, 11)
	if calls != 1 {
		t.Fatal("spent callback replayed")
	}
}

func TestHumanConfirmationNoticeRefusesChannelWithoutCallback(t *testing.T) {
	cfg := testConfig(t)
	g, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	user := cfg.Users[0]
	mapping := JobMapping{JobID: "job-slack", PrincipalID: "alice", Trigger: "message", Status: "running",
		Channel: "slack_app", ConversationID: "slack-thread", ChatID: 11, CreatedAt: time.Now()}
	if err := atomicJSON(filepath.Join(g.spool.root, "mappings", spoolFileID("job-slack")+".json"), mapping); err != nil {
		t.Fatal(err)
	}
	proof := api.HumanConfirmation{OnboardingID: "onboard-human", Actor: user.Envelope(11), Nonce: "opaque", Revision: 1, ActionDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
	if err := g.deliverPrepareNotice(user, user.Envelope(11), "notice", "confirm", prepareOutcomeRequest{OnboardingID: proof.OnboardingID, Confirmation: &proof}, false); err != errNoPrepareChannel {
		t.Fatalf("unsupported callback channel: %v", err)
	}
	if d, err := g.spool.ClaimDelivery(); err != nil || d != nil {
		t.Fatalf("unsupported confirmation was delivered: %v %v", d, err)
	}
}

func TestHumanConfirmationExpiredCallbackIsSwept(t *testing.T) {
	cfg := testConfig(t)
	actor := cfg.Users[0].Envelope(11)
	proof := api.HumanConfirmation{OnboardingID: "onboard-human", Actor: actor, Nonce: "opaque-human-link", Revision: 3, ActionDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
	g, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	button, err := g.spool.humanConfirmationButton(proof, "telegram_bot", 11)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(g.spool.root, "callbacks", "human-"+spoolFileID(button.Data)+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("callback record missing before sweep: %v", err)
	}
	g.now = func() time.Time { return proof.ExpiresAt.Add(time.Second) }
	if err := g.confirmHumanCallback(context.Background(), actor, "telegram_bot", 11, button.Data); err == nil {
		t.Fatal("expired callback accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired callback record left behind: %v", err)
	}
}

func TestHumanConfirmationCallbackReadIsBounded(t *testing.T) {
	cfg := testConfig(t)
	actor := cfg.Users[0].Envelope(11)
	proof := api.HumanConfirmation{OnboardingID: "onboard-human", Actor: actor, Nonce: "opaque-human-link", Revision: 3, ActionDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg.ToolHubURL = server.URL
	g, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	data := "h:oversized"
	body, _ := json.Marshal(map[string]any{"proof": proof, "channel": "telegram_bot", "chat_id": int64(11), "pad": strings.Repeat("x", 256<<10)})
	path := filepath.Join(g.spool.root, "callbacks", "human-"+spoolFileID(data)+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.confirmHumanCallback(context.Background(), actor, "telegram_bot", 11, data); err == nil {
		t.Fatal("oversized callback record accepted")
	}
	if calls != 0 {
		t.Fatalf("oversized callback reached ToolHub: %d", calls)
	}
}

func TestHumanConfirmationNoticeReissueDeliversFreshProof(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "synthetic-service-confirmation-bearer"
	actor := cfg.Users[0].Envelope(11)
	g, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	post := func(proof api.HumanConfirmation) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"onboarding_id": proof.OnboardingID, "phase": "awaiting-confirm", "definition_id": "connector", "confirmation": proof})
		req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+cfg.ControlAuth)
		req.Header.Set("X-Hub-Principal", "alice")
		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("confirmation notice status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	proof := api.HumanConfirmation{OnboardingID: "onboard-human", Actor: actor, Nonce: "nonce-one", Revision: 3, ActionDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
	post(proof)
	first, err := g.spool.ClaimDelivery()
	if err != nil || first == nil || len(first.Buttons) != 1 {
		t.Fatalf("first confirmation notice missing: %+v %v", first, err)
	}
	proof.Nonce, proof.ActionDigest = "nonce-two", "sha256:"+strings.Repeat("b", 64)
	post(proof)
	second, err := g.spool.ClaimDelivery()
	if err != nil || second == nil || len(second.Buttons) != 1 {
		t.Fatalf("reissued proof lost to stale notice key: %+v %v", second, err)
	}
	if second.Buttons[0].Data == first.Buttons[0].Data {
		t.Fatal("reissued notice reused the stale proof callback")
	}
}

func TestHumanConfirmationNoticeRequiresServiceAndExactScope(t *testing.T) {
	for _, name := range []string{"runtime", "shared-runtime", "actor", "context", "runtime-scope", "policy", "expiry", "nonce", "onboarding", "phase", "origin"} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.ControlAuth = "synthetic-service-confirmation-bearer"
			actor := cfg.Users[0].Envelope(11)
			proof := api.HumanConfirmation{OnboardingID: "onboard-human", Actor: actor, Nonce: "opaque-token", Revision: 3, ActionDigest: "sha256:" + strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
			token, phase, origin := cfg.ControlAuth, "awaiting-confirm", ""
			switch name {
			case "runtime":
				token = "synthetic-enrolled-runtime-bearer-0001"
				cfg.ControlTokensFile = filepath.Join(t.TempDir(), "synthetic-tokens.json")
				body, _ := json.Marshal(map[string]any{token: actor})
				if err := os.WriteFile(cfg.ControlTokensFile, body, 0600); err != nil {
					t.Fatal(err)
				}
			case "shared-runtime":
				cfg.RuntimeAuth = cfg.ControlAuth
			case "actor":
				proof.Actor.PrincipalID = "bob"
			case "context":
				proof.Actor.ContextID = "other"
			case "runtime-scope":
				proof.Actor.RuntimeID = "other"
			case "policy":
				proof.Actor.PolicyVersion = "other"
			case "expiry":
				proof.ExpiresAt = time.Now().Add(-time.Minute)
			case "nonce":
				proof.Nonce = ""
			case "onboarding":
				proof.OnboardingID = "other"
			case "phase":
				phase = "enabled"
			case "origin":
				origin = "https://untrusted.example"
			}
			g, err := newTestGateway(t, cfg)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{"onboarding_id": "onboard-human", "phase": phase, "definition_id": "connector", "confirmation": proof})
			req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Hub-Principal", "alice")
			req.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()
			g.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("untrusted proof notice accepted: %d", rec.Code)
			}
			if d, err := g.spool.ClaimDelivery(); err != nil || d != nil {
				t.Errorf("untrusted notice delivered: %v %v", d, err)
			}
		})
	}
}

// TestHumanConfirmationMemoryProposalNotice carries the #519 contract on the
// channel side: a ToolHub memory edit proposal posts its confirmation
// through the same prepare-outcome lane — the memprop- id is a valid target,
// the verified button is what the owner taps.
func TestHumanConfirmationMemoryProposalNotice(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAuth = "synthetic-service-confirmation-bearer"
	actor := cfg.Users[0].Envelope(11)
	g, err := newTestGateway(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	proof := api.HumanConfirmation{OnboardingID: "memprop-9f2c1ab07d3e4e55", Actor: actor, Nonce: "nonce-mem", Revision: 1, ActionDigest: "sha256:" + strings.Repeat("c", 64), ExpiresAt: time.Now().Add(time.Minute)}
	body, _ := json.Marshal(map[string]any{"onboarding_id": proof.OnboardingID, "phase": "awaiting-confirm", "repository": "truth memory/cards/notes/suppliers.md", "confirmation": proof})
	req := httptest.NewRequest(http.MethodPost, "/v1/prepare-outcome", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+cfg.ControlAuth)
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("memory proposal notice status=%d body=%s", rec.Code, rec.Body.String())
	}
	notice, err := g.spool.ClaimDelivery()
	if err != nil || notice == nil || len(notice.Buttons) != 1 {
		t.Fatalf("memory proposal confirmation button missing: %+v %v", notice, err)
	}
}
