package runtime

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// controlBodyDenied inspects only bounded JSON object bodies for denied
// control keys; everything else passes through to upstream validation.
func TestControlBodyDenied(t *testing.T) {
	mk := func(body string) *httptest.ResponseRecorder { return nil }
	_ = mk
	for _, tc := range []struct {
		name   string
		body   string
		denied bool
	}{
		{"empty", "", false},
		{"non-json", "hello", false},
		{"json-array", "[1,2]", false},
		{"clean-object", `{"cmd":"ls"}`, false},
	} {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(tc.body))
		denied, err := controlBodyDenied(req)
		if err != nil || denied != tc.denied {
			t.Fatalf("%s: denied=%v err=%v", tc.name, denied, err)
		}
	}
	// Denied keys are caught; the body is rewound for the real handler.
	for _, key := range controlDeniedBodyKeys {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"`+key+`":1}`))
		denied, err := controlBodyDenied(req)
		if err != nil || !denied {
			t.Fatalf("key %s not denied: %v", key, err)
		}
	}
	// Oversized body is denied without reading further.
	req := httptest.NewRequest("POST", "/x", io.NopCloser(strings.NewReader(strings.Repeat("x", controlBodyInspectLimit+10))))
	denied, err := controlBodyDenied(req)
	if err != nil || !denied {
		t.Fatalf("oversize: denied=%v err=%v", denied, err)
	}
}
