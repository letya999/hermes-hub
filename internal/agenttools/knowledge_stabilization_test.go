package agenttools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestM10KnowledgeClientProvenanceSearchAndLimits(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	v.CommunicationAuth = "token"
	status, body := 200, `{"fragments":[{"source":"notes.md","line":1,"sha256":"fingerprint"}]}`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || r.Header.Get("X-Hub-Principal") != "alice" {
			t.Error("missing knowledge authorization")
		}
		w.Header().Set("X-Hub-SHA256", "fingerprint")
		w.Header().Set("X-Hub-Context", "alice")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer api.Close()
	v.CommunicationURL = api.URL
	out, err := v.knowledgeSearch(context.Background(), "needle")
	if err != nil || len(out["fragments"].([]any)) != 1 {
		t.Fatalf("search=%v %v", out, err)
	}
	body = "snapshot"
	out, err = v.knowledgeRead(context.Background(), Input{Name: "notes.md"})
	if err != nil || out["sha256"] != "fingerprint" || out["context_id"] != "alice" {
		t.Fatalf("provenance=%v %v", out, err)
	}
	for _, query := range []string{"", strings.Repeat("x", 257)} {
		if _, err = v.knowledgeSearch(context.Background(), query); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	body = "broken"
	if _, err = v.knowledgeSearch(context.Background(), "needle"); err == nil {
		t.Fatal("malformed search accepted")
	}
	status = 403
	if _, err = v.knowledgeRead(context.Background(), Input{Name: "notes.md"}); err == nil {
		t.Fatal("denial accepted")
	}
	status = 200
	body = strings.Repeat("x", (9<<20)+1)
	if _, err = v.knowledgeRead(context.Background(), Input{Name: "notes.md"}); err == nil {
		t.Fatal("oversize knowledge silently truncated")
	}
	v.CommunicationURL = ":invalid"
	if _, err = v.knowledgeRead(context.Background(), Input{Name: "notes.md"}); err == nil {
		t.Fatal("invalid URL accepted")
	}
}
