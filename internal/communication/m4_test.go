package communication

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
)

type stubSTT struct {
	text string
	err  error
}

func (s stubSTT) Transcribe(context.Context, string, string) (string, error) {
	return s.text, s.err
}

type stubTTS struct {
	audio []byte
	err   error
}

func (s stubTTS) Synthesize(context.Context, string) ([]byte, string, error) {
	return s.audio, "audio/ogg", s.err
}

func TestHandleUpdateIsolationCommandsFilesAndIdempotency(t *testing.T) {
	c := testConfig(t)
	bobState := filepath.Join(t.TempDir(), "bob", "state")
	bobWS := filepath.Join(t.TempDir(), "bob", "workspace")
	if err := os.MkdirAll(filepath.Join(bobState, "hermes", "memories"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bobWS, 0700); err != nil {
		t.Fatal(err)
	}
	secret := "bob-private-memory"
	if err := os.WriteFile(filepath.Join(bobState, "hermes", "memories", "USER.md"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	c.Users = append(c.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: bobState, WorkspaceDir: bobWS})
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	priv := func(id, msg int, sender int64, text string) Update {
		return Update{UpdateID: id, Message: &Message{MessageID: msg, From: &TGUser{ID: sender}, Chat: TGChat{ID: sender, Type: "private"}, Text: text}}
	}
	for _, u := range []Update{
		priv(1, 1, 11, "/start"),
		priv(2, 2, 11, "/status"),
		priv(3, 3, 11, "/connections"),
		priv(4, 4, 11, "/connection"),
		priv(5, 5, 11, "/session"),
		priv(6, 6, 11, "/voice"),
		priv(7, 7, 11, "/voice on"),
		priv(8, 8, 11, "/approve job-x req yes"),
		priv(15, 15, 11, "/cancel job-x"),
		priv(9, 9, 11, "hello alice"),
		priv(9, 9, 11, "hello alice"),
		priv(10, 10, 22, "hello bob"),
		priv(11, 11, 99, "intruder"),
		{UpdateID: 12, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 99, Type: "group"}, Text: "group hello"}},
		{UpdateID: 13, EditedMessage: &Message{MessageID: 9, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "edited"}},
		{UpdateID: 14, Message: &Message{MessageID: 14, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Caption: "notes", Document: &TGMedia{FileID: "doc1", FileName: "notes.txt", MimeType: "text/plain", FileSize: 12}}},
	} {
		if err := g.handleUpdate(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	var jobs []Job
	for {
		job, err := g.spool.ClaimJob()
		if err != nil {
			t.Fatal(err)
		}
		if job == nil {
			break
		}
		jobs = append(jobs, *job)
		if strings.Contains(job.Text, secret) || job.UserID == "alice" && strings.Contains(job.Text, "bob") {
			t.Fatalf("cross-home leak: %+v", job)
		}
		_ = g.spool.CompleteJob(job.ID)
	}
	if len(jobs) != 3 {
		t.Fatalf("jobs=%d %+v", len(jobs), jobs)
	}
	seen := map[string]int{}
	for _, job := range jobs {
		seen[job.UserID]++
		if job.UserID == "alice" && job.RuntimeID != "alice" {
			t.Fatalf("alice job used wrong home: %+v", job)
		}
		if job.UserID == "bob" && (job.PrincipalID != "bob" || strings.Contains(job.Text, "alice")) {
			t.Fatalf("bob job: %+v", job)
		}
	}
	if seen["alice"] != 2 || seen["bob"] != 1 {
		t.Fatalf("seen=%v", seen)
	}
	for range 20 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	if strings.Contains(joined, secret) || !strings.Contains(joined, "не настроен") || !strings.Contains(joined, "Голосовые ответы включены") {
		t.Fatalf("sent=%v", fake.sent)
	}
	env := strings.Join(processEnv(map[string]string{"TELEGRAM_BOT_TOKEN": "bot-secret", "SLACK_SIGNING_SECRET": "slack-secret", "OPENAI_API_KEY": "k"}), "\n")
	if strings.Contains(env, "TELEGRAM_BOT_TOKEN") || strings.Contains(env, "SLACK_SIGNING_SECRET") {
		t.Fatal("bot/app tokens leaked into runtime env")
	}
}

func TestEmptyVoiceUpdateIsTranscribedNotDropped(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	g.transcriber = stubSTT{text: "voice transcript"}
	update := Update{UpdateID: 50, Message: &Message{MessageID: 3, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Voice: &TGMedia{FileID: "Aw1", MimeType: "audio/ogg", FileSize: 12, Duration: 2}}}
	if err := g.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || !strings.Contains(job.Text, "voice transcript") {
		t.Fatalf("voice dropped: %+v %v", job, err)
	}
	tmp, _ := os.ReadDir(filepath.Join(g.spool.root, "tmp"))
	if len(tmp) != 0 {
		t.Fatalf("temp files remained: %v", tmp)
	}
}

func TestVoiceLimitsAndSTTErrorStayText(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	g.transcriber = stubSTT{err: errors.New("stt failed")}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 51, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Voice: &TGMedia{FileID: "x", MimeType: "audio/ogg", FileSize: 12, Duration: 1}}}); err != nil {
		t.Fatal(err)
	}
	if job, _ := g.spool.ClaimJob(); job != nil {
		t.Fatal("stt error enqueued a job")
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "распознать") {
		t.Fatalf("sent=%v", fake.sent)
	}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 52, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Voice: &TGMedia{FileID: "big", MimeType: "audio/ogg", FileSize: mediaSizeLimit + 1, Duration: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 53, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Voice: &TGMedia{FileID: "long", MimeType: "video/mp4", FileSize: 12, Duration: mediaDurationLimit + 1}}}); err != nil {
		t.Fatal(err)
	}
}

func TestTTSOptInFallbackAndNoDefaultVoice(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	g.synthesizer = stubTTS{err: errors.New("tts down")}
	if err := g.spool.EnqueueDelivery(Delivery{ID: "job-text-response", JobID: "job-text", Channel: "telegram_bot", ChatID: 11, Text: "canonical"}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 || fake.sent[0] != "canonical" {
		t.Fatalf("text default broken: %v", fake.sent)
	}
	if err := g.spool.SetConversationVoice("alice", "alice", "telegram-11", true); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.EnqueueDelivery(Delivery{ID: "job-voice-response", JobID: "job-voice", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "spoken"}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 2 || fake.sent[1] != "spoken" {
		t.Fatalf("tts failure must still deliver text: %v", fake.sent)
	}
	if len(fake.voices) != 0 {
		t.Fatalf("failed tts still uploaded voice: %v", fake.voices)
	}
	g.synthesizer = stubTTS{audio: []byte("ogg-bytes")}
	if err := g.spool.EnqueueDelivery(Delivery{ID: "job-voice-ok", JobID: "job-voice-ok", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "spoken-ok"}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.voices) != 1 || fake.voices[0] != "ogg-bytes" || fake.sent[len(fake.sent)-1] != "spoken-ok" {
		t.Fatalf("opt-in tts did not add voice after text: sent=%v voices=%v", fake.sent, fake.voices)
	}
	g.config.TTSUploadURL = "https://example.invalid/upload"
	if err := g.spool.EnqueueDelivery(Delivery{ID: "job-voice-upload", JobID: "job-voice-upload", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "no-upload"}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.voices) != 1 {
		t.Fatalf("third-party upload path must not send voice locally: %v", fake.voices)
	}
}

func slackSig(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + ts + ":"))
	_, _ = mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestSlackEventsSignatureMappingDedupAndAudience(t *testing.T) {
	c := testConfig(t)
	c.SlackSigningSecret = "slack-secret"
	c.SlackBotToken = "xoxb-test"
	c.ControlAuth = "control-token"
	c.Users[0].SlackIDs = []SlackLink{{TeamID: "TTEAM", UserID: "UUSER"}}
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	handler := g.Handler()
	event := []byte(`{"type":"event_callback","team_id":"TTEAM","event_id":"Ev1","event":{"type":"message","user":"UUSER","text":"hello slack","channel":"D1","channel_type":"im","user_profile":{"email":"other@example.com","display_name":"Other Person"}}}`)
	post := func(body []byte, sig, ts string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/slack/events", bytes.NewReader(body))
		req.Header.Set("X-Slack-Signature", sig)
		req.Header.Set("X-Slack-Request-Timestamp", ts)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	ts := strconv.FormatInt(g.now().Unix(), 10)
	if rec := post(event, slackSig("wrong", ts, event), ts); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature accepted: %d", rec.Code)
	}
	if rec := post(event, "", ts); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature accepted: %d", rec.Code)
	}
	if rec := post(event, slackSig(c.SlackSigningSecret, "1", event), "1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale signature accepted: %d", rec.Code)
	}
	unmapped := []byte(`{"type":"event_callback","team_id":"TTEAM","event_id":"EvMail","event":{"type":"message","user":"UOTHER","text":"hello","channel":"D2","channel_type":"im","user_profile":{"email":"alice@example.com","display_name":"Alice"}}}`)
	if rec := post(unmapped, slackSig(c.SlackSigningSecret, ts, unmapped), ts); rec.Code != http.StatusForbidden {
		t.Fatalf("email/display name selected a slack principal: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(event, slackSig(c.SlackSigningSecret, ts, event), ts); rec.Code != http.StatusOK {
		t.Fatalf("valid signature rejected: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(event, slackSig(c.SlackSigningSecret, ts, event), ts); rec.Code != http.StatusOK {
		t.Fatal("duplicate event rejected instead of deduped")
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || job.Channel != "slack_app" || job.PrincipalID != "alice" || job.ExternalIdentityID != identity.SlackIdentity("TTEAM", "UUSER") {
		t.Fatalf("slack job: %+v %v", job, err)
	}
	if extra, _ := g.spool.ClaimJob(); extra != nil {
		t.Fatal("duplicate slack event created a second job")
	}
	channel := []byte(`{"type":"event_callback","team_id":"TTEAM","event_id":"Ev2","event":{"type":"message","user":"UUSER","text":"hello","channel":"C1","channel_type":"channel"}}`)
	if rec := post(channel, slackSig(c.SlackSigningSecret, ts, channel), ts); rec.Code != http.StatusForbidden {
		t.Fatalf("unmapped channel allowed: %d", rec.Code)
	}
	cfg := stack.Config(stack.Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", Features: []string{"workspace", "slack_app"}})
	servers := cfg["mcp_servers"].(stack.M)
	if _, ok := servers["slack"]; ok {
		t.Fatal("slack_app communication permission created Slack data tools")
	}
}

func TestRoutineCRUDOwnershipAndCatchup(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	g.users = map[int64]User{11: {ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	caller := o.Job.Envelope
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	created, err := s.CreateSchedule(Schedule{ScheduleID: "morning-brief", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "once:" + due.Format(time.RFC3339), Input: "brief me"}, caller, "")
	if err != nil || created.Revision != 1 {
		t.Fatal(created, err)
	}
	bob := identity.TelegramEnvelope("bob", 22, "bob", "policy-1")
	if _, err := s.ListSchedules(bob); err != nil {
		t.Fatal(err)
	}
	listed, _ := s.ListSchedules(bob)
	if len(listed) != 0 {
		t.Fatal("bob listed alice routines")
	}
	if err := s.DeleteSchedule("morning-brief", bob); err == nil {
		t.Fatal("bob deleted alice routine")
	}
	if err := s.PauseSchedule("morning-brief", caller); err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(due.Add(time.Second), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, _ := s.ClaimJob(); job != nil {
		t.Fatal("paused schedule ticked")
	}
	enabled := true
	if _, err := s.UpdateSchedule("morning-brief", caller, "once:"+due.Add(2*time.Hour).Format(time.RFC3339), "brief me later", &enabled); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "native", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "once:" + due.Format(time.RFC3339), Input: "nope"}, caller, "unmigrated"); err == nil {
		t.Fatal("dual native cron accepted")
	}
	live, err := s.CreateSchedule(Schedule{ScheduleID: "tick-one", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "every minute"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(live.NextDue.Add(time.Second), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(live.NextDue.Add(time.Second), g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil || job.Trigger != "cron" || job.Text != "every minute" {
		t.Fatalf("ticked job: %+v %v", job, err)
	}
}

func TestCatchupEnqueuesAtMostOneMissedOccurrence(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	g.users = map[int64]User{11: {ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	caller := o.Job.Envelope
	created, err := s.CreateSchedule(Schedule{ScheduleID: "catch-up", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "* * * * *", Input: "catch"}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	late := created.NextDue.Add(4 * time.Minute)
	if err := s.TickSchedules(late, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("catch-up occurrences=%d", len(entries))
	}
	listed, err := s.ListSchedules(caller)
	if err != nil || len(listed) != 1 || !listed[0].NextDue.After(late) {
		t.Fatalf("next due not skipped to after window: %+v %v", listed, err)
	}
}

func TestOnceScheduleDisablesAfterFire(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	g.users = map[int64]User{11: {ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	caller := o.Job.Envelope
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "once-brief", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "once:" + due.Format(time.RFC3339), Input: "once"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(due.Add(time.Second), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListSchedules(caller)
	if err != nil || len(listed) != 1 || listed[0].Enabled || !listed[0].Paused {
		t.Fatalf("once schedule still live: %+v %v", listed, err)
	}
}

func TestRoutineHTTPControlAndTelegramCommand(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	bobState := filepath.Join(t.TempDir(), "bob", "state")
	bobWS := filepath.Join(t.TempDir(), "bob", "workspace")
	if err := os.MkdirAll(bobState, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bobWS, 0700); err != nil {
		t.Fatal(err)
	}
	c.Users = append(c.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: bobState, WorkspaceDir: bobWS})
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	g.now = func() time.Time { return time.Unix(50, 0) }
	due := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 80, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "/routine create morning UTC once:" + due + " say hello"}}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/routines", nil)
	req.Header.Set("Authorization", "Bearer control-token")
	req.Header.Set("X-Hub-Principal", "alice")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "morning") {
		t.Fatalf("http list: %d %s", rec.Code, rec.Body.String())
	}
	bad := httptest.NewRequest(http.MethodGet, "/v1/routines", nil)
	bad.Header.Set("Authorization", "Bearer control-token")
	bad.Header.Set("X-Hub-Principal", "bob")
	denied := httptest.NewRecorder()
	g.Handler().ServeHTTP(denied, bad)
	if denied.Code != http.StatusOK || strings.Contains(denied.Body.String(), "morning") {
		t.Fatalf("bob read alice routines over HTTP: %d %s", denied.Code, denied.Body.String())
	}
	pause := httptest.NewRequest(http.MethodPost, "/v1/routines/morning/pause", nil)
	pause.Header.Set("Authorization", "Bearer control-token")
	pause.Header.Set("X-Hub-Principal", "alice")
	paused := httptest.NewRecorder()
	g.Handler().ServeHTTP(paused, pause)
	if paused.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", paused.Code, paused.Body.String())
	}
}

func TestVerifySlackChallenge(t *testing.T) {
	c := testConfig(t)
	c.SlackSigningSecret = "slack-secret"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	body := []byte(`{"type":"url_verification","challenge":"abc"}`)
	ts := strconv.FormatInt(g.now().Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "/v1/slack/events", bytes.NewReader(body))
	req.Header.Set("X-Slack-Signature", slackSig("slack-secret", ts, body))
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Body.String() != "abc" {
		t.Fatalf("challenge=%s", rec.Body.String())
	}
}

func TestCommandTranscriberAndSynthesizerRejectEmpty(t *testing.T) {
	if _, err := (CommandTranscriber{}).Transcribe(context.Background(), "x", "audio/ogg"); err == nil {
		t.Fatal("empty stt command accepted")
	}
	if _, _, err := (CommandSynthesizer{}).Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("empty tts command accepted")
	}
	dir := t.TempDir()
	sttPath := filepath.Join(dir, "stt")
	ttsPath := filepath.Join(dir, "tts")
	if runtime.GOOS == "windows" {
		sttPath += ".cmd"
		ttsPath += ".cmd"
		if err := os.WriteFile(sttPath, []byte("@echo off\r\necho transcribed-ok\r\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ttsPath, []byte("@echo off\r\necho ogg-audio\r\n"), 0700); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(sttPath, []byte("#!/bin/sh\necho transcribed-ok\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ttsPath, []byte("#!/bin/sh\necho ogg-audio\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	text, err := (CommandTranscriber{Command: sttPath}).Transcribe(context.Background(), filepath.Join(dir, "tone.wav"), "audio/wav")
	if err != nil || text != "transcribed-ok" {
		t.Fatalf("stt worker: %q %v", text, err)
	}
	audio, mime, err := (CommandSynthesizer{Command: ttsPath}).Synthesize(context.Background(), "hello")
	if err != nil || mime != "audio/ogg" || !strings.Contains(string(audio), "ogg-audio") {
		t.Fatalf("tts worker: %q %s %v", audio, mime, err)
	}
}
