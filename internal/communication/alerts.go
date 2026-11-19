package communication

import (
	"context"
	"encoding/json"
	"fmt"
	identitycontract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

// Owner alerts (issue #199): a durable, deduplicated, rate-bound notice to
// the owner's home chat when a durable job fails or a runtime's crash
// budget is exhausted. Alerts carry ids and reason words only — never job
// content, never secrets. The rate bound is what makes the path safe: a
// wedged runtime can crash-loop forever without flooding the owner.

const (
	ownerAlertLimit  = 6
	ownerAlertWindow = time.Hour
)

type ownerAlertLedger struct {
	windowStart time.Time
	sent        int
	suppressed  int
}

// ownerHome resolves the user's private notice address: the first
// verified telegram id still mapped to that user, else their slack DM.
func (g *Gateway) ownerHome(user User) (chatID int64, slackChannel string, ok bool) {
	if g.config.Identity == nil || !user.Enabled {
		return 0, "", false
	}
	bindings, err := g.config.Identity.Bindings(context.Background(), user.ID)
	if err != nil {
		return 0, "", false
	}
	for _, binding := range bindings {
		if binding.PrincipalID == user.ID && binding.Channel.Channel == identitycontract.Telegram {
			id, err := strconv.ParseInt(binding.Channel.ID, 10, 64)
			if err == nil && id > 0 {
				return id, "", true
			}
		}
	}
	for _, binding := range bindings {
		if binding.PrincipalID == user.ID && binding.Channel.Channel == identitycontract.Slack {
			_, id, found := strings.Cut(binding.Channel.ID, "/")
			if found && id != "" {
				return 0, strings.ToUpper(id), true
			}
		}
	}
	return 0, "", false
}

// alertOwner enqueues one deduped delivery to the owner's home chat.
// kind+dedupKey form the idempotency id, so a repeated cause never
// re-alerts; a fresh epoch (new generation, new crash count) gets a new
// key and alerts again. The rolling window caps the rate; over-cap alerts
// are counted and surface once as a suppressed summary.
func (g *Gateway) alertOwner(user User, kind, dedupKey, text string) {
	if !user.Enabled {
		return
	}
	chatID, slackChannel, ok := g.ownerHome(user)
	if !ok {
		return
	}
	now := g.now()
	g.alertMu.Lock()
	led := &g.alertLedger
	if led.windowStart.IsZero() || now.Sub(led.windowStart) >= ownerAlertWindow {
		// A closed window flushes its suppressed count as one line —
		// the owner learns alerts were dropped, never how many times
		// the same cause repeated.
		if led.suppressed > 0 {
			lang := locale.LangOf(user.WorkspaceDir)
			n := led.suppressed
			summary := fmt.Sprintf(locale.T(lang, "alert.suppressed"), n)
			key := fmt.Sprintf("alert-suppressed-%d", led.windowStart.Unix())
			g.enqueueAlert(user, chatID, slackChannel, key, summary)
		}
		led.windowStart, led.sent, led.suppressed = now, 0, 0
	}
	if led.sent >= ownerAlertLimit {
		led.suppressed++
		g.alertMu.Unlock()
		return
	}
	led.sent++
	g.alertMu.Unlock()
	g.enqueueAlert(user, chatID, slackChannel, "alert-"+kind+"-"+dedupKey, text)
}

func (g *Gateway) enqueueAlert(user User, chatID int64, slackChannel, key, text string) {
	channel := "telegram_bot"
	if slackChannel != "" {
		channel = "slack_app"
	}
	_ = g.spool.EnqueueDelivery(Delivery{
		ID: key, IdempotencyKey: key,
		OwnerPrincipalID: user.ID, OwnerContextID: user.ID,
		Channel: channel, ChatID: chatID, SlackChannel: slackChannel,
		Text: text, CreatedAt: g.now().UTC(),
	})
}

// alertJobFailed fires on a durable failed outcome — never on uncertain
// (the reconciliation window owns that path) and never for content. When
// the terminal error delivery already landed in the owner's home chat
// (DM-origin jobs) the alert is skipped: two messages for one failure is
// noise, and the contextual error carries the better hint.
func (g *Gateway) alertJobFailed(job Job) {
	user := g.user(job.UserID)
	chatID, slackChannel, ok := g.ownerHome(user)
	if !ok {
		return
	}
	if (chatID != 0 && chatID == job.ChatID) || (slackChannel != "" && slackChannel == job.SlackChannel) {
		return
	}
	lang := locale.LangOf(user.WorkspaceDir)
	g.alertOwner(user, "job-failed", job.ID, fmt.Sprintf(locale.T(lang, "alert.job-failed"), job.ID))
}

// supervisorStatusWire decodes only the fields the alert sweep needs —
// the full bounded contract lives in supervisor.StatusView; the gateway
// must not import supervisor internals for a read-only wire.
type supervisorStatusWire struct {
	Runtimes []struct {
		PrincipalID string `json:"principal_id"`
		ContextID   string `json:"context_id"`
		Generation  string `json:"generation"`
		Phase       string `json:"phase"`
		CrashCount  int    `json:"crash_count"`
		Conditions  []struct {
			Name   string `json:"name"`
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		} `json:"conditions"`
	} `json:"runtimes"`
}

const supervisorStatusSweep = time.Minute

// alertCrashBudgets sweeps the supervisor status once a minute and alerts
// the owner when a runtime's crash budget is freshly consumed. The dedup
// key carries generation+count so a steady wedged state alerts once while
// a new crash epoch re-alerts. Supervised spaces only: the unsupervised
// gateway has no supervisor to ask.
func (g *Gateway) alertCrashBudgets(ctx context.Context) {
	if !g.config.Supervised || g.config.RuntimeURL == "" || g.config.RuntimeAuth == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/status", nil) // #nosec G704 -- operator-configured control-plane origin
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var view supervisorStatusWire
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&view); err != nil {
		return
	}
	for _, rt := range view.Runtimes {
		if rt.Phase != "failed" {
			continue
		}
		exhausted := false
		for _, c := range rt.Conditions {
			if c.Name == "crash_budget" && c.Reason == "budget_exhausted" {
				exhausted = true
			}
		}
		if !exhausted {
			continue
		}
		user := g.user(rt.PrincipalID)
		lang := locale.LangOf(user.WorkspaceDir)
		scope := rt.PrincipalID + ":" + rt.ContextID
		dedup := scope + "-" + rt.Generation + "-" + fmt.Sprint(rt.CrashCount)
		g.alertOwner(user, "crash-budget", dedup, fmt.Sprintf(locale.T(lang, "alert.crash-budget"), scope, rt.CrashCount))
	}
}
