package communication

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/secrets"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOpenCredentialSurfaceSkipsLocalStoreWhenBrokerApprovalIsConfigured(t *testing.T) {
	service, ledger, err := openCredentialSurface(Config{
		CredentialStore: filepath.Join(t.TempDir(), "must-not-open"),
		BrokerApprove:   credentialbroker.Config{URL: "https://broker.example"},
	})
	if err != nil || service != nil || ledger != nil {
		t.Fatalf("credential surface was opened: service=%v ledger=%v err=%v", service != nil, ledger != nil, err)
	}
}

type fakeAPI struct {
	mu       sync.Mutex
	updates  []Update
	sent     []string
	voices   []string
	deleted  []int
	fileSize int64
	err      error
}

func (f *fakeAPI) GetUpdates(context.Context, int64, int) ([]Update, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updates, f.err
}
func (f *fakeAPI) SendMessage(_ context.Context, _ int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return f.err
}
func (f *fakeAPI) DeleteMessage(_ context.Context, _ int64, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return f.err
}
func (f *fakeAPI) SendChatAction(context.Context, int64, string) error { return f.err }
func (f *fakeAPI) GetFile(_ context.Context, fileID string) (TelegramFile, error) {
	size := f.fileSize
	if size == 0 {
		size = 12
	}
	return TelegramFile{FileID: fileID, FilePath: "voice/" + fileID, FileSize: size}, f.err
}
func (f *fakeAPI) DownloadFile(_ context.Context, _ string, w io.Writer) error {
	_, err := w.Write([]byte("voice-bytes"))
	if f.err != nil {
		return f.err
	}
	return err
}
func (f *fakeAPI) SendVoice(_ context.Context, _ int64, audio []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.voices = append(f.voices, string(audio))
	return f.err
}

type fakeRunner struct {
	mu   sync.Mutex
	seen []string
	jobs []Job
}

func (f *fakeRunner) Run(_ context.Context, job Job, user User) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, user.ID+":"+job.Text)
	f.jobs = append(f.jobs, job)
	return "answer for " + user.ID, nil
}

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "alice", "state"), 0700)
	_ = os.MkdirAll(filepath.Join(root, "alice", "workspace"), 0700)
	return Config{OrganizationID: "personal", TelegramToken: "token", SpoolDir: filepath.Join(root, "spool"), PollTimeout: time.Second, Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}, StateDir: filepath.Join(root, "alice", "state"), WorkspaceDir: filepath.Join(root, "alice", "workspace"), Features: []string{"workspace", "github"}, ConfiguredEnv: map[string]bool{"GITHUB_TOKEN": true}}}}
}

func TestConfigAndYAML(t *testing.T) {
	c := testConfig(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Config{
		{OrganizationID: "bad/id", TelegramToken: "x", SpoolDir: "x", Users: c.Users},
		{OrganizationID: "personal", TelegramToken: "x", SpoolDir: "", Users: c.Users},
		{OrganizationID: "personal", TelegramToken: "x", SpoolDir: "x", Users: nil},
		{OrganizationID: "personal", TelegramToken: "x", SpoolDir: "x", Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11, 11}, StateDir: "s", WorkspaceDir: "w"}}},
		{OrganizationID: "personal", TelegramToken: "x", SpoolDir: "x", Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}, StateDir: "users/alice", WorkspaceDir: "users/alice/workspace"}, {ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: "users/alice", WorkspaceDir: "users/bob/workspace"}}},
	} {
		if bad.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
	overlap := c
	overlap.Users = append(overlap.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: filepath.Join(filepath.Dir(c.Users[0].StateDir), "shared"), WorkspaceDir: c.Users[0].WorkspaceDir})
	if overlap.Validate() == nil {
		t.Fatal("overlapping user roots accepted")
	}
	for _, disabled := range [][]string{{"slack", "slack"}, {"not-a-service"}} {
		invalid := c
		invalid.Users = append([]User(nil), c.Users...)
		invalid.Users[0].PolicyDisabled = disabled
		if invalid.Validate() == nil {
			t.Fatal("invalid policy-disabled list accepted", disabled)
		}
	}
	path := filepath.Join(t.TempDir(), "communication.yaml")
	content := "organization_id: personal\nusers:\n  - id: alice\n    enabled: true\n    telegram_ids: [11]\n    state_dir: state\n    workspace_dir: workspace\nspool_dir: spool\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadYAML(path)
	if err != nil || loaded.Users[0].ID != "alice" {
		t.Fatal(loaded, err)
	}
	if err := os.WriteFile(path, append([]byte(content), []byte("---\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadYAML(path); err == nil {
		t.Fatal("multiple YAML documents accepted")
	}
}

func TestJobMappingIsDurableAndIdempotent(t *testing.T) {
	spool, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-one", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-one", Text: "hello", CreatedAt: time.Now().UTC()}
	if accepted, err := spool.Enqueue(job); err != nil || !accepted {
		t.Fatalf("enqueue=%v err=%v", accepted, err)
	}
	if accepted, err := spool.Enqueue(job); err != nil || accepted {
		t.Fatalf("duplicate enqueue=%v err=%v", accepted, err)
	}
	collision := job
	collision.ID, collision.Text = "job-two", "different"
	if _, err := spool.Enqueue(collision); err == nil {
		t.Fatal("idempotency collision accepted")
	}
	claimed, err := spool.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	if err := spool.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "completed", LastEvent: "run.completed", Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	if err := spool.CompleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	spool, err = NewSpool(spool.root)
	if err != nil {
		t.Fatal(err)
	}
	mapping, found, err := spool.Mapping(job.ID)
	if err != nil || !found || mapping.Status != "completed" || mapping.RunID != "run-1" || mapping.Result != "answer" {
		t.Fatalf("mapping=%+v found=%v err=%v", mapping, found, err)
	}
	conversation, found, err := spool.SessionFor("alice", "alice", "telegram-11")
	if err != nil || !found || conversation.SessionID != "session-1" {
		t.Fatalf("conversation=%+v found=%v err=%v", conversation, found, err)
	}
}

func TestJobOutcomeRejectsReplacementIdentityBeforeWriting(t *testing.T) {
	spool, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "fenced-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "fenced", Text: "hello"}
	if _, err := spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	initial := RunOutcome{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "running"}
	if err := spool.RecordOutcome(job.ID, initial); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"generation", "run", "session"} {
		bad := initial
		bad.Status, bad.Text = "completed", "stale answer"
		switch field {
		case "generation":
			bad.RuntimeGeneration = "gen-2"
		case "run":
			bad.RunID = "run-2"
		case "session":
			bad.SessionID = "session-2"
		}
		if err := spool.RecordOutcome(job.ID, bad); err == nil {
			t.Fatalf("accepted changed %s", field)
		}
	}
	mapping, _, err := spool.Mapping(job.ID)
	if err != nil || mapping.Status != "running" || mapping.Result != "" || mapping.RuntimeGeneration != "gen-1" {
		t.Fatalf("rejected outcome changed mapping: %+v err=%v", mapping, err)
	}
	conversation, _, err := spool.SessionFor("alice", "alice", job.ConversationID)
	if err != nil || conversation.SessionID != "session-1" {
		t.Fatalf("rejected outcome changed session: %+v err=%v", conversation, err)
	}
}

func TestJobMappingRecoversLegacyFilesAndFailsClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(filepath.Join(root, "pending"), 0700); err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "legacy-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "legacy-idem", Text: "hello", CreatedAt: time.Now().UTC()}
	body, _ := json.Marshal(job)
	if err := os.WriteFile(filepath.Join(root, "pending", "legacy-job.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := spool.Mapping(job.ID); err != nil || !found {
		t.Fatalf("legacy mapping found=%v err=%v", found, err)
	}
	if _, found, err := spool.Mapping("missing"); err != nil || found {
		t.Fatalf("missing mapping found=%v err=%v", found, err)
	}
	if err := os.WriteFile(spool.mappingPath("bad"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := spool.Mapping("bad"); err == nil {
		t.Fatal("corrupt mapping accepted")
	}
	if _, found, err := spool.SessionFor("alice", "alice", "missing"); err != nil || found {
		t.Fatalf("missing conversation found=%v err=%v", found, err)
	}
}

func TestMappedRunningJobBecomesUncertainOnRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "running-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "running-idem", Text: "hello", CreatedAt: time.Now().UTC()}
	if _, err := spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	mapping, found, err := restarted.Mapping(job.ID)
	if err != nil || !found || mapping.Status != "uncertain" || mapping.LastKnownEvent != "run.unknown" {
		t.Fatalf("mapping=%+v found=%v err=%v", mapping, found, err)
	}
	if retry, err := restarted.ClaimJob(); err != nil || retry != nil {
		t.Fatalf("uncertain job retried: job=%+v err=%v", retry, err)
	}
}

func TestJobMappingRejectsMismatchedOutcomeAndCorruptConversation(t *testing.T) {
	spool, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "mapping-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "mapping-idem", Text: "hello", CreatedAt: time.Now().UTC()}
	if _, err := spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if err := spool.RecordOutcome(job.ID, RunOutcome{JobID: "other-job"}); err == nil {
		t.Fatal("mismatched runtime job accepted")
	}
	if err := spool.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, SessionID: "session-1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spool.conversationPath("alice", "alice", "telegram-11"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := spool.SessionFor("alice", "alice", "telegram-11"); err == nil {
		t.Fatal("corrupt conversation mapping accepted")
	}
}

func TestConfigFromEnvAndIDs(t *testing.T) {
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_ORGANIZATION_ID", "acme")
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11,22")
	t.Setenv("HUB_FEATURES", "workspace,github")
	t.Setenv("GITHUB_TOKEN", "secret")
	c, err := ConfigFromEnv()
	if err != nil || len(c.Users) != 1 || len(c.Users[0].TelegramIDs) != 2 || !c.Users[0].ConfiguredEnv["GITHUB_TOKEN"] {
		t.Fatal(c, err)
	}
	if _, ok := c.Users[0].Env["TELEGRAM_BOT_TOKEN"]; ok {
		t.Fatal("gateway bot token leaked into Hermes environment")
	}
	for _, raw := range []string{"", "*", "11,11", "0"} {
		t.Setenv("TELEGRAM_ALLOWED_USERS", raw)
		if _, err := ConfigFromEnv(); err == nil {
			t.Fatal("invalid IDs accepted", raw)
		}
	}
	t.Setenv("OTHER_SECRET", "must-not-leak")
	env := strings.Join(processEnv(map[string]string{"OPENAI_API_KEY": "alice-key", "TELEGRAM_BOT_TOKEN": "bot-secret", "bad-key": "ignored"}), "\n")
	if !strings.Contains(env, "OPENAI_API_KEY=alice-key") || strings.Contains(env, "OTHER_SECRET") || strings.Contains(env, "TELEGRAM_BOT_TOKEN") || strings.Contains(env, "bad-key") {
		t.Fatal("Hermes environment is not scoped", env)
	}
	for key, value := range map[string]string{"JIRA_URL": "https://jira.example", "JIRA_USERNAME": "alice@example.com", "JIRA_API_TOKEN": "jira-token", "GITLAB_HOST": "gitlab.example", "GOOGLE_EMAIL": "alice@example.com", "GOOGLE_OAUTH_REDIRECT_URI": "http://localhost", "HH_USER_AGENT": "hub/test", "SLACK_MCP_ADD_MESSAGE_TOOL": "true"} {
		t.Setenv(key, value)
	}
	scoped := runtimeEnv([]string{"atlassian", "gitlab", "google", "workspace", "hh", "slack"})
	for key := range map[string]bool{"JIRA_URL": true, "JIRA_USERNAME": true, "JIRA_API_TOKEN": true, "GITLAB_HOST": true, "GOOGLE_EMAIL": true, "GOOGLE_OAUTH_REDIRECT_URI": true, "HH_USER_AGENT": true, "SLACK_MCP_ADD_MESSAGE_TOOL": true} {
		if scoped[key] == "" {
			t.Fatal("feature environment was not selected", key)
		}
	}
	if scoped["TELEGRAM_BOT_TOKEN"] != "" {
		t.Fatal("gateway token selected for Hermes")
	}
}

func TestConfigFromYAMLEnvBranch(t *testing.T) {
	root := t.TempDir()
	state, workspace, spool := filepath.Join(root, "state"), filepath.Join(root, "workspace"), filepath.Join(root, "spool")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "communication.yaml")
	content := "organization_id: personal\nusers:\n  - id: alice\n    enabled: true\n    telegram_ids: [11]\n    state_dir: " + state + "\n    workspace_dir: " + workspace + "\nspool_dir: " + spool + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_COMMUNICATION_CONFIG", path)
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	c, err := ConfigFromEnv()
	if err != nil || c.Users[0].ID != "alice" || c.TelegramToken != "token" {
		t.Fatal(c, err)
	}
}

func TestSpoolDurabilityAndSecretIsolation(t *testing.T) {
	spool, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{ID: "job1", UserID: "alice", Text: "hello", CreatedAt: time.Now()}
	if ok, err := spool.Enqueue(job); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := spool.Enqueue(job); err != nil || ok {
		t.Fatal("duplicate accepted", ok, err)
	}
	claimed, err := spool.ClaimJob()
	if err != nil || claimed.Text != "hello" {
		t.Fatal(claimed, err)
	}
	if err := spool.CompleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	crashed := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "crashed", UserID: "alice", Text: "retry me", CreatedAt: time.Now()}
	if _, err := spool.Enqueue(crashed); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if recovered, err := NewSpool(spool.root); err != nil {
		t.Fatal(err)
	} else if retry, err := recovered.ClaimJob(); err != nil || retry.ID != crashed.ID || retry.RuntimeID != crashed.RuntimeID || retry.PolicyVersion != crashed.PolicyVersion {
		t.Fatal(retry, err)
	} else if err := recovered.CompleteJob(crashed.ID); err != nil {
		t.Fatal(err)
	}
	secret := Job{ID: "secret1", UserID: "alice", Text: "GITHUB_TOKEN=super-secret", Sensitive: true, CreatedAt: time.Now()}
	if _, err := spool.Enqueue(secret); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(spool.root, "pending", "secret1.json"))
	if err != nil || strings.Contains(string(b), "super-secret") || !strings.Contains(string(b), "text_sha256") {
		t.Fatal(string(b), err)
	}
	claimed, err = spool.ClaimJob()
	if err != nil || claimed.Text != secret.Text {
		t.Fatal(claimed, err)
	}
	if err := spool.FailJob(secret.ID); err != nil {
		t.Fatal(err)
	}
	if err := spool.EnqueueDelivery(Delivery{ID: "delivery1", ChatID: 11, Text: "answer"}); err != nil {
		t.Fatal(err)
	}
	delivery, err := spool.ClaimDelivery()
	if err != nil || delivery.ID != "delivery1" {
		t.Fatal(delivery, err)
	}
	if err := spool.UncertainDelivery(delivery.ID); err != nil {
		t.Fatal(err)
	}
	if err := spool.EnqueueDelivery(Delivery{ID: "uncertain-restart", ChatID: 11, Text: "reply"}); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.ClaimDelivery(); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewSpool(spool.root)
	if err != nil {
		t.Fatal(err)
	}
	if retry, err := recovered.ClaimDelivery(); err != nil || retry != nil {
		t.Fatal("uncertain delivery was requeued", retry, err)
	}
	if _, err := os.Stat(filepath.Join(spool.root, "outbox", "failed", "uncertain-restart.json")); err != nil {
		t.Fatal("uncertain delivery was not retained as failed", err)
	}
	if err := spool.SaveOffset(9); err != nil {
		t.Fatal(err)
	}
	if offset, err := spool.LoadOffset(); err != nil || offset != 9 {
		t.Fatal(offset, err)
	}
	if _, err := spool.Enqueue(Job{ID: "restart-secret", UserID: "alice", Text: "TOKEN=lost", Sensitive: true}); err != nil {
		t.Fatal(err)
	}
	second, err := NewSpool(spool.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.ClaimJob(); err == nil {
		t.Fatal("sensitive job survived restart without payload")
	}
}

func TestGatewayRoutesCommandsJobsAndDeletesSensitiveInput(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &fakeRunner{}
	g.api, g.runner = fake, runner
	g.now = func() time.Time { return time.Unix(100, 0) }
	base := func(updateID, messageID int, text string, sender int64) Update {
		return Update{UpdateID: updateID, Message: &Message{MessageID: messageID, From: &TGUser{ID: sender}, Chat: TGChat{ID: sender, Type: "private"}, Text: text}}
	}
	if err := g.handleUpdate(context.Background(), base(1, 1, "/start", 11)); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), base(2, 2, "/status", 11)); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), base(3, 3, "/connections", 11)); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), base(4, 4, "hello", 11)); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), base(5, 5, "GITHUB_TOKEN=super-secret", 11)); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), base(7, 7, "JIRA_URL=https://jira.example.atlassian.net\nJIRA_USERNAME=owner@example.com\nJIRA_API_TOKEN=secret-token", 11)); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), base(6, 6, "hello", 99)); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		g.deliverOne(context.Background())
	}
	for range 10 {
		if job, _ := g.spool.ClaimJob(); job != nil {
			response, err := g.runner.Run(context.Background(), *job, g.user(job.UserID))
			if err != nil {
				t.Fatal(err)
			}
			if job.Sensitive {
				_ = g.api.DeleteMessage(context.Background(), job.ChatID, job.MessageID)
			}
			_ = g.spool.CompleteJob(job.ID)
			_ = g.spool.EnqueueDelivery(Delivery{ID: "reply-" + job.ID, ChatID: job.ChatID, Text: response})
		}
	}
	for range 10 {
		g.deliverOne(context.Background())
	}
	if len(runner.seen) != 1 || len(fake.deleted) != 2 || len(fake.sent) != 7 {
		t.Fatalf("seen=%v deleted=%v sent=%v", runner.seen, fake.deleted, fake.sent)
	}
	if strings.Contains(strings.Join(runner.seen, "\n"), "GITHUB_TOKEN=") || strings.Contains(strings.Join(fake.sent, "\n"), "super-secret") || strings.Contains(strings.Join(fake.sent, "\n"), "secret-token") {
		t.Fatal("chat secret reached Hermes or replies")
	}
	for _, job := range runner.jobs {
		if job.PrincipalID != "alice" || job.ExternalIdentityID != "telegram-11" || job.ContextID != "alice" || job.RuntimeID != "alice" || job.ConversationID != "telegram-11" || job.DeliveryTargetID != "telegram-11" || job.PolicyVersion != "policy-1" {
			t.Fatalf("bad identity envelope: %+v", job.Envelope)
		}
	}
	if got := g.userByChatID(9999); got.ID != "" {
		t.Fatal("unknown Telegram chat resolved to a user")
	}
	joined := strings.Join(fake.sent, "\n")
	if !strings.Contains(joined, "github — готово") || !strings.Contains(joined, "не настроен") {
		t.Fatal(fake.sent)
	}
	c.Users[0].PolicyDisabled = []string{"slack"}
	if !strings.Contains(connectionList(c.Users[0]), "slack — policy-disabled") {
		t.Fatal("policy-disabled connection was not reported")
	}
}

func TestSignalRestartAfterDeliveryRequiresPendingRequestAndSafeMarker(t *testing.T) {
	state := t.TempDir()
	if err := signalRestartAfterDelivery(state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "restart.request"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), []byte(`{"pids":[1]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := signalRestartAfterDelivery(state); err == nil {
		t.Fatal("unsafe runtime marker accepted")
	}
}

func TestTelegramAPIAndRunnerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bottoken/getUpdates" {
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		if r.URL.Path == "/bottoken/sendMessage" || r.URL.Path == "/bottoken/deleteMessage" {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer server.Close()
	api := newTelegramAPI(server.URL, "token", time.Second)
	if updates, err := api.GetUpdates(context.Background(), 1, 1); err != nil || len(updates) != 0 {
		t.Fatal(updates, err)
	}
	if err := api.SendMessage(context.Background(), 1, "hi"); err != nil {
		t.Fatal(err)
	}
	if err := api.DeleteMessage(context.Background(), 1, 2); err != nil {
		t.Fatal(err)
	}
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/bottoken/getFile":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"Aw1","file_path":"voice/file.ogg","file_size":12}}`))
		case r.URL.Path == "/file/bottoken/voice/file.ogg":
			_, _ = w.Write([]byte("audio-bytes"))
		case r.URL.Path == "/bottoken/sendVoice", r.URL.Path == "/bottoken/sendChatAction":
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false}`))
		}
	}))
	defer media.Close()
	files := newTelegramAPI(media.URL, "token", time.Second)
	got, err := files.GetFile(context.Background(), "Aw1")
	if err != nil || got.FilePath != "voice/file.ogg" {
		t.Fatal(got, err)
	}
	var buf strings.Builder
	if err := files.DownloadFile(context.Background(), got.FilePath, &buf); err != nil || buf.String() != "audio-bytes" {
		t.Fatal(buf.String(), err)
	}
	if err := files.DownloadFile(context.Background(), "../escape", io.Discard); err == nil {
		t.Fatal("escaped telegram file path")
	}
	if err := files.SendVoice(context.Background(), 11, []byte("ogg"), "caption"); err != nil {
		t.Fatal(err)
	}
	if err := files.SendVoice(context.Background(), 11, nil, ""); err == nil {
		t.Fatal("empty voice accepted")
	}
	if err := files.SendChatAction(context.Background(), 11, "typing"); err != nil {
		t.Fatal(err)
	}
	config := testConfig(t)
	runner := HermesRunner{Command: filepath.Join(t.TempDir(), "missing")}
	if _, err := runner.Run(context.Background(), Job{}, config.Users[0]); err == nil {
		t.Fatal("empty prompt accepted")
	}
	if _, err := runner.Run(context.Background(), Job{Text: "hello"}, config.Users[0]); err == nil {
		t.Fatal("missing command accepted")
	}
	if limitTelegramText(strings.Repeat("x", 5000)) != strings.Repeat("x", 4000) || limitTelegramText("ok") != "ok" {
		t.Fatal("telegram text limit broken")
	}
	var check map[string]any
	if err := json.Unmarshal([]byte(`{"ok":true}`), &check); err != nil {
		t.Fatal(err)
	}
	if newID() == "" {
		t.Fatal("empty ID")
	}
}

func TestSpoolAndTelegramErrorPaths(t *testing.T) {
	root := t.TempDir()
	spool, err := NewSpool(filepath.Join(root, "spool"))
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.SaveOffset(-1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if err := os.WriteFile(filepath.Join(spool.root, "offset.json"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.LoadOffset(); err == nil {
		t.Fatal("invalid offset accepted")
	}
	if err := spool.EnqueueDelivery(Delivery{ID: "bad:id", ChatID: 11, Text: "reply"}); err != nil {
		t.Fatal(err)
	}
	if err := spool.EnqueueDelivery(Delivery{ID: "bad:id", ChatID: 11, Text: "duplicate"}); err != nil {
		t.Fatal(err)
	}
	if err := spool.EnqueueDelivery(Delivery{ID: "bad-delivery", ChatID: 11, DeliveryTargetID: "telegram-22", Text: "reply"}); err == nil {
		t.Fatal("cross-audience delivery accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()
	api := newTelegramAPI(server.URL, "token", 0)
	if _, err := api.GetUpdates(context.Background(), 0, 1); err == nil {
		t.Fatal("Telegram HTTP error accepted")
	}
	config := testConfig(t)
	config.TelegramToken = ""
	if _, err := New(config); err == nil {
		t.Fatal("missing Telegram token accepted")
	}
	config = testConfig(t)
	config.Users[0].StateDir = filepath.Join(root, "missing")
	if _, err := New(config); err == nil {
		t.Fatal("missing user root accepted")
	}
}

func TestGatewayInterceptsSecretsAndRejectsGroups(t *testing.T) {
	c := testConfig(t)
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	c.CredentialStore = filepath.Join(t.TempDir(), "store.enc")
	c.CredentialKeyFile = keyFile
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &fakeRunner{}
	g.api, g.runner = fake, runner
	secret := "group-or-chat-secret-value"
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{MessageID: 9, From: &TGUser{ID: 11}, Chat: TGChat{ID: 99, Type: "group"}, Text: "GOOGLE_TOKEN=" + secret}}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 2, Message: &Message{MessageID: 10, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "GOOGLE_TOKEN=" + secret}}); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	if job, _ := g.spool.ClaimJob(); job != nil {
		t.Fatalf("secret job enqueued: %+v", job)
	}
	if len(runner.seen) != 0 {
		t.Fatalf("hermes saw %v", runner.seen)
	}
	joined := strings.Join(fake.sent, "\n")
	if strings.Contains(joined, secret) || !strings.Contains(joined, "Группы не принимают секреты") || !strings.Contains(joined, "GOOGLE_TOKEN") {
		t.Fatalf("sent=%v", fake.sent)
	}
	listed, err := g.secrets.List("alice")
	if err != nil || len(listed) != 1 || listed[0].Name != "GOOGLE_TOKEN" {
		t.Fatalf("stored=%+v err=%v", listed, err)
	}
}

type correlatingRunner struct {
	runID string
}

func (c correlatingRunner) Run(_ context.Context, job Job, _ User) (string, error) {
	return "answer for " + job.UserID, nil
}

func (c correlatingRunner) RunOutcome(_ context.Context, job Job) (RunOutcome, error) {
	return RunOutcome{Text: "answer for " + job.UserID, JobID: job.ID, RunID: c.runID, Status: "completed", LastEvent: "run.completed"}, nil
}

func TestWorkerAppendsJobCorrelation(t *testing.T) {
	c := testConfig(t)
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	c.CredentialStore = filepath.Join(t.TempDir(), "store.enc")
	c.CredentialKeyFile = keyFile
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	g.runner = correlatingRunner{runID: "run-worker"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	if _, err := g.spool.Enqueue(Job{ID: "worker-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", ChatID: 11, Text: "hello", Envelope: identity.TelegramEnvelope("alice", 11, "runtime", "policy-1")}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g.audit != nil {
			events, err := g.audit.List("alice")
			if err == nil && len(events) > 0 {
				cancel()
				<-done
				found := false
				for _, event := range events {
					if event.Kind == "job" && event.JobID == "worker-job" && event.HermesRunID == "run-worker" {
						found = true
					}
				}
				if !found {
					t.Fatalf("job correlation missing: %+v", events)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("worker did not append job correlation")
}

type chatStopRecorder struct{ ids []string }

func (s *chatStopRecorder) Stop(id string) error {
	s.ids = append(s.ids, id)
	return nil
}

type chatBearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t chatBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyRequest.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copyRequest)
}

type chatCallCounter struct{ n *atomic.Int32 }

func (c chatCallCounter) Call(context.Context, toolhub.EffectiveBinding, toolhub.ToolSpec, map[string]any) (toolhub.BackendResult, error) {
	c.n.Add(1)
	return toolhub.BackendResult{Text: "ok"}, nil
}

func TestGatewayChatCutsOpenToolHubSession(t *testing.T) {
	c := testConfig(t)
	key, err := credstore.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := credstore.WriteKeyFile(keyFile, key); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "store.enc")
	svc, err := secrets.Open(storePath, keyFile, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	infos, err := svc.Set("alice", map[string]string{"GOOGLE_TOKEN": "chat-cut-secret"})
	if err != nil {
		t.Fatal(err)
	}
	registry := toolhub.NewStore()
	definition := toolhub.ToolDefinition{
		Schema: toolhub.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: toolhub.RemoteMCP,
		Source:      toolhub.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:       []toolhub.ToolSpec{{Name: "search", Effect: toolhub.ReadEffect}},
		Credentials: []toolhub.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}},
		Workload:    toolhub.WorkloadPolicy{Class: toolhub.PerUser, Rationale: "OAuth account and provider session are user-owned"},
		Execution:   toolhub.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:      toolhub.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := registry.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := toolhub.CredentialReference{Schema: toolhub.SchemaVersion, CredentialRefID: toolhub.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: infos[0].Locator, Keys: []string{"GOOGLE_TOKEN"}, Status: toolhub.ActiveStatus}
	if err := registry.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := toolhub.Connection{Schema: toolhub.SchemaVersion, ConnectionID: "google-work", Owner: toolhub.OwnerRef{Type: toolhub.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: toolhub.ActiveStatus}
	if err := registry.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	binding := toolhub.ToolBinding{Schema: toolhub.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: toolhub.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := registry.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID = toolhub.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	workload, err := toolhub.NewWorkloadInstance(binding, &toolhub.OwnerRef{Type: toolhub.ContextOwner, ID: "alice"}, "", 1, time.Now().UTC(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status = toolhub.RunningStatus
	if err := registry.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	toolHubPath := filepath.Join(c.Users[0].StateDir, "runtime", "toolhub", "store.json")
	if err := registry.Save(toolHubPath); err != nil {
		t.Fatal(err)
	}
	c.CredentialStore = storePath
	c.CredentialKeyFile = keyFile
	c.ToolHubStore = toolHubPath
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	live, err := toolhub.Load(toolHubPath)
	if err != nil {
		t.Fatal(err)
	}
	stops := &chatStopRecorder{}
	live.Stopper = stops
	var calls atomic.Int32
	handler, err := (&toolhub.Gateway{
		Store:   live,
		Tokens:  map[string]identity.Envelope{"01234567890123456789012345678901": auth},
		Backend: chatCallCounter{n: &calls},
	}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "chat-entry-cut", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + toolhub.DefaultEndpointPath, HTTPClient: &http.Client{Transport: chatBearerTransport{base: http.DefaultTransport, token: "01234567890123456789012345678901"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	name := toolhub.ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "ok"}}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 9, Message: &Message{MessageID: 21, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "GOOGLE_TOKEN=rotated-chat-secret"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "after"}}); err == nil {
		t.Fatal("chat intercept did not cut open session")
	}
	if calls.Load() != 1 {
		t.Fatalf("backend ran after chat rotate: %d", calls.Load())
	}
}

func TestGatewayRejectsNonPrivateAndUnknownMessages(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	for _, update := range []Update{
		{UpdateID: 1, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "group"}, Text: "ignore"}},
		{UpdateID: 2, Message: &Message{From: &TGUser{ID: 0}, Chat: TGChat{ID: 11, Type: "private"}, Text: "ignore"}},
		{UpdateID: 3, Message: &Message{From: &TGUser{ID: 99}, Chat: TGChat{ID: 99, Type: "private"}, Text: "hello"}},
	} {
		if err := g.handleUpdate(context.Background(), update); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		g.deliverOne(context.Background())
	}
	if len(fake.sent) != 2 || !strings.Contains(fake.sent[0], "не настроен") {
		t.Fatal(fake.sent)
	}
}

func TestGatewayRunPersistsOffset(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	api := &runAPI{update: Update{UpdateID: 42, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "hello"}}, cancel: cancel}
	g.api = api
	if err := g.Run(ctx); err != context.Canceled {
		t.Fatal(err)
	}
	if offset, err := g.spool.LoadOffset(); err != nil || offset != 43 {
		t.Fatal(offset, err)
	}
}

type runAPI struct {
	mu     sync.Mutex
	update Update
	calls  int
	cancel context.CancelFunc
}

func (r *runAPI) GetUpdates(ctx context.Context, _ int64, _ int) ([]Update, error) {
	r.mu.Lock()
	r.calls++
	calls := r.calls
	r.mu.Unlock()
	if calls == 1 {
		return []Update{r.update}, nil
	}
	r.cancel()
	<-ctx.Done()
	return nil, ctx.Err()
}
func (r *runAPI) SendMessage(context.Context, int64, string) error { return nil }
func (r *runAPI) DeleteMessage(context.Context, int64, int) error  { return nil }
func (r *runAPI) SendChatAction(context.Context, int64, string) error {
	return nil
}
func (r *runAPI) GetFile(context.Context, string) (TelegramFile, error) {
	return TelegramFile{}, nil
}
func (r *runAPI) DownloadFile(context.Context, string, io.Writer) error  { return nil }
func (r *runAPI) SendVoice(context.Context, int64, []byte, string) error { return nil }

func TestWorkerRunsWithIsolatedUserAndHandlesError(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &fakeRunner{}
	g.api, g.runner = fake, runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	if _, err := g.spool.Enqueue(Job{ID: "worker-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", ChatID: 11, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		done := len(fake.sent) > 0
		fake.mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.mu.Lock()
	seen := append([]string(nil), runner.seen...)
	runner.mu.Unlock()
	if len(seen) != 1 || !strings.HasPrefix(seen[0], "alice:") {
		t.Fatal(seen)
	}
	cancel()
	<-done

	g.runner = errorRunner{}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	if _, err := g.spool.Enqueue(Job{ID: "error-job", UserID: "alice", ChatID: 11, Text: "again"}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(g.spool.root, "failed", "error-job.json")); err == nil {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("worker did not fail job")
}

type errorRunner struct{}

func (errorRunner) Run(context.Context, Job, User) (string, error) { return "", errors.New("fail") }

func TestHermesRunnerSuccess(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "helper.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Print(\"reply\")}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "helper")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if output, err := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Skipf("cannot build helper: %v (%s)", err, output)
	}
	state, workspace := filepath.Join(root, "state"), filepath.Join(root, "workspace")
	response, err := (HermesRunner{Command: bin, Timeout: 10 * time.Second}).Run(context.Background(), Job{Text: "hello"}, User{StateDir: state, WorkspaceDir: workspace})
	if err != nil || response != "reply" {
		t.Fatal(response, err)
	}
}
