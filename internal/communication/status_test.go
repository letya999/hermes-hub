package communication

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSpoolRecord(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSpoolQueueStatusCounts(t *testing.T) {
	root := t.TempDir()
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	writeSpoolRecord(t, root, filepath.Join("pending", "j1.json"))
	writeSpoolRecord(t, root, filepath.Join("pending", "j2.json"))
	writeSpoolRecord(t, root, filepath.Join("running", "j3.json"))
	writeSpoolRecord(t, root, filepath.Join("failed", "j4.json"))
	writeSpoolRecord(t, root, filepath.Join("outbox", "pending", "d1.json"))
	writeSpoolRecord(t, root, filepath.Join("outbox", "failed", "d2.json"))

	queue, outbox := spool.QueueStatus(time.Now())
	if queue.Pending != 2 || queue.Running != 1 || queue.Failed != 1 {
		t.Fatalf("queue=%+v", queue)
	}
	if outbox.Pending != 1 || outbox.Failed != 1 {
		t.Fatalf("outbox=%+v", outbox)
	}
	if outbox.OldestPendingSeconds < 0 {
		t.Fatalf("oldest age negative: %+v", outbox)
	}
}

func TestHandleStatusAuthAndShape(t *testing.T) {
	root := t.TempDir()
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	writeSpoolRecord(t, root, filepath.Join("failed", "j9.json"))
	alice := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}
	g := &Gateway{config: Config{ControlAuth: "service", Users: []User{alice}}, spool: spool, now: time.Now}
	provisionTestIdentity(t, &g.config)

	server := httptest.NewServer(g.Handler())
	defer server.Close()

	// no bearer → 401
	resp, err := http.Get(server.URL + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer service")
	req.Header.Set("X-Hub-Principal", "alice")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var view GatewayStatusView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Queue.Failed != 1 {
		t.Fatalf("queue failed=%d want 1", view.Queue.Failed)
	}
	// no ToolHubURL configured → routines report unavailable, not a guess
	if view.Routines.State != "unavailable" {
		t.Fatalf("routines state=%q want unavailable", view.Routines.State)
	}
}
