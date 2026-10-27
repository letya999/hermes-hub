package communication

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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
	for _, want := range []string{"demo-skill", "+ new line", "provenance", "agent:alice", "Publish", "Reject"} {
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
