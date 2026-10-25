package communication

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPassiveGroupTranscriptIsBoundedAndSurvivesRestart(t *testing.T) {
	c := testConfig(t)
	c.Users = append(c.Users, User{ID: "team", Enabled: true, StateDir: t.TempDir(), WorkspaceDir: t.TempDir()})
	c.Groups = []Group{{ChatID: -100123, Space: "team", Members: []int64{11}, Mention: "@helper"}}
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	for i := 0; i < 60; i++ {
		text := fmt.Sprintf("marker-%03d ", i) + strings.Repeat("z", 9000)
		if err := g.handleUpdate(context.Background(), Update{UpdateID: i + 1, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: text}}); err != nil {
			t.Fatal(err)
		}
	}
	g, err = New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	if err := g.handleUpdate(context.Background(), Update{UpdateID: 1000, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "@helper summarize"}}); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("missing group job: %v", err)
	}
	if len(job.Text) > 36<<10 || strings.Contains(job.Text, "marker-000") || !strings.Contains(job.Text, "marker-059") {
		t.Fatalf("transcript not bounded/recovered: bytes=%d", len(job.Text))
	}
	info, err := os.Stat(filepath.Join(c.SpoolDir, "groups", "-100123.json"))
	if err != nil || info.Size() > 36<<10 {
		t.Fatalf("durable transcript unbounded: %v %v", info, err)
	}
}
