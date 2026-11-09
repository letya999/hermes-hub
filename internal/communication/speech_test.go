package communication

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/locale"
)

func TestSpeechDeliveryBindsJobOwnerAndIsIdempotent(t *testing.T) {
	g, err := New(testConfig(t))
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
