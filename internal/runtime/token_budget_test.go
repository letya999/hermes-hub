package runtime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestModelTokenPreflightDoesNotSend(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	hop := &modelHop{modelHealth: &modelHealth{}, name: "test", base: upstream.URL}
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"` + strings.Repeat("x", 1600*1024) + `"}]}`)
	_, response, err := modelCall(context.Background(), upstream.Client(), []*modelHop{hop}, "POST", "/v1/chat/completions", "", "application/json", body, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 413 || calls != 0 {
		t.Fatalf("budget refusal status=%d upstream calls=%d", response.StatusCode, calls)
	}
	if !hop.available(time.Now()) {
		t.Fatal("budget refusal poisoned provider cooldown")
	}
}

// The ceiling sits above the managed compaction trigger (200000 tokens), so a
// request the in-loop compressor can handle must reach the provider. Refusing
// earlier would break every long session before compaction could fire.
func TestModelTokenPreflightPassesCompressibleRequest(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer upstream.Close()
	hop := &modelHop{modelHealth: &modelHealth{}, name: "test", base: upstream.URL}
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"` + strings.Repeat("x", 200000) + `"}]}`)
	_, response, err := modelCall(context.Background(), upstream.Client(), []*modelHop{hop}, "POST", "/v1/chat/completions", "", "application/json", body, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if calls != 1 {
		t.Fatalf("compressible request refused preflight: upstream calls=%d status=%d", calls, response.StatusCode)
	}
}
