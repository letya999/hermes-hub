package communication

// Locale flow tests: the /language command, first-contact detection and the
// channel-agnostic preference file under WorkspaceDir.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/compaction"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func langUpdate(updateID, msgID int, sender int64, text, langCode string) Update {
	return Update{UpdateID: updateID, Message: &Message{MessageID: msgID, From: &TGUser{ID: sender, LanguageCode: langCode}, Chat: TGChat{ID: sender, Type: "private"}, Text: text}}
}

// wantText renders an expected catalog string and fails when lang's catalog
// lacks the key: T echoes the raw key for missing entries, which would make a
// Contains-assertion against gateway output vacuous.
func wantText(t *testing.T, lang locale.Lang, key string, args ...any) string {
	t.Helper()
	if _, ok := locale.Lookup(lang, key); !ok {
		t.Errorf("catalog %s missing key %q", lang, key)
	}
	return locale.T(lang, key, args...)
}

// /language <code> persists a chosen preference in the user's workspace and
// replies in the newly selected language.
func TestLanguageCommandSetsWorkspacePref(t *testing.T) {
	g, err := New(baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/language kk", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	p := locale.LoadPrefs(g.config.Users[0].WorkspaceDir)
	if p.Lang != locale.LangKK || p.Source != locale.SourceChosen {
		t.Fatalf("prefs = %+v", p)
	}
	joined := ""
	for _, s := range fake.sent {
		joined += s + "\n"
	}
	if want := locale.T(locale.LangKK, "lang.saved", locale.Names[locale.LangKK]); !strings.Contains(joined, want) {
		t.Fatalf("sent=%q want %q", joined, want)
	}
}

// Bare /language re-opens the picker: on an Actions-capable channel the
// delivery carries one button per supported language.
func TestLanguageCommandBareShowsPicker(t *testing.T) {
	g, err := New(baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/language", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	if len(fake.keyboards) == 0 || len(fake.keyboards[0]) != len(locale.All) {
		t.Fatalf("keyboards=%v", fake.keyboards)
	}
	for i, b := range fake.keyboards[0] {
		if b.Data != "lang:"+string(locale.All[i]) || b.Text != locale.Names[locale.All[i]] {
			t.Fatalf("button %d = %+v", i, b)
		}
	}
	if _, err := os.Stat(filepath.Join(g.config.Users[0].WorkspaceDir, locale.PrefsFile)); err != nil {
		t.Fatalf("picker must persist a default: %v", err)
	}
	if p := locale.LoadPrefs(g.config.Users[0].WorkspaceDir); p.Source != locale.SourceDefault {
		t.Fatalf("prefs = %+v", p)
	}
}

// A picker tap persists a chosen preference and answers the callback in the
// newly selected language.
func TestLanguageCallbackPersistsChoice(t *testing.T) {
	g, err := New(baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	u := Update{UpdateID: 9, CallbackQuery: &CallbackQuery{ID: "q1", From: &TGUser{ID: 11}, Message: &Message{Chat: TGChat{ID: 11, Type: "private"}}, Data: "lang:es"}}
	if err := g.handleUpdate(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	p := locale.LoadPrefs(g.config.Users[0].WorkspaceDir)
	if p.Lang != locale.LangES || p.Source != locale.SourceChosen {
		t.Fatalf("prefs = %+v", p)
	}
	want := wantText(t, locale.LangES, "lang.saved", locale.Names[locale.LangES])
	for _, a := range fake.callbackAnswers {
		if a == want {
			return
		}
	}
	t.Fatalf("callbackAnswers=%v want %q", fake.callbackAnswers, want)
}

// A first-contact message carrying a strong channel locale hint adopts the
// language, persists it as detected, and acknowledges in that language.
func TestFirstContactDetectsTelegramLocale(t *testing.T) {
	g, err := New(baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "hello", "kk-KZ")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	p := locale.LoadPrefs(g.config.Users[0].WorkspaceDir)
	if p.Lang != locale.LangKK || p.Source != locale.SourceDetected {
		t.Fatalf("prefs = %+v", p)
	}
	joined := strings.Join(fake.sent, "\n")
	if want := locale.T(locale.LangKK, "lang.detected", locale.Names[locale.LangKK]); !strings.Contains(joined, want) {
		t.Fatalf("sent=%q want notice %q", joined, want)
	}
	// The ordinary message still flowed into a job.
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("job not enqueued: %v", err)
	}
}

// Without any hint a first-contact message gets the picker once, the default
// is persisted, and later messages are not re-asked.
func TestFirstContactShowsPickerOnce(t *testing.T) {
	g, err := New(baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "hello", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	if len(fake.keyboards) != 1 {
		t.Fatalf("expected one picker keyboard, got %v", fake.keyboards)
	}
	p := locale.LoadPrefs(g.config.Users[0].WorkspaceDir)
	if p.Lang != locale.Fallback() || p.Source != locale.SourceDefault {
		t.Fatalf("prefs = %+v", p)
	}
	before := len(fake.keyboards)
	if err := g.handleUpdate(context.Background(), langUpdate(2, 2, 11, "again", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	if len(fake.keyboards) != before {
		t.Fatalf("picker re-shown: %v", fake.keyboards)
	}
}

// A durable delivery carries the owner's language so channel adapters can
// render localized surface text (file notices, voice selection) without
// re-resolving the user.
func TestDeliveryCarriesOwnerLang(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangKK)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.mockLinkClaim(42, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.EnqueueDelivery(Delivery{ID: "lang-out", IdempotencyKey: "lang-out", Channel: "mock", ChatID: 42, Text: "hi", CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	matches, err := filepath.Glob(filepath.Join(g.spool.root, "mock-sent", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("mock-sent: %v %v", matches, err)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"lang":"kk"`) {
		t.Fatalf("delivery record lacks owner lang: %s", b)
	}
}

// Job-linked deliveries inherit the principal's language even when the
// transport address alone could not identify the owner (group topics, etc.).
func TestJobDeliveryCarriesOwnerLang(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	s, job := streamJob(t)
	g.spool = s
	if err := g.mockLinkClaim(42, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDelivery(Delivery{ID: "lang-job", JobID: job.ID, Channel: "mock", ChatID: 42, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Text: "hi", CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	matches, err := filepath.Glob(filepath.Join(s.root, "mock-sent", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("mock-sent: %v %v", matches, err)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"lang":"es"`) {
		t.Fatalf("delivery record lacks owner lang: %s", b)
	}
}

// A group-target delivery cannot resolve an owner from the chat id; the job's
// durable mapping supplies the principal and thus the language, persisted on
// the delivery record itself.
func TestGroupJobDeliveryResolvesLangFromMapping(t *testing.T) {
	c := baseTestConfig(t)
	c.Groups = Groups{{ChatID: -100, Mode: "topics"}}
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	g.spool = s
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	env.ConversationID = "telegram--100"
	env.DeliveryTargetID = "telegram--100"
	job := Job{Envelope: env, ID: "group-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "group-key", ChatID: -100, TopicID: 5, Text: "hi", CreatedAt: g.now().UTC()}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := s.EnqueueDelivery(Delivery{ID: "lang-group", JobID: job.ID, Channel: "telegram_bot", ChatID: -100, TopicID: 5, ConversationID: "telegram--100", DeliveryTargetID: "telegram--100", Text: "hi", CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	for _, dir := range []string{"pending", "sending", "done", "failed"} {
		if b, err := os.ReadFile(filepath.Join(s.root, "outbox", dir, "lang-group.json")); err == nil {
			t.Logf("%s: %s", dir, b)
		}
	}
	b, err := os.ReadFile(filepath.Join(s.root, "outbox", "done", "lang-group.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"lang":"es"`) {
		t.Fatalf("durable record lacks lang: %s", b)
	}
}

// Direct private-chat deliveries (command replies, pickers) carry no job
// mapping; the chat id resolves the owner for language stamping.
func TestDirectDeliveryResolvesLangFromChat(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.spool.EnqueueDelivery(Delivery{ID: "lang-direct", IdempotencyKey: "lang-direct", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "hi", CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	b, err := os.ReadFile(filepath.Join(g.spool.root, "outbox", "done", "lang-direct.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"lang":"es"`) {
		t.Fatalf("durable record lacks lang: %s", b)
	}
}

// The spawned runtime sees the workspace language via HUB_USER_LANG so the
// model keeps answering in the user's language without prompt patching.
func TestRunnerPassesUserLang(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "helper.go")
	if err := os.WriteFile(source, []byte("package main\nimport (\"fmt\";\"os\")\nfunc main(){fmt.Print(os.Getenv(\"HUB_USER_LANG\"))}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "helper")
	if goruntime.GOOS == "windows" {
		bin += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Skipf("cannot build helper: %v (%s)", err, output)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	pinLang(t, workspace, locale.LangKK)
	response, err := (HermesRunner{Command: bin, Timeout: 10 * time.Second}).Run(context.Background(), Job{Text: "hello"}, User{StateDir: filepath.Join(root, "state"), WorkspaceDir: workspace})
	if err != nil || response != "kk" {
		t.Fatalf("response=%q err=%v", response, err)
	}
}

// Deterministic command replies follow the workspace language: /help renders
// the localized inventory and unknown commands answer in kind.
func TestHelpAndUnknownCommandLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/help", "")); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), langUpdate(2, 2, 11, "/frobnicate", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	if !strings.Contains(joined, wantText(t, locale.LangES, "help.title")) {
		t.Fatalf("help title not localized: %q", joined)
	}
	if !strings.Contains(joined, wantText(t, locale.LangES, "menu.desc.start")) {
		t.Fatalf("help body not localized: %q", joined)
	}
	want := wantText(t, locale.LangES, "chat.unknown-command", "frobnicate", "HELP-BODY")
	want = strings.TrimSuffix(want, "\n\nHELP-BODY")
	if !strings.Contains(joined, want) {
		t.Fatalf("unknown-command reply not localized: %q want %q", joined, want)
	}
}

// An unsupported /language argument is rejected with a localized notice —
// silently showing help would hide the failed choice.
func TestLanguageCommandRejectsUnknownTag(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/language xx", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	if want := wantText(t, locale.LangES, "lang.unknown", "xx"); !strings.Contains(joined, want) {
		t.Fatalf("sent=%q want %q", joined, want)
	}
	if p := locale.LoadPrefs(c.Users[0].WorkspaceDir); p.Lang != locale.LangES {
		t.Fatalf("unsupported tag overwrote prefs: %+v", p)
	}
}

// Command replies use the workspace language across the dispatch switch:
// start, approve-usage, cancel-usage and credentials-usage all localize.
func TestCommandRepliesLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	for i, text := range []string{"/start", "/approve", "/cancel", "/credentials"} {
		if err := g.handleUpdate(context.Background(), langUpdate(i+1, i+1, 11, text, "")); err != nil {
			t.Fatal(err)
		}
	}
	for range 8 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "chat.started"),
		wantText(t, locale.LangES, "chat.approve-usage"),
		wantText(t, locale.LangES, "chat.cancel-usage"),
		wantText(t, locale.LangES, "chat.creds-usage"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// Approval/cancel error branches answer in the user's language too — the
// scope-denial strings are part of the localized surface.
func TestApprovalAndCancelErrorsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	for i, text := range []string{"/approve nope nope once", "/cancel nope"} {
		if err := g.handleUpdate(context.Background(), langUpdate(i+1, i+1, 11, text, "")); err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "chat.approve-scope"),
		wantText(t, locale.LangES, "chat.cancel-scope"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// /routines lists reminders in the user's language.
func TestRoutinesListLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/routines", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "chat.routines-title"),
		wantText(t, locale.LangES, "chat.routines-empty"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// A populated /routines listing localizes the per-item state fragments, not
// just the header.
func TestRoutinesFragmentsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	env := c.Users[0].Envelope(11)
	if _, err := g.spool.CreateSchedule(Schedule{ScheduleID: "frag-check", Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "x", JobKind: "reminder"}, env, ""); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/routines", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	if !strings.Contains(joined, "frag-check") || !strings.Contains(joined, wantText(t, locale.LangES, "routine.active")) {
		t.Fatalf("sent=%q missing localized state", joined)
	}
}

// State and delivery fragments in /routines localize per item: paused,
// completed, blocked, missed occurrences, unconfirmed deliveries and the
// no-next-due tail each render in the user's language.
func TestRoutinesStateVariantsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	env := c.Users[0].Envelope(11)
	base := Schedule{Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "x", JobKind: "reminder", Enabled: true, NextDue: time.Now().UTC().Add(time.Hour)}
	base.ScheduleID = "paused-one"
	if _, err := g.spool.CreateSchedule(base, env, ""); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.PauseSchedule("paused-one", env); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.SpoolDir, "schedules"), 0700); err != nil {
		t.Fatal(err)
	}
	seed := func(id string, mut func(*Schedule)) {
		sc := base
		sc.ScheduleID = id
		mut(&sc)
		body, _ := json.Marshal(sc)
		if err := os.WriteFile(filepath.Join(c.SpoolDir, "schedules", commodel.FileID(id)+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	seed("completed-one", func(s *Schedule) { s.Enabled, s.NextDue = false, time.Time{} })
	seed("missed-one", func(s *Schedule) { s.LastOccurrenceState = "missed" })
	seed("delivered-one", func(s *Schedule) { s.LastDeliveryState = "delivered" })
	seed("uncertain-one", func(s *Schedule) { s.LastDeliveryState = "uncertain" })
	seed("nodue-one", func(s *Schedule) { s.NextDue = time.Time{} })
	seed("blocked-one", func(s *Schedule) { s.BlockedReason = "quota" })
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/routines", "")); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "routine.paused"),
		wantText(t, locale.LangES, "routine.completed"),
		wantText(t, locale.LangES, "routine.blocked"),
		wantText(t, locale.LangES, "routine.missed"),
		wantText(t, locale.LangES, "routine.delivered"),
		wantText(t, locale.LangES, "routine.unconfirmed"),
		wantText(t, locale.LangES, "routine.no-due"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// Edge-path replies in handleUpdate localize: access denial follows the
// stranger's platform hint (there is no workspace yet), while verified-user
// rejections, edit notices, task-open failures and the callback fallback all
// come from the stored workspace preference.
func TestGatewayEdgeRepliesLocalized(t *testing.T) {
	c := baseTestConfig(t)
	c.Groups = []Group{{ChatID: -55, Mode: "topics"}, {ChatID: -77, Mode: "topics"}}
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	ctx := context.Background()
	drain := func() { g.deliverOne(ctx); g.deliverOne(ctx) }

	// Unverified stranger: Spanish platform hint picks the reply language.
	if err := g.handleUpdate(ctx, langUpdate(1, 1, 99, "hola", "es")); err != nil {
		t.Fatal(err)
	}
	drain()
	// Unverified stranger without a hint falls back to the operator's
	// configured hub default (TestMain pins HUB_DEFAULT_LANG=ru).
	if err := g.handleUpdate(ctx, langUpdate(2, 2, 98, "hello", "")); err != nil {
		t.Fatal(err)
	}
	drain()
	// Verified user pasting env material into a group chat.
	if err := g.handleUpdate(ctx, Update{UpdateID: 3, Message: &Message{MessageID: 3, From: &TGUser{ID: 11}, Chat: TGChat{ID: -55, Type: "supergroup"}, Text: "A=1\nB=2"}}); err != nil {
		t.Fatal(err)
	}
	drain()
	// An edited message never restarts work.
	if err := g.handleUpdate(ctx, Update{UpdateID: 4, EditedMessage: &Message{MessageID: 4, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "corregir"}}); err != nil {
		t.Fatal(err)
	}
	drain()
	// Corrupt topic binding makes ResolveTask fail inside a forum topic.
	binding := g.spool.topicTaskPath("alice", -77, 9)
	if err := os.MkdirAll(filepath.Dir(binding), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binding, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, Update{UpdateID: 5, Message: &Message{MessageID: 5, From: &TGUser{ID: 11}, Chat: TGChat{ID: -77, Type: "supergroup"}, MessageThreadID: 9, IsTopicMessage: true, Text: "/start"}}); err != nil {
		t.Fatal(err)
	}
	drain()
	// A bogus callback payload is answered with the localized rejection.
	if err := g.handleCallback(ctx, &CallbackQuery{ID: "cb1", From: &TGUser{ID: 11}, Message: &Message{Chat: TGChat{ID: 11}}, Data: "bogus"}); err != nil {
		t.Fatal(err)
	}
	// A corrupt schedule record fails /routines with the localized error.
	schedules := filepath.Join(c.SpoolDir, "schedules")
	if err := os.MkdirAll(schedules, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schedules, "bad.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, langUpdate(6, 6, 11, "/routines", "")); err != nil {
		t.Fatal(err)
	}
	drain()

	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "chat.no-access"),
		locale.T(locale.Fallback(), "chat.no-access"),
		wantText(t, locale.LangES, "chat.group-no-secrets"),
		wantText(t, locale.LangES, "chat.edit-norestart"),
		wantText(t, locale.LangES, "chat.task-open-failed"),
		wantText(t, locale.LangES, "chat.routines-unavail"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
	if len(fake.callbackAnswers) != 1 || fake.callbackAnswers[0] != wantText(t, locale.LangES, "chat.approve-denied") {
		t.Fatalf("callback answers=%q", fake.callbackAnswers)
	}
}

// The live progress line renders its fallback text and elapsed suffix in the
// job owner's workspace language — the mapping carries PrincipalID, so a
// running job for an es user ticks in Spanish.
func TestProgressStatusLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	seed := func(jobID string, age time.Duration) {
		mapping := commodel.JobMapping{
			JobID:            jobID,
			PrincipalID:      "alice",
			Status:           "running",
			RunID:            "run-" + jobID,
			Channel:          "telegram_bot",
			ChatID:           11,
			DeliveryTargetID: "telegram-11",
			CreatedAt:        time.Now().UTC().Add(-age),
		}
		body, _ := json.Marshal(mapping)
		if err := os.MkdirAll(filepath.Join(c.SpoolDir, "mappings"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(c.SpoolDir, "mappings", spoolFileID(jobID)+".json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	seed("job-prog", 90*time.Second)
	seed("job-prog-sec", 42*time.Second)
	seed("job-prog-hour", 3660*time.Second)
	g.progressTick(context.Background())
	for _, tc := range []struct{ jobID, elapsed string }{
		{"job-prog", wantText(t, locale.LangES, "time.min", 1, 30)},
		{"job-prog-sec", wantText(t, locale.LangES, "time.sec", 42)},
		{"job-prog-hour", wantText(t, locale.LangES, "time.hour", 1, 1)},
	} {
		raw, err := os.ReadFile(filepath.Join(c.SpoolDir, "outbox", "pending", spoolFileID("job-"+tc.jobID+"-status")+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var d commodel.Delivery
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if d.Text != wantText(t, locale.LangES, "chat.working")+" · "+tc.elapsed {
			t.Errorf("status text %q for %s", d.Text, tc.jobID)
		}
	}
}

// Deterministic secret/conflict surfaces localize: a DM with env-looking
// material is deleted and answered in the user's language, and the duplicate
// poller notice reaches the owner in their language.
func TestSecretInterceptAndConflictLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	ctx := context.Background()
	if err := g.handleUpdate(ctx, langUpdate(1, 1, 11, "TOKEN=abc123", "")); err != nil {
		t.Fatal(err)
	}
	// With a form listener configured the denial upgrades to the secure-form
	// link; the URL itself must pass through unmodified.
	g.config.FormOrigin = "http://localhost:9"
	if err := g.handleUpdate(ctx, langUpdate(2, 2, 11, "TOKEN2=def456", "")); err != nil {
		t.Fatal(err)
	}
	g.notifyTelegramConflict()
	for range 4 {
		g.deliverOne(ctx)
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "chat.secrets-rejected"),
		wantText(t, locale.LangES, "chat.bot-conflict"),
		"http://localhost:9/credentials/",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
	formLine := wantText(t, locale.LangES, "chat.secrets-form", "")
	if !strings.Contains(joined, strings.TrimSuffix(formLine, " ")) && !strings.Contains(joined, formLine) {
		t.Fatalf("sent=%q missing secrets-form prefix", joined)
	}
}

// The bare secret denial (no form listener configured) is its own reply —
// separate from the secure-form upgrade.
func TestSecretDeniedOnlyLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "TOKEN=abc123", "")); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 || fake.sent[0] != wantText(t, locale.LangES, "chat.secrets-rejected") {
		t.Fatalf("sent=%q", fake.sent)
	}
}

// /stop covers two more localized branches: a failed active-job lookup and a
// per-job cancel refusal render in the user's language. Auto-generated task
// names follow the workspace language at creation time.
func TestStopCommandEdgeCasesLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	ctx := context.Background()

	// Missing mappings directory → the lookup failure line.
	if err := os.RemoveAll(filepath.Join(c.SpoolDir, "mappings")); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, langUpdate(1, 1, 11, "/stop", "")); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || fake.sent[0] != wantText(t, locale.LangES, "stop.check-failed") {
		t.Fatalf("sent=%q", fake.sent)
	}

	// A live job owned by a different context refuses cancel → per-job line.
	other := commodel.JobMapping{
		JobID: "job-foreign", PrincipalID: "alice", UserID: "alice", ContextID: "other-ctx",
		Status: "running", RunID: "r1", Channel: "telegram_bot", ChatID: 11, TaskID: commodel.DefaultTaskID,
		ConversationID: "telegram-11", DeliveryTargetID: "telegram-11",
	}
	body, _ := json.Marshal(other)
	if err := os.MkdirAll(filepath.Join(c.SpoolDir, "mappings"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.SpoolDir, "mappings", spoolFileID("job-foreign")+".json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, langUpdate(2, 2, 11, "/stop", "")); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	got := fake.sent[len(fake.sent)-1]
	if !strings.Contains(got, wantText(t, locale.LangES, "stop.cancel-unavailable", "job-foreign")) {
		t.Fatalf("sent=%q", fake.sent)
	}

	// Auto-name follows the creator's workspace language.
	task, _, err := g.spool.CreateTask(c.Users[0], 11, 0, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(task.Name, wantText(t, locale.LangES, "task.autoname-pattern", "")) {
		t.Fatalf("auto name %q", task.Name)
	}
}

// The auto-title adoption notice follows the task owner's workspace language.
func TestAutoTitleNoticeLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	task, _, err := g.spool.CreateTask(c.Users[0], 11, 0, "", false)
	if err != nil {
		t.Fatal(err)
	}
	count := int64(autoTitleMinMessages)
	runner := &usageStubRunner{report: hubruntime.SessionUsage{SessionFound: true, MessageCount: &count, Title: "Limpieza de datos"}}
	g.runner = runner
	envelope := c.Users[0].Envelope(11)
	envelope.DeliveryTargetID = "telegram-11"
	job := Job{Envelope: envelope, ID: "job-title", UserID: "alice", ActorID: "alice", Channel: "telegram_bot", ChatID: 11, TaskID: task.TaskID}
	g.maybeAutoTitle(context.Background(), job)
	for range 3 {
		g.deliverOne(context.Background())
	}
	if want := wantText(t, locale.LangES, "chat.auto-titled", "Limpieza de datos"); !strings.Contains(strings.Join(fake.sent, "\n"), want) {
		t.Fatalf("sent=%q missing %q", fake.sent, want)
	}
}

// Stream-event replies localize in the job owner's workspace language:
// the run.started fallback, the approval prompt block, approval-expired and
// approval-denied cancels, and the terminal failure lines.
func TestStreamEventTextsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	s, err := NewSpool(c.SpoolDir)
	if err != nil {
		t.Fatal(err)
	}
	s.langOf = func(string) locale.Lang { return locale.LangES }
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.spool = fake, s
	ctx := context.Background()
	envelope := c.Users[0].Envelope(11)

	mkJob := func(id string) Job {
		job := Job{Envelope: envelope, ID: id, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: id + "-key", ChatID: 11, Text: "x", CreatedAt: time.Now().UTC()}
		if _, err := s.Enqueue(job); err != nil {
			t.Fatal(err)
		}
		return job
	}
	emit := func(job Job, ev hubruntime.ExecuteResponse) {
		if err := s.RecordStreamEvent(job, ev); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			g.deliverOne(ctx)
		}
	}

	// run.started with no text falls back to the working line.
	jobS := mkJob("j-start")
	started := streamEvent(jobS)
	started.EventID, started.LastEvent = "s1", "run.started"
	emit(jobS, started)

	// approval.request renders prompt + deadline window + command hint.
	jobA := mkJob("j-appr")
	ap := streamEvent(jobA)
	ap.EventID, ap.LastEvent = "a1", "approval.request"
	ap.ApprovalID, ap.ApprovalChoices = "req-1", []string{"once", "deny"}
	ap.ApprovalSummary = "git push"
	emit(jobA, ap)

	// Terminal outcomes.
	for _, tc := range []struct{ id, status string }{
		{"j-cancel", "cancelled"},
		{"j-interr", "interrupted"},
		{"j-fail", "failed"},
	} {
		job := mkJob(tc.id)
		ev := streamEvent(job)
		ev.EventID, ev.Status = tc.id+"-e", tc.status
		ev.LastEvent = "run." + tc.status
		emit(job, ev)
	}

	// Approval-expired and approval-denied cancels reuse the mapping's
	// deadline/choice recorded at approval time.
	for _, deny := range []bool{false, true} {
		id := "j-exp"
		if deny {
			id = "j-deny"
		}
		job := mkJob(id)
		s.mu.Lock()
		m, _ := s.loadMappingLocked(job.ID)
		m.ApprovalID, m.ApprovalDeadline = "req-x", time.Now().Add(-time.Minute)
		m.Control = &PendingControl{Action: "approve", State: "closed"}
		if deny {
			m.Control.Choice = "deny"
		}
		_ = s.writeMappingLocked(m)
		s.mu.Unlock()
		ev := streamEvent(job)
		ev.EventID, ev.Status, ev.LastEvent = id+"-e", "cancelled", "run.cancelled"
		emit(job, ev)
	}

	joined := strings.Join(fake.sent, "\n")
	windowPrefix := strings.SplitN(wantText(t, locale.LangES, "job.approval-window", "\x00"), "\x00", 2)[0]
	for _, want := range []string{
		wantText(t, locale.LangES, "chat.working"),
		wantText(t, locale.LangES, "job.approval-needed"),
		windowPrefix,
		wantText(t, locale.LangES, "job.approval-hint", "j-appr", "req-1", "once, deny", "j-appr"),
		wantText(t, locale.LangES, "job.cancelled", "j-cancel"),
		wantText(t, locale.LangES, "job.interrupted", "j-interr"),
		wantText(t, locale.LangES, "job.failed", "j-fail"),
		wantText(t, locale.LangES, "job.approval-expired", "j-exp"),
		wantText(t, locale.LangES, "job.approval-denied", "j-deny"),
		"git push",
		"/approve j-appr req-1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// /compact localizes usage, availability, busy and async result replies, plus
// the allowlisted compactReply projection and its failure classes.
func TestCompactCommandLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	ctx := context.Background()
	user := c.Users[0]
	task := Task{TaskID: "t1", ConversationID: "telegram-11"}

	// Bad args show the localized usage line.
	if err := g.compactCommand(ctx, user, task, "telegram_bot", 11, 0, 11, "c1", "/compact bogus-token"); err != nil {
		t.Fatal(err)
	}
	// Without a compact-capable runner the reply reports unavailability.
	if err := g.compactCommand(ctx, user, task, "telegram_bot", 11, 0, 11, "c2", "/compact"); err != nil {
		t.Fatal(err)
	}
	// With a runner but a saturated gate the reply reports busy.
	g.runner = &compactStubRunner{}
	g.usageQueries.Add(3)
	if err := g.compactCommand(ctx, user, task, "telegram_bot", 11, 0, 11, "c3", "/compact"); err != nil {
		t.Fatal(err)
	}
	g.usageQueries.Add(-3)
	// Happy path: immediate "working" reply, then the async result line.
	if err := g.compactCommand(ctx, user, task, "telegram_bot", 11, 0, 11, "c4", "/compact here"); err != nil {
		t.Fatal(err)
	}
	g.usageWG.Wait()
	// A runner error surfaces as the localized availability line.
	g.runner = &compactStubRunner{err: errors.New("transport down")}
	if err := g.compactCommand(ctx, user, task, "telegram_bot", 11, 0, 11, "c5", "/compact"); err != nil {
		t.Fatal(err)
	}
	g.usageWG.Wait()
	for range 8 {
		g.deliverOne(ctx)
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "compact.usage"),
		wantText(t, locale.LangES, "compact.unsupported-runner"),
		wantText(t, locale.LangES, "compact.busy"),
		wantText(t, locale.LangES, "compact.working"),
		wantText(t, locale.LangES, "compact.applied", "hub", 100, 40, 8, 3),
		wantText(t, locale.LangES, "compact.unavailable", "transport down"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}

	// Direct coverage of the remaining result/failure classes.
	i64 := func(v int64) *int64 { return &v }
	preview := compactReply(locale.LangES, compaction.CompactResult{WouldMethod: "native", TokensBefore: i64(50), MessagesBefore: 4})
	if want := wantText(t, locale.LangES, "compact.preview", "native", 50, 4); preview != want {
		t.Errorf("preview=%q want %q", preview, want)
	}
	queued := compactReply(locale.LangES, compaction.CompactResult{FailureClass: "queued"})
	if want := wantText(t, locale.LangES, "compact.queued"); queued != want {
		t.Errorf("queued=%q want %q", queued, want)
	}
	for _, tc := range []struct{ class, key string }{
		{"lease_held", "compact.err.lease-held"},
		{"engine_denied", "compact.err.engine-denied"},
		{"unsupported", "compact.err.unsupported"},
		{"nothing_to_compress", "compact.err.nothing"},
		{"nothing_to_prune", "compact.err.nothing"},
		{"under_threshold", "compact.err.under-threshold"},
		{"estimate_failed", "compact.err.estimate-failed"},
		{"disabled", "compact.err.disabled"},
		{"", "compact.err.unknown"},
	} {
		if want := wantText(t, locale.LangES, "compact.err.prefix", wantText(t, locale.LangES, tc.key)); compactReply(locale.LangES, compaction.CompactResult{FailureClass: tc.class}) != want {
			t.Errorf("class=%q got %q want %q", tc.class, compactReply(locale.LangES, compaction.CompactResult{FailureClass: tc.class}), want)
		}
	}
	// Unknown classes pass the raw class through unchanged.
	got := compactReply(locale.LangES, compaction.CompactResult{FailureClass: "custom-class"})
	if want := wantText(t, locale.LangES, "compact.err.prefix", "custom-class"); got != want {
		t.Errorf("custom class=%q want %q", got, want)
	}
}

type compactStubRunner struct {
	fakeRunner
	result compaction.CompactResult
	err    error
}

func (c *compactStubRunner) Compact(_ context.Context, _ hubruntime.CompactRequest) (compaction.CompactResult, error) {
	if c.err != nil {
		return compaction.CompactResult{}, c.err
	}
	i64 := func(v int64) *int64 { return &v }
	return compaction.CompactResult{Applied: true, Method: "hub", TokensBefore: i64(100), TokensAfter: i64(40), MessagesBefore: 8, MessagesAfter: 3}, nil
}

// /goal localizes every verb reply — status, pause/resume, clear, the
// unsupported-verb list, runner availability, failure classes and set.
func TestGoalCommandLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := c.Users[0]
	task, _, err := g.spool.CreateTask(user, 11, 0, "meta", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	call := func(verb, arg string) string {
		answer, err := g.goalVerb(ctx, user, task, 11, "telegram_bot", 11, verb, arg)
		if err != nil {
			t.Fatal(err)
		}
		return answer
	}
	checks := []struct{ got, want string }{}
	want := func(key string, args ...any) string { return wantText(t, locale.LangES, key, args...) }

	checks = append(checks, struct{ got, want string }{call("unsupported", "draft"), want("goal.unsupported", "draft")})
	checks = append(checks, struct{ got, want string }{call("status", ""), want("goal.none")})
	checks = append(checks, struct{ got, want string }{call("clear", ""), want("goal.none")})
	checks = append(checks, struct{ got, want string }{call("pause", ""), want("goal.none")})
	checks = append(checks, struct{ got, want string }{call("resume", ""), want("goal.none")})
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: "alice", ConversationID: task.ConversationID, Text: "cerrar el sprint", Engine: "hub", Status: "active", MaxTurns: 5, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	checks = append(checks, struct{ got, want string }{call("status", ""), want("goal.status", "cerrar el sprint", "active", "hub", 0, 5)})
	checks = append(checks, struct{ got, want string }{call("pause", ""), want("goal.paused")})
	checks = append(checks, struct{ got, want string }{call("resume", ""), want("goal.resumed")})
	checks = append(checks, struct{ got, want string }{call("clear", ""), want("goal.cleared")})
	checks = append(checks, struct{ got, want string }{call("bogus", ""), want("goal.unhandled")})

	// set with no goal-capable runner, then a rejected and an accepted set.
	checks = append(checks, struct{ got, want string }{call("set", "enviar el informe"), want("goal.unsupported-runner")})
	g.runner = &goalStubRunner{result: hubruntime.GoalResult{FailureClass: "engine_conflict"}}
	checks = append(checks, struct{ got, want string }{call("set", "enviar el informe"), want("goal.rejected", wantText(t, locale.LangES, "goal.err.engine-conflict"))})
	g.runner = &goalStubRunner{result: hubruntime.GoalResult{Engine: "hub", MaxTurns: 7, KickoffPrompt: "begin"}}
	checks = append(checks, struct{ got, want string }{call("set", "enviar el informe"), want("goal.set", "hub", 7, "enviar el informe")})

	for i, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("check %d got %q want %q", i, tc.got, tc.want)
		}
	}

	// Storage errors surface as the localized unavailable line.
	if err := os.MkdirAll(filepath.Join(g.spool.root, "goals"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.spool.goalPath("alice", task.ConversationID), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := call("status", ""); !strings.Contains(got, wantText(t, locale.LangES, "goal.unavailable", "invalid goal record")) {
		t.Errorf("status err got %q", got)
	}
	_ = os.Remove(g.spool.goalPath("alice", task.ConversationID))
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: "alice", ConversationID: task.ConversationID, Text: "cerrar el sprint", Engine: "hub", Status: "active", MaxTurns: 5, Epoch: 9}); err != nil {
		t.Fatal(err)
	}

	// goalAfterJob's done-verdict notice follows the owner's language.
	goal, found, err := g.spool.GoalForTask("alice", task.ConversationID)
	if err != nil || !found {
		t.Fatalf("goal record: %v %v", found, err)
	}
	g.runner = &goalStubRunner{result: hubruntime.GoalResult{Verdict: "done", Reason: "entregado", Evidence: "tests verdes"}}
	envelope := c.Users[0].Envelope(11)
	envelope.DeliveryTargetID = "telegram-11"
	turn := Job{Envelope: envelope, ID: "goal-turn-1", UserID: "alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, GoalEpoch: goal.Epoch}
	g.goalAfterJob(ctx, turn, "respuesta final")
	fake := &fakeAPI{}
	g.api = fake
	for range 3 {
		g.deliverOne(ctx)
	}
	if want := wantText(t, locale.LangES, "goal.achieved", goal.Text, "entregado", "tests verdes"); !strings.Contains(strings.Join(fake.sent, "\n"), want) {
		t.Errorf("achieved sent=%q want %q", fake.sent, want)
	}
}

type goalStubRunner struct {
	fakeRunner
	result hubruntime.GoalResult
	err    error
}

func (r *goalStubRunner) Goal(_ context.Context, _ hubruntime.GoalRequest) (hubruntime.GoalResult, error) {
	return r.result, r.err
}

// The worker's terminal-failure deliveries follow the job owner's workspace
// language: plain terminal failures, goal-turn pause notices and both
// uncertain-outcome notices.
func TestWorkerFailureNoticesLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	c.RuntimeURL = "http://runtime.test"
	c.RuntimeAuth = "token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	user := c.Users[0]
	task, _, err := g.spool.CreateTask(user, 11, 0, "meta", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: "alice", ConversationID: task.ConversationID, Text: "informe", Engine: "hub", Status: "active", MaxTurns: 5, Epoch: 3}); err != nil {
		t.Fatal(err)
	}
	env := c.Users[0].Envelope(11)
	env.DeliveryTargetID = "telegram-11"
	mkJob := func(id, trigger string) Job {
		return Job{Envelope: env, ID: id, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: trigger, IdempotencyKey: id + "-key", ChatID: 11, TaskID: task.TaskID, Text: "x", GoalEpoch: 3, CreatedAt: time.Now().UTC()}
	}
	g.runner = &scriptedRunner{outcomes: map[string]RunOutcome{
		"w-interr":   {Status: "interrupted", LastEvent: "run.interrupted"},
		"w-goal":     {Status: "cancelled", LastEvent: "run.cancelled"},
		"w-uncertain": {Status: "uncertain", RunID: "run-9", LastEvent: "run.unknown"},
		"w-uncertain2": {Status: "uncertain", LastEvent: "run.unknown"},
	}}
	for _, id := range []string{"w-interr", "w-goal", "w-uncertain", "w-uncertain2"} {
		trigger := "message"
		if id == "w-goal" {
			trigger = "goal"
		}
		if _, err := g.spool.Enqueue(mkJob(id, trigger)); err != nil {
			t.Fatal(err)
		}
	}
	// Past the reconciliation window the owner is told the outcome is unknown.
	g.now = func() time.Time { return time.Now().Add(hubruntime.RunBudget() + time.Minute) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		n := len(fake.sent)
		fake.mu.Unlock()
		if n >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "job.interrupted", "w-interr"),
		wantText(t, locale.LangES, "job.cancelled", "w-goal") + " " + wantText(t, locale.LangES, "job.goal-paused"),
		wantText(t, locale.LangES, "job.uncertain", "w-uncertain2"),
		wantText(t, locale.LangES, "job.recheck", "w-uncertain"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

type scriptedRunner struct {
	fakeRunner
	outcomes map[string]RunOutcome
}

func (r *scriptedRunner) RunOutcome(_ context.Context, job Job) (RunOutcome, error) {
	outcome, ok := r.outcomes[job.ID]
	if !ok {
		return RunOutcome{Status: "completed", LastEvent: "run.completed", Text: "ok"}, nil
	}
	outcome.JobID = job.ID
	if outcome.Status == "uncertain" {
		return outcome, ErrUncertain
	}
	if outcome.Status != "completed" {
		return outcome, errors.New("run failed")
	}
	return outcome, nil
}

// /watches, /connections and the ToolHub connector rendering localize in the
// caller's workspace language.
func TestToolHubTextsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := c.Users[0]
	env := user.Envelope(11)
	want := func(key string, args ...any) string { return wantText(t, locale.LangES, key, args...) }

	// /watches: one paused event subscription over a tool probe.
	if err := os.MkdirAll(filepath.Join(c.SpoolDir, "schedules"), 0700); err != nil {
		t.Fatal(err)
	}
	sc := Schedule{ScheduleID: "ev-loc", Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 * * * *", Input: "mail events", JobKind: "event", Enabled: true, Paused: true, Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-mail-events-abc"}}
	body, _ := json.Marshal(sc)
	if err := os.WriteFile(filepath.Join(c.SpoolDir, "schedules", commodel.FileID("ev-loc")+".json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	list := g.watchesCommand(user, 11)
	for _, want := range []string{want("watches.title"), want("watches.paused")} {
		if !strings.Contains(list, want) {
			t.Errorf("watches=%q missing %q", list, want)
		}
	}

	// Removing the only subscription renders the empty tail.
	if err := os.Remove(filepath.Join(c.SpoolDir, "schedules", commodel.FileID("ev-loc")+".json")); err != nil {
		t.Fatal(err)
	}
	if got := g.watchesCommand(user, 11); !strings.HasSuffix(got, want("watches.empty")) {
		t.Errorf("watches empty=%q missing %q", got, want("watches.empty"))
	}

	// Corrupt schedule storage flips the listing to the unavailable reply.
	bad := filepath.Join(c.SpoolDir, "schedules", commodel.FileID("bad-loc")+".json")
	if err := os.WriteFile(bad, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := g.watchesCommand(user, 11); got != want("watches.unavailable") {
		t.Errorf("watches err=%q want %q", got, want("watches.unavailable"))
	}
	_ = os.Remove(bad)

	// /connections without a ToolHub endpoint ends with the registry notice.
	if got := g.connectionsCommand(user, 11); !strings.HasSuffix(got, "\n"+want("connectors.unavailable")) {
		t.Errorf("connections=%q missing %q", got, want("connectors.unavailable"))
	}

	// connectionList: four per-service status fragments.
	fresh := User{ID: "x", Features: []string{"gitlab"}, ConfiguredEnv: map[string]bool{"GITLAB_TOKEN": true}}
	configured := connectionListWithLang(locale.LangES, fresh)
	freshNoEnv := User{ID: "x", Features: []string{"gitlab"}, ConfiguredEnv: map[string]bool{}}
	needs := connectionListWithLang(locale.LangES, freshNoEnv)
	freshDisabled := User{ID: "x", PolicyDisabled: []string{"gitlab"}}
	_ = connectionListWithLang(locale.LangES, freshDisabled)
	freshBare := User{ID: "x"}
	hostManaged := connectionListWithLang(locale.LangES, freshBare)
	lines := configured + "\n" + needs + "\n" + hostManaged
	for _, w := range []string{
		want("connections.title"),
		want("connections.configured"),
		want("connections.needs-config", "GITLAB_TOKEN"),
		want("connections.available", "GITLAB_TOKEN"),
		want("connections.host-managed"),
	} {
		if !strings.Contains(lines, w) {
			t.Errorf("connectionList missing %q", w)
		}
	}

	// connectorLine: short and long inventory renderings.
	short := connectorLine(locale.LangES, "mail", "http", []string{"a", "b"})
	if want := want("connector.line", "mail [http]", 2, "a, b"); short != want {
		t.Errorf("connectorLine=%q want %q", short, want)
	}
	long := connectorLine(locale.LangES, "mail", "", []string{"a", "b", "c", "d", "e", "f", "g"})
	if want := want("connector.line-tail", "mail", 7, "a, b, c"); long != want {
		t.Errorf("connectorLine long=%q want %q", long, want)
	}

	// toolHubConnectorsHTTP renders a fake registry over the supervisor bearer.
	var payload atomic.Value
	payload.Store(`{"connectors":[],"inventory":[]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload.Load().(string)))
	}))
	defer srv.Close()
	c2 := baseTestConfig(t)
	pinLang(t, c2.Users[0].WorkspaceDir, locale.LangES)
	c2.ToolHubURL, c2.Supervised, c2.RuntimeURL = srv.URL, true, srv.URL
	c2.RuntimeAuth = "bearer-token-0123456789abcdef0123456789ab"
	g2, err := New(c2)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := g2.toolHubConnectorsHTTP(locale.LangES, c2.Users[0])
	if !ok || !strings.Contains(out, want("connectors.empty")) {
		t.Errorf("connectors=%q ok=%v", out, ok)
	}

	// A non-empty registry renders the localized connectors title.
	payload.Store(`{"connectors":[{"definition_id":"mail","transport":"http","tools":["hub-mail-list-00000000"]}],"inventory":[]}`)
	out, ok = g2.toolHubConnectorsHTTP(locale.LangES, c2.Users[0])
	if !ok || !strings.Contains(out, want("connectors.title")) {
		t.Errorf("connectors=%q ok=%v missing title", out, ok)
	}
}

type failTranscriber struct{}

func (failTranscriber) Transcribe(context.Context, string, string) (string, error) {
	return "", errors.New("stt down")
}

// Media error replies, the Slack secret rejection and the admin-hold notice
// all localize to the caller's workspace language.
func TestMediaAndNoticeRepliesLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	user := c.Users[0]
	want := func(key string, args ...any) string { return wantText(t, locale.LangES, key, args...) }

	voiceMsg := func(size int64, dur int) *Message {
		return &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Voice: &TGMedia{FileID: "v1", MimeType: "audio/ogg", FileSize: size, Duration: dur}}
	}
	upd := func(id int) Update { return Update{UpdateID: id} }

	// Oversized voice is rejected before any download.
	if err := g.handleVoice(context.Background(), upd(1), user, voiceMsg(mediaSizeLimit+1, 5)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())

	// No transcriber configured.
	if err := g.handleVoice(context.Background(), upd(2), user, voiceMsg(12, 5)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())

	// Transcriber present but the download fails.
	g.transcriber = failTranscriber{}
	fake.err = errors.New("telegram down")
	if err := g.handleVoice(context.Background(), upd(3), user, voiceMsg(12, 5)); err != nil {
		t.Fatal(err)
	}
	fake.err = nil
	g.deliverOne(context.Background())

	// Download succeeds, transcription fails.
	if err := g.handleVoice(context.Background(), upd(4), user, voiceMsg(12, 5)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())

	// Oversized document.
	doc := &Message{MessageID: 2, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Document: &TGMedia{FileID: "d1", FileName: "a.bin", FileSize: mediaSizeLimit + 1}}
	if err := g.handleFile(context.Background(), upd(5), user, doc); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())

	// Download failure leaves nothing staged.
	doc.Document.FileSize = 12
	fake.err = errors.New("telegram down")
	if err := g.handleFile(context.Background(), upd(6), user, doc); err != nil {
		t.Fatal(err)
	}
	fake.err = nil
	g.deliverOne(context.Background())

	// Staged file but the task binding is unrecoverable (corrupt pointer).
	if err := os.MkdirAll(filepath.Join(c.SpoolDir, "tasks", "current"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.spool.currentTaskPath("alice", 11), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.handleFile(context.Background(), upd(7), user, doc); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())

	// A failed header probe (unreadable staged file) answers with saveErr.
	noName := &Message{MessageID: 3, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Document: &TGMedia{FileID: "d2", FileSize: 12}}
	env := MediaEnvelope{FileID: "d2", MIME: "application/octet-stream", Size: 12}
	inbox, replied, err := g.stageTelegramFile(context.Background(), upd(8), noName, user, env, noName.Document, "in-8", t.TempDir(), want("media.save-failed"))
	if err != nil || inbox != "" || !replied {
		t.Fatalf("stageTelegramFile=(%q,%v,%v) want sniff rejection", inbox, replied, err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) == 0 || fake.sent[len(fake.sent)-1] != want("media.save-failed") {
		t.Errorf("sniff rejection=%q want %q", fake.sent[len(fake.sent)-1], want("media.save-failed"))
	}

	joined := strings.Join(fake.sent, "\n")
	for _, w := range []string{
		want("media.voice-rejected"), want("media.stt-missing"), want("media.download-failed"),
		want("media.stt-failed"), want("media.file-too-big"), want("media.save-failed"),
		want("media.task-failed"),
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("sent missing %q", w)
		}
	}

	// Slack secret interception replies in the mapped user's language.
	g.slackUsers["t1/u1"] = user
	if err := g.ingestSlackEvent(context.Background(), "T1", "ev-loc", "message", "", "U1", "OPENAI_API_KEY=zzz", "D001", "im", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(c.SpoolDir, "outbox", "pending", commodel.FileID("slack-ev-loc-secret")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var d Delivery
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if d.Text != want("slack.secret-rejected") {
		t.Errorf("slack secret text=%q want %q", d.Text, want("slack.secret-rejected"))
	}

	// An admin hold notice renders in the held principal's language.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/holds":
			_, _ = w.Write([]byte(`[{"principal_id":"alice","context_id":"alice","active":true,"epoch":1,"reason":"manual","notice_id":"hold-x","notice_pending":true}]`))
		default:
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()
	c2 := baseTestConfig(t)
	pinLang(t, c2.Users[0].WorkspaceDir, locale.LangES)
	c2.Supervised, c2.RuntimeURL, c2.RuntimeAuth = true, srv.URL, "token"
	g2, err := New(c2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g2.syncMasterNotices(context.Background()); err != nil {
		t.Fatal(err)
	}
	hold, err := os.ReadFile(filepath.Join(c2.SpoolDir, "outbox", "pending", commodel.FileID("hold-x")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var hd Delivery
	if err := json.Unmarshal(hold, &hd); err != nil {
		t.Fatal(err)
	}
	if hd.Text != want("hold.notice", "manual") {
		t.Errorf("hold text=%q want %q", hd.Text, want("hold.notice", "manual"))
	}
}

// MCP prepare outcome notices localize to the caller's workspace language.
// prepareContinuation stays model-facing (phase 5) and is not in this table.
func TestPrepareOutcomeNoticesLocalized(t *testing.T) {
	want := func(key string, args ...any) string { return wantText(t, locale.LangES, key, args...) }
	repo := "repo-x"
	form := "http://127.0.0.1:1/credentials/f?k=1"
	cases := []struct {
		req   prepareOutcomeRequest
		login string
		want  string
	}{
		{prepareOutcomeRequest{Event: "prepare-started", Repository: repo}, "", want("prepare.started", repo)},
		{prepareOutcomeRequest{Event: "credentials-check"}, "", want("prepare.creds-check")},
		{prepareOutcomeRequest{Event: "credentials-rejected", Repository: repo, Detail: "bad"}, "", want("prepare.creds-rejected", repo) + want("prepare.reason", "bad") + want("prepare.creds-rejected-tail")},
		{prepareOutcomeRequest{Event: "credentials-rejected", Repository: repo}, "", want("prepare.creds-rejected", repo) + want("prepare.creds-rejected-tail")},
		{prepareOutcomeRequest{Event: "binding-failed", Repository: repo, Detail: "x"}, "", want("prepare.binding-failed", repo) + want("prepare.reason", "x") + want("prepare.binding-failed-tail")},
		{prepareOutcomeRequest{FormURL: "http://127.0.0.1:1/remove/z?k=1", Repository: repo}, "", want("prepare.remove-confirm", repo, "http://127.0.0.1:1/remove/z?k=1")},
		{prepareOutcomeRequest{Phase: "awaiting-credentials", CredentialIntake: "telegram_qr", Repository: repo, FormURL: form}, "http://127.0.0.1:9/tg?t=1", want("prepare.tg-qr", "http://127.0.0.1:9/tg?t=1") + want("prepare.form-fallback", form)},
		{prepareOutcomeRequest{Phase: "awaiting-credentials", CredentialIntake: "telegram_qr", Repository: repo}, "http://127.0.0.1:9/tg?t=1", want("prepare.tg-qr", "http://127.0.0.1:9/tg?t=1")},
		{prepareOutcomeRequest{Phase: "awaiting-credentials", Repository: repo, FormURL: form}, "", want("prepare.image-ready", repo) + want("prepare.form-open", form) + want("prepare.no-token")},
		{prepareOutcomeRequest{Phase: "awaiting-credentials", Repository: repo}, "", want("prepare.image-ready", repo) + want("prepare.form-next") + want("prepare.no-token")},
		{prepareOutcomeRequest{Phase: "failed", Repository: repo, Detail: "d"}, "", want("prepare.failed", repo) + want("prepare.reason", "d")},
		{prepareOutcomeRequest{Phase: "awaiting-confirm", Repository: repo, FormURL: "http://127.0.0.1:1/confirm/c?k=1"}, "", want("prepare.ready-confirm", repo) + want("prepare.confirm-link", "http://127.0.0.1:1/confirm/c?k=1")},
		{prepareOutcomeRequest{Phase: "awaiting-confirm", Repository: repo, FormURL: "http://127.0.0.1:1/connect/w?k=1"}, "", want("prepare.ready-confirm", repo) + want("prepare.confirm-link", "http://127.0.0.1:1/connect/w?k=1")},
		{prepareOutcomeRequest{Phase: "awaiting-confirm", Repository: repo}, "", want("prepare.ready-confirm", repo) + want("prepare.continuing")},
		{prepareOutcomeRequest{Phase: "awaiting-oauth", Repository: repo, FormURL: "http://127.0.0.1:1/connect/o?k=1"}, "", want("prepare.oauth-wait", repo) + want("prepare.oauth-open", "http://127.0.0.1:1/connect/o?k=1")},
		{prepareOutcomeRequest{Phase: "awaiting-oauth", Repository: repo, FormURL: "http://127.0.0.1:1/device/d?k=1"}, "", want("prepare.oauth-wait", repo) + want("prepare.device-open", "http://127.0.0.1:1/device/d?k=1")},
		{prepareOutcomeRequest{Phase: "awaiting-oauth", Repository: repo}, "", want("prepare.oauth-wait", repo)},
		{prepareOutcomeRequest{Phase: "enabled", Repository: repo, Tools: 3}, "", want("prepare.enabled-tools", repo, 3)},
		{prepareOutcomeRequest{Phase: "enabled", Repository: repo}, "", want("prepare.enabled", repo)},
		{prepareOutcomeRequest{Phase: "weird", Repository: repo}, "", want("prepare.unknown-phase", repo, "weird")},
		{prepareOutcomeRequest{Event: "prepare-started"}, "", want("prepare.started", want("prepare.repo-fallback"))},
	}
	for i, tc := range cases {
		if got := prepareOutcomeNotice(locale.LangES, tc.req, tc.login); got != tc.want {
			t.Errorf("case %d = %q want %q", i, got, tc.want)
		}
	}

	// The browser-viewer notice is enqueued through deliverPrepareNotice.
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	viewer := "http://127.0.0.1:9/viewer/v?k=1"
	g.handleViewerNotice(rec, c.Users[0].Envelope(11), prepareOutcomeRequest{ConnectionID: "conn1", ViewerURL: viewer})
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer notice status %d", rec.Code)
	}
	files, err := filepath.Glob(filepath.Join(c.SpoolDir, "outbox", "pending", "viewer-conn1-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("viewer delivery missing: %v %v", files, err)
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var d Delivery
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if d.Text != want("prepare.viewer", viewer) {
		t.Errorf("viewer text=%q want %q", d.Text, want("prepare.viewer", viewer))
	}
}

// /broker-approve reports both broker outcomes in the workspace language:
// an accepted approval and a rejected/expired one.
func TestBrokerApproveLocalized(t *testing.T) {
	c := baseTestConfig(t)
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "broker.key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/req-ok/approve") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer broker.Close()
	c.BrokerApprove = credentialbroker.Config{URL: broker.URL, KeyFile: keyFile, KeyID: "hub", Issuer: "hermes-hub"}
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	ctx := context.Background()
	for i, text := range []string{"/broker-approve req-ok 1234", "/broker-approve req-bad 1234"} {
		if err := g.handleUpdate(ctx, langUpdate(1+i, 1+i, 11, text, "")); err != nil {
			t.Fatal(err)
		}
		g.deliverOne(ctx)
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "chat.creds-saved"),
		wantText(t, locale.LangES, "chat.creds-expired"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// Session-management commands localize their replies for a non-English
// workspace: usage hints, empty lists, missing-work notices.
func TestTaskCommandsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	for i, text := range []string{"/use", "/archived", "/stop", "/session"} {
		if err := g.handleUpdate(context.Background(), langUpdate(i+1, i+1, 11, text, "")); err != nil {
			t.Fatal(err)
		}
	}
	for range 8 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "task.use-usage"),
		wantText(t, locale.LangES, "task.archive-empty"),
		wantText(t, locale.LangES, "stop.none"),
		wantText(t, locale.LangES, "task.current", "default ("+wantText(t, locale.LangES, "label.primary")+")"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// Session lifecycle replies (/new, /use, /archive, /archived, /sessions,
// /delete) render in the workspace language: created/exists/auto-name,
// archive flow, restore, not-found, list markers and the switch hint.
func TestTaskLifecycleCommandsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	cmds := []string{
		"/new foo",      // created + selected
		"/new foo",      // already exists
		"/new",          // created + auto-name note
		"/archive foo",  // archived reply
		"/archived",     // archive list title + item
		"/use foo",      // restored from archive
		"/use nope",     // not found
		"/archive nope", // not found on addressed archive
		"/sessions",     // sessions title + current marker + hint
		"/delete foo",   // deleted
	}
	for i, text := range cmds {
		if err := g.handleUpdate(context.Background(), langUpdate(i+1, i+1, 11, text, "")); err != nil {
			t.Fatal(err)
		}
	}
	for range 20 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		"Sesión creada y seleccionada:",
		"ya existe",
		"se asignará automáticamente",
		"Restaurar: /use",
		"Sesiones archivadas:",
		"— archivada",
		"restaurada del archivo",
		"no encontrada",
		"Sesiones:\n-",
		"— actual",
		"Cambiar: /use",
		"la actual es default",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// /stop, /cancel and /approve success replies render in the workspace
// language: real spool jobs drive each branch to its localized answer.
func TestStopCancelApproveLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	env := c.Users[0].Envelope(11)
	job := Job{Envelope: env, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", ChatID: 11, TaskID: defaultTaskID, Text: "hi", CreatedAt: time.Now().UTC()}
	job.ID, job.IdempotencyKey = "job-stop", "job-stop-key"
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	job.ID, job.IdempotencyKey = "job-cancel", "job-cancel-key"
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/stop", "")); err != nil {
		t.Fatal(err)
	}
	job.ID, job.IdempotencyKey = "job-appr", "job-appr-key"
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:read", "read"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := g.spool.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"/cancel job-cancel", "/approve job-appr read once"} {
		if err := g.handleUpdate(context.Background(), langUpdate(i+2, i+2, 11, text, "")); err != nil {
			t.Fatal(err)
		}
	}
	for range 8 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		wantText(t, locale.LangES, "stop.stopping", "job-stop", "cancelled"),
		wantText(t, locale.LangES, "chat.cancel-ok", "job-cancel", "cancelled"),
		wantText(t, locale.LangES, "chat.approve-saved"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// /session tails — created date, pending auto-name, bound topic, session
// reference — render in the workspace language.
func TestSessionStatusTailsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := c.Users[0]
	task, _, err := g.spool.CreateTask(user, 11, 0, "foo", true)
	if err != nil {
		t.Fatal(err)
	}
	env := user.Envelope(11)
	env.ConversationID = task.ConversationID
	j := Job{Envelope: env, ID: "job-sess", IdempotencyKey: "sess-key", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", ChatID: 11, TaskID: task.TaskID, Text: "hi", CreatedAt: time.Now().UTC()}
	if _, err := g.spool.Enqueue(j); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.RecordOutcome(j.ID, RunOutcome{Status: "completed", SessionID: "sess-9"}); err != nil {
		t.Fatal(err)
	}
	task.TopicID, task.NameAuto = 42, true
	got := g.taskStatus(user, task)
	for _, want := range []string{
		"Creada ", "se asignará automáticamente", "Tema #42", "Sesión: sess-9",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status=%q missing %q", got, want)
		}
	}
}

// The additive tool-surface lines under /session come from the runtime's
// health payload; the hub renders their framing in the workspace language.
func TestSurfaceLinesLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tool_surface":"partial","missing_tools":["search"],"model_status":"model_not_found"}`))
	}))
	defer server.Close()
	g.config.RuntimeURL, g.config.RuntimeAuth = server.URL, "token"
	got := g.taskStatus(c.Users[0], Task{TaskID: defaultTaskID})
	for _, want := range []string{"No disponible:", "- search —", "no se encuentra en el catálogo"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status=%q missing %q", got, want)
		}
	}
}

// /usage renders in the workspace language: the args hint (selected by a
// typed result, not a string sniff), the measuring notice, the session
// report, journal totals and the quota wrapper.
func TestUsageCommandsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	if err := os.MkdirAll(c.SpoolDir, 0700); err != nil {
		t.Fatal(err)
	}
	entry := usageEntry{Version: 2, ScopeID: "user:alice", ContextID: "c", SessionID: "s", JobID: "j1", RunID: "r1", MeasuredAt: time.Now().UTC()}
	body, _ := json.Marshal(entry)
	if err := os.WriteFile(filepath.Join(c.SpoolDir, "usage.jsonl"), append(body, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	in, out, cr, cw := int64(10), int64(20), int64(1), int64(2)
	comp, ctxTok, ctxWin := int64(2), int64(100), int64(200)
	g.runner = &usageStubRunner{report: hubruntime.SessionUsage{SessionFound: true, InputTokens: &in, OutputTokens: &out, CacheReadTokens: &cr, CacheWriteTokens: &cw, Compactions: &comp, ContextTokens: &ctxTok, ContextWindow: &ctxWin}}
	for i, text := range []string{"/usage bogus", "/usage session", "/usage"} {
		if err := g.handleUpdate(context.Background(), langUpdate(i+1, i+1, 11, text, "")); err != nil {
			t.Fatal(err)
		}
	}
	g.usageWG.Wait()
	for range 8 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	for _, want := range []string{
		"Usa /usage [day|week|month]",
		"Midiendo consumo",
		"Registro: 1 intentos",
		"entrada 10, salida 20",
		"Compactaciones",
		"(50%)",
		"Límites de suscripción: fuente no configurada",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("sent=%q missing %q", joined, want)
		}
	}
}

// usageCommand's session-missing and runner-error answers localize too;
// the concurrent busy notice goes through the same workspace language.
func TestUsageCommandVariantsLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	task := Task{TaskID: defaultTaskID, ConversationID: "conv-x", Name: "x"}
	user := c.Users[0]
	g.runner = &usageStubRunner{report: hubruntime.SessionUsage{SessionFound: false}}
	if got := g.usageCommand(context.Background(), user, 11, task); !strings.Contains(got, "aún no existe") {
		t.Fatalf("no-session=%q", got)
	}
	g.runner = &usageStubRunner{err: errors.New("boom")}
	if got := g.usageCommand(context.Background(), user, 11, task); !strings.Contains(got, "no disponible: boom") {
		t.Fatalf("err=%q", got)
	}
	g.runner = &usageStubRunner{err: errors.New("HTTP 404")}
	if got := g.usageCommand(context.Background(), user, 11, task); !strings.Contains(got, "Reintenta /usage en un minuto") {
		t.Fatalf("transient=%q", got)
	}
	g.usageQueries.Add(2)
	defer g.usageQueries.Add(-2)
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/usage", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	joined := strings.Join(fake.sent, "\n")
	if !strings.Contains(joined, "ya está en curso") {
		t.Fatalf("busy=%q", joined)
	}
}

// /model renders the account view, the plain catalog list and every
// switch/degradation answer in the workspace language.
func TestModelCommandLocalized(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	user := c.Users[0]
	registryView := func(switchLevel string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
				_, _ = io.WriteString(w, `{"data":[{"id":"default"},{"id":"turbo"}]}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v1/model":
				_, _ = io.WriteString(w, `{"model":"turbo"}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v1/registry":
				fmt.Fprintf(w, `{"switch":%q,"models":[{"name":"default","ladder":[{"provider":"cliproxy","model":"m1"}]},{"name":"turbo","ladder":[{"provider":"cliproxy","model":"m2"},{"provider":"openai","model":"m3","down":true}]}],"accounts":[{"provider":"cliproxy","catalog":["m1","m2","extra-1"]},{"provider":"openai","down":true,"catalog":["m3"]}]}`, switchLevel)
			case r.Method == http.MethodPut && r.URL.Path == "/v1/model":
				var body struct {
					Model string `json:"model"`
				}
				decodeBody(r, &body)
				if body.Model == "turbo" {
					_, _ = io.WriteString(w, `{"model":"turbo"}`)
					return
				}
				w.WriteHeader(http.StatusForbidden)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}
	}
	relay := httptest.NewServer(registryView("catalog"))
	defer relay.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", relay.URL)
	t.Setenv("HUB_MODEL_RELAY_ADMIN", "k")
	out := modelCommand(context.Background(), user, "/model")
	for _, want := range []string{
		"Cuentas:", "— principal", "← actual", "más: cliproxy/extra-1", "(no disponible)",
		"Cambiar: /model <nombre> o <cuenta>/<modelo>.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("account view missing %q: %q", want, out)
		}
	}
	if out := modelCommand(context.Background(), user, "/model turbo"); !strings.Contains(out, "Modelo cambiado: turbo.") {
		t.Fatalf("switch=%q", out)
	}
	if out := modelCommand(context.Background(), user, "/model secret"); !strings.Contains(out, "Sin permiso para el modelo secret.") {
		t.Fatalf("denied=%q", out)
	}

	// Registry without accounts renders the plain ladder view.
	relay2 := httptest.NewServer(func(switchLevel string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
				_, _ = io.WriteString(w, `{"data":[{"id":"default"},{"id":"turbo"}]}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v1/model":
				_, _ = io.WriteString(w, `{"model":"default"}`)
			case r.Method == http.MethodGet && r.URL.Path == "/v1/registry":
				fmt.Fprintf(w, `{"switch":%q,"models":[{"name":"default","ladder":[{"provider":"cliproxy","model":"m1"},{"provider":"openai","model":"m2","down":true}]}]}`, switchLevel)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}
	}("none"))
	defer relay2.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", relay2.URL)
	out = modelCommand(context.Background(), user, "/model")
	for _, want := range []string{"— actual:", "(reserva, no disponible)", "Cambio de modelos deshabilitado."} {
		if !strings.Contains(out, want) {
			t.Fatalf("ladder view missing %q: %q", want, out)
		}
	}

	// No registry endpoint at all: plain catalog list.
	relay3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"default"},{"id":"turbo"}]}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/model" {
			_, _ = io.WriteString(w, `{"model":"turbo"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer relay3.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", relay3.URL)
	out = modelCommand(context.Background(), user, "/model")
	for _, want := range []string{"Modelos: default, turbo.", " Actual: turbo.", "Cambiar: /model <nombre>."} {
		if !strings.Contains(out, want) {
			t.Fatalf("list view missing %q: %q", want, out)
		}
	}

	// Dead relay, empty catalog and missing relay degrade in-language.
	t.Setenv("HUB_MODEL_RELAY_URL", "http://127.0.0.1:1")
	if out := modelCommand(context.Background(), user, "/model"); !strings.Contains(out, "no responde") {
		t.Fatalf("dead relay=%q", out)
	}
	relay4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer relay4.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", relay4.URL)
	if out := modelCommand(context.Background(), user, "/model"); !strings.Contains(out, "está vacía") {
		t.Fatalf("empty=%q", out)
	}
	t.Setenv("HUB_MODEL_RELAY_URL", "")
	if out := modelCommand(context.Background(), user, "/model"); !strings.Contains(out, "relé administrado") {
		t.Fatalf("no relay=%q", out)
	}
}

// /language is itself the picker surface: on a fresh workspace the
// first-contact nudge defers to it instead of stacking a second keyboard.
func TestLanguageCommandSendsOnePicker(t *testing.T) {
	g, err := New(baseTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.handleUpdate(context.Background(), langUpdate(1, 1, 11, "/language", "")); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	if len(fake.keyboards) != 1 {
		t.Fatalf("expected exactly one picker keyboard, got %d", len(fake.keyboards))
	}
}

// The ops wiring stamps the owner's workspace language into commops.Owner so
// /status health text and canary notices render in that language.
func TestOpsWiringUsesWorkspaceLang(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	if got := g.healthLines(c.Users[0]); !strings.Contains(got, wantText(t, locale.LangES, "ops.health-empty")) {
		t.Fatalf("health %q", got)
	}
	g.recordCanary(context.Background(), time.Now(), "timeout")
	var texts []string
	for {
		d, err := g.spool.ClaimDelivery()
		if err != nil || d == nil {
			break
		}
		texts = append(texts, d.Text)
	}
	want := wantText(t, locale.LangES, "ops.canary-failing", "timeout")
	if len(texts) != 1 || !strings.Contains(texts[0], want) {
		t.Fatalf("notices=%v want %q", texts, want)
	}
}

// The routines engine resolves notice language through the spool's principal
// resolver — a blocked schedule reaches the owner already localized.
func TestRoutineBlockedNoticeLangWired(t *testing.T) {
	c := baseTestConfig(t)
	pinLang(t, c.Users[0].WorkspaceDir, locale.LangES)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	item := commodel.Schedule{ScheduleID: "r-one", Envelope: c.Users[0].Envelope(11), Channel: "telegram_bot"}
	if err := g.spool.rt.BlockScheduleLocked(&item, "bad cron", g.now()); err != nil {
		t.Fatal(err)
	}
	d, err := g.spool.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("no blocked delivery: %v", err)
	}
	if !strings.Contains(d.Text, strings.Split(wantText(t, locale.LangES, "routines.blocked", "r-one", "bad cron"), "bad cron")[0]) {
		t.Fatalf("notice %q", d.Text)
	}
}
