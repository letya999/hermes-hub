package communication

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAgentMemoryReplyNeverEntersHostContentLog(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	s, job := streamJob(t)
	g.spool = s
	g.contentUntil = time.Now().Add(time.Minute)
	logs := captureLogs(t)
	d := Delivery{ID: "memory-reply", JobID: job.ID, Channel: "telegram_bot", ChatID: job.ChatID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Text: "private-memory-marker", CreatedAt: time.Now()}
	if err := s.EnqueueDelivery(d); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if strings.Contains(logs.String(), "private-memory-marker") {
		t.Fatal("agent memory content copied into shared host log")
	}
	if !strings.Contains(logs.String(), "telegram-sent") {
		t.Fatal("test did not exercise outgoing logging")
	}
}
