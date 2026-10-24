package communication

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGroupUsesDedicatedSpaceAndMentionGate(t *testing.T) {
	c := testConfig(t)
	c.Users = append(c.Users, User{ID: "team", Enabled: true, StateDir: t.TempDir(), WorkspaceDir: t.TempDir()})
	body, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("groups:\n  - chat_id: -100123\n    space: team\n    members: [11]\n    mention: '@helper'\n")...)
	name := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(name, body, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadYAML(name)
	if err != nil {
		t.Fatal(err)
	}
	loaded.TelegramToken = c.TelegramToken
	g, err := New(loaded)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	send := func(id int, sender int64, text string) {
		t.Helper()
		if err := g.handleUpdate(context.Background(), Update{UpdateID: id, Message: &Message{MessageID: id, From: &TGUser{ID: sender}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: text}}); err != nil {
			t.Fatal(err)
		}
	}
	send(1, 11, "shared-plan-marker")
	send(2, 99, "@helper outsider-marker")
	if job, err := g.spool.ClaimJob(); err != nil || job != nil {
		t.Fatalf("passive/foreign message activated runtime: %+v %v", job, err)
	}
	send(3, 11, "@helper summarize")
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("mention not admitted: %+v %v", job, err)
	}
	if job.PrincipalID != "team" || job.RuntimeID != "team" || job.ContextID != "team" || job.ScopeID != "group:team" || job.DeliveryTargetID != "telegram--100123" {
		t.Fatalf("personal scope selected: %+v", job.Envelope)
	}
	if !strings.Contains(job.Text, "shared-plan-marker") || strings.Contains(job.Text, "outsider-marker") {
		t.Fatal("group transcript lost shared input or included outsider")
	}
}
