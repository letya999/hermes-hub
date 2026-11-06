package supervisor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSettingsEditHTTPRequiresAuthAndBoundSpace(t *testing.T) {
	called := 0
	m := &Manager{cfg: Config{RuntimeAuth: "control-secret", Environment: "dev", SpacesRoot: t.TempDir(),
		SettingsEdit: func(_ context.Context, principal, environment, key string, value *string, base string) (map[string]any, error) {
			called++
			if principal != "alice" || environment != "dev" || key != "compression.enabled" || value == nil || *value != "true" || base != "r-old" {
				t.Fatal("publisher received a different request")
			}
			return map[string]any{"published_revision": "r-new"}, nil
		},
	}}
	for _, test := range []struct {
		token  string
		body   string
		status int
	}{
		{"", `{"principal_id":"alice","environment":"dev","key":"compression.enabled","value":"true","base_revision":"r-old"}`, http.StatusUnauthorized},
		{"wrong", `{"principal_id":"alice","environment":"dev","key":"compression.enabled","value":"true","base_revision":"r-old"}`, http.StatusUnauthorized},
		{"control-secret", `{"principal_id":"../bob","environment":"dev","key":"compression.enabled","value":"true","base_revision":"r-old"}`, http.StatusBadRequest},
		{"control-secret", `{"principal_id":"alice","environment":"prod","key":"compression.enabled","value":"true","base_revision":"r-old"}`, http.StatusBadRequest},
		{"control-secret", `{"principal_id":"alice","environment":"dev","key":"compression.enabled","value":"true"}`, http.StatusBadRequest},
		{"control-secret", `{"principal_id":"alice","environment":"dev","key":"compression.enabled","value":"true","base_revision":"r-old"}`, http.StatusOK},
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/settings", bytes.NewBufferString(test.body))
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		response := httptest.NewRecorder()
		m.serveHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s: status %d, want %d", test.body, response.Code, test.status)
		}
	}
	if called != 1 {
		t.Fatalf("publisher called %d times", called)
	}
}
