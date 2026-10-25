package communication

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"
)

func TestRevokedGroupMemberCannotDispatchOrReceiveQueuedWork(t *testing.T) {
	for _, stage := range []string{"dispatch", "delivery"} {
		t.Run(stage, func(t *testing.T) {
			c := groupConfig(t)
			g, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			api := &fakeAPI{}
			runner := &fakeRunner{}
			g.api, g.runner = api, runner
			if err := g.handleUpdate(context.Background(), Update{UpdateID: 1, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "@helper private group question"}}); err != nil {
				t.Fatal(err)
			}
			if stage == "delivery" {
				job, err := g.spool.ClaimJob()
				if err != nil || job == nil {
					t.Fatal(err)
				}
				if err := g.spool.EnqueueDelivery(Delivery{ID: "reply", JobID: job.ID, Channel: "telegram_bot", ChatID: job.ChatID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Text: "group-private-marker"}); err != nil {
					t.Fatal(err)
				}
			}
			g.config.Groups[0].Members = []int64{22}
			if stage == "dispatch" {
				var logs bytes.Buffer
				previous := log.Writer()
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(previous) })
				ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
				defer cancel()
				g.worker(ctx)
				if !strings.Contains(logs.String(), "status=denied reason=group_authorization") || strings.Contains(logs.String(), "private group question") {
					t.Fatalf("missing safe denial diagnostic: %s", logs.String())
				}
				runner.mu.Lock()
				defer runner.mu.Unlock()
				if len(runner.seen) != 0 {
					t.Error("revoked group member's queued job executed")
				}
			} else {
				g.deliverOne(context.Background())
				api.mu.Lock()
				defer api.mu.Unlock()
				if len(api.sent) != 0 {
					t.Error("queued private group reply delivered after membership revocation")
				}
			}
		})
	}
}
