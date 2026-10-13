package communication

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInboundStaging(t *testing.T) {
	spool, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rec := InboundRecord{ID: "in-42", UserID: "alice", ConversationID: "telegram-11", Name: "report.zip", Mime: "application/zip", Size: 99, CreatedAt: time.Now().UTC()}
	if err := spool.StageInbound(rec, strings.NewReader("bytes")); err != nil {
		t.Fatalf("stage: %v", err)
	}
	got, data, err := spool.ReadInbound("in-42")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Name != "report.zip" || got.UserID != "alice" || got.Size != int64(len(data)) || string(data) != "bytes" {
		t.Fatalf("staged record lost: %+v %q", got, data)
	}
	if _, err := os.Stat(filepath.Join(spool.root, "inbound")); err != nil {
		t.Fatalf("inbound dir missing: %v", err)
	}
	if err := spool.StageInbound(InboundRecord{ID: "../escape", UserID: "alice", Name: "x"}, strings.NewReader("x")); err == nil {
		t.Fatal("unsafe id accepted")
	}
	if _, _, err := spool.ReadInbound("../escape"); err == nil {
		t.Fatal("unsafe read accepted")
	}
	if err := spool.ConsumeInbound("in-42"); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if _, _, err := spool.ReadInbound("in-42"); err == nil {
		t.Fatal("consumed record still readable")
	}
}

func TestDocumentUpdateStagesInboundFile(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	update := Update{UpdateID: 90, Message: &Message{MessageID: 5, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Document: &TGMedia{FileID: "doc1", FileName: "report.zip", MimeType: "application/zip", FileSize: 12}}}
	if err := g.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("document update enqueued no job: %v", err)
	}
	if !strings.Contains(job.Text, "inbox=in-90") || !strings.Contains(job.Text, "report.zip") {
		t.Fatalf("job text missing inbox reference: %q", job.Text)
	}
	rec, data, err := g.spool.ReadInbound("in-90")
	if err != nil {
		t.Fatalf("staged inbound missing: %v", err)
	}
	if rec.Name != "report.zip" || rec.UserID == "" || string(data) != "voice-bytes" {
		t.Fatalf("staged record wrong: %+v %q", rec, data)
	}
}

func TestInboundEndpointAuthAndConsume(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	bobState, bobWs := c.Users[0].StateDir+"-bob", c.Users[0].WorkspaceDir+"-bob"
	_ = os.MkdirAll(bobState, 0700)
	_ = os.MkdirAll(bobWs, 0700)
	c.Users = append(c.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: bobState, WorkspaceDir: bobWs})
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.StageInbound(InboundRecord{ID: "in-1", UserID: "alice", Name: "a.txt"}, strings.NewReader("payload")); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	call := func(method, path, principal string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+c.ControlAuth)
		req.Header.Set("X-Hub-Principal", principal)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, _ := call(http.MethodGet, "/v1/inbound/in-1", "mallory"); code != http.StatusUnauthorized {
		t.Fatalf("unknown principal got %d", code)
	}
	if code, _ := call(http.MethodGet, "/v1/inbound/in-1", "bob"); code != http.StatusForbidden {
		t.Fatalf("foreign owner's file got %d", code)
	}
	var name string
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/inbound/in-1", nil)
	req.Header.Set("Authorization", "Bearer "+c.ControlAuth)
	req.Header.Set("X-Hub-Principal", "alice")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		name = resp.Header.Get("X-Hub-Name")
		resp.Body.Close()
	}
	if name != "a.txt" {
		t.Fatalf("name header lost: %q", name)
	}
	code, body := call(http.MethodGet, "/v1/inbound/in-1", "alice")
	if code != http.StatusOK || body != "payload" {
		t.Fatalf("fetch: %d %q", code, body)
	}
	if code, _ := call(http.MethodPost, "/v1/inbound/in-1/consume", "alice"); code != http.StatusOK {
		t.Fatalf("consume: %d", code)
	}
	if code, _ := call(http.MethodGet, "/v1/inbound/in-1", "alice"); code != http.StatusNotFound {
		t.Fatalf("consumed record still served: %d", code)
	}
}
