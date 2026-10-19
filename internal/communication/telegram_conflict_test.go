package communication

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type conflictPoller struct {
	fakeAPI
	calls atomic.Int32
}

func (c *conflictPoller) GetUpdates(context.Context, int64, int) ([]Update, error) {
	c.calls.Add(1)
	return nil, telegramStatusError{code: http.StatusConflict}
}

func (c *conflictPoller) sentCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

// A second gateway polling the same bot token must not fight for updates:
// HTTP 409 means log once, enqueue exactly one owner notice keyed
// telegram-conflict, back off 60 s and keep retrying quietly.
func TestGetUpdatesConflictNotifiesOwnerOnce(t *testing.T) {
	prev := telegramConflictBackoff
	telegramConflictBackoff = 20 * time.Millisecond
	defer func() { telegramConflictBackoff = prev }()
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	api := &conflictPoller{}
	g.api = api
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (api.calls.Load() < 3 || api.sentCount() == 0) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if api.calls.Load() < 3 {
		t.Fatalf("poller gave up after %d conflicts", api.calls.Load())
	}
	notices := 0
	for _, text := range api.sent {
		if strings.Contains(text, "токен") || strings.Contains(text, "экземпляр") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("owner notified %d times for a repeated conflict: %v", notices, api.sent)
	}
}
