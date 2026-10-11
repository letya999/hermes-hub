package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInspectManyOneOrderedRequestAndNoPartialSend(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) != 1 || len(request.Messages[0].Content) != 3 {
			t.Errorf("images not in one request: %+v", request)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"comparison"}}]}`))
	}))
	defer api.Close()
	s, dir := openSession(t)
	grantFile(t, dir, api.URL, false)
	t.Setenv("OPENAI_API_KEY", "fixture-key")
	writeWorkspace(t, dir, "a.png", pngBytes(t, 1, 1))
	writeWorkspace(t, dir, "b.png", pngBytes(t, 2, 2))
	result, err := s.InspectMany(context.Background(), []string{"a.png", "b.png"})
	if err != nil || calls != 1 || result["text"] != "comparison" {
		t.Fatalf("result=%v calls=%d %v", result, calls, err)
	}
	images := result["results"].([]map[string]any)
	if len(images) != 2 || images[0]["path"] != "a.png" || images[1]["width"] != 2 {
		t.Fatalf("order=%v", images)
	}
	for _, paths := range [][]string{nil, {"a.png", "missing.png"}, {"a.png", "../outside.png"}, strings.Split(strings.Repeat("a.png,", 8)+"a.png", ",")} {
		if _, err = s.InspectMany(context.Background(), paths); err == nil {
			t.Fatalf("bad set accepted: %v", paths)
		}
	}
	if calls != 1 {
		t.Fatalf("bad set partially sent: %d calls", calls)
	}
}
