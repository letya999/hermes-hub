// nightjob.go — the hub-owned per-space nightly memory rollup driver
// (SPEC-0085 R5.1, ADR-0086 D4): the control loop ticks it once the
// space-local clock crosses memory.rollup_time; night.Run owns every
// read, gate, write and commit — this file only binds the seams.
package communication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/memory/night"
	"github.com/letya999/hermes-hub/internal/memory/prune"
)

// nightDue reports whether the rollup should fire: the space-local clock
// has passed today's rollup time and the marker is an earlier day.
// An empty rollup time disables the job.
func nightDue(now time.Time, rollupAt, lastRunDay string) bool {
	if rollupAt == "" {
		return false
	}
	t, err := time.Parse("15:04", rollupAt)
	if err != nil {
		return false
	}
	local := now.Local()
	due := time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, local.Location())
	if local.Before(due) {
		return false
	}
	return lastRunDay != local.Format("2006-01-02")
}

// tickNightRollup fires the gated rollup once the space-local clock
// crosses memory.rollup_time (SPEC-0085 R5.1).
func (g *Gateway) tickNightRollup(now time.Time) {
	if g.memory == nil || g.config.VaultDir == "" {
		return
	}
	marker := filepath.Join(g.config.VaultDir, "runtime", "memory", "night", "last-run.txt")
	last, _ := os.ReadFile(marker)
	if !nightDue(now, g.config.MemoryRollupTime, strings.TrimSpace(string(last))) {
		return
	}
	// R5.7: verified intents become owner-approval draft assignments in
	// the owner's own envelope — never facts, never auto-applied.
	var sink func(context.Context, night.IntentProposal) error
	owner := g.nightOwner()
	if owner.ID != "" && len(owner.TelegramIDs) > 0 {
		caller := owner.Envelope(owner.TelegramIDs[0])
		sink = func(_ context.Context, p night.IntentProposal) error {
			return g.draftNightIntent(caller, p)
		}
	}
	relay := g.nightRelay
	if relay == nil {
		relay = nightRelayCall
	}
	// The job folds the last closed local day — firing at rollup_time on
	// day D rolls up leaf D-1, so every calendar leaf is complete when it
	// is promoted and each is processed exactly once.
	res, err := night.Run(context.Background(), g.config.VaultDir, now.AddDate(0, 0, -1), night.Options{
		Relay:      relay,
		Now:        now,
		TZ:         time.Local,
		IntentSink: sink,
		FoldBytes:  int64(g.config.MemoryLeafFoldBytes),
	})
	if err != nil {
		// An errored run leaves the day unmarked — the next tick resumes
		// from the cached steps and spends only the remaining budget.
		return
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		return
	}
	_ = os.WriteFile(marker, []byte(now.Local().Format("2006-01-02")), 0600)
	// R5.9 fold-up sweep: close the most recently finished week, month
	// and year whose summaries are still missing.
	g.nightFolds(now, night.Options{Relay: relay, Now: now, TZ: time.Local}, res.Calls)
	digest := res.Digest
	// R7.5 leaf prune: after the night's distillation, retention-age
	// verified leaves expire and oversized completed leaves fold — every
	// lane is fail-closed on the day's rollup state. Prune actions merge
	// into the same morning digest so housekeeping is never silent.
	if g.config.MemoryLeafRetentionDays > 0 || g.config.MemoryLeafFoldBytes > 0 {
		if pr, err := prune.Apply(g.config.VaultDir, prune.Options{
			RetentionDays: g.config.MemoryLeafRetentionDays,
			FoldBytes:     int64(g.config.MemoryLeafFoldBytes),
			Now:           now, TZ: time.Local,
		}); err == nil {
			digest = pruneLine(digest, pr, locale.LangOf(g.nightOwner().WorkspaceDir))
		}
	}
	if digest != "" {
		g.deliverNightDigest(digest)
	}
}

// pruneLine folds the prune report into the night digest — the actions are
// data lines like the promoted facts they follow.
func pruneLine(digest string, pr prune.Report, lang locale.Lang) string {
	for _, fr := range pr.Folded {
		digest += "\n- " + locale.T(lang, "memory.prune-folded", fr.Day, fr.Before, fr.After, fr.Stubs)
	}
	for _, rel := range pr.Expired {
		day := strings.TrimSuffix(filepath.Base(rel), ".md")
		digest += "\n- " + locale.T(lang, "memory.prune-expired", day)
	}
	for _, k := range pr.Kept {
		key := "memory.prune-kept-incomplete"
		if k.Reason == "day summary missing" {
			key = "memory.prune-kept-summary"
		}
		digest += "\n- " + locale.T(lang, key, k.Day)
	}
	return digest
}

// nightFolds attempts the R5.9 fold for each just-closed period whose
// summary is absent — the week ending last Sunday, last month, last
// year. Every fold self-gates on terminal inputs, so the sweep costs
// one stat per period on ordinary nights and retries a failed or
// late-completing fold on the next. Fold calls share the night's spend
// cap with the day run; a fold error stops the sweep and the next night
// retries it.
func (g *Gateway) nightFolds(now time.Time, o night.Options, spent int) {
	limit := o.MaxCalls
	if limit <= 0 {
		limit = 40
	}
	local := now.Local()
	weekDay := local.AddDate(0, 0, -7)
	wy, ww := weekDay.ISOWeek()
	monthDay := local.AddDate(0, -1, 0)
	yearDay := local.AddDate(-1, 0, 0)
	folds := []struct {
		rel  string
		day  time.Time
		fold func(context.Context, string, time.Time, night.Options) (night.Result, error)
	}{
		{fmt.Sprintf("memory/summaries/weekly/%04d-W%02d.md", wy, ww), weekDay, night.FoldWeek},
		{fmt.Sprintf("memory/summaries/monthly/%s.md", monthDay.Format("2006-01")), monthDay, night.FoldMonth},
		{fmt.Sprintf("memory/summaries/yearly/%04d.md", yearDay.Year()), yearDay, night.FoldYear},
	}
	for _, f := range folds {
		if spent >= limit {
			return
		}
		if _, err := os.Stat(filepath.Join(g.config.VaultDir, filepath.FromSlash(f.rel))); err == nil {
			continue
		}
		fr, err := f.fold(context.Background(), g.config.VaultDir, f.day, o)
		spent += fr.Calls
		if err != nil {
			return
		}
	}
}

// deliverNightDigest spools the "what I remembered" digest to the owner's
// DM through the ordinary delivery outbox (R5.6).
func (g *Gateway) deliverNightDigest(digest string) {
	owner := g.nightOwner()
	if owner.ID == "" || len(owner.TelegramIDs) == 0 {
		return
	}
	chatID := owner.TelegramIDs[0]
	key := "night-digest-" + g.now().Local().Format("2006-01-02")
	target := "telegram-" + strconv.FormatInt(chatID, 10)
	_ = g.spool.EnqueueDelivery(Delivery{
		ID: key, IdempotencyKey: key, Channel: "telegram_bot", ChatID: chatID,
		ConversationID: target, DeliveryTargetID: target,
		Text:      locale.T(locale.LangOf(owner.WorkspaceDir), "memory.night-digest", digest),
		CreatedAt: g.now().UTC(),
	})
}

// nightOwner is the single-vault space's owner — the first enabled user.
// Shared gateways never mount one VaultDir, so this shape is already
// single-space by construction.
func (g *Gateway) nightOwner() User {
	for _, u := range g.config.Users {
		if u.Enabled {
			return u
		}
	}
	return User{}
}

// draftNightIntent records one quote-verified intent as a draft assignment
// (R5.7): the deterministic id makes a rollup retry or resume replay the
// stored record instead of duplicating it, and State "draft" means only the
// owner-approval path can ever activate it.
func (g *Gateway) draftNightIntent(caller identity.Envelope, p night.IntentProposal) error {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(caller.PrincipalID + "\x00" + text))
	title := text
	if len(title) > 80 {
		title = title[:80]
	}
	now := g.now().UTC()
	var created commodel.Assignment
	err := g.automationCall(context.Background(), caller, "assignment.create", map[string]any{"assignment": commodel.Assignment{
		AssignmentID:   "asg-night-" + hex.EncodeToString(sum[:])[:16],
		Revision:       1,
		Envelope:       caller,
		OrganizationID: g.config.OrganizationID,
		Intent:         text,
		IntentHash:     hex.EncodeToString(sum[:]),
		Title:          title,
		State:          "draft",
		CreatedAt:      now,
		UpdatedAt:      now,
	}}, &created)
	return err
}

// nightRelayCall is the production model seam (R5.1): one bounded
// chat-completions POST to the space's managed model-relay. The "model"
// field is present but empty so the relay resolves the space's current
// selection; the caller bearer is HUB_MODEL_RELAY_AUTH, never the admin
// credential. An unconfigured or failing relay returns a uniform error —
// the run reports it, the marker stays unwritten and the next tick
// resumes.
func nightRelayCall(ctx context.Context, system, user string) (string, error) {
	base := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_URL"))
	if base == "" {
		return "", errNightRelayUnavailable
	}
	payload, err := json.Marshal(map[string]any{
		"model": "",
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	})
	if err != nil {
		return "", errNightRelayUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/v1/chat/completions", bytes.NewReader(payload)) // #nosec G704 -- operator-rendered managed-relay URL
	if err != nil {
		return "", errNightRelayUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if auth := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_AUTH")); auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	client := &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req) // #nosec G704 -- operator-rendered managed-relay URL
	if err != nil {
		return "", errNightRelayUnavailable
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", errNightRelayUnavailable
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &out) != nil || len(out.Choices) == 0 {
		return "", errNightRelayUnavailable
	}
	return out.Choices[0].Message.Content, nil
}

var errNightRelayUnavailable = errors.New("night model relay unavailable")
