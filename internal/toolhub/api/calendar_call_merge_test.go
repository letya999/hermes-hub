package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCalendarBackendCallContract(t *testing.T) {
	def := CalendarDefinition(false)
	conn := &Connection{ConnectionID: "c1", DefinitionID: def.DefinitionID, Metadata: map[string]string{"google_sub": "sub-1"}}
	eff := EffectiveBinding{Definition: def, Connection: conn}
	env := map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": CalendarReadScope}
	client := &http.Client{Transport: RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"kind":"calendar#events","items":[]}`)), Request: r}, nil
	})}
	backend := CalendarBackend{HTTP: client}
	tool := def.Tools[0] // list

	// denied without the shipped definition, connection owner, or scope
	if _, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"calendar_id": "c"}, map[string]string{"ACCESS_TOKEN": "tok"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("no scope: %v", err)
	}
	noConn := eff
	noConn.Connection = nil
	if _, err := backend.CallEnv(t.Context(), noConn, tool, map[string]any{"calendar_id": "c"}, env); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("no connection: %v", err)
	}
	if _, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"credential_locator": "x"}, env); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("authority arg: %v", err)
	}
	// undeclared args and path-unsafe calendar ids are rejected pre-request
	if _, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"calendar_id": "c", "x": "1"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("extra arg: %v", err)
	}
	if _, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"calendar_id": "../escape"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("calendar escape: %v", err)
	}
	res, err := backend.CallEnv(t.Context(), eff, tool, map[string]any{"calendar_id": "cal-1", "page_token": "n1"}, env)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	payload, _ := res.Structured.(map[string]any)
	if payload["kind"] != "calendar#events" {
		t.Fatalf("payload: %+v", res.Structured)
	}

	// write path: update returns a receipt bound to the event id
	wdef := CalendarDefinition(true)
	weff := EffectiveBinding{Definition: wdef, Connection: conn}
	wenv := map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": CalendarWriteScope}
	wclient := &http.Client{Transport: RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPatch {
			return &http.Response{StatusCode: 405, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"ev-1"}`)), Request: r}, nil
	})}
	wbackend := CalendarBackend{HTTP: wclient}
	wtool := wdef.Tools[1] // update
	res, err = wbackend.CallEnv(t.Context(), weff, wtool, map[string]any{"calendar_id": "cal-1", "event_id": "ev-1", "event_json": `{"summary":"x"}`}, wenv)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if res.Receipt != "cal-1:ev-1" {
		t.Fatalf("receipt: %q", res.Receipt)
	}
	// invalid event_json never reaches the provider
	if _, err := wbackend.CallEnv(t.Context(), weff, wtool, map[string]any{"calendar_id": "c", "event_id": "e", "event_json": "not json"}, wenv); !errors.Is(err, ErrInvalid) {
		t.Fatalf("event json: %v", err)
	}
}
