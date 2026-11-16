package communication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/letya999/hermes-hub/internal/netguard"
)

// Slack Socket Mode (SLACK_TRANSPORT=socket): the gateway opens an outbound
// WebSocket to Slack instead of exposing a public Events endpoint, so a
// deployment fully inside a VPN perimeter receives messages with no inbound
// listener. The app-level SLACK_APP_TOKEN mints the wss URL through
// apps.connections.open; both the API call and the WebSocket ride the
// bounded HUB_EGRESS_PROXY path. An envelope is acked only after its payload
// is durably ingested — the same at-least-once contract the HTTP Events
// handler keeps with its status code.

const (
	slackSocketReadLimit = 1 << 20
	slackSocketReconnect = 5 * time.Second
	slackSocketAPITime   = 15 * time.Second
)

// slackEventEnvelope is the Events API payload — shared by the HTTP handler
// and the events_api socket frame.
type slackEventEnvelope struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	TeamID    string `json:"team_id"`
	EventID   string `json:"event_id"`
	Event     struct {
		Type        string `json:"type"`
		Subtype     string `json:"subtype"`
		User        string `json:"user"`
		Text        string `json:"text"`
		Channel     string `json:"channel"`
		ChannelType string `json:"channel_type"`
		TS          string `json:"ts"`
		ThreadTS    string `json:"thread_ts"`
		BotID       string `json:"bot_id"`
		UserProfile struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
		} `json:"user_profile"`
	} `json:"event"`
}

// slackSocketClient holds the app token and the two egress transports: one
// bounded-timeout client for apps.connections.open, one for the WebSocket
// handshake. The app token is an auth reference only — it is never logged.
type slackSocketClient struct {
	baseURL  string
	appToken string
	api      *http.Client
	dial     *http.Client
}

func newSlackSocketClient(appToken string) *slackSocketClient {
	return &slackSocketClient{
		baseURL:  "https://slack.com/api",
		appToken: appToken,
		api:      &http.Client{Timeout: slackSocketAPITime, Transport: netguard.EgressTransport()},
		dial:     &http.Client{Timeout: slackSocketAPITime, Transport: netguard.EgressTransport()},
	}
}

func (c *slackSocketClient) openURL(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL, "/")+"/apps.connections.open", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.appToken)
	resp, err := c.api.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, slackSocketReadLimit)).Decode(&out); err != nil {
		return "", fmt.Errorf("slack connections.open decode: %w", err)
	}
	if !out.OK || !validSocketURL(out.URL) {
		if out.Error == "" {
			out.Error = "no socket url"
		}
		return "", fmt.Errorf("slack connections.open: %s", out.Error)
	}
	return out.URL, nil
}

// validSocketURL requires TLS for the Socket Mode endpoint; ws:// is allowed
// only to loopback so tests can exercise the real dial path.
func validSocketURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme == "wss" {
		return u.Host != ""
	}
	if u.Scheme != "ws" {
		return false
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}

// slackSocketLoop keeps one Socket Mode session alive: reconnects are bounded
// by a fixed delay and each outage/recovery flips the named
// slack-socket-egress condition once.
func (g *Gateway) slackSocketLoop(ctx context.Context) {
	for ctx.Err() == nil {
		err := g.slackSocketSession(ctx)
		if ctx.Err() != nil {
			return
		}
		g.noteEgress("slack-socket", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(slackSocketReconnect):
		}
	}
}

func (g *Gateway) slackSocketSession(ctx context.Context) error {
	sockURL, err := g.slackSocket.openURL(ctx)
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, sockURL, &websocket.DialOptions{HTTPClient: g.slackSocket.dial})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(slackSocketReadLimit)
	g.noteEgress("slack-socket", nil)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var frame struct {
			EnvelopeID string          `json:"envelope_id"`
			Type       string          `json:"type"`
			Payload    json.RawMessage `json:"payload"`
			Reason     string          `json:"reason"`
		}
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		if frame.Type == "disconnect" {
			return fmt.Errorf("slack socket disconnect: %s", frame.Reason)
		}
		if err := g.ingestSocketFrame(ctx, frame.EnvelopeID, frame.Type, frame.Payload); err != nil {
			// No ack: Slack retries the envelope a bounded number of times,
			// same contract as a non-200 Events response.
			log.Printf("slack-socket: %s frame not acked: %v", frame.Type, err)
			continue
		}
		if frame.EnvelopeID != "" {
			ack, _ := json.Marshal(map[string]string{"envelope_id": frame.EnvelopeID})
			if err := conn.Write(ctx, websocket.MessageText, ack); err != nil {
				return err
			}
		}
	}
}

// ingestSocketFrame dedupes and ingests one Socket Mode envelope. Message
// events and slash commands land on the same ingestSlackEvent seam as the
// Events API handler, so normalization, allowlists and spool durability are
// identical across transports.
func (g *Gateway) ingestSocketFrame(ctx context.Context, envelopeID, frameType string, payload json.RawMessage) error {
	switch frameType {
	case "events_api":
		var env slackEventEnvelope
		if json.Unmarshal(payload, &env) != nil || env.EventID == "" {
			return errors.New("invalid events_api payload")
		}
		seen, err := g.spool.EventSeen("slack", env.EventID)
		if err != nil || seen {
			return err
		}
		if err := g.ingestSlackEvent(ctx, env.TeamID, env.EventID, env.Event.Type, env.Event.Subtype, env.Event.User, env.Event.Text, env.Event.Channel, env.Event.ChannelType, env.Event.BotID, env.Event.UserProfile.Email, env.Event.UserProfile.DisplayName, env.Event.ThreadTS); err != nil {
			return err
		}
		return g.spool.MarkEvent("slack", env.EventID)
	case "slash_commands":
		var cmd struct {
			Command   string `json:"command"`
			Text      string `json:"text"`
			UserID    string `json:"user_id"`
			ChannelID string `json:"channel_id"`
			TeamID    string `json:"team_id"`
		}
		if json.Unmarshal(payload, &cmd) != nil || !strings.HasPrefix(cmd.Command, "/") || envelopeID == "" {
			return errors.New("invalid slash_commands payload")
		}
		eventID := "slash-" + envelopeID
		seen, err := g.spool.EventSeen("slack", eventID)
		if err != nil || seen {
			return err
		}
		// The slash payload carries no channel_type; the IM-only contract is
		// enforced by ingestSlackEvent, so derive the class from the id — a
		// command typed in a C…/G… channel is denied exactly like an Events
		// API channel message.
		channelType := "channel"
		if strings.HasPrefix(cmd.ChannelID, "D") {
			channelType = "im"
		}
		if err := g.ingestSlackEvent(ctx, cmd.TeamID, eventID, "message", "", cmd.UserID, strings.TrimSpace(cmd.Command+" "+cmd.Text), cmd.ChannelID, channelType, "", "", "", ""); err != nil {
			return err
		}
		return g.spool.MarkEvent("slack", eventID)
	default:
		// hello/interactive/unknown frames carry no work; the session loop
		// still acks the envelope_id so Slack counts delivery done.
		return nil
	}
}
