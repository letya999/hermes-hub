package wire

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerSchemeRequired(t *testing.T) {
	calls := 0
	handler := Handle("contract-token", func(*http.Request) (any, error) {
		calls++
		return map[string]string{"status": "ok"}, nil
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "contract-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("non-bearer authorization: status=%d backend_calls=%d", response.Code, calls)
	}
}
