package testkit_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/shared/testkit"
)

func TestModelServerStreamsOpenAIChunks(t *testing.T) {
	srv := testkit.ModelServer(t, "hello ", "world")
	resp, err := srv.Client().Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"fixture","stream":true,"messages":[{"role":"user","content":"ping"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE contract: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	scan := bufio.NewScanner(resp.Body)
	text, terminal, done := "", false, false
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			done = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta        struct{ Content string }
				FinishReason *string `json:"finish_reason"`
			}
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil || len(chunk.Choices) != 1 {
			t.Fatalf("invalid chunk %s: %v", data, err)
		}
		text += chunk.Choices[0].Delta.Content
		terminal = chunk.Choices[0].FinishReason != nil && *chunk.Choices[0].FinishReason == "stop"
	}
	if scan.Err() != nil || text != "hello world" || !terminal || !done {
		t.Fatalf("stream: text=%q terminal=%v done=%v err=%v", text, terminal, done, scan.Err())
	}
}
