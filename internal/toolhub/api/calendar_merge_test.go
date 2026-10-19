package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func calendarEffective() EffectiveBinding {
	return EffectiveBinding{
		Definition: CalendarDefinition(false),
		Connection: &Connection{Metadata: map[string]string{"google_sub": "sub-1"}},
	}
}

func TestCalendarBackendCallEnv(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	backend := CalendarBackend{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		if r.Header.Get("Authorization") != "Bearer tok" {
			return jsonResponse(401, `{}`), nil
		}
		return jsonResponse(200, `{"kind":"calendar#events","items":[]}`), nil
	})}}
	env := map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": CalendarReadScope + " other"}
	e := calendarEffective()
	tool := CalendarDefinition(false).Tools[0] // list
	result, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"calendar_id": "primary", "page_token": "p2"}, env)
	if err != nil || result.Structured == nil {
		t.Fatalf("list: %v %+v", err, result)
	}
	if gotMethod != http.MethodGet || !strings.Contains(gotPath, "/calendars/primary/events") || !strings.Contains(gotQuery, "pageToken=p2") {
		t.Fatalf("request: %s %s?%s", gotMethod, gotPath, gotQuery)
	}
	if _, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"calendar_id": "primary"}, map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": "other"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("scope missing admitted: %v", err)
	}
	if _, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"calendar_id": "../evil"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("traversal calendar id: %v", err)
	}
	if _, err := backend.CallEnv(context.Background(), e, tool, map[string]any{"calendar_id": "primary", "ghost": "x"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("undeclared arg: %v", err)
	}
	get := CalendarDefinition(false).Tools[1]
	if _, err := backend.CallEnv(context.Background(), e, get, map[string]any{"calendar_id": "primary", "event_id": "e/1"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("traversal event id: %v", err)
	}
}

func TestCalendarWriteAndDelete(t *testing.T) {
	var gotMethod string
	backend := CalendarBackend{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotMethod = r.Method
		switch {
		case strings.Contains(r.URL.Path, "/events/ev1"):
			return jsonResponse(200, `{"id":"ev1"}`), nil
		default:
			return jsonResponse(200, `{"id":"ev-new"}`), nil
		}
	})}}
	env := map[string]string{"ACCESS_TOKEN": "tok", "OAUTH_SCOPE": CalendarWriteScope}
	e := EffectiveBinding{
		Definition: CalendarDefinition(true),
		Connection: &Connection{Metadata: map[string]string{"google_sub": "sub-1"}},
	}
	update := CalendarDefinition(true).Tools[1]
	result, err := backend.CallEnv(context.Background(), e, update, map[string]any{"calendar_id": "primary", "event_id": "ev1", "event_json": `{"summary":"x"}`}, env)
	if err != nil || result.Receipt == "" || gotMethod != http.MethodPatch {
		t.Fatalf("update: %v %s %+v", err, gotMethod, result)
	}
	if _, err := backend.CallEnv(context.Background(), e, update, map[string]any{"calendar_id": "primary", "event_id": "ev1", "event_json": `{`}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad event json: %v", err)
	}
	del := CalendarDefinition(true).Tools[2]
	delBackend := CalendarBackend{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 204, Body: http.NoBody, Header: make(http.Header)}, nil
	})}}
	result, err = delBackend.CallEnv(context.Background(), e, del, map[string]any{"calendar_id": "primary", "event_id": "ev1"}, env)
	if err != nil || result.Receipt == "" {
		t.Fatalf("delete: %v %+v", err, result)
	}
	if _, err := delBackend.CallEnv(context.Background(), e, del, map[string]any{"calendar_id": "primary"}, env); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete without event id: %v", err)
	}
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"primary", true}, {"ev_1-x", true},
		{"", false}, {"..", false}, {"a/b", false}, {"a?b", false},
	} {
		if got := CalendarResource(tc.v); got != tc.want {
			t.Fatalf("CalendarResource(%q) = %v", tc.v, got)
		}
	}
}
