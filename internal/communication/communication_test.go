package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeAPI struct {
	mu      sync.Mutex
	updates []Update
	sent    []string
	deleted []int
	err     error
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

type fakeRunner struct {
	mu   sync.Mutex
	seen []string
}

func (f *fakeRunner) Run(_ context.Context, job Job, user User) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, user.ID+":"+job.Text)
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
	crashed := Job{ID: "crashed", UserID: "alice", Text: "retry me", CreatedAt: time.Now()}
	if _, err := spool.Enqueue(crashed); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if recovered, err := NewSpool(spool.root); err != nil {
		t.Fatal(err)
	} else if retry, err := recovered.ClaimJob(); err != nil || retry.ID != crashed.ID {
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
	if len(runner.seen) != 3 || len(fake.deleted) != 2 || len(fake.sent) != 7 {
		t.Fatalf("seen=%v deleted=%v sent=%v", runner.seen, fake.deleted, fake.sent)
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
	if err := spool.EnqueueDelivery(Delivery{ID: "bad:id", Text: "reply"}); err != nil {
		t.Fatal(err)
	}
	if err := spool.EnqueueDelivery(Delivery{ID: "bad:id", Text: "duplicate"}); err != nil {
		t.Fatal(err)
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
	defer cancel()
	go g.worker(ctx)
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
	if _, err := g.spool.Enqueue(Job{ID: "error-job", UserID: "alice", ChatID: 11, Text: "again"}); err != nil {
		t.Fatal(err)
	}
	g.runner = errorRunner{}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(g.spool.root, "failed", "error-job.json")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	response, err := (HermesRunner{Command: bin, Timeout: time.Second}).Run(context.Background(), Job{Text: "hello"}, User{StateDir: state, WorkspaceDir: workspace})
	if err != nil || response != "reply" {
		t.Fatal(response, err)
	}
}
