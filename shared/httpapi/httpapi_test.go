package httpapi

import (
	"encoding/json"
	"fmt"
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

// Live S7a regression: handlers return detail through fmt.Errorf("%w:
// <detail>", ErrForbidden) — errors.As unwraps to the empty sentinel, so
// Handle wrote {"error":""} and the wire dropped every refusal reason.
func TestHandleWritesWrappedDetail(t *testing.T) {
	handler := Handle("contract-token", func(*http.Request) (any, error) {
		return nil, fmt.Errorf("%w: skill-source mirror is missing", ErrForbidden)
	})
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Authorization", "Bearer contract-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "skill-source mirror is missing" {
		t.Fatalf("wire detail = %q", body.Error)
	}
}

// Live S7a regression (companion): the in-process contract leg runs
// Normalize before Handle writes the response — it returned the bare
// sentinel, so wrapped refusal detail never reached the wire either.
func TestNormalizeKeepsWrappedDetail(t *testing.T) {
	err := fmt.Errorf("%w: skill-source mirror is missing", ErrForbidden)
	got := Normalize(err)
	typed, ok := got.(*Error)
	if !ok || typed.Code != "forbidden" {
		t.Fatalf("normalize lost the code: %v", got)
	}
	if typed.Message != "skill-source mirror is missing" {
		t.Fatalf("normalize lost the detail: %q", typed.Message)
	}
}
