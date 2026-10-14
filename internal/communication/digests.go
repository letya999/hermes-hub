package communication

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type digestReceipt struct {
	Delivery Delivery    `json:"delivery"`
	Cursor   DigestState `json:"cursor"`
}

func (s *Spool) dispatchDigestLocked(o RoutineOccurrence, now time.Time, authorize func(Job) error) error {
	path := filepath.Join(s.root, "occurrences", occurrenceID(o)+".digest-result")
	var receipt digestReceipt
	if body, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(body, &receipt) != nil {
			return errors.New("invalid digest receipt")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		cursor := DigestState{ScheduleID: o.ScheduleID, Generations: map[string]uint64{}}
		if body, err := os.ReadFile(s.digestPath(o.ScheduleID)); err == nil {
			if json.Unmarshal(body, &cursor) != nil {
				return errors.New("invalid digest cursor")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if cursor.Generations == nil {
			cursor.Generations = map[string]uint64{}
		}
		if cursor.Errors == nil {
			cursor.Errors = map[string]string{}
		}
		unavailable := 0
		digest, _ := s.loadScheduleLocked(o.ScheduleID)
		entries, err := os.ReadDir(filepath.Join(s.root, "watchers"))
		if err != nil {
			return err
		}
		lines := []string{}
		for _, e := range entries {
			if len(lines) >= 16 {
				break
			}
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(s.root, "watchers", e.Name()))
			if err != nil {
				return err
			}
			var st WatchState
			if json.Unmarshal(body, &st) != nil {
				return errors.New("invalid watch state")
			}
			owner, err := s.loadScheduleLocked(st.ScheduleID)
			if err != nil || owner.Probe == nil || owner.Envelope.PrincipalID != o.Job.PrincipalID || owner.Envelope.ContextID != o.Job.ContextID || (len(digest.SourceIDs) > 0 && !slices.Contains(digest.SourceIDs, owner.ScheduleID)) {
				continue
			}
			job := Job{Envelope: owner.Envelope, OrganizationID: owner.OrganizationID, UserID: owner.UserID, ActorID: owner.ActorID, ScopeID: owner.ScopeID, Channel: owner.Channel, ChatID: owner.ChatID, Trigger: "cron"}
			if authorize(job) != nil {
				continue
			}
			if st.LastError != "" {
				unavailable++
				errorID := watchHash(st.LastError + st.LastSuccess.UTC().Format(time.RFC3339Nano))
				if cursor.Errors[st.ScheduleID] == errorID {
					continue
				}
				cursor.Errors[st.ScheduleID] = errorID
				lastSuccess := "нет успешных проверок"
				if !st.LastSuccess.IsZero() {
					lastSuccess = st.LastSuccess.UTC().Format(time.RFC3339)
				}
				lines = append(lines, "• "+boundedWatchText(owner.Input)+": источник недоступен; последние данные "+lastSuccess)
				continue
			}
			delete(cursor.Errors, st.ScheduleID)
			changed := st.Generation > cursor.Generations[st.ScheduleID]
			if st.Generation == 0 {
				changed = !st.ChangedAt.IsZero() && st.ChangedAt.After(cursor.LastDigestAt)
			}
			if !changed {
				continue
			}
			if len(lines) >= 16 {
				break
			} // Keep unread generations for the next digest.
			lines = append(lines, fmt.Sprintf("• %s: %s\n%s\n%s", boundedWatchText(owner.Input), boundedWatchText(st.Value), owner.Probe.URL, st.ChangedAt.UTC().Format(time.RFC3339)))
			cursor.Generations[st.ScheduleID] = st.Generation
			if st.ChangedAt.After(cursor.LastDigestAt) {
				cursor.LastDigestAt = st.ChangedAt
			}
		}
		if len(lines) == 0 {
			message := "Новых изменений нет."
			if unavailable > 0 {
				message += fmt.Sprintf(" Источников недоступно: %d.", unavailable)
			}
			lines = append(lines, message)
		}
		delivery, err := normalizeDelivery(Delivery{ID: "digest-" + o.Job.ID, OccurrenceID: o.Job.ID, Channel: o.Job.Channel, ConversationID: o.Job.ConversationID, DeliveryTargetID: o.Job.DeliveryTargetID, ChatID: o.Job.ChatID, TaskID: o.Job.TaskID, TopicID: o.Job.TopicID, SlackChannel: o.Job.SlackChannel, SlackThread: o.Job.SlackThread, Text: o.Job.Text + "\n" + strings.Join(lines, "\n"), NotBefore: quietDefer(digest, now), CreatedAt: now.UTC()})
		if err != nil {
			return err
		}
		receipt = digestReceipt{Delivery: delivery, Cursor: cursor}
		if err := atomicJSON(path, receipt); err != nil {
			return err
		}
	}
	if err := s.enqueueDeliveryLocked(receipt.Delivery); err != nil {
		return err
	}
	return atomicJSON(s.digestPath(o.ScheduleID), receipt.Cursor)
}
