package communication

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/envstore"
)

// Slack IM/channel ids (D…/C…/G…) are not identity.ValidID; they are only a
// chat.postMessage target and never fill Envelope.DeliveryTargetID.
var slackIMChannel = regexp.MustCompile(`^[CDG][A-Za-z0-9]{1,32}$`)
var slackThreadTS = regexp.MustCompile(`^[0-9]{10,16}\.[0-9]{1,6}$`)

func validSlackIMChannel(id string) bool { return slackIMChannel.MatchString(id) }
func validSlackThread(ts string) bool    { return ts == "" || slackThreadTS.MatchString(ts) }

const slackTimestampSkew = 5 * time.Minute

type SlackAPI interface {
	PostMessage(ctx context.Context, channel, thread, text string) error
}

type slackAPI struct {
	token   string
	client  *http.Client
	baseURL string
}

func newSlackAPI(token string) SlackAPI {
	return &slackAPI{token: token, client: &http.Client{Timeout: 15 * time.Second}, baseURL: "https://slack.com/api"}
}

func (s *slackAPI) PostMessage(ctx context.Context, channel, thread, text string) error {
	if !validSlackIMChannel(channel) || strings.TrimSpace(text) == "" || !validSlackThread(thread) {
		return errors.New("invalid slack delivery")
	}
	payload := map[string]string{"channel": channel, "text": limitTelegramText(text)}
	if thread != "" {
		payload["thread_ts"] = thread
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	base := s.baseURL
	if base == "" {
		base = "https://slack.com/api"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return errors.New("slack delivery failed")
	}
	return nil
}

// VerifySlackSignature implements Slack's official Events API HMAC:
// v0:{timestamp}:{raw body} signed with the signing secret.
func VerifySlackSignature(secret, timestamp, signature string, body []byte, now time.Time) error {
	if secret == "" || timestamp == "" || !strings.HasPrefix(signature, "v0=") {
		return errors.New("missing slack signature")
	}
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("invalid slack timestamp")
	}
	if now.IsZero() {
		now = time.Now()
	}
	delta := now.UTC().Unix() - unix
	if delta < 0 {
		delta = -delta
	}
	if delta > int64(slackTimestampSkew/time.Second) {
		return errors.New("stale slack timestamp")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":"))
	_, _ = mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return errors.New("invalid slack signature")
	}
	return nil
}

func (g *Gateway) HandleSlackEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := VerifySlackSignature(g.config.SlackSigningSecret, r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), body, g.now()); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var envelope struct {
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
	if json.Unmarshal(body, &envelope) != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if envelope.Type == "url_verification" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(envelope.Challenge))
		return
	}
	if envelope.Type != "event_callback" || envelope.EventID == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	fresh, err := g.spool.RememberEvent("slack", envelope.EventID)
	if err != nil || !fresh {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := g.ingestSlackEvent(r.Context(), envelope.TeamID, envelope.EventID, envelope.Event.Type, envelope.Event.Subtype, envelope.Event.User, envelope.Event.Text, envelope.Event.Channel, envelope.Event.ChannelType, envelope.Event.BotID, envelope.Event.UserProfile.Email, envelope.Event.UserProfile.DisplayName, envelope.Event.ThreadTS); err != nil {
		http.Error(w, "rejected", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (g *Gateway) ingestSlackEvent(ctx context.Context, teamID, eventID, eventType, subtype, slackUser, text, channel, channelType, botID, email, displayName, threadTS string) error {
	_ = ctx
	_ = email
	_ = displayName
	if eventType != "message" || botID != "" || subtype == "bot_message" {
		return nil
	}
	if channelType != "im" || !validSlackIMChannel(channel) {
		return errors.New("slack audience denied")
	}
	if !validSlackThread(threadTS) {
		threadTS = ""
	}
	user, ok := g.slackUsers[strings.ToLower(teamID)+"/"+strings.ToLower(slackUser)]
	if !ok {
		return errors.New("unmapped slack sender")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	envelope := user.slackEnvelope(teamID, slackUser)
	if envstore.LooksLikeEnv(text) {
		return g.spool.EnqueueDelivery(Delivery{ID: "slack-" + eventID + "-secret", IdempotencyKey: "slack-" + eventID + "-secret", Channel: "slack_app", ConversationID: envelope.ConversationID, DeliveryTargetID: envelope.DeliveryTargetID, SlackChannel: channel, SlackThread: threadTS, Text: "Секреты в Slack App канале отклонены.", CreatedAt: g.now().UTC()})
	}
	job := Job{Envelope: envelope, ID: "slack-" + eventID, OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID, ScopeID: "user:" + user.ID, Channel: "slack_app", Trigger: "message", IdempotencyKey: "slack:" + eventID, Text: text, SlackChannel: channel, SlackThread: threadTS, CreatedAt: g.now().UTC()}
	_, err := g.spool.Enqueue(job)
	return err
}

func (s *Spool) RememberEvent(provider, eventID string) (bool, error) {
	if provider == "" || eventID == "" || strings.ContainsAny(eventID, "/\\") {
		return false, errors.New("invalid event id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := sha256.Sum256([]byte(provider + "\x00" + eventID))
	path := filepath.Join(s.root, "events", hex.EncodeToString(sum[:])+".json")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, atomicJSON(path, map[string]string{"provider": provider, "event_id": eventID})
}
