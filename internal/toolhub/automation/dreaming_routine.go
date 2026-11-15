package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/memory/night"
	"github.com/letya999/hermes-hub/internal/routines"
)

type dreamingDay struct {
	PrincipalID string `json:"principal_id"`
	RunDate     string `json:"run_date"`
	LeafDate    string `json:"leaf_date"`
	State       string `json:"state"`
	Digest      string `json:"digest,omitempty"`
}

func (h *Host) dreamingLock(principal string) *sync.Mutex {
	value, _ := h.nightLocks.LoadOrStore(principal, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// syncDreaming only projects the published clock. TickSchedules calculates
// due time and persists the occurrence; it owns no separate night timer.
func (h *Host) syncDreaming(e *routines.Engine, principal string, now time.Time) error {
	if h.cfg.DreamingPlan == nil {
		return nil
	}
	auth, err := h.policyAuth(principal)
	if err != nil {
		return err
	}
	plan, err := h.cfg.DreamingPlan(auth)
	if err != nil {
		return err
	}
	rearm := false
	if plan.Enabled {
		decision, err := h.cfg.Store.Decide(auth, "memory", "dreaming", "", h.now())
		if err != nil {
			return err
		}
		rearm = decision.Allow
	}
	if err := e.SyncDreamingSchedule(auth, plan.LocalTime, plan.Timezone, plan.Revision, plan.Enabled, rearm, now); err != nil {
		return err
	}
	// The old marker is inert after the governed schedule is durable.
	old := filepath.Join(h.ownerRoot(principal), "system", "dreaming.json")
	if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (h *Host) authorizeDreaming(job commodel.Job) error {
	if job.Channel != "system" || job.Trigger != "cron" || job.PrincipalID == "" ||
		job.UserID != job.PrincipalID || job.ActorID != job.PrincipalID || job.ScopeID != "user:"+job.PrincipalID || h.cfg.DreamingPlan == nil {
		return errUnauthorized
	}
	auth, err := h.policyAuth(job.PrincipalID)
	if err != nil || !auth.Equal(job.Envelope) {
		return errUnauthorized
	}
	plan, err := h.cfg.DreamingPlan(auth)
	if err != nil || !plan.Enabled || plan.Revision != job.Text {
		return errUnauthorized
	}
	decision, err := h.cfg.Store.Decide(auth, "memory", "dreaming", "", h.now())
	if err != nil || !decision.Allow {
		return errUnauthorized
	}
	return nil
}

// runDreamingOccurrence is the operation of the protected routine. Its day
// checkpoint survives a crash after memory commit or before digest enqueue.
func (h *Host) runDreamingOccurrence(ctx context.Context, occurrence commodel.RoutineOccurrence) error {
	principal := occurrence.Job.PrincipalID
	lock := h.dreamingLock(principal)
	lock.Lock()
	defer lock.Unlock()
	if err := h.authorizeDreaming(occurrence.Job); err != nil {
		return err
	}
	if h.cfg.DreamingRun == nil {
		return errors.New("automation: dreaming operation unavailable")
	}
	auth := occurrence.Job.Envelope
	plan, err := h.cfg.DreamingPlan(auth)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(plan.Timezone)
	if err != nil {
		return err
	}
	local := occurrence.DueAt.In(loc)
	runDate := local.Format("2006-01-02")
	previous := time.Date(local.Year(), local.Month(), local.Day()-1, 12, 0, 0, 0, loc)
	leafDate := previous.Format("2006-01-02")
	days := filepath.Join(h.ownerRoot(principal), "system", "dreaming")
	if err := os.MkdirAll(days, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(days)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("automation: unsafe dreaming occurrence storage")
	}
	dayPath := filepath.Join(days, runDate+".json")
	var day dreamingDay
	if err := loadJSON(dayPath, &day); err == nil {
		if day.PrincipalID != principal || day.RunDate != runDate || day.LeafDate != leafDate ||
			day.State != "pending" && day.State != "prepared" && day.State != "complete" {
			return errors.New("automation: invalid dreaming day checkpoint")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		day = dreamingDay{PrincipalID: principal, RunDate: runDate, LeafDate: leafDate, State: "pending"}
		if err := writeJSONAtomic(dayPath, day); err != nil {
			return err
		}
	}
	if day.State == "complete" {
		return nil
	}
	if day.State == "pending" {
		digest, err := h.cfg.DreamingRun(ctx, auth, leafDate, plan, h.langOf(principal), func(ctx context.Context, p night.IntentProposal) error {
			return h.draftNightIntent(auth, p)
		})
		if err != nil {
			return err
		}
		day.State, day.Digest = "prepared", digest
		if err := writeJSONAtomic(dayPath, day); err != nil {
			return err
		}
	}
	if err := h.authorizeDreaming(occurrence.Job); err != nil {
		return err
	}
	if day.Digest != "" {
		if h.cfg.RoutingUser == nil {
			return errors.New("automation: dreaming delivery route unavailable")
		}
		user, err := h.cfg.RoutingUser(principal)
		if err != nil || !user.Enabled || len(user.TelegramIDs) == 0 {
			return errors.New("automation: dreaming delivery route unavailable")
		}
		chatID := user.TelegramIDs[0]
		key := "night-digest-" + principal + "-" + runDate
		target := "telegram-" + strconv.FormatInt(chatID, 10)
		_, err = h.outbox.enqueue(principal, commodel.Delivery{
			ID: key, IdempotencyKey: key, Channel: "telegram_bot", ChatID: chatID,
			ConversationID: target, DeliveryTargetID: target,
			Text:      locale.T(h.langOf(principal), "memory.night-digest", day.Digest),
			CreatedAt: h.now().UTC(),
		}, h.now())
		if err != nil {
			return err
		}
	}
	day.State = "complete"
	return writeJSONAtomic(dayPath, day)
}

func (h *Host) draftNightIntent(auth identity.Envelope, p night.IntentProposal) error {
	text := p.Text
	if text == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(auth.PrincipalID + "\x00" + text))
	title := text
	if len(title) > 80 {
		title = title[:80]
	}
	now := h.now().UTC()
	e, err := h.engineFor(auth.PrincipalID)
	if err != nil {
		return err
	}
	_, _, err = e.CreateAssignment(commodel.Assignment{
		AssignmentID: "asg-night-" + hex.EncodeToString(sum[:])[:16], Revision: 1,
		Envelope: auth, OrganizationID: auth.Organization, Intent: text,
		IntentHash: hex.EncodeToString(sum[:]), Title: title, State: "draft",
		CreatedAt: now, UpdatedAt: now,
	}, auth)
	return err
}
