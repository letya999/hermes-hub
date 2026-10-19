package media

import (
	"bytes"
	"context"
	"encoding/base64"
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

func TestImageInputUsesBytesForInspectAndEdit(t *testing.T) {
	s, dir := openSession(t)
	inputs := [][]byte{jpegBytes(t), pngBytes(t, 3, 4)}
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload struct {
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.Messages) != 1 || len(payload.Messages[0].Content) != 3 {
			t.Errorf("bad multimodal request: %+v %v", payload, err)
			return
		}
		for i, mime := range []string{"image/jpeg", "image/png"} {
			uri := payload.Messages[0].Content[i+1]["image_url"].(map[string]any)["url"].(string)
			prefix := "data:" + mime + ";base64,"
			if !strings.HasPrefix(uri, prefix) {
				t.Errorf("wrong MIME at %d: %s", i, uri[:min(len(uri), 50)])
			}
			body, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
			if err != nil || !bytes.Equal(body, inputs[i]) {
				t.Errorf("invalid image body %d: %v", i, err)
			}
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"two images"}}]}`))
	}))
	defer api.Close()
	grantFile(t, dir, api.URL, false)
	t.Setenv("OPENAI_API_KEY", "fixture-key")
	writeWorkspace(t, dir, "photo-id", inputs[0])
	writeWorkspace(t, dir, "misleading.jpg", inputs[1])
	result, err := s.InspectMany(context.Background(), []string{"photo-id", "misleading.jpg"})
	if err != nil || result["text"] != "two images" || calls != 1 {
		t.Fatalf("inspect: %v calls=%d %v", result, calls, err)
	}
	format, body, err := s.imageFile("photo-id")
	if err != nil || format != "jpeg" || len(body) == 0 {
		t.Fatalf("edit input: %s %v", format, err)
	}
	writeWorkspace(t, dir, "broken.png", []byte("not an image"))
	if _, err := s.InspectMany(context.Background(), []string{"photo-id", "broken.png"}); err == nil || calls != 1 {
		t.Fatalf("corrupt set sent: calls=%d err=%v", calls, err)
	}
	t.Setenv("OPENAI_API_KEY", "${OPENAI_API_KEY}")
	if _, err := s.Inspect(context.Background(), "misleading.jpg"); err == nil || calls != 1 {
		t.Fatalf("unresolved key sent: calls=%d err=%v", calls, err)
	}
}
