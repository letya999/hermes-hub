//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/commops"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestM32ReminderClockAndDeferredDelivery(t *testing.T) {
	f, err := devcheck.NewM32Fixture(t.TempDir(), "prod", "http://127.0.0.1:1/v1", "delivery")
	m32Require(t, err)
	h := newM32Lifecycle(t, t.Context(), "", "prod", f)
	stop := h.startToolHub(t, t.Context())
	t.Cleanup(func() { stop() })
	h.dueWorkRecovery(t, t.Context(), func() { stop(); stop = h.startToolHub(t, t.Context()) })
}

type m32DeliveryRecord struct {
	Principal string            `json:"principal"`
	Sent      bool              `json:"sent"`
	Delivery  commodel.Delivery `json:"delivery"`
}

func (h *m32Lifecycle) dueWorkRecovery(t *testing.T, ctx context.Context, restartToolHub func()) {
	t.Helper()
	ids := make([]string, len(h.f.Users))
	clock := time.Now().Add(3 * time.Second)
	if h.image != "" {
		clock = time.Now().Add(15 * time.Second)
	}
	control := commops.ControlClient{URL: h.toolURL, Token: h.control}
	for i, user := range h.f.Users {
		allowed, err := (api.DecideClient{URL: h.toolURL, Token: h.control, Principal: user, Environment: h.environment}).GrantsRight(ctx, "channels", "send", "telegram_bot")
		m32Require(t, err)
		if !allowed {
			t.Fatal("deferred delivery fixture lacks the authored channels.send grant")
		}
		ids[i] = "m32-due-" + user
		kind, input := "reminder", "M32 reminder acceptance"
		if h.image != "" {
			kind, input = "agent", "M32 due agent acceptance"
		}
		routing := h.gatewayConfig.Users[i]
		auth := routing.Envelope(int64(11 + i))
		item := commodel.Schedule{ScheduleID: ids[i], Envelope: auth, OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", ChatID: int64(11 + i), Timezone: "UTC", Expression: "once:" + clock.UTC().Format(time.RFC3339), Input: input, JobKind: kind, Enabled: true}
		m32Require(t, control.Call(ctx, auth, "schedule.create", map[string]any{"schedule": item}, nil))
	}
	h.communicationOnline.Store(false)
	defer h.communicationOnline.Store(true)
	var pending []m32DeliveryRecord
	settleTimeout := time.Minute
	if h.image != "" {
		settleTimeout = 3 * time.Minute
	}
	m32Wait(t, ctx, settleTimeout, func() bool {
		pending = h.deliveryRecords(t)
		if len(pending) != len(h.f.Users) {
			return false
		}
		for _, record := range pending {
			if record.Sent {
				t.Fatal("communication outage accepted a delivery")
			}
		}
		return true
	})
	for _, record := range pending {
		expectedChat := int64(0)
		for i, user := range h.f.Users {
			if record.Principal == user {
				expectedChat = int64(11 + i)
			}
		}
		if expectedChat == 0 || record.Delivery.ChatID != expectedChat {
			t.Fatal("durable result crossed its owner's channel")
		}
		if h.image != "" {
			run, err := (task.NewHTTPClient(h.supervisor.URL, h.control, nil)).Run(ctx, record.Delivery.JobID)
			m32Require(t, err)
			if run.Status != "completed" || api.Kind(run) != api.RunKindBackground || run.Envelope.PrincipalID != record.Principal {
				t.Fatalf("due agent lifecycle status=%s kind=%s owner_match=%t", run.Status, api.Kind(run), run.Envelope.PrincipalID == record.Principal)
			}
			if record.Delivery.Text != "M32 acceptance complete" {
				t.Fatal("due agent reply did not reach its durable delivery intent")
			}
		}
	}
	restartToolHub()
	h.communicationOnline.Store(true)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- h.gateway.Run(runCtx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("deferred delivery gateway: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("deferred delivery gateway did not stop")
		}
	}()
	seen := map[int64]bool{}
	for range h.f.Users {
		select {
		case chat := <-h.channelSends:
			if seen[chat] || chat != 11 && chat != 12 {
				t.Fatal("delivery duplicated or crossed its channel audience")
			}
			seen[chat] = true
		case <-time.After(30 * time.Second):
			t.Fatal("deferred delivery did not reach the channel fixture")
		}
	}
	m32Wait(t, ctx, 15*time.Second, func() bool {
		for _, record := range h.deliveryRecords(t) {
			if !record.Sent {
				return false
			}
		}
		return true
	})
	restartToolHub()
	select {
	case <-h.channelSends:
		t.Fatal("restart replayed an already acknowledged delivery")
	case <-time.After(6 * time.Second):
	case <-ctx.Done():
		t.Fatal("deferred delivery acceptance cancelled")
	}
	for i, user := range h.f.Users {
		var history []struct {
			Run struct {
				ID string `json:"occurrence_id"`
			} `json:"run"`
		}
		m32Require(t, control.Call(ctx, h.f.Auth[user], "schedule.history", map[string]any{"id": ids[i]}, &history))
		if len(history) != 1 || history[0].Run.ID == "" {
			t.Fatal("once schedule did not retain exactly one occurrence across restarts")
		}
	}
	if h.image != "" {
		h.assignmentWatchExecution(t, ctx, restartToolHub)
	}
	h.pendingDeliveryFences(t, ctx, restartToolHub)
	t.Log("passed: ToolHub clock during communication outage, durable result, deferred owner delivery, restart without duplicate occurrence or send")
}

func (h *m32Lifecycle) pendingDeliveryFences(t *testing.T, ctx context.Context, restartToolHub func()) {
	t.Helper()
	control := commops.ControlClient{URL: h.toolURL, Token: h.control}
	h.communicationOnline.Store(false)
	defer h.communicationOnline.Store(true)
	ids := make([]string, len(h.f.Users))
	due := time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339)
	for i, user := range h.f.Users {
		ids[i] = "m32-fence-" + user
		auth := h.gatewayConfig.Users[i].Envelope(int64(11 + i))
		item := commodel.Schedule{ScheduleID: ids[i], Envelope: auth, OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", ChatID: int64(11 + i), Timezone: "UTC", Expression: "once:" + due, Input: "M32 fenced payload", JobKind: "reminder", Enabled: true}
		m32Require(t, control.Call(ctx, auth, "schedule.create", map[string]any{"schedule": item}, nil))
	}
	var pending []m32DeliveryRecord
	m32Wait(t, ctx, 20*time.Second, func() bool {
		pending = nil
		for _, record := range h.deliveryRecords(t) {
			if !record.Sent && record.Delivery.Text == "M32 fenced payload" {
				pending = append(pending, record)
			}
		}
		return len(pending) == len(h.f.Users)
	})
	owner := h.f.Auth[h.f.Users[0]]
	var schedule commodel.Schedule
	m32Require(t, control.Call(ctx, owner, "schedule.load", map[string]any{"id": ids[0]}, &schedule))
	m32Require(t, control.Call(ctx, owner, "schedule.pause", map[string]any{"id": ids[0], "expected_revision": schedule.Revision}, nil))
	governance := toolhub.NewGovernanceStore(filepath.Join(filepath.Dir(h.f.StorePath), "governance.json"))
	m32Require(t, governance.RevokeRight(api.DeterministicID("grant", h.f.Users[1], "tasks", "schedule"), "operator", "M32 pending delivery fence", time.Now()))
	restartToolHub()
	h.communicationOnline.Store(true)
	m32Wait(t, ctx, 20*time.Second, func() bool {
		for _, record := range pending {
			path := filepath.Join(h.gatewayConfig.SpoolDir, "outbox", "failed", commodel.FileID(record.Delivery.ID)+".json")
			body, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return false
			}
			m32Require(t, err)
			var rejected commodel.Delivery
			m32Require(t, json.Unmarshal(body, &rejected))
			if rejected.ID != record.Delivery.ID || rejected.OwnerPrincipalID != record.Principal {
				t.Fatal("send-time fence lost the delivery owner")
			}
		}
		return true
	})
	select {
	case <-h.channelSends:
		t.Fatal("paused or revoked pending result reached the channel")
	default:
	}
	t.Log("passed: already materialized deliveries denied after pause/right revoke and service restart")
}

func (h *m32Lifecycle) deliveryRecords(t *testing.T) []m32DeliveryRecord {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(h.f.Root, "automation", "outbox", "*", "*.json"))
	m32Require(t, err)
	var records []m32DeliveryRecord
	for _, file := range files {
		body, err := os.ReadFile(file)
		m32Require(t, err)
		var record m32DeliveryRecord
		m32Require(t, json.Unmarshal(body, &record))
		records = append(records, record)
	}
	return records
}

func m32Wait(t *testing.T, ctx context.Context, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("M32 durable state did not settle")
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("M32 state wait cancelled")
		}
	}
}

func m32ChannelFixture(t *testing.T) (string, <-chan int64) {
	t.Helper()
	sends := make(chan int64, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/botsynthetic-m32-bot/getUpdates":
			select {
			case <-time.After(100 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
		case "/botsynthetic-m32-bot/setMyCommands":
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		case "/botsynthetic-m32-bot/sendMessage":
			var message struct {
				ChatID int64 `json:"chat_id"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&message) != nil {
				w.WriteHeader(400)
				return
			}
			select {
			case sends <- message.ChatID:
			default:
				t.Error("unbounded channel sends")
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
		default:
			t.Errorf("unexpected fixture channel operation: %s", r.URL.Path)
			w.WriteHeader(403)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, sends
}
