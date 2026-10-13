package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
)

// Only this run's random ID is touched. A far-future reminder cannot fire while
// the agent operates; timed-out agent runs retain their fixture for diagnosis.
func routineAgentCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, mode, name string) (status, reason string) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "FAIL", "random fixture identity unavailable"
	}
	id := "e2e-routine-" + hex.EncodeToString(nonce[:8])
	secret := "E2E_PRIVATE_" + hex.EncodeToString(nonce[8:])
	marker := "E2E " + hex.EncodeToString(nonce[:8])
	const expression = "once:2099-01-01T00:00:00Z"
	const changed = "once:2099-02-01T00:00:00Z"
	const until = "2099-03-01T00:00:00Z"
	draft := communication.Schedule{ScheduleID: id, Timezone: "UTC", Expression: expression, JobKind: "reminder", Input: secret}
	prompt, expected := "", marker
	switch name {
	case "routine_create":
		prompt = fmt.Sprintf("Use routine_create to create exactly id %s, kind reminder, timezone UTC, expression %s, text %s.", id, expression, secret)
	case "routine_list":
		// The answer is absent from the prompt: only reading the fixture reveals it.
		prompt = fmt.Sprintf("Use routine_list. Find exactly schedule_id %s. Return only %s followed by a space and its input field. Do not change any routines.", id, marker)
		expected = marker + " " + secret
	case "routine_update":
		prompt = fmt.Sprintf("Use routine_update only on id %s, expression %s, text %s.", id, changed, marker)
	case "routine_snooze":
		prompt = fmt.Sprintf("Use routine_snooze only on id %s, expression (until) %s.", id, until)
	case "routine_pause", "routine_delete":
		prompt = fmt.Sprintf("Use %s only on id %s.", name, id)
	default:
		return "BLOCKED", "no routine agent oracle for this operation"
	}
	if name != "routine_list" {
		prompt += " After success return exactly " + marker + "; no explanation. Do not touch any other routine."
	}
	if name != "routine_create" {
		if err := routineRequest(ctx, client, config, user.ID, http.MethodPost, "/routines", draft, nil); err != nil {
			return "FAIL", "future routine fixture creation failed"
		}
	}
	run := mockPromptCase
	if mode == "telegram-server" {
		run = serverPromptCase
	}
	status, reason = run(ctx, client, config, user, "text", prompt, expected)
	if status != "PASS" {
		return status, reason + "; future routine fixture retained; no automatic retry"
	}
	var schedules []communication.Schedule
	if err := routineRequest(ctx, client, config, user.ID, http.MethodGet, "/routines", nil, &schedules); err != nil {
		return "FAIL", "independent routine read failed; fixture retained"
	}
	var actual *communication.Schedule
	for i := range schedules {
		if schedules[i].ScheduleID == id {
			actual = &schedules[i]
		}
	}
	defer func() {
		if actual != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := routineRequest(cleanup, client, config, user.ID, http.MethodDelete, "/routines/"+id, nil, nil); err != nil {
				status, reason = "FAIL", "own future routine cleanup failed"
			}
		}
	}()
	if name == "routine_delete" {
		if actual != nil {
			return "FAIL", "agent claimed deletion but routine remains"
		}
	} else {
		if actual == nil || actual.JobKind != "reminder" || actual.Timezone != "UTC" || actual.UserID != user.ID {
			return "FAIL", "routine missing or owner/kind/timezone changed"
		}
		valid := actual.Expression == expression && actual.Input == secret
		switch name {
		case "routine_update":
			valid = actual.Expression == changed && actual.Input == marker && actual.Revision > 1
		case "routine_pause":
			valid = valid && actual.Paused
		case "routine_snooze":
			valid = valid && actual.SnoozedUntil.Equal(time.Date(2099, 3, 1, 0, 0, 0, 0, time.UTC))
		case "routine_create", "routine_list":
			valid = valid && actual.Enabled && !actual.Paused
		}
		if !valid {
			return "FAIL", "independent routine state contradicts agent reply"
		}
	}
	return "PASS", reason + "; independent routine state verified; own fixture removed"
}
