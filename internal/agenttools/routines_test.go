package agenttools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutineToolsCallCommunicationHub(t *testing.T) {
	v := fixture(t)
	if _, err := v.Routine(context.Background(), "routine_list", Input{}); err == nil {
		t.Fatal("routine without hub url succeeded")
	}
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer control-token" || r.Header.Get("X-Hub-Principal") != "alice" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		seen[r.Method+" "+r.URL.Path]++
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/routines":
			_ = json.NewEncoder(w).Encode([]map[string]string{})
		case "POST /v1/routines":
			if !json.Valid(body) {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"schedule_id":"morning"}`))
		case "POST /v1/routines/morning":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"schedule_id":"morning"}`))
		case "POST /v1/routines/morning/pause", "DELETE /v1/routines/morning":
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	v.CommunicationAuth = "control-token"
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	ctx := context.Background()
	if _, err := v.Routine(ctx, "routine_list", Input{}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_create", Input{ID: "morning", Timezone: "UTC", Expression: "once:2099-01-01T00:00:00Z", Text: "brief"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_update", Input{ID: "morning", Expression: "* * * * *", Text: "later"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_pause", Input{ID: "morning"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_delete", Input{ID: "morning"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Routine(ctx, "routine_delete", Input{ID: "bad/id"}); err == nil {
		t.Fatal("slash id accepted")
	}
	if _, err := v.Routine(ctx, "unknown", Input{}); err == nil {
		t.Fatal("unknown op accepted")
	}
	if seen["GET /v1/routines"] != 1 || seen["POST /v1/routines"] != 1 || seen["DELETE /v1/routines/morning"] != 1 {
		t.Fatalf("hub calls: %v", seen)
	}
}

func TestCredentialFormCallCommunicationHub(t *testing.T) {
	v := fixture(t)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/credential-forms" || r.Header.Get("Authorization") != "Bearer control-token" || r.Header.Get("X-Hub-Principal") != "alice" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil || request["service"] != "google" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"input":"protected-form","form_url":"http://127.0.0.1:8081/credentials/id?nonce=nonce","fields":["GOOGLE_OAUTH_CLIENT_SECRET"]}`))
	}))
	defer server.Close()
	v.CommunicationURL = server.URL
	v.CommunicationAuth = "control-token"
	form, err := v.credentialForm(context.Background(), "google", []string{"GOOGLE_OAUTH_CLIENT_SECRET"})
	if err != nil || form["form_url"] != "http://127.0.0.1:8081/credentials/id?nonce=nonce" {
		t.Fatalf("form=%v err=%v", form, err)
	}
	if _, err := v.EnvUpdate(Input{Text: "GOOGLE_OAUTH_CLIENT_SECRET=must-not-be-stored"}); err == nil {
		t.Fatal("env_update accepted credentials with protected form configured")
	}
}
