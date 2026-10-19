package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func slackEffective() EffectiveBinding {
	return EffectiveBinding{
		Definition: SlackDefinition(false),
		Connection: &Connection{Metadata: map[string]string{"slack_team": "T123", "slack_user": "U456"}},
	}
}

func TestSlackBackendCallEnv(t *testing.T) {
	backend := SlackBackend{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			return jsonResponse(401, `{"ok":false}`), nil
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth.test"):
			return jsonResponse(200, `{"ok":true,"team_id":"T123","user_id":"U456"}`), nil
		case strings.HasSuffix(r.URL.Path, "/conversations.history"):
			return jsonResponse(200, `{"ok":true,"messages":[{"ts":"1.000001"}]}`), nil
		default:
			return jsonResponse(200, `{"ok":true}`), nil
		}
	})}}
	env := map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": "search:read channels:history groups:history im:history mpim:history"}
	e := slackEffective()
	tool := SlackDefinition(false).Tools[1] // history
	result, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"channel": "C789"}, env)
	if err != nil || result.Structured == nil {
		t.Fatalf("history call: %v %+v", err, result)
	}
	if _, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"channel": "C789"}, map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": "chat:write"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing scope admitted: %v", err)
	}
	if _, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"channel": "C789", "approved_by": "x"}, env); err == nil {
		t.Fatal("authority argument passed")
	}
	if _, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"channel": "X789"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-channel id: %v", err)
	}
	if _, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"channel": "C789", "ts": "not-a-ts"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad ts: %v", err)
	}
	if _, err := backend.CallEnv(context.Background(), slackEffective(), SlackDefinition(false).Tools[0], map[string]any{"channel": "C789"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("search without query: %v", err)
	}
	if _, err := backend.CallEnv(context.Background(), EffectiveBinding{Definition: SlackDefinition(true), Connection: e.Connection}, tool, map[string]any{"channel": "C789"}, env); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("definition mismatch admitted: %v", err)
	}
	if _, err := backend.CallEnv(context.Background(), e, ToolSpec{Name: "unknown", Effect: ReadEffect}, map[string]any{}, env); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown tool admitted: %v", err)
	}
}

func TestSlackWriteAndAccountOps(t *testing.T) {
	backend := SlackBackend{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth.test"):
			return jsonResponse(200, `{"ok":true,"team_id":"T123","user_id":"U456"}`), nil
		case strings.HasSuffix(r.URL.Path, "/chat.postMessage"):
			return jsonResponse(200, `{"ok":true,"channel":"C789","ts":"1234.000001"}`), nil
		case strings.HasSuffix(r.URL.Path, "/auth.revoke"):
			return jsonResponse(200, `{"ok":true,"revoked":true}`), nil
		default:
			return jsonResponse(200, `{"ok":true}`), nil
		}
	})}}
	env := map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": "chat:write"}
	e := EffectiveBinding{
		Definition: SlackDefinition(true),
		Connection: &Connection{Metadata: map[string]string{"slack_team": "T123", "slack_user": "U456"}},
	}
	tool := SlackDefinition(true).Tools[0] // send
	result, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"channel": "C789", "text": "hi"}, env)
	if err != nil || result.Receipt == "" {
		t.Fatalf("send: %v %+v", err, result)
	}
	if err := backend.VerifyAccount(context.Background(), "tok", "T123", "U456"); err != nil {
		t.Fatal(err)
	}
	if err := backend.VerifyAccount(context.Background(), "tok", "T999", "U456"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("foreign team verified: %v", err)
	}
	if err := backend.Revoke(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
	if d := SlackScopes(false); len(d) == 0 || !slices.Contains(d, "search:read") {
		t.Fatalf("read scopes: %v", d)
	}
}
