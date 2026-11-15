//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exercises real MCP/PDP/writer, channel gateway and durable callbacks. The
// lifecycle authority and Telegram HTTP transport remain fixture boundaries.
func TestM32MemoryChannelConfirmationRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	f, err := devcheck.NewM32Fixture(t.TempDir(), "prod", "http://127.0.0.1:1/v1", "channel")
	m32Require(t, err)
	h := newM32Lifecycle(t, ctx, "", "prod", f)
	runs := map[string]api.RunContext{}
	var active [2]api.RunContext
	for i, user := range f.Users {
		runs["job-"+user] = api.RunContext{JobID: "job-" + user, RunID: "run-" + user + "-1", SessionID: "channel-session", RuntimeGeneration: "channel-gen", CurrentGeneration: "channel-gen", Environment: "prod", Status: "running", ScopeID: "user:" + user, Trigger: "message", Channel: "telegram_bot", UserID: user, Envelope: f.Auth[user]}
		active[i] = runs["job-"+user]
	}
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record, ok := runs[strings.TrimPrefix(r.URL.Path, "/v1/jobs/")]
		if r.Method != http.MethodGet || !ok || r.Header.Get("Authorization") != "Bearer "+h.control {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(record)
	}))
	t.Cleanup(authority.Close)
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", authority.URL)
	stopHub := h.startToolHub(t, ctx)
	t.Cleanup(func() { stopHub() })
	h.memoryChannelConfirmation(t, ctx, active, func() {
		stopHub()
		stopHub = h.startToolHub(t, ctx)
	})
}

func (h *m32Lifecycle) memoryChannelConfirmation(t *testing.T, ctx context.Context, runs [2]api.RunContext, restartHub func()) {
	t.Helper()
	f := h.f
	service, vaults := h.control, filepath.Join(f.Root, "vaults")
	docs, err := catalog.Generate()
	m32Require(t, err)
	type button struct {
		chat int64
		data string
	}
	buttons, answered := make(chan button, 8), make(chan string, 8)
	var updateMu sync.Mutex
	var updates []communication.Update
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/botsynthetic-memory-channel/getUpdates":
			var poll struct {
				Offset int `json:"offset"`
			}
			if json.NewDecoder(r.Body).Decode(&poll) != nil {
				w.WriteHeader(400)
				return
			}
			select {
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			// Telegram retains updates until an offset acknowledges them.
			updateMu.Lock()
			batch := []communication.Update{}
			for _, update := range updates {
				if update.UpdateID >= poll.Offset {
					batch = append(batch, update)
				}
			}
			updateMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": batch})
		case "/botsynthetic-memory-channel/sendMessage":
			var message struct {
				ChatID int64 `json:"chat_id"`
				Reply  struct {
					Rows [][]struct {
						Data string `json:"callback_data"`
					} `json:"inline_keyboard"`
				} `json:"reply_markup"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&message) != nil {
				w.WriteHeader(400)
				return
			}
			for _, row := range message.Reply.Rows {
				for _, item := range row {
					if strings.HasPrefix(item.Data, "h:") {
						buttons <- button{message.ChatID, item.Data}
					}
				}
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
		case "/botsynthetic-memory-channel/answerCallbackQuery":
			var answer struct {
				ID string `json:"callback_query_id"`
			}
			if json.NewDecoder(r.Body).Decode(&answer) != nil {
				w.WriteHeader(400)
				return
			}
			answered <- answer.ID
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		case "/botsynthetic-memory-channel/setMyCommands":
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			t.Errorf("unexpected memory channel operation: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(telegram.Close)
	cfg := h.gatewayConfig
	cfg.TelegramToken, cfg.APIBaseURL, cfg.Workers = "synthetic-memory-channel", telegram.URL, 1
	var current atomic.Pointer[communication.Gateway]
	var stopPump func()
	start := func() {
		t.Helper()
		gateway, err := communication.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		current.Store(gateway)
		pumpCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- gateway.Run(pumpCtx) }()
		var once sync.Once
		stopPump = func() {
			once.Do(func() {
				stop()
				select {
				case err := <-done:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("memory channel pump did not stop")
				}
			})
		}
	}
	start()
	t.Cleanup(func() { stopPump() })
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { current.Load().Handler().ServeHTTP(w, r) }))
	t.Cleanup(channel.Close)
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", channel.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", service)
	var updateID int
	tap := func(b button, sender int64) {
		t.Helper()
		updateID++
		id := fmt.Sprintf("memory-tap-%d", updateID)
		updateMu.Lock()
		updates = append(updates, communication.Update{UpdateID: updateID, CallbackQuery: &communication.CallbackQuery{ID: id, From: &communication.TGUser{ID: sender}, Message: &communication.Message{MessageID: 1, Chat: communication.TGChat{ID: b.chat, Type: "private"}}, Data: b.data}})
		updateMu.Unlock()
		for {
			select {
			case got := <-answered:
				if got == id {
					return
				}
			case <-ctx.Done():
				t.Fatalf("memory owner callback %s did not finish", id)
			}
		}
	}
	readCard := func(user string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join(vaults, user, "memory", "cards", "notes", "channel.md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	waitButton := func(chat int64) button {
		t.Helper()
		select {
		case b := <-buttons:
			if b.chat != chat {
				t.Fatal("memory confirmation reached a foreign owner")
			}
			return b
		case <-ctx.Done():
			t.Fatal("memory proposal did not reach the channel button")
			return button{}
		}
	}
	for i, user := range f.Users {
		session := h.session(t, ctx, user, runs[i])
		t.Cleanup(func() { _ = session.Close() })
		call := func(args map[string]any) map[string]any {
			t.Helper()
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_card", Arguments: args})
			if err != nil || result == nil || result.IsError {
				t.Fatalf("memory channel MCP call failed: %v", err)
			}
			out, ok := result.StructuredContent.(map[string]any)
			if !ok {
				t.Fatal("memory channel result lost its structured contract")
			}
			return out
		}
		if call(map[string]any{"op": "fact", "card": "channel", "text": "original-" + user})["status"] != "ok" {
			t.Fatal("owner card seed failed")
		}
		before := readCard(user)
		out := call(map[string]any{"op": "propose", "inner": "truth", "card": "channel", "text": "confirmed-" + user})
		if out["status"] != "proposal" || readCard(user) != before {
			t.Fatal("proposal changed memory before owner confirmation")
		}
		// A self-apply is refused whether the admitted schema rejects the op
		// outright or the governed writer answers status=invalid — either way
		// the proposal stays unapplied until the owner taps the confirmation.
		applied, applyErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "memory_card", Arguments: map[string]any{"op": "apply", "id": out["id"]}})
		m32Require(t, applyErr)
		if applied == nil {
			t.Fatal("self-apply returned no result")
		}
		if !applied.IsError {
			structured, _ := applied.StructuredContent.(map[string]any)
			if structured["status"] != "invalid" {
				t.Fatal("model confirmed its own proposal")
			}
		}
		b := waitButton(int64(11 + i))
		tap(b, int64(12-i))
		if readCard(user) != before {
			t.Fatal("foreign sender confirmed memory")
		}
		// The opaque callback must survive gateway reconstruction over its spool.
		stopPump()
		start()
		tap(b, int64(11+i))
		after := readCard(user)
		if after == before || strings.Count(after, "confirmed-"+user) != 1 || strings.Contains(after, "confirmed-"+f.Users[1-i]) {
			t.Fatal("verified callback did not apply the exact owner diff")
		}
		head, err := exec.CommandContext(ctx, "git", "-C", filepath.Join(vaults, user), "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		tap(b, int64(11+i))
		replayed, err := exec.CommandContext(ctx, "git", "-C", filepath.Join(vaults, user), "rev-parse", "HEAD").Output()
		if err != nil || string(replayed) != string(head) || readCard(user) != after {
			t.Fatal("confirmation replay changed the vault")
		}
		if call(map[string]any{"op": "propose", "inner": "truth", "card": "channel", "text": "stale-" + user})["status"] != "proposal" {
			t.Fatal("stale-base proposal was not prepared")
		}
		stale := waitButton(int64(11 + i))
		if call(map[string]any{"op": "fact", "card": "channel", "text": "concurrent-" + user})["status"] != "ok" {
			t.Fatal("concurrent owner edit failed")
		}
		changed := readCard(user)
		tap(stale, int64(11+i))
		if readCard(user) != changed || strings.Contains(changed, "stale-"+user) {
			t.Fatal("stale confirmation overwrote a newer base")
		}
		if call(map[string]any{"op": "propose", "inner": "truth", "card": "channel", "text": "revoked-" + user})["status"] != "proposal" {
			t.Fatal("revocation proposal was not prepared")
		}
		revoked := waitButton(int64(11 + i))
		gov := toolhub.NewGovernanceStore(filepath.Join(filepath.Dir(f.StorePath), "governance.json"))
		ledger, err := gov.Load()
		m32Require(t, err)
		found := false
		for _, grant := range ledger.Rights {
			if grant.PrincipalID == user && grant.Capability == "memory" && grant.Operation == "remember" && grant.Status == api.GrantActive {
				m32Require(t, gov.RevokeRight(grant.GrantID, "operator", "M32 channel confirmation revoke", time.Now()))
				found = true
			}
		}
		if !found {
			t.Fatal("channel revocation scenario had no issued write right")
		}
		tap(revoked, int64(11+i))
		if readCard(user) != changed || strings.Contains(changed, "revoked-"+user) {
			t.Fatal("confirmation applied after the issued write right was revoked")
		}
		grant := api.NewRightGrant(api.DeterministicID("grant-channel", user, "memory", "remember"), user, "memory", "remember", "", "M32 channel recovery", "operator", time.Now().Add(time.Hour))
		m32Require(t, api.Confirm(&grant, "operator", time.Now()))
		m32Require(t, gov.GrantRight(grant, docs.Rights, time.Now()))

		out = call(map[string]any{"op": "propose", "inner": "truth", "card": "channel", "text": "recovered-" + user})
		if out["status"] != "proposal" {
			t.Fatal("recovery proposal was not prepared")
		}
		recovery := waitButton(int64(11 + i))
		root := filepath.Join(vaults, user)
		head, err = exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
		m32Require(t, err)
		// A real Git lock interrupts after content lands, before commit/receipt.
		lock := filepath.Join(root, ".git", "index.lock")
		m32Require(t, os.WriteFile(lock, nil, 0o600))
		t.Cleanup(func() { _ = os.Remove(lock) })
		tap(recovery, int64(11+i))
		interrupted := readCard(user)
		if strings.Count(interrupted, "recovered-"+user) != 1 {
			t.Fatal("recovery fault did not reach the content-before-commit boundary")
		}
		readState := func() (bool, string) {
			t.Helper()
			body, err := os.ReadFile(filepath.Join(root, "runtime", "memory", "proposals", out["id"].(string)+".json"))
			m32Require(t, err)
			var p struct {
				Used  bool   `json:"used"`
				State string `json:"state"`
			}
			m32Require(t, json.Unmarshal(body, &p))
			return p.Used, p.State
		}
		used, state := readState()
		replayed, err = exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
		if err != nil || !used || state != "applying" || string(replayed) != string(head) {
			t.Fatal("interrupted confirmation lost its durable applying state or committed early")
		}
		m32Require(t, os.Remove(lock))
		m32Require(t, session.Close())
		restartHub()
		used, state = readState()
		recoveredHead, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
		if err != nil || !used || state != "complete" || readCard(user) != interrupted || string(recoveredHead) == string(head) {
			t.Fatal("ToolHub startup did not finish the exact consumed edit")
		}
		stopPump()
		start()
		tap(recovery, int64(11+i))
		replayed, err = exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
		if err != nil || string(replayed) != string(recoveredHead) || readCard(user) != interrupted {
			t.Fatal("recovered channel callback applied the edit twice")
		}
	}
}
