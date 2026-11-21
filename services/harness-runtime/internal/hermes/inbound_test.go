package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchInboundUsesPrivateRoute(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/inbound-fetch" || r.Header.Get("Authorization") != "Bearer owner-token" {
			t.Errorf("wrong private route or bearer: %s", r.URL.Path)
		}
		var body struct {
			ID string `json:"id"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.ID != "in-90" {
			t.Error("inbound id was not sent")
		}
		_, _ = w.Write([]byte(`{"path":"inbox/people/in-90/files/report.pdf"}`))
	}))
	defer httpServer.Close()
	t.Setenv("HUB_TOOLHUB_ENDPOINT", httpServer.URL+"/mcp")
	t.Setenv("HUB_RUNTIME_AUTH", "owner-token")
	if path, err := fetchInbound(context.Background(), "in-90"); err != nil || path != "inbox/people/in-90/files/report.pdf" {
		t.Fatalf("private inbound fetch rejected: path=%q err=%v", path, err)
	}
}

func TestFetchInboundUsesOwnerToolAndRejectsFailure(t *testing.T) {
	returnedPath := "inbox/people/in-90/files/report.pdf"
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owner-token" {
			t.Error("wrong runtime credential")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"path": returnedPath})
	}))
	defer httpServer.Close()
	t.Setenv("HUB_TOOLHUB_ENDPOINT", httpServer.URL+"/mcp")
	t.Setenv("HUB_RUNTIME_AUTH", "owner-token")
	if path, err := fetchInbound(context.Background(), "in-90"); err != nil || path != returnedPath {
		t.Fatalf("path=%q err=%v", path, err)
	}
	t.Setenv("HUB_TOOLHUB_TOKEN_ENV", "OWNER_TOKEN")
	t.Setenv("OWNER_TOKEN", "owner-token")
	t.Setenv("HUB_RUNTIME_AUTH", "different-token")
	if path, err := fetchInbound(context.Background(), "in-90"); err != nil || path != returnedPath {
		t.Fatalf("configured owner token ignored: path=%q err=%v", path, err)
	}
	returnedPath = "inbox/people/in-90/files"
	if path, err := fetchInbound(context.Background(), "in-90"); err != nil || path != returnedPath {
		t.Fatalf("ZIP directory receipt rejected: path=%q err=%v", path, err)
	}
	returnedPath = "inbox/people/in-90/files/../../foreign.pdf"
	if _, err := fetchInbound(context.Background(), "in-90"); err == nil {
		t.Fatal("unsafe receipt path accepted")
	}
	returnedPath = "inbox/people/in-91/files/foreign.pdf"
	if _, err := fetchInbound(context.Background(), "in-90"); err == nil {
		t.Fatal("foreign receipt path accepted")
	}
	for _, id := range []string{"../in-90", "in-", "in-a", "in-1/../in-90", "in-" + "999999999999999999999999999999999999"} {
		if _, err := fetchInbound(context.Background(), id); err == nil {
			t.Fatalf("unsafe inbound ID accepted: %q", id)
		}
	}
}

func TestAdmissionRequiresInboundSave(t *testing.T) {
	runs := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if r.URL.Path == "/v1/runs" {
			runs++
			_, _ = w.Write([]byte(`{"run_id":"unexpected"}`))
			return
		}
		if r.URL.Path == "/v1/runs/unexpected" {
			_, _ = w.Write([]byte(`{"status":"completed","output":"no file"}`))
		}
	}))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	t.Setenv("HUB_TOOLHUB_ENDPOINT", "")
	req := validExecuteRequest("alice", "personal", "inbound-run", "summarize")
	req.InboundID = "in-90"
	_, err := (&runtimeHTTP{}).admitHermesRun(context.Background(), req, nil)
	if err == nil || runs != 0 {
		t.Fatalf("file-less run admitted: runs=%d err=%v port=%s", runs, err, fmt.Sprint(port))
	}
}

func TestAdmissionGivesAgentSavedDocumentPath(t *testing.T) {
	toolServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"path":"inbox/people/in-90/files/report.pdf"}`))
	}))
	defer toolServer.Close()
	t.Setenv("HUB_TOOLHUB_ENDPOINT", toolServer.URL+"/mcp")
	t.Setenv("HUB_RUNTIME_AUTH", "owner-token")
	input := ""
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions":
			w.WriteHeader(http.StatusConflict)
		case "/v1/runs":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			input, _ = body["input"].(string)
			_, _ = w.Write([]byte(`{"run_id":"inbound-run"}`))
		case "/v1/runs/inbound-run":
			_, _ = w.Write([]byte(`{"status":"completed","output":"done"}`))
		}
	}))
	defer api.Close()
	host, port, _ := net.SplitHostPort(api.Listener.Addr().String())
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	req := validExecuteRequest("alice", "personal", "path-run", "summarize")
	req.InboundID = "in-90"
	if _, err := (&runtimeHTTP{}).admitHermesRun(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input, "inbox/people/in-90/files/report.pdf") {
		t.Fatalf("agent was not given saved path: %q", input)
	}
}
