package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

type masterHold struct {
	PrincipalID   string `json:"principal_id"`
	ContextID     string `json:"context_id"`
	Active        bool   `json:"active"`
	Epoch         uint64 `json:"epoch"`
	Reason        string `json:"reason"`
	NoticeID      string `json:"notice_id"`
	NoticePending bool   `json:"notice_pending"`
}

// holdsSnapshot returns the last fetched hold projection — the pure read
// the spool's under-lock admission check needs (no I/O under the lock).
func (g *Gateway) holdsSnapshot() []masterHold {
	g.holdsMu.Lock()
	defer g.holdsMu.Unlock()
	return g.holdsLast
}

// refreshHolds fetches the hold projection at the public enqueue boundary;
// enqueueLocked itself only reads the snapshot so no caller waits on I/O
// while holding the spool lock.
func (g *Gateway) refreshHolds(ctx context.Context) error {
	holds, err := g.masterHolds(ctx)
	if err != nil {
		return err
	}
	g.holdsMu.Lock()
	g.holdsLast = holds
	g.holdsMu.Unlock()
	return nil
}

func (g *Gateway) masterHolds(ctx context.Context) ([]masterHold, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/holds", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("hold projection unavailable")
	}
	var holds []masterHold
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&holds); err != nil {
		return nil, err
	}
	return holds, nil
}

// syncMasterNotices transfers a durable hold notice to the existing outbox
// before acknowledging it. Neither step needs the contained Hermes runtime.
func (g *Gateway) syncMasterNotices(ctx context.Context) (bool, error) {
	if !g.config.Supervised || g.config.RuntimeURL == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := strings.TrimRight(g.config.RuntimeURL, "/")
	holds, err := g.masterHolds(ctx)
	if err != nil {
		return false, err
	}
	pending := false
	for _, hold := range holds {
		if !hold.NoticePending || hold.NoticeID == "" {
			continue
		}
		pending = true
		user, ok := g.configUser(hold.PrincipalID)
		if !ok || len(user.TelegramIDs) == 0 {
			// No channel can ever carry this notice: acknowledge it as closed
			// rather than spinning the poll loop on it forever.
			if err := ackHoldNotice(ctx, client, base, g.config.RuntimeAuth, hold.NoticeID); err != nil {
				return pending, err
			}
			continue
		}
		text := locale.T(locale.LangOf(user.WorkspaceDir), "hold.notice", hold.Reason)
		if err := g.queueDelivery(ctx, hold.NoticeID, user.TelegramIDs[0], 0, text); err != nil {
			return pending, err
		}
		if err := ackHoldNotice(ctx, client, base, g.config.RuntimeAuth, hold.NoticeID); err != nil {
			return pending, err
		}
	}
	return pending, nil
}

func ackHoldNotice(ctx context.Context, client *http.Client, base, auth, noticeID string) error {
	body, _ := json.Marshal(struct {
		NoticeID string `json:"notice_id"`
	}{noticeID})
	ack, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/holds/ack", bytes.NewReader(body))
	if err != nil {
		return err
	}
	ack.Header.Set("Authorization", "Bearer "+auth)
	ack.Header.Set("Content-Type", "application/json")
	result, err := client.Do(ack)
	if err != nil {
		return err
	}
	_ = result.Body.Close()
	if result.StatusCode != 200 {
		return errors.New("hold notice acknowledgement unavailable")
	}
	return nil
}
