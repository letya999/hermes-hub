package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestModelBudgetIncludesRequestedOutput(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(200) }))
	defer server.Close()
	for _, body := range []string{`{"model":"m","messages":[],"max_tokens":400000}`, `{"model":"m","messages":[],"max_completion_tokens":400000}`} {
		_, response, err := modelCall(context.Background(), server.Client(), []*modelHop{{modelHealth: &modelHealth{}, name: "test", base: server.URL}}, "POST", "/v1/chat/completions", "", "application/json", []byte(body), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 413 || calls != 0 {
			t.Fatalf("output allowance sent upstream: status=%d calls=%d", response.StatusCode, calls)
		}
	}
}
