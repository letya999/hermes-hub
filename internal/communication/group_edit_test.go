package communication

import (
	"context"
	"testing"
)

func TestGroupMessageEditNeverReadmitsAgent(t *testing.T) {
	g, err := New(groupConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	update := forumUpdate(1, 1, "hello", 11, -100123, 0)
	if err := g.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("original mention not admitted: %v", err)
	}
	if err := g.spool.CompleteJob(job.ID); err != nil {
		t.Fatal(err)
	}
	update = forumUpdate(2, 1, "edited", 11, -100123, 0)
	update.EditedMessage, update.Message = update.Message, nil
	if err := g.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if job, err := g.spool.ClaimJob(); err != nil || job != nil {
		t.Fatalf("message edit re-admitted work: %+v %v", job, err)
	}
}
