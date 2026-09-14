package communication

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/skills"
)

func TestSlackPostMessageAndGatewayDelivery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" || r.Header.Get("Authorization") != "Bearer xoxb-test" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	api := &slackAPI{token: "xoxb-test", client: server.Client(), baseURL: server.URL}
	if err := api.PostMessage(context.Background(), "D1", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := api.PostMessage(context.Background(), "", "hello"); err == nil {
		t.Fatal("empty channel accepted")
	}
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.slack = api
	if err := g.spool.EnqueueDelivery(Delivery{ID: "slack-out", Channel: "slack_app", ConversationID: "slack-tteam-uuser", DeliveryTargetID: "slack-tteam-uuser", Text: "reply"}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
}

func TestRoutineHTTPCreateUpdateDeleteAndCommands(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	due := time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339)
	handler := g.Handler()
	post := func(path, body, principal string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer control-token")
		req.Header.Set("X-Hub-Principal", principal)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := post("/v1/routines", `{"schedule_id":"http-one","timezone":"UTC","expression":"once:`+due+`","input":"from http"}`, "alice"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "http-one") {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/v1/routines/http-one", `{"expression":"once:`+time.Now().UTC().Add(4*time.Hour).Format(time.RFC3339)+`","input":"later"}`, "alice"); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/v1/routines", `{`, "alice"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", rec.Code)
	}
	del := httptest.NewRequest(http.MethodDelete, "/v1/routines/http-one", nil)
	del.Header.Set("Authorization", "Bearer control-token")
	del.Header.Set("X-Hub-Principal", "alice")
	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, del)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	unauth := httptest.NewRequest(http.MethodGet, "/v1/routines", nil)
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, unauth)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth: %d", denied.Code)
	}
	for _, text := range []string{
		"/routine",
		"/routine list",
		"/routine create second UTC once:" + due + " say later",
		"/routine pause second",
		"/routine delete second",
		"/voice",
		"/voice off",
		"/session",
	} {
		if err := g.handleUpdate(context.Background(), Update{UpdateID: len(text), Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: text}}); err != nil {
			t.Fatal(text, err)
		}
	}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 900, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Photo: []TGMedia{{FileID: "p1", FileSize: 10}}}}); err != nil {
		t.Fatal(err)
	}
}

func TestCronExpressionsAndSlackLinks(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for _, expr := range []string{"0 9 * * 1-5", "*/15 * * * *", "0,30 8-18 * * *", "0 0 1 1 *"} {
		next, err := nextCron(expr, time.UTC, now)
		if err != nil || next.IsZero() {
			t.Fatalf("%s: %v %v", expr, next, err)
		}
	}
	for _, expr := range []string{"*", "a b c d e", "60 * * * *", "* * * * * *"} {
		if _, err := nextCron(expr, time.UTC, now); err == nil {
			t.Fatal("invalid cron accepted", expr)
		}
	}
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11")
	t.Setenv("SLACK_ALLOWED_USERS", "TTEAM/UUSER,bad,T2/U2")
	t.Setenv("SLACK_SIGNING_SECRET", "sign")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb")
	c, err := ConfigFromEnv()
	if err != nil || len(c.Users[0].SlackIDs) != 2 || c.SlackSigningSecret != "sign" {
		t.Fatal(c, err)
	}
	if OccurrenceID(RoutineOccurrence{ScheduleID: "x", Revision: 1, DueAt: now}) == "" {
		t.Fatal("occurrence id")
	}
	if _, err := skills.CopyLimited(bytes.NewReader([]byte("skill"))); err != nil {
		t.Fatal(err)
	}
	hashed := identity.SlackIdentity(strings.Repeat("t", 40), strings.Repeat("u", 40))
	if !identity.ValidID(hashed) || hashed == "slack-"+strings.Repeat("t", 40)+"-"+strings.Repeat("u", 40) {
		t.Fatalf("expected hashed slack id, got %s", hashed)
	}
}

func TestSlackEventsIgnoreBotsAndSecrets(t *testing.T) {
	c := testConfig(t)
	c.SlackSigningSecret = "slack-secret"
	c.Users[0].SlackIDs = []SlackLink{{TeamID: "TTEAM", UserID: "UUSER"}}
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	handler := g.Handler()
	ts := "1700000000"
	post := func(body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/slack/events", bytes.NewReader(body))
		req.Header.Set("X-Slack-Signature", slackSig("slack-secret", ts, body))
		req.Header.Set("X-Slack-Request-Timestamp", ts)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	bot := []byte(`{"type":"event_callback","team_id":"TTEAM","event_id":"EvBot","event":{"type":"message","subtype":"bot_message","user":"UUSER","text":"hi","channel":"D1","channel_type":"im","bot_id":"B1"}}`)
	if rec := post(bot); rec.Code != http.StatusOK {
		t.Fatalf("bot: %d", rec.Code)
	}
	secret := []byte(`{"type":"event_callback","team_id":"TTEAM","event_id":"EvSec","event":{"type":"message","user":"UUSER","text":"OPENAI_API_KEY=sk-test","channel":"D1","channel_type":"im"}}`)
	if rec := post(secret); rec.Code != http.StatusOK {
		t.Fatalf("secret: %d", rec.Code)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/slack/events", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("get: %d", rec.Code)
	}
	if job, _ := g.spool.ClaimJob(); job != nil {
		t.Fatalf("bot/secret created job: %+v", job)
	}
}

func TestRoutineHTTPItemErrorsAndVoiceDownloadFailure(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	handler := g.Handler()
	req := httptest.NewRequest(http.MethodDelete, "/v1/routines/", nil)
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id: %d", rec.Code)
	}
	pause := httptest.NewRequest(http.MethodPost, "/v1/routines/missing/pause", nil)
	pause.Header.Set("Authorization", "Bearer control-token")
	pause.Header.Set("X-Hub-Principal", "alice")
	paused := httptest.NewRecorder()
	handler.ServeHTTP(paused, pause)
	if paused.Code != http.StatusForbidden {
		t.Fatalf("missing pause: %d", paused.Code)
	}
	put := httptest.NewRequest(http.MethodPut, "/v1/routines", nil)
	put.Header.Set("Authorization", "Bearer control-token")
	put.Header.Set("X-Hub-Principal", "alice")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, put)
	if denied.Code != http.StatusMethodNotAllowed {
		t.Fatalf("put: %d", denied.Code)
	}
	g.api = &fakeAPI{}
	g.transcriber = nil
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 70, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Voice: &TGMedia{FileID: "Aw2", MimeType: "audio/ogg", FileSize: 12, Duration: 1}}}); err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{err: errors.New("telegram down")}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 71, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Voice: &TGMedia{FileID: "Aw3", MimeType: "audio/ogg", FileSize: 12, Duration: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := nextCron("1-5/2 * * * *", time.UTC, time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

func TestTickSchedulesExpiredWindowExistingOccurrenceAndSlackUser(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	g.users = map[int64]User{11: {ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	caller := o.Job.Envelope
	created, err := s.CreateSchedule(Schedule{ScheduleID: "expire-me", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "later"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(created.NextDue.Add(2*time.Hour), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("expired catch-up wrote occurrence: %v %v", entries, err)
	}
	live, err := s.CreateSchedule(Schedule{ScheduleID: "exists", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "exists"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	occ := RoutineOccurrence{ScheduleID: live.ScheduleID, Revision: live.Revision, DueAt: live.NextDue, Job: Job{Envelope: caller, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Channel: "telegram_bot", ChatID: 11, Text: "exists"}}
	if err := os.WriteFile(filepath.Join(s.root, "occurrences", occurrenceID(occ)+".json"), []byte(`{"state":"pending"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(live.NextDue.Add(time.Second), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.CreateSchedule(Schedule{ScheduleID: "blocked", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "blocked"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(blocked.NextDue.Add(time.Second), "", func(Job) error { return errors.New("blocked") }); err != nil {
		t.Fatal(err)
	}
	c := testConfig(t)
	c.ControlAuth = "control-token"
	slackState := filepath.Join(t.TempDir(), "carol", "state")
	slackWS := filepath.Join(t.TempDir(), "carol", "workspace")
	if err := os.MkdirAll(slackState, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(slackWS, 0700); err != nil {
		t.Fatal(err)
	}
	c.Users = append(c.Users, User{ID: "carol", Enabled: true, SlackIDs: []SlackLink{{TeamID: "TTEAM", UserID: "UUSER"}}, StateDir: slackState, WorkspaceDir: slackWS})
	gw, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/routines", nil)
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "carol")
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("slack user control: %d %s", rec.Code, rec.Body.String())
	}
	patch := httptest.NewRequest(http.MethodPost, "/v1/routines/missing", strings.NewReader(`{`))
	patch.Header.Set("Authorization", "Bearer control-token")
	patch.Header.Set("X-Hub-Principal", "alice")
	bad := httptest.NewRecorder()
	gw.Handler().ServeHTTP(bad, patch)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad patch: %d", bad.Code)
	}
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer fail.Close()
	if err := (&slackAPI{token: "x", client: fail.Client(), baseURL: fail.URL}).PostMessage(context.Background(), "D1", "x"); err == nil {
		t.Fatal("failed slack delivery accepted")
	}
	gw.api = &fakeAPI{fileSize: mediaSizeLimit + 1}
	gw.transcriber = stubSTT{text: "x"}
	if err := gw.handleUpdate(context.Background(), Update{UpdateID: 77, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Voice: &TGMedia{FileID: "big", MimeType: "audio/ogg", FileSize: 12, Duration: 1}}}); err != nil {
		t.Fatal(err)
	}
	c.STTCommand = "false"
	c.TTSCommand = "false"
	if _, err := New(c); err != nil {
		t.Fatal(err)
	}
}

func TestMediaAudioCaptionLimitsAndHTTPErrorBranches(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	g.transcriber = stubSTT{text: "from-audio"}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 201, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Caption: "note", Audio: &TGMedia{FileID: "A1", MimeType: "audio/mpeg", FileSize: 12, Duration: 2}}}); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || !strings.Contains(job.Text, "from-audio") || !strings.Contains(job.Text, "note") {
		t.Fatalf("audio caption: %+v %v", job, err)
	}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 202, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Document: &TGMedia{FileID: "big", FileName: "big.bin", FileSize: mediaSizeLimit + 2}}}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 203, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Voice: &TGMedia{FileID: "v", MimeType: "video/unknown", FileSize: 12, Duration: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := rejectMedia(MediaEnvelope{}); err == nil {
		t.Fatal("empty media accepted")
	}
	if _, err := (CommandTranscriber{Command: "false", Timeout: time.Nanosecond}).Transcribe(context.Background(), "x", "audio/ogg"); err == nil {
		t.Fatal("stt timeout accepted")
	}
	if _, _, err := (CommandSynthesizer{Command: "false"}).Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("empty tts accepted")
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/routines", nil)
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-User", "alice")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("x-hub-user: %d", rec.Code)
	}
	g.config.NativeCron = "unmigrated"
	due := time.Now().UTC().Add(5 * time.Hour).Format(time.RFC3339)
	create := httptest.NewRequest(http.MethodPost, "/v1/routines", strings.NewReader(`{"schedule_id":"nope","timezone":"UTC","expression":"once:`+due+`","input":"x"}`))
	create.Header.Set("Authorization", "Bearer control-token")
	create.Header.Set("X-Hub-Principal", "alice")
	create.Header.Set("Content-Type", "application/json")
	denied := httptest.NewRecorder()
	g.Handler().ServeHTTP(denied, create)
	if denied.Code != http.StatusBadRequest {
		t.Fatalf("native cron create: %d %s", denied.Code, denied.Body.String())
	}
	item := httptest.NewRequest(http.MethodGet, "/v1/routines/nope", nil)
	item.Header.Set("Authorization", "Bearer control-token")
	item.Header.Set("X-Hub-Principal", "alice")
	itemRec := httptest.NewRecorder()
	g.Handler().ServeHTTP(itemRec, item)
	if itemRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("get item: %d", itemRec.Code)
	}
	upd := httptest.NewRequest(http.MethodPost, "/v1/routines/missing", strings.NewReader(`{"input":"later"}`))
	upd.Header.Set("Authorization", "Bearer control-token")
	upd.Header.Set("X-Hub-Principal", "alice")
	upd.Header.Set("Content-Type", "application/json")
	updRec := httptest.NewRecorder()
	g.Handler().ServeHTTP(updRec, upd)
	if updRec.Code != http.StatusForbidden {
		t.Fatalf("update missing: %d", updRec.Code)
	}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 204, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "/routine pause missing"}}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 205, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "/routine delete missing"}}); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleValidateAndNativeTickSkip(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "bad id", Envelope: caller, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "x"}, caller, ""); err == nil {
		t.Fatal("invalid id accepted")
	}
	if err := s.TickSchedules(time.Now().UTC(), "unmigrated", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.MkdirAll(home+"/hermes/skills", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := skills.Install(home, "notes", "", "user", []byte("# Notes\nfirst"), false, caller); err == nil {
		t.Fatal("consent skipped")
	}
	first, err := skills.Install(home, "notes", "https://example.invalid/notes", "user", []byte("# Notes\nfirst"), true, caller)
	if err != nil {
		t.Fatal(err)
	}
	second, err := skills.Install(home, "notes", "https://example.invalid/notes", "user", []byte("# Notes\nsecond"), true, caller)
	if err != nil || second.EnabledRevision != first.EnabledRevision+1 || second.RollbackRevision != first.EnabledRevision {
		t.Fatal(second, err)
	}
}
