package communication

import (
	"context"
	"sync"
	"testing"
)

func TestGroupConcurrentDuplicateIsOneAdmission(t *testing.T) {
	g, err := New(groupConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	update := Update{UpdateID: 9, Message: &Message{MessageID: 9, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "@helper concurrent"}}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- g.handleUpdate(context.Background(), update) }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("admission: %v %v", job, err)
	}
	if next, err := g.spool.ClaimJob(); err != nil || next != nil {
		t.Fatalf("duplicate admission: %v %v", next, err)
	}
}
