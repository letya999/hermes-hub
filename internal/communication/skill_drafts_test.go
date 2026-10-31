package communication

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
)

// SPEC-0083 R5.3: every skill_manage write mints a protected confirmation
// card; publication only happens through the owner's click on the loopback
// page, which relays to the runtime /v1/skill-apply — never inside the tool.
func mintSkillDraft(t *testing.T, g *Gateway) string {
	t.Helper()
	body := `{"draft_id":"d1","skill_name":"demo-skill","scope":"user","summary":"create skill","diff":"+ new line","findings":"none","provenance":"agent:alice","base_revision":"rev0"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/skill-drafts", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, req)
	if result.Code != http.StatusOK {
		t.Fatalf("skill-drafts mint status=%d body=%s", result.Code, result.Body.String())
	}
	var response struct {
		FormURL string `json:"form_url"`
	}
	if json.Unmarshal(result.Body.Bytes(), &response) != nil || !strings.Contains(response.FormURL, "/skill-drafts/") || !strings.Contains(response.FormURL, "nonce=") {
		t.Fatalf("invalid mint response: %s", result.Body.String())
	}
	return response.FormURL
}

func draftRequest(t *testing.T, method, rawURL string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, parsed.Path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, rawURL, nil)
	}
	req.Host = parsed.Host
	result := httptest.NewRecorder()
	return result
}

func TestSkillDraftCardFlow(t *testing.T) {
	var applyCalls []map[string]any
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/skill-apply" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		applyCalls = append(applyCalls, payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer runtimeAPI.Close()
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.FormOrigin = "http://127.0.0.1:8081"
	c.RuntimeURL = runtimeAPI.URL
	c.RuntimeAuth = "runtime-secret"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	formURL := mintSkillDraft(t, g)

	// Card shows summary, diff, findings and provenance.
	req := httptest.NewRequest(http.MethodGet, formURL, nil)
	req.Host = "127.0.0.1:8081"
	card := httptest.NewRecorder()
	g.Handler().ServeHTTP(card, req)
	if card.Code != http.StatusOK {
		t.Fatalf("card status=%d", card.Code)
	}
	// The card follows the owner's pinned language — testConfig chooses ru.
	if !strings.Contains(card.Body.String(), `<html lang="ru"`) {
		t.Fatalf("card missing lang attr:\n%s", card.Body.String())
	}
	for _, want := range []string{"demo-skill", "+ new line", "agent:alice", "Происхождение", "Опубликовать", "Отклонить"} {
		if !strings.Contains(card.Body.String(), want) {
			t.Fatalf("card missing %q:\n%s", want, card.Body.String())
		}
	}

	// Wrong nonce is unauthorized.
	bad := httptest.NewRequest(http.MethodGet, strings.Replace(formURL, "nonce=", "nonce=bad", 1), nil)
	bad.Host = "127.0.0.1:8081"
	badResult := httptest.NewRecorder()
	g.Handler().ServeHTTP(badResult, bad)
	if badResult.Code != http.StatusUnauthorized && badResult.Code != http.StatusNotFound {
		t.Fatalf("bad nonce status=%d", badResult.Code)
	}

	// Publish relays to the runtime and re-arms an undo.
	publish := draftRequest(t, http.MethodPost, formURL+"&action=publish", nil)
	pubResult := httptest.NewRecorder()
	parsed, _ := url.Parse(formURL)
	post := httptest.NewRequest(http.MethodPost, parsed.Path+"?"+parsed.RawQuery+"&action=publish", nil)
	post.Host = "127.0.0.1:8081"
	g.Handler().ServeHTTP(pubResult, post)
	_ = publish
	if pubResult.Code != http.StatusOK || len(applyCalls) != 1 || applyCalls[0]["draft_id"] != "d1" {
		t.Fatalf("publish status=%d apply=%v", pubResult.Code, applyCalls)
	}
	// The apply relays the owner's routing envelope so the supervisor can
	// resolve the owning runtime (managed principals are unreachable directly).
	for key, want := range map[string]string{"principal_id": "alice", "organization_id": "personal", "user_id": "alice", "actor_id": "alice", "scope_id": "user:alice"} {
		if applyCalls[0][key] != want {
			t.Fatalf("apply missing routing field %s=%v in %v", key, applyCalls[0][key], applyCalls[0])
		}
	}
	if !strings.Contains(pubResult.Body.String(), "undo") && !strings.Contains(pubResult.Body.String(), "Откат") {
		t.Fatalf("publish result must re-arm undo:\n%s", pubResult.Body.String())
	}

	// Replay of the same publish action fails.
	replay := httptest.NewRequest(http.MethodPost, parsed.Path+"?"+parsed.RawQuery+"&action=publish", nil)
	replay.Host = "127.0.0.1:8081"
	replayResult := httptest.NewRecorder()
	g.Handler().ServeHTTP(replayResult, replay)
	if replayResult.Code == http.StatusOK || len(applyCalls) != 1 {
		t.Fatalf("replay status=%d apply=%v", replayResult.Code, applyCalls)
	}

	// The re-armed undo link carries a fresh nonce; posting it calls the
	// runtime rollback and the old publish nonce is dead.
	page := pubResult.Body.String()
	marker := "nonce="
	at := strings.Index(page, marker+"")
	if at < 0 {
		t.Fatalf("publish page must carry a fresh undo nonce:\n%s", page)
	}
	freshNonce := page[at+len(marker):]
	if cut := strings.IndexAny(freshNonce, `&"' `); cut >= 0 {
		freshNonce = freshNonce[:cut]
	}
	if !strings.Contains(page, "nonce="+freshNonce+"&action=undo") {
		t.Fatalf("no undo form with fresh nonce:\n%s", page)
	}
	undo := httptest.NewRequest(http.MethodPost, parsed.Path+"?nonce="+freshNonce+"&action=undo", nil)
	undo.Host = "127.0.0.1:8081"
	undoResult := httptest.NewRecorder()
	g.Handler().ServeHTTP(undoResult, undo)
	if undoResult.Code != http.StatusOK || len(applyCalls) != 2 || applyCalls[1]["action"] != "rollback" {
		t.Fatalf("undo status=%d apply=%v", undoResult.Code, applyCalls)
	}
	// Undo is single-use as well.
	undoAgain := httptest.NewRequest(http.MethodPost, parsed.Path+"?nonce="+freshNonce+"&action=undo", nil)
	undoAgain.Host = "127.0.0.1:8081"
	undoAgainResult := httptest.NewRecorder()
	g.Handler().ServeHTTP(undoAgainResult, undoAgain)
	if undoAgainResult.Code == http.StatusOK || len(applyCalls) != 2 {
		t.Fatalf("undo replay status=%d apply=%v", undoAgainResult.Code, applyCalls)
	}
}

// Cards lived only in memory — every gateway restart murdered every
// pending approval link and the owner's click landed on a bare 401.
// The registry must survive a restart (the spool dir is persistent) so a
// card minted before the restart still resolves.
func TestSkillDraftCardsSurviveRestart(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.FormOrigin = "http://127.0.0.1:8081"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	formURL := mintSkillDraft(t, g)

	// A new Gateway over the same spool is a restart — the card must resolve.
	restarted, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, formURL, nil)
	req.Host = "127.0.0.1:8081"
	card := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(card, req)
	if card.Code != http.StatusOK {
		t.Fatalf("card after restart status=%d body=%s", card.Code, card.Body.String())
	}
	if !strings.Contains(card.Body.String(), "demo-skill") {
		t.Fatalf("restored card lost its content:\n%s", card.Body.String())
	}
}

// Publish rotates the nonce and marks the card handled — both must survive
// a restart, or the spent publish link revives and the undo link dies.
func TestSkillDraftPublishStateSurvivesRestart(t *testing.T) {
	var applyCalls []map[string]any
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		applyCalls = append(applyCalls, payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer runtimeAPI.Close()
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.FormOrigin = "http://127.0.0.1:8081"
	c.RuntimeURL = runtimeAPI.URL
	c.RuntimeAuth = "runtime-secret"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	formURL := mintSkillDraft(t, g)
	parsed, _ := url.Parse(formURL)
	post := httptest.NewRequest(http.MethodPost, parsed.Path+"?"+parsed.RawQuery+"&action=publish", nil)
	post.Host = "127.0.0.1:8081"
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, post)
	if result.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", result.Code, result.Body.String())
	}

	restarted, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	// The spent publish nonce must stay dead across the restart.
	replay := httptest.NewRequest(http.MethodGet, formURL, nil)
	replay.Host = "127.0.0.1:8081"
	replayResult := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(replayResult, replay)
	if replayResult.Code != http.StatusUnauthorized {
		t.Fatalf("spent card after restart status=%d", replayResult.Code)
	}
	// The undo link minted by the publish must still resolve.
	undoURL := extractUndoURL(t, parsed.Path, result.Body.String())
	undoReq := httptest.NewRequest(http.MethodGet, undoURL, nil)
	undoReq.Host = "127.0.0.1:8081"
	undoCard := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(undoCard, undoReq)
	if undoCard.Code != http.StatusOK {
		t.Fatalf("undo card after restart status=%d", undoCard.Code)
	}
}

// The publish response renders an undo form with a relative action
// (?nonce=<fresh>&action=undo) — rebuild the card URL with that query.
func extractUndoURL(t *testing.T, cardPath, body string) string {
	t.Helper()
	idx := strings.Index(body, `action="?nonce=`)
	if idx < 0 {
		t.Fatalf("publish response missing undo form:\n%s", body)
	}
	start := idx + len(`action="`)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		t.Fatalf("undo form unterminated:\n%s", body)
	}
	return cardPath + body[start:start+end]
}

// Drafts on disk never expire, so a 30-minute card window only forced
// owners to re-issue installs. The link must stay usable across days, not
// minutes — the nonce remains the sole bearer secret.
func TestSkillDraftCardLifetimeCoversDays(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.FormOrigin = "http://127.0.0.1:8081"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	formURL := mintSkillDraft(t, g)
	now := time.Now()
	g.now = func() time.Time { return now.Add(48 * time.Hour) }
	req := httptest.NewRequest(http.MethodGet, formURL, nil)
	req.Host = "127.0.0.1:8081"
	card := httptest.NewRecorder()
	g.Handler().ServeHTTP(card, req)
	if card.Code != http.StatusOK {
		t.Fatalf("48h-old card status=%d — the link must outlive a workday", card.Code)
	}
}

// The publish relay used to hide the runtime's rejection behind a bare
// "apply failed" — unfixable for the owner (a frontmatter name mismatch
// looks identical to a network blip). The card must carry the real reason.
func TestSkillDraftPublishSurfacesRuntimeError(t *testing.T) {
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": `frontmatter name "book-ingester" must equal directory "ingester"`})
	}))
	defer runtimeAPI.Close()
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.FormOrigin = "http://127.0.0.1:8081"
	c.RuntimeURL = runtimeAPI.URL
	c.RuntimeAuth = "runtime-secret"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	formURL := mintSkillDraft(t, g)
	parsed, _ := url.Parse(formURL)
	post := httptest.NewRequest(http.MethodPost, parsed.Path+"?"+parsed.RawQuery+"&action=publish", nil)
	post.Host = "127.0.0.1:8081"
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, post)
	if result.Code != http.StatusBadGateway {
		t.Fatalf("publish status=%d body=%s", result.Code, result.Body.String())
	}
	if !strings.Contains(result.Body.String(), "book-ingester") {
		t.Fatalf("apply failure must carry the runtime's reason: %s", result.Body.String())
	}
}

// The rollback relay must carry the runtime's reason just like publish —
// a bare "rollback failed" leaves the owner blind.
func TestSkillDraftUndoSurfacesRuntimeError(t *testing.T) {
	var calls int
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no such revision"})
	}))
	defer runtimeAPI.Close()
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.FormOrigin = "http://127.0.0.1:8081"
	c.RuntimeURL = runtimeAPI.URL
	c.RuntimeAuth = "runtime-secret"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	formURL := mintSkillDraft(t, g)
	parsed, _ := url.Parse(formURL)
	post := httptest.NewRequest(http.MethodPost, parsed.Path+"?"+parsed.RawQuery+"&action=publish", nil)
	post.Host = "127.0.0.1:8081"
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, post)
	if result.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", result.Code, result.Body.String())
	}
	undoURL := extractUndoURL(t, parsed.Path, result.Body.String())
	undo := httptest.NewRequest(http.MethodPost, undoURL, nil)
	undo.Host = "127.0.0.1:8081"
	undoResult := httptest.NewRecorder()
	g.Handler().ServeHTTP(undoResult, undo)
	if undoResult.Code != http.StatusBadGateway {
		t.Fatalf("undo status=%d body=%s", undoResult.Code, undoResult.Body.String())
	}
	if !strings.Contains(undoResult.Body.String(), "no such revision") {
		t.Fatalf("undo failure must carry the runtime's reason: %s", undoResult.Body.String())
	}
}

// A cold supervisor spawn (~15s+) exceeds the apply client's budget, so the
// publish times out at the client while the draft is still applied upstream —
// the card then reports failure for a succeeded publish. The timeout must
// cover spawn+apply, and tests shrink the var to prove the client honors it.
func TestSkillApplyTimeoutCoversSpawn(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	c := testConfig(t)
	c.RuntimeURL = slow.URL
	c.RuntimeAuth = "runtime-secret"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	prev := skillApplyTimeout
	skillApplyTimeout = 30 * time.Millisecond
	defer func() { skillApplyTimeout = prev }()
	draft := skillDraft{Envelope: identity.Envelope{PrincipalID: "alice"}, DraftID: "d1", SkillName: "demo", OwnerID: "alice"}
	if err := g.runtimeSkillApply(httptest.NewRequest(http.MethodPost, "/", nil), draft, "publish"); err == nil {
		t.Fatal("apply client must honor the configured timeout")
	}
	if prev < 2*time.Minute {
		t.Fatalf("production timeout %v cannot cover a cold runtime spawn", prev)
	}
}

// The card page renders in the owner's stored preference, not the hub
// default — the same ladder the credential forms use.
func TestSkillDraftCardLocalizes(t *testing.T) {
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer runtimeAPI.Close()
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.FormOrigin = "http://127.0.0.1:8081"
	c.RuntimeURL = runtimeAPI.URL
	c.RuntimeAuth = "runtime-secret"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	formURL := mintSkillDraft(t, g)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangEN)
	req := httptest.NewRequest(http.MethodGet, formURL, nil)
	req.Host = "127.0.0.1:8081"
	card := httptest.NewRecorder()
	g.Handler().ServeHTTP(card, req)
	for _, want := range []string{`<html lang="en"`, "Skill draft card", "Skill:", "Scope:", "Change:", "Check results:", "Provenance:", "Publish", "Reject"} {
		if !strings.Contains(card.Body.String(), want) {
			t.Fatalf("en card missing %q:\n%s", want, card.Body.String())
		}
	}
	// Publish, handled and reject result pages render in the same language.
	parsed, _ := url.Parse(formURL)
	pub := httptest.NewRequest(http.MethodPost, parsed.Path+"?"+parsed.RawQuery+"&action=publish", nil)
	pub.Host = "127.0.0.1:8081"
	pubResult := httptest.NewRecorder()
	g.Handler().ServeHTTP(pubResult, pub)
	for _, want := range []string{`<html lang="en"`, "Published", "published", "Roll back"} {
		if !strings.Contains(pubResult.Body.String(), want) {
			t.Fatalf("en publish page missing %q:\n%s", want, pubResult.Body.String())
		}
	}
	page := pubResult.Body.String()
	freshNonce := page[strings.Index(page, "nonce=")+len("nonce="):]
	if cut := strings.IndexAny(freshNonce, `&"' `); cut >= 0 {
		freshNonce = freshNonce[:cut]
	}
	undo := httptest.NewRequest(http.MethodPost, parsed.Path+"?nonce="+freshNonce+"&action=undo", nil)
	undo.Host = "127.0.0.1:8081"
	undoResult := httptest.NewRecorder()
	g.Handler().ServeHTTP(undoResult, undo)
	for _, want := range []string{`<html lang="en"`, "Rolled back", "previous revision"} {
		if !strings.Contains(undoResult.Body.String(), want) {
			t.Fatalf("en undo page missing %q:\n%s", want, undoResult.Body.String())
		}
	}
	formURL2 := mintSkillDraft(t, g)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	card2 := httptest.NewRecorder()
	g.Handler().ServeHTTP(card2, httptest.NewRequest(http.MethodGet, formURL2, nil))
	for _, want := range []string{`<html lang="es"`, "Borrador de skill", "Skill:", "Ámbito:", "Cambio:", "Procedencia:", "Publicar", "Rechazar"} {
		if !strings.Contains(card2.Body.String(), want) {
			t.Fatalf("es card missing %q:\n%s", want, card2.Body.String())
		}
	}
	parsed2, _ := url.Parse(formURL2)
	rej := httptest.NewRequest(http.MethodPost, parsed2.Path+"?"+parsed2.RawQuery+"&action=reject", nil)
	rej.Host = "127.0.0.1:8081"
	rejResult := httptest.NewRecorder()
	g.Handler().ServeHTTP(rejResult, rej)
	for _, want := range []string{`<html lang="es"`, "Rechazado", "rechazado"} {
		if !strings.Contains(rejResult.Body.String(), want) {
			t.Fatalf("es reject page missing %q:\n%s", want, rejResult.Body.String())
		}
	}
	// A handled draft keeps its nonce — revisiting shows the handled page in
	// the stored language.
	handled := httptest.NewRecorder()
	g.Handler().ServeHTTP(handled, httptest.NewRequest(http.MethodGet, formURL2, nil))
	for _, want := range []string{`<html lang="es"`, "Ya procesado", "ya está procesado"} {
		if !strings.Contains(handled.Body.String(), want) {
			t.Fatalf("es handled page missing %q:\n%s", want, handled.Body.String())
		}
	}
}

func TestSkillDraftRejectConsumesAttempt(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.FormOrigin = "http://127.0.0.1:8081"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	formURL := mintSkillDraft(t, g)
	parsed, _ := url.Parse(formURL)
	reject := httptest.NewRequest(http.MethodPost, parsed.Path+"?"+parsed.RawQuery+"&action=reject", nil)
	reject.Host = "127.0.0.1:8081"
	result := httptest.NewRecorder()
	g.Handler().ServeHTTP(result, reject)
	if result.Code != http.StatusOK {
		t.Fatalf("reject status=%d", result.Code)
	}
	again := httptest.NewRequest(http.MethodPost, parsed.Path+"?"+parsed.RawQuery+"&action=reject", nil)
	again.Host = "127.0.0.1:8081"
	againResult := httptest.NewRecorder()
	g.Handler().ServeHTTP(againResult, again)
	if againResult.Code == http.StatusOK {
		t.Fatal("rejected draft accepted a second action")
	}
}
