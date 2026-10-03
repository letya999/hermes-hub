package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func artifactJob() Job {
	return Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "artifact-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "artifact-key", ChatID: 11}
}

// A completed run streams artifact refs; the runner fetches bytes into the
// spool while the runtime is still answering and records failures explicitly.
func TestAttachArtifactsStagesBlobsAndMarksFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/artifact" || r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request hubruntime.ArtifactRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Name == "" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if request.PrincipalID != "alice" || request.UserID != "alice" {
			http.Error(w, "foreign binding", http.StatusForbidden)
			return
		}
		if request.Name == "images/missing.png" {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("bytes-for-" + request.Name))
	}))
	defer server.Close()
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := HTTPRunner{URL: server.URL, Auth: "secret", Spool: spool}
	event := hubruntime.ExecuteResponse{Status: "completed", Artifacts: []hubruntime.ArtifactRef{
		{Name: "report.pdf", Path: "documents/report.pdf", Mime: "application/pdf", Size: 20},
		{Name: "missing.png", Path: "images/missing.png", Mime: "image/png", Size: 10},
	}}
	runner.attachArtifacts(context.Background(), artifactJob(), &event)
	if event.Artifacts[0].Blob == "" || event.Artifacts[0].Error != "" {
		t.Fatalf("first artifact: %+v", event.Artifacts[0])
	}
	data, err := spool.ReadDeliveryBlob("job-artifact-job-response", event.Artifacts[0].Blob)
	if err != nil || string(data) != "bytes-for-documents/report.pdf" {
		t.Fatalf("blob=%q err=%v", data, err)
	}
	if event.Artifacts[1].Error == "" || event.Artifacts[1].Blob != "" {
		t.Fatalf("failed fetch must be explicit, not silent: %+v", event.Artifacts[1])
	}
	if _, err := spool.ReadDeliveryBlob("job-artifact-job-response", ".."); err == nil {
		t.Fatal("blob traversal accepted")
	}
	if _, err := spool.ReadDeliveryBlob("job-artifact-job-response", "../escape"); err == nil {
		t.Fatal("blob escape accepted")
	}
}

func TestDeliverySendsArtifactsAfterText(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	delivery := Delivery{ID: "with-artifacts", JobID: "artifact-job", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "Готово.", Format: formatMarkdown, Artifacts: []hubruntime.ArtifactRef{
		{Name: "report.pdf", Mime: "application/pdf", Size: 9},
		{Name: "pic.png", Mime: "image/png", Size: 9},
	}}
	for i, name := range []string{"0-report.pdf", "1-pic.png"} {
		if _, err := g.spool.WriteDeliveryBlob(delivery.ID, name, []byte("content-"+name)); err != nil {
			t.Fatal(err)
		}
		delivery.Artifacts[i].Blob = name
	}
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 || len(fake.docs) != 1 || len(fake.photos) != 1 {
		t.Fatalf("sent=%v docs=%v photos=%v", fake.sent, fake.docs, fake.photos)
	}
	if !strings.HasPrefix(fake.docs[0], "report.pdf\x00") || !strings.HasPrefix(fake.photos[0], "pic.png\x00") {
		t.Fatalf("wrong artifacts delivered: %v %v", fake.docs, fake.photos)
	}
	if _, err := os.Stat(filepath.Join(g.spool.root, "outbox", "blobs", delivery.ID)); !os.IsNotExist(err) {
		t.Fatal("staged blobs survived completion")
	}
}

func TestArtifactFailureKeepsSentBoundaryUncertain(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{failDocOn: 2}
	g.api = fake
	delivery := Delivery{ID: "partial-artifacts", JobID: "artifact-job", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "Готово.", Artifacts: []hubruntime.ArtifactRef{
		{Name: "one.txt"}, {Name: "two.txt"},
	}}
	for i, name := range []string{"0-one.txt", "1-two.txt"} {
		if _, err := g.spool.WriteDeliveryBlob(delivery.ID, name, []byte("x")); err != nil {
			t.Fatal(err)
		}
		delivery.Artifacts[i].Blob = name
	}
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.docs) != 1 {
		t.Fatalf("docs=%v", fake.docs)
	}
	stored, err := os.ReadFile(filepath.Join(g.spool.root, "outbox", "failed", "partial-artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var failed Delivery
	if err := json.Unmarshal(stored, &failed); err != nil || failed.SentArtifacts != 1 {
		t.Fatalf("boundary=%+v err=%v", failed, err)
	}
	// Redelivery after intervention resumes at the persisted boundary: the
	// already-sent document is not repeated.
	fake.failDocOn = 0
	if err := g.spool.move("outbox/failed", "outbox/pending", delivery.ID); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.docs) != 2 || !strings.HasPrefix(fake.docs[1], "two.txt") {
		t.Fatalf("resume resent or lost artifacts: %v", fake.docs)
	}
}

func TestPhotoArtifactFallsBackToDocument(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{photoErr: errors.New("photo rejected")}
	g.api = fake
	delivery := Delivery{ID: "photo-fallback", JobID: "artifact-job", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "plain", Artifacts: []hubruntime.ArtifactRef{{Name: "pic.png", Mime: "image/png"}}}
	blob, err := g.spool.WriteDeliveryBlob(delivery.ID, "0-pic.png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	delivery.Artifacts[0].Blob = blob
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.photos) != 0 || len(fake.docs) != 1 || !strings.HasPrefix(fake.docs[0], "pic.png") {
		t.Fatalf("photo failure did not fall back to document: %v %v", fake.photos, fake.docs)
	}
}

func TestVideoArtifactSendsVideoAndFallsBackToDocument(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	delivery := Delivery{ID: "video-artifact", JobID: "artifact-job", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "plain", Artifacts: []hubruntime.ArtifactRef{{Name: "clip.mp4", Mime: "video/mp4"}}}
	blob, err := g.spool.WriteDeliveryBlob(delivery.ID, "0-clip.mp4", []byte("mp4"))
	if err != nil {
		t.Fatal(err)
	}
	delivery.Artifacts[0].Blob = blob
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.videos) != 1 || !strings.HasPrefix(fake.videos[0], "clip.mp4\x00") || len(fake.docs) != 0 {
		t.Fatalf("video not routed to sendVideo: %v %v", fake.videos, fake.docs)
	}

	fake.videoErr = errors.New("video rejected")
	fake.videos = nil
	delivery.ID = "video-fallback"
	delivery.Artifacts[0].Blob = ""
	if blob, err := g.spool.WriteDeliveryBlob(delivery.ID, "0-clip.mp4", []byte("mp4")); err == nil {
		delivery.Artifacts[0].Blob = blob
	}
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.videos) != 0 || len(fake.docs) != 1 || !strings.HasPrefix(fake.docs[0], "clip.mp4") {
		t.Fatalf("video failure did not fall back to document: %v %v", fake.videos, fake.docs)
	}
}

func TestSlackArtifactPostsExplicitNotice(t *testing.T) {
	var posted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		posted = append(posted, body["text"])
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.slack = &slackAPI{token: "xoxb-test", client: server.Client(), baseURL: server.URL}
	g.api = &fakeAPI{}
	delivery := Delivery{ID: "slack-artifacts", JobID: "artifact-job", Channel: "slack_app", ConversationID: "slack-tteam-uuser", DeliveryTargetID: "slack-tteam-uuser", SlackChannel: "D1", Text: "Done.", Artifacts: []hubruntime.ArtifactRef{
		{Name: "report.pdf", Mime: "application/pdf"},
		{Name: "gone.png", Error: "artifact unavailable: 404"},
	}}
	blob, err := g.spool.WriteDeliveryBlob(delivery.ID, "0-report.pdf", []byte("pdf"))
	if err != nil {
		t.Fatal(err)
	}
	delivery.Artifacts[0].Blob = blob
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(posted) != 3 || !strings.Contains(posted[1], "report.pdf") || !strings.Contains(posted[2], "gone.png") {
		t.Fatalf("slack notices=%v", posted)
	}
}

func TestFailedFetchArtifactSendsExplicitNotice(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	if err := g.spool.EnqueueDelivery(Delivery{ID: "notice-artifact", Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "plain", Artifacts: []hubruntime.ArtifactRef{
		{Name: "gone.pdf", Error: "artifact unavailable: 404"},
	}}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 2 || !strings.Contains(fake.sent[1], "gone.pdf") {
		t.Fatalf("failed artifact was silent: %v", fake.sent)
	}
}
