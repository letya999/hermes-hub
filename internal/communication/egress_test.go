package communication

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A malformed HUB_EGRESS_PROXY refuses startup: the gateway must never boot
// into silent direct egress outside the reviewed perimeter.
func TestConfigRejectsMalformedEgressProxy(t *testing.T) {
	t.Setenv("HUB_EGRESS_PROXY", "http://")
	if _, err := newTestGateway(t, testConfig(t)); err == nil || !strings.Contains(err.Error(), "HUB_EGRESS_PROXY") {
		t.Fatalf("malformed proxy must fail startup closed: %v", err)
	}
}

// The telegram client rides the configured proxy: the request reaches the
// forward proxy in absolute-URI form and never dials the API host directly.
func TestTelegramAPIHonorsEgressProxy(t *testing.T) {
	var got string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.String()
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"username":"bot"}}`))
	}))
	defer proxy.Close()
	t.Setenv("HUB_EGRESS_PROXY", proxy.URL)
	api := newTelegramAPI("http://example.invalid", "token", time.Second)
	if _, err := api.GetMe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "http://example.invalid/bottoken/getMe" {
		t.Fatalf("telegram call did not ride the proxy: %q", got)
	}
}

// The named egress condition logs once per transition — an outage is a
// state, not a per-retry error storm.
func TestNoteEgressLogsTransitions(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	g := &Gateway{}
	down := errors.New("dial refused")
	g.noteEgress("slack-socket", down)
	g.noteEgress("slack-socket", down)
	g.noteEgress("slack-socket", nil)
	g.noteEgress("slack-socket", nil)
	if got := strings.Count(buf.String(), "slack-socket-egress:"); got != 2 {
		t.Fatalf("want unreachable+recovered transitions only, got %d lines:\n%s", got, buf.String())
	}
}
