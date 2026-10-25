package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// topicsConfig builds one configured topics-mode group: the group is a
// personal interface, every forum topic adopts a session inside the sender's
// own home — no dedicated space, no mention gate.
func topicsConfig(t *testing.T) Config {
	c := testConfig(t)
	c.Groups = []Group{{ChatID: -100777, Mode: "topics"}}
	return c
}

func groupGateway(t *testing.T, c Config) *Gateway {
	t.Helper()
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{username: "helper_bot"}
	return g
}

func TestGroupTopicsRunsInSenderHome(t *testing.T) {
	c := topicsConfig(t)
	// Sender 11 is the configured user of c.Users[0]; topic 42 must adopt a
	// task bound to that sender, not to a dedicated group space.
	g := groupGateway(t, c)
	err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{
		MessageID: 1, MessageThreadID: 42, IsTopicMessage: true,
		From: &TGUser{ID: 11}, Chat: TGChat{ID: -100777, Type: "supergroup"}, Text: "summarize this thread",
	}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("topics-mode message produced no job: %+v %v", job, err)
	}
	user := c.Users[0]
	if job.PrincipalID != user.ID || job.ScopeID != "user:"+user.ID {
		t.Fatalf("topics mode must run in the sender home, got %+v", job.Envelope)
	}
	if job.TaskID == "" || job.ConversationID == "" {
		t.Fatalf("topic adoption lost: %+v", job.Envelope)
	}
}

func TestGroupTopicsDistinctTopicsDistinctTasks(t *testing.T) {
	c := topicsConfig(t)
	g := groupGateway(t, c)
	send := func(id, topic int64) {
		t.Helper()
		if err := g.handleUpdate(context.Background(), Update{UpdateID: int(id), Message: &Message{
			MessageID: int(id), MessageThreadID: topic, IsTopicMessage: true,
			From: &TGUser{ID: 11}, Chat: TGChat{ID: -100777, Type: "supergroup"}, Text: "job it",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	send(1, 42)
	send(2, 43)
	j1, err := g.spool.ClaimJob()
	if err != nil || j1 == nil {
		t.Fatal("first topic job missing")
	}
	// The spool serializes one context at a time, so the second topic job is
	// still pending; read it straight from the queue.
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("second topic job missing: %v %v", entries, err)
	}
	body, err := os.ReadFile(filepath.Join(g.spool.root, "pending", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var j2 Job
	if err := json.Unmarshal(body, &j2); err != nil {
		t.Fatal(err)
	}
	if j1.TaskID == j2.TaskID || j1.ConversationID == j2.ConversationID {
		t.Fatalf("topics must get separate sessions: %+v vs %+v", j1.Envelope, j2.Envelope)
	}
}

func TestGroupTopicsDropsForeignSenders(t *testing.T) {
	g := groupGateway(t, topicsConfig(t))
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{
		MessageID: 1, From: &TGUser{ID: 99}, Chat: TGChat{ID: -100777, Type: "supergroup"}, Text: "hi",
	}}); err != nil {
		t.Fatal(err)
	}
	if job, err := g.spool.ClaimJob(); err != nil || job != nil {
		t.Fatalf("foreign sender activated a job: %+v %v", job, err)
	}
}

func TestGroupUnconfiguredStillDrops(t *testing.T) {
	// A group absent from config.Groups stays silent even for a configured
	// user — opt-in is the security boundary.
	g := groupGateway(t, testConfig(t))
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{
		MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100777, Type: "supergroup"}, Text: "@bot hi",
	}}); err != nil {
		t.Fatal(err)
	}
	if job, err := g.spool.ClaimJob(); err != nil || job != nil {
		t.Fatalf("unconfigured group activated a job: %+v %v", job, err)
	}
}

func TestGroupMapSyntax(t *testing.T) {
	c := testConfig(t)
	c.Users = append(c.Users, User{ID: "team", Enabled: true, StateDir: t.TempDir(), WorkspaceDir: t.TempDir()})
	body, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("groups:\n  -100777: topics\n  -100123:\n    space: team\n    members: [11]\n    mention: \"@helper\"\n")...)
	name := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(name, body, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadYAML(name)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Groups) != 2 {
		t.Fatalf("map form parsed %d groups", len(loaded.Groups))
	}
	byChat := map[int64]Group{}
	for _, group := range loaded.Groups {
		byChat[group.ChatID] = group
	}
	if byChat[-100777].Mode != "topics" {
		t.Fatalf("scalar mode not parsed: %+v", byChat[-100777])
	}
	if got := byChat[-100123]; got.Space != "team" || len(got.Members) != 1 || got.Mention != "@helper" {
		t.Fatalf("object form not parsed: %+v", got)
	}
}

func TestGroupMentionEmptyMembersAdmitsConfiguredUsers(t *testing.T) {
	c := testConfig(t)
	c.Users = append(c.Users, User{ID: "team", Enabled: true, StateDir: t.TempDir(), WorkspaceDir: t.TempDir()})
	c.Groups = []Group{{ChatID: -100123, Space: "team", Mention: "@helper"}}
	g := groupGateway(t, c)
	err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{
		MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "@helper go",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if job, err := g.spool.ClaimJob(); err != nil || job == nil {
		t.Fatal("configured user rejected by empty members list")
	}
	err = g.handleUpdate(context.Background(), Update{UpdateID: 2, Message: &Message{
		MessageID: 2, From: &TGUser{ID: 99}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "@helper go",
	}})
	if err != nil {
		t.Fatal(err)
	}
	// The first job still holds the context, so a wrongly-admitted second job
	// would sit in pending rather than be claimable.
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("unknown sender queued a job: %v %v", entries, err)
	}
}

func TestGroupModeValidation(t *testing.T) {
	c := testConfig(t)
	c.Users = append(c.Users, User{ID: "team", Enabled: true, StateDir: t.TempDir(), WorkspaceDir: t.TempDir()})
	write := func(extra string) string {
		t.Helper()
		body, err := yaml.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(t.TempDir(), "gateway.yaml")
		if err := os.WriteFile(name, append(body, []byte(extra)...), 0600); err != nil {
			t.Fatal(err)
		}
		return name
	}
	for name, tc := range map[string]struct {
		yaml    string
		wantErr bool
	}{
		"invalid mode":     {"groups:\n  -1: {chat_id: -1, mode: weird, space: team, members: [11], mention: \"@h\"}\n", true},
		"topics+space":     {"groups:\n  -1: {chat_id: -1, mode: topics, space: team}\n", true},
		"mention no space": {"groups:\n  -1: {chat_id: -1, mode: mention}\n", true},
		"mention ok":       {"groups:\n  -1: {chat_id: -1, mode: mention, space: team}\n", false},
		"topics ok":        {"groups:\n  -1: {chat_id: -1, mode: topics}\n", false},
	} {
		_, err := LoadYAML(write(tc.yaml))
		if tc.wantErr && err == nil {
			t.Fatalf("%s: unsafe group accepted", name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestTelegramGetMeParsesUsername(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Errorf("unexpected method %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"username":"helper_bot"}}`))
	}))
	defer server.Close()
	me, err := newTelegramAPI(server.URL, "token", time.Second).GetMe(context.Background())
	if err != nil || me.Username != "helper_bot" {
		t.Fatalf("getMe broken: %+v %v", me, err)
	}
}

func TestGroupMentionDefaultsToBotUsername(t *testing.T) {
	c := testConfig(t)
	c.Users = append(c.Users, User{ID: "team", Enabled: true, StateDir: t.TempDir(), WorkspaceDir: t.TempDir()})
	c.Groups = []Group{{ChatID: -100123, Space: "team", Members: []int64{11}}}
	g := groupGateway(t, c) // fakeAPI reports @helper_bot
	err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{
		MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "@helper_bot go",
	}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("bot-username mention not admitted")
	}
	if job.PrincipalID != "team" || job.ScopeID != "group:team" {
		t.Fatalf("mention mode scope changed: %+v", job.Envelope)
	}
}

func TestGroupTopicsBindingKeepsPersonalScope(t *testing.T) {
	g := groupGateway(t, topicsConfig(t))
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{
		MessageID: 1, MessageThreadID: 42, IsTopicMessage: true,
		From: &TGUser{ID: 11}, Chat: TGChat{ID: -100777, Type: "supergroup"}, Text: "hi",
	}}); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("topics job missing")
	}
	if err := g.authorizeGroupMapping(mappingFromJob(*job, g.now())); err != nil {
		t.Fatalf("topics job lost its authority at claim: %v", err)
	}
}
