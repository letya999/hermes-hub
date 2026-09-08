package companion

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthAndOrigin(t *testing.T) {
	called := 0
	h := Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(204) }), "owner-token")
	for _, tc := range []struct {
		token, origin string
		status        int
	}{{"", "", 401}, {"Bearer other", "", 401}, {"Bearer owner-token", "https://evil.example", 403}, {"Bearer owner-token", "", 204}} {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set("Authorization", tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("status %d want %d", w.Code, tc.status)
		}
	}
	if called != 1 {
		t.Fatal(called)
	}
}
