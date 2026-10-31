package hosting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestControlAndExchangeOverflowAndServeAudit(t *testing.T) {
	s, _ := fixture(t)
	s.routes = routesFake{}
	r := request("share")
	r.Access = "link"
	site, err := s.Publish(context.Background(), s.Binding(), r)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(site.Origin, "https://")
	got := serve(s, "POST", host, "/_hosting/exchange", strings.Repeat("x", 129), nil)
	if got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("exchange overflow %d", got.Code)
	}
	req := httptest.NewRequest("POST", "/v1/publish", strings.NewReader(`{"name":"`+strings.Repeat("x", 9000)+`"}`))
	token := strings.Repeat("t", 32)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	s.ControlHandler(token).ServeHTTP(out, req)
	if out.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("control overflow %d", out.Code)
	}
	files, _, err := s.auditFiles()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range files {
		body, err := s.root.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			var e auditEvent
			if json.Unmarshal([]byte(line), &e) != nil {
				t.Fatal("invalid journal")
			}
			if e.Kind == "serve" && e.Outcome == "413" && e.Bytes > 0 {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("serve completion omitted actual status and bytes")
	}
}
