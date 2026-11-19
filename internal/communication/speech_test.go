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

	"github.com/letya999/hermes-hub/internal/locale"
)

func TestSpeechDeliveryBindsJobOwnerAndIsIdempotent(t *testing.T) {
	g, err := newTestGateway(t, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.config.AutomationAuth = "speech-service"
	g.synthesizer = HTTPSynthesizer{Base: "http://unused.invalid"}
	user := g.user("alice")
	job := Job{Envelope: user.Envelope(11), ID: "speech-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "speech-job", ChatID: 11, Text: "fixture"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	mapping, found, err := g.spool.Mapping(job.ID)
	if err != nil || !found {
		t.Fatalf("mapping: %+v %v", mapping, err)
	}
	request := map[string]string{"job_id": job.ID, "conversation_id": mapping.ConversationID, "delivery_id": "tool-" + strings.Repeat("a", 64), "text": "Проверка голоса", "language": "ru"}
	post := func(principal, token string) int {
		body, _ := json.Marshal(request)
		r := httptest.NewRequest(http.MethodPost, "/v1/media/speak", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Hub-Principal", principal)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if status := post("bob", "speech-service"); status != http.StatusForbidden {
		t.Fatalf("sibling accepted: %d", status)
	}
	if status := post("alice", "runtime-token"); status != http.StatusUnauthorized {
		t.Fatalf("runtime bearer accepted: %d", status)
	}
	validID := request["delivery_id"]
	for _, invalid := range []string{"../escape", strings.Repeat("a", 129)} {
		request["delivery_id"] = invalid
		if status := post("alice", "speech-service"); status != http.StatusBadRequest {
			t.Fatalf("invalid delivery id accepted: %d", status)
		}
	}
	request["delivery_id"] = validID
	for i := 0; i < 2; i++ {
		if status := post("alice", "speech-service"); status != http.StatusOK {
			t.Fatalf("enqueue %d: %d", i, status)
		}
	}
	d, err := g.spool.ClaimDelivery()
	if err != nil || d == nil || d.ChatID != 11 || d.OwnerPrincipalID != "alice" || d.Voice != "Проверка голоса" || d.SpeechLang != locale.LangRU || d.Text != "" {
		t.Fatalf("wrong delivery: %+v %v", d, err)
	}
	if duplicate, err := g.spool.ClaimDelivery(); err != nil || duplicate != nil {
		t.Fatalf("duplicate delivery: %+v %v", duplicate, err)
	}
}

func TestMockSpeechRequiresOwnedChatAndCapturesAudio(t *testing.T) {
	g, err := newTestGateway(t, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.config.AutomationAuth = "speech-service"
	g.synthesizer = stubTTS{audio: []byte("ogg-audio")}
	user := g.user("alice")
	job := Job{Envelope: user.MockEnvelope(42), ID: "mock-speech-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "mock", Trigger: "message", IdempotencyKey: "mock-speech-job", ChatID: 42, Text: "fixture"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	mapping, found, err := g.spool.Mapping(job.ID)
	if err != nil || !found {
		t.Fatalf("mapping: %+v %v", mapping, err)
	}
	body, _ := json.Marshal(map[string]string{"job_id": job.ID, "conversation_id": mapping.ConversationID, "delivery_id": "tool-mock-speech", "text": "Проверка голоса", "language": "ru"})
	post := func() int {
		r := httptest.NewRequest(http.MethodPost, "/v1/media/speak", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer speech-service")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if status := post(); status != http.StatusForbidden {
		t.Fatalf("unclaimed mock chat accepted: %d", status)
	}
	if err := g.mockLinkClaim(42, "alice"); err != nil {
		t.Fatal(err)
	}
	if status := post(); status != http.StatusOK {
		t.Fatalf("owned mock chat rejected: %d", status)
	}
	d, err := g.spool.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("claim: %+v %v", d, err)
	}
	if err := g.captureMock(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(g.mockRoot(), "mock-sent", "tool-mock-speech.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Delivery  Delivery `json:"delivery"`
		VoiceData []byte   `json:"voice_data"`
		VoiceMIME string   `json:"voice_mime"`
	}
	if err := json.Unmarshal(b, &rec); err != nil || !rec.Delivery.VoiceSent || rec.VoiceMIME != "audio/ogg" || string(rec.VoiceData) != "ogg-audio" {
		t.Fatalf("mock voice not captured: sent=%t mime=%q bytes=%d err=%v", rec.Delivery.VoiceSent, rec.VoiceMIME, len(rec.VoiceData), err)
	}
	if err := g.captureMock(context.Background(), &Delivery{ID: "foreign", Channel: "mock", ChatID: 42, OwnerPrincipalID: "bob", Voice: "secret"}); err == nil {
		t.Fatal("foreign owner voice captured")
	}
}
