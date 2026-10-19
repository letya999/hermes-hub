package communication

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// HTTPRunner submits the job envelope to the supervisor execute endpoint,
// rejects empty prompts client-side, and maps terminal/uncertain/invalid
// responses onto RunOutcome.
func TestHTTPRunnerOutcome(t *testing.T) {
	var auth, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		data := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(data)
		body = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job_id":"j-1","run_id":"r-1","session_id":"s-1","status":"completed","text":"done"}`))
	}))
	defer srv.Close()
	r := &HTTPRunner{URL: srv.URL, Auth: "tok", HTTP: srv.Client(), Voice: true}
	job := Job{ID: "j-1", UserID: "alice", ActorID: "alice", Text: "hi", Channel: "telegram_bot"}

	if _, err := r.RunOutcome(context.Background(), Job{ID: "empty"}); err == nil || !strings.Contains(err.Error(), "empty Hermes prompt") {
		t.Fatalf("empty prompt: %v", err)
	}
	outcome, err := r.RunOutcome(context.Background(), job)
	if err != nil || outcome.Status != "completed" || outcome.RunID != "r-1" {
		t.Fatalf("run outcome: %+v %v", outcome, err)
	}
	if auth != "Bearer tok" || !strings.Contains(body, `"job_id":"j-1"`) || !strings.Contains(body, "VOICE: ") {
		t.Fatalf("request: %q %s", auth, body[:200])
	}

	// Resume clears the text and posts to /v1/resume.
	r.Resume = true
	resumeJob := job
	resumeJob.Text = "ignored"
	if _, err := r.RunOutcome(context.Background(), resumeJob); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "ignored") {
		t.Fatal("resume kept the prompt text")
	}

	// Uncertain and non-2xx statuses map onto their outcome errors.
	for name, c := range map[string]struct {
		code    int
		payload string
	}{
		"uncertain": {http.StatusOK, `{"job_id":"j-1","status":"uncertain"}`},
		"error":     {http.StatusBadGateway, `{"status":"failed","last_event":"run.failed"}`},
		"invalid":   {http.StatusOK, `not-json`},
	} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
			_, _ = w.Write([]byte(c.payload))
		}))
		defer bad.Close()
		rb := &HTTPRunner{URL: bad.URL, HTTP: bad.Client()}
		if _, err := rb.RunOutcome(context.Background(), job); err == nil {
			t.Fatalf("%s response accepted", name)
		}
	}
}
