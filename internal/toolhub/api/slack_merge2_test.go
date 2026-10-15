package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func slackStub(routes map[string]string) *http.Client {
	return &http.Client{Transport: RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		for method, body := range routes {
			if strings.HasSuffix(r.URL.Path, "/"+method) {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":false}`)), Request: r}, nil
	})}
}

func TestSlackBackendCallContract(t *testing.T) {
	def := SlackDefinition(false)
	eff := EffectiveBinding{Definition: def, Connection: &Connection{
		ConnectionID: "conn-1", DefinitionID: def.DefinitionID,
		Metadata: map[string]string{"slack_team": "T1", "slack_user": "U1"},
	}}
	env := map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": "search:read channels:history groups:history im:history mpim:history"}
	backend := SlackBackend{HTTP: slackStub(map[string]string{
		"auth.test":             `{"ok":true,"team_id":"T1","user_id":"U1"}`,
		"conversations.history": `{"ok":true,"messages":[]}`,
	})}
	tool := ToolSpec{Name: "history", Effect: ReadEffect, Arguments: def.Tools[1].Arguments}

	// model-supplied authority selectors are denied before any request
	if _, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"credential_ref": "x"}, env); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("authority arg: %v", err)
	}
	// a definition that does not equal the shipped one is denied
	bad := eff
	bad.Definition.Tools = append(bad.Definition.Tools, ToolSpec{Name: "extra", Effect: WriteEffect})
	if _, err := backend.CallEnv(t.Context(), bad, tool, nil, env); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("def mismatch: %v", err)
	}
	// missing oauth scope denies the call
	if _, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"channel": "C1"}, map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": "chat:write"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("scope: %v", err)
	}
	// undeclared args and bad channel/ts shapes fail before the provider call
	if _, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"channel": "C1", "extra": "x"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("undeclared arg: %v", err)
	}
	if _, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"channel": "X1"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("channel shape: %v", err)
	}
	// happy path returns provider payload
	res, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"channel": "C1"}, env)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	structured, _ := res.Structured.(map[string]any)
	if structured["ok"] != true {
		t.Fatalf("result: %+v", res.Structured)
	}

	// write path: send requires chat:write scope and returns a receipt
	wdef := SlackDefinition(true)
	weff := EffectiveBinding{Definition: wdef, Connection: eff.Connection}
	wenv := map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": "chat:write"}
	wbackend := SlackBackend{HTTP: slackStub(map[string]string{
		"auth.test":        `{"ok":true,"team_id":"T1","user_id":"U1"}`,
		"chat.postMessage": `{"ok":true,"channel":"C1","ts":"1700000000.000001"}`,
	})}
	wtool := wdef.Tools[0]
	res, err = wbackend.CallEnv(t.Context(), weff, wtool, map[string]any{"channel": "C1", "text": "hi"}, wenv)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.Receipt != "T1:C1:1700000000.000001" {
		t.Fatalf("receipt: %q", res.Receipt)
	}
	// a bot account is never a valid owner
	botBackend := SlackBackend{HTTP: slackStub(map[string]string{"auth.test": `{"ok":true,"team_id":"T1","user_id":"U1","bot_id":"B1"}`})}
	if _, err := botBackend.CallEnv(t.Context(), weff, wtool, map[string]any{"channel": "C1", "text": "hi"}, wenv); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bot account: %v", err)
	}
	// dispatcher routes by definition id only
	if _, err := (PersonalProviderBackend{}).CallEnv(t.Context(), EffectiveBinding{Definition: ToolDefinition{DefinitionID: "other"}}, tool, nil, env); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("dispatch unknown: %v", err)
	}
}
