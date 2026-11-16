package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func socketConfig(t *testing.T) Config {
	t.Helper()
	c := testConfig(t)
	c.SlackBotToken = "xoxb-test"
	c.SlackAppToken = "xapp-test"
	c.SlackTransport = "socket"
	c.Users[0].SlackIDs = []SlackLink{{TeamID: "TTEAM", UserID: "UUSER"}}
	return c
}

func TestSlackTransportValidation(t *testing.T) {
	c := testConfig(t)
	if _, err := New(c); err != nil {
		t.Fatalf("default must stay events: %v", err)
	}
	c.SlackTransport = "carrier-pigeon"
	if _, err := New(c); err == nil || !strings.Contains(err.Error(), "SLACK_TRANSPORT") {
		t.Fatalf("unsupported transport must fail: %v", err)
	}
	c = socketConfig(t)
	c.SlackAppToken = ""
	if _, err := New(c); err == nil || !strings.Contains(err.Error(), "SLACK_APP_TOKEN") {
		t.Fatalf("socket without app token must fail: %v", err)
	}
	if _, err := New(socketConfig(t)); err != nil {
		t.Fatalf("socket with app+bot token must start: %v", err)
	}
}

// Socket Mode mounts no webhook: the events route answers 404 while the rest
// of the control-plane mux is untouched.
func TestSlackSocketModeDropsEventsRoute(t *testing.T) {
	g, err := New(socketConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/slack/events", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("events route must be off in socket mode: %d", rec.Code)
	}
}

// The WebSocket handshake must ride the configured egress proxy — a dead
// proxy means the dial fails at the proxy, never direct.
func TestSlackSocketDialRidesEgressProxy(t *testing.T) {
	t.Setenv("HUB_EGRESS_PROXY", "http://127.0.0.1:1")
	client := newSlackSocketClient("xapp-test")
	conn, _, err := websocket.Dial(context.Background(), "wss://example.invalid/ws", &websocket.DialOptions{HTTPClient: client.dial})
	if err == nil {
		conn.CloseNow()
		t.Fatal("dead proxy must fail the dial")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("dial must fail at the proxy, not the destination: %v", err)
	}
}

// End-to-end over a fake Slack: apps.connections.open hands back the socket
// URL, an events_api envelope lands as a durable job, a slash_commands
// envelope runs the command path — each acked after durable ingest.
func TestSlackSocketSessionDeliversAndAcks(t *testing.T) {
	g, err := New(socketConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	acks := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps.connections.open":
			if got := r.Header.Get("Authorization"); got != "Bearer xapp-test" {
				t.Errorf("app token header = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": "ws://" + r.Host + "/ws"})
		case "/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			ctx := r.Context()
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"envelope_id":"env-1","type":"events_api","payload":{"type":"event_callback","team_id":"TTEAM","event_id":"EvSock1","event":{"type":"message","user":"UUSER","text":"hello socket","channel":"D001","channel_type":"im"}}}`))
			_, ack, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var env struct {
				EnvelopeID string `json:"envelope_id"`
			}
			_ = json.Unmarshal(ack, &env)
			acks <- env.EnvelopeID
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"envelope_id":"env-2","type":"slash_commands","payload":{"command":"/status","text":"","user_id":"UUSER","channel_id":"D001","team_id":"TTEAM"}}`))
			_, ack, err = conn.Read(ctx)
			if err != nil {
				return
			}
			_ = json.Unmarshal(ack, &env)
			acks <- env.EnvelopeID
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	g.slackSocket = newSlackSocketClient("xapp-test")
	g.slackSocket.baseURL = srv.URL + "/api"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// The server closes after two frames; the session ends on the closed
	// connection — an error return is the expected terminal state.
	_ = g.slackSocketSession(ctx)
	if got := <-acks; got != "env-1" {
		t.Fatalf("events ack = %q", got)
	}
	if got := <-acks; got != "env-2" {
		t.Fatalf("slash ack = %q", got)
	}
	if seen, _ := g.spool.EventSeen("slack", "EvSock1"); !seen {
		t.Fatal("events_api envelope not marked durable")
	}
	if seen, _ := g.spool.EventSeen("slack", "slash-env-2"); !seen {
		t.Fatal("slash_commands envelope not marked durable")
	}
	if job, _ := g.spool.ClaimJob(); job == nil || job.ID != "slack-EvSock1" {
		t.Fatalf("message event did not become a job: %+v", job)
	}
}

// Replayed envelopes are deduped before ingest: the durable marker, not the
// retry, decides whether the payload is new.
func TestSlackSocketDedupesReplayedEnvelope(t *testing.T) {
	g, err := New(socketConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"type":"event_callback","team_id":"TTEAM","event_id":"EvDup","event":{"type":"message","user":"UUSER","text":"same","channel":"D001","channel_type":"im"}}`)
	if err := g.ingestSocketFrame(context.Background(), "env-a", "events_api", payload); err != nil {
		t.Fatal(err)
	}
	if err := g.ingestSocketFrame(context.Background(), "env-b", "events_api", payload); err != nil {
		t.Fatal(err)
	}
	job, _ := g.spool.ClaimJob()
	if job == nil || job.ID != "slack-EvDup" {
		t.Fatalf("expected one job, got %+v", job)
	}
	if dup, _ := g.spool.ClaimJob(); dup != nil {
		t.Fatalf("replay produced a second job: %+v", dup)
	}
}
