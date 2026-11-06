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
)

const dreamingID = "system-dreaming"

type dreamingRoutine struct {
	ID          string `json:"id"`
	PrincipalID string `json:"principal_id"`
	Environment string `json:"environment"`
	LocalTime   string `json:"local_time"`
	Timezone    string `json:"timezone"`
	Revision    string `json:"revision"`
}

type dreamingDay struct {
	PrincipalID string `json:"principal_id"`
	RunDate     string `json:"run_date"`
	LeafDate    string `json:"leaf_date"`
	State       string `json:"state"`
	Digest      string `json:"digest,omitempty"`
}

func dreamingDates(now time.Time, plan night.Schedule) (runDate, leafDate string, due bool, err error) {
	clock, err := time.Parse("15:04", plan.LocalTime)
	if err != nil || clock.Format("15:04") != plan.LocalTime {
		return "", "", false, errors.New("automation: invalid dreaming time")
	}
	loc, err := time.LoadLocation(plan.Timezone)
	if err != nil {
		return "", "", false, errors.New("automation: invalid dreaming timezone")
	}
	local := now.In(loc)
	if local.Hour()*60+local.Minute() < clock.Hour()*60+clock.Minute() {
		return "", "", false, nil
	}
	previous := time.Date(local.Year(), local.Month(), local.Day()-1, 12, 0, 0, 0, loc)
	return local.Format("2006-01-02"), previous.Format("2006-01-02"), true, nil
}

func (h *Host) dreamingLock(principal string) *sync.Mutex {
	value, _ := h.nightLocks.LoadOrStore(principal, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (h *Host) tickDreaming(ctx context.Context, principal string, now time.Time) error {
	if h.cfg.DreamingPlan == nil {
		return nil
	}
	lock := h.dreamingLock(principal)
	lock.Lock()
	defer lock.Unlock()
	auth, err := h.policyAuth(principal)
	if err != nil {
		return err
	}
	plan, err := h.cfg.DreamingPlan(auth)
	if err != nil {
		return err
	}
	root := filepath.Join(h.ownerRoot(principal), "system")
	path := filepath.Join(root, "dreaming.json")
	info, err := os.Lstat(root)
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("automation: unsafe system routine storage")
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !plan.Enabled {
		if os.IsNotExist(err) {
			return nil
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if plan.Revision == "" {
		return errors.New("automation: dreaming revision unavailable")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	info, err = os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("automation: unsafe system routine storage")
	}
	want := dreamingRoutine{dreamingID, principal, h.cfg.Environment, plan.LocalTime, plan.Timezone, plan.Revision}
	var current dreamingRoutine
	if err := loadJSON(path, &current); err == nil {
		if current == want {
			// The published plan is already materialized.
		} else if err := writeJSONAtomic(path, want); err != nil {
			return err
		}
	} else if os.IsNotExist(err) {
		if err := writeJSONAtomic(path, want); err != nil {
			return err
		}
	} else {
		return err
	}
	runDate, leafDate, due, err := dreamingDates(now, plan)
	if err != nil || !due {
		return err
	}
	days := filepath.Join(root, "dreaming")
	if err := os.MkdirAll(days, 0o700); err != nil {
		return err
	}
	info, err = os.Lstat(days)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("automation: unsafe dreaming occurrence storage")
	}
	dayPath := filepath.Join(days, runDate+".json")
	var day dreamingDay
	if err := loadJSON(dayPath, &day); err == nil {
		if day.PrincipalID != principal || day.RunDate != runDate || day.LeafDate != leafDate || day.State != "pending" && day.State != "prepared" && day.State != "complete" {
			return errors.New("automation: invalid dreaming occurrence")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		day = dreamingDay{PrincipalID: principal, RunDate: runDate, LeafDate: leafDate, State: "pending"}
		if err := writeJSONAtomic(dayPath, day); err != nil {
			return err
		}
	}
	if day.State == "complete" || h.cfg.DreamingRun == nil {
		return nil
	}
	if day.State == "pending" {
		// A failed or revoked run leaves the occurrence pending for the next tick.
		digest, err := h.cfg.DreamingRun(ctx, auth, day.LeafDate, plan, h.langOf(principal), func(ctx context.Context, p night.IntentProposal) error {
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
			CreatedAt: now.UTC(),
		}, now)
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
