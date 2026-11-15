package communication

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareNoticeCannotRegressAfterRestart(t *testing.T) {
	root := t.TempDir()
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	old := Delivery{ID: "prepare-old", ChatID: 11, Text: "credentials", PrepareKey: key, PrepareVersion: 1, CreatedAt: time.Now().UTC()}
	if ok, err := s.enqueuePrepareDelivery(old); err != nil || !ok {
		t.Fatalf("first notice: %t %v", ok, err)
	}
	newer := old
	newer.ID, newer.Text, newer.PrepareVersion, newer.Control = "prepare-new", "confirmation", 2, true
	if ok, err := s.enqueuePrepareDelivery(newer); err != nil || !ok {
		t.Fatalf("second notice: %t %v", ok, err)
	}
	s, err = NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	old.ID = "prepare-late"
	if ok, err := s.enqueuePrepareDelivery(old); err != nil || ok {
		t.Fatalf("late old notice accepted: %t %v", ok, err)
	}
	claimed, err := s.ClaimDelivery()
	if err != nil || claimed == nil || claimed.ID != newer.ID {
		t.Fatalf("current notice: %+v %v", claimed, err)
	}
	if err := s.CompleteDelivery(claimed.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimDelivery(); err != nil || claimed != nil {
		t.Fatalf("old pending notice survived: %+v %v", claimed, err)
	}
	if _, err := os.Stat(filepath.Join(root, "outbox", "superseded", spoolFileID("prepare-old")+".json")); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueDelivery(Delivery{ID: "prepare-old", ChatID: 11}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimDelivery(); err != nil || claimed != nil {
		t.Fatalf("superseded notice resurrected: %+v %v", claimed, err)
	}
}

func TestPrepareFenceRejectsUnsafeIdentity(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.enqueuePrepareDelivery(Delivery{ID: "unsafe", ChatID: 11, PrepareKey: "../origin", PrepareVersion: 1}); err == nil {
		t.Fatal("unsafe path key admitted")
	}
}
