package agenttools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestM10RegressionExistingInboxSurvivesDuplicateZIP(t *testing.T) {
	v := fixture(t)
	if err := v.Workspace.MkdirAll("inbox/pack", 0700); err != nil {
		t.Fatal(err)
	}
	if err := v.Workspace.WriteFile("inbox/pack/original.txt", []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	data := zipBytes(t, map[string]string{"original.txt": "new"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Hub-Name", "pack.zip")
		_, _ = w.Write(data)
	}))
	defer srv.Close()
	v.CommunicationURL = srv.URL
	_, err := v.inboxFetch(context.Background(), v.Workspace, Input{ID: "in-duplicate"})
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := v.Workspace.ReadFile("inbox/pack/original.txt")
	if readErr != nil || string(body) != "keep me" {
		t.Fatalf("PREEXISTING FILE LOST: fetch=%v read=%v body=%q", err, readErr, body)
	}
}
func TestM10RegressionKnowledgeSendsRequiredAuthorization(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	v.CommunicationAuth = "audit-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer audit-token" || r.Header.Get("X-Hub-Principal") != "alice" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	v.CommunicationURL = srv.URL
	if _, err := v.knowledgeList(context.Background()); err != nil {
		t.Fatalf("VALID CONFIG CANNOT READ KNOWLEDGE: %v", err)
	}
	_ = os.ErrNotExist
}
