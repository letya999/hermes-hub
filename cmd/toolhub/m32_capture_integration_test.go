//go:build integration

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestM32CaptureLostAcknowledgementNative(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	f, err := devcheck.NewM32Fixture(t.TempDir(), "prod", "http://127.0.0.1:1/v1", "capture")
	m32Require(t, err)
	h := newM32Lifecycle(t, ctx, "", "prod", f)
	var runs [2]api.RunContext
	turnHash := sha256.Sum256([]byte("M32 parallel memory acceptance"))
	replyHash := sha256.Sum256([]byte("M32 acceptance complete"))
	for i, user := range f.Users {
		runs[i] = api.RunContext{JobID: "job-" + user, RunID: "run-" + user + "-1", SessionID: "capture-session",
			RuntimeGeneration: "capture-gen", CurrentGeneration: "capture-gen", Environment: "prod", Status: "completed",
			ScopeID: "user:" + user, Trigger: "message", Channel: "telegram_bot", UserID: user, Envelope: f.Auth[user],
			TextSHA256: fmt.Sprintf("%x", turnHash), ResponseSHA256: fmt.Sprintf("%x", replyHash)}
	}
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+h.control {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		for _, run := range runs {
			if r.URL.Path == "/v1/jobs/"+run.JobID {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(run)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(authority.Close)
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", authority.URL)
	stopHub := h.startToolHub(t, ctx)
	t.Cleanup(func() { stopHub() })
	h.captureLostAcknowledgement(t, ctx, runs, [2]string{"M32 parallel memory acceptance", "M32 parallel memory acceptance"}, func() {
		stopHub()
		stopHub = h.startToolHub(t, ctx)
	})
}

func (h *m32Lifecycle) captureLostAcknowledgement(t *testing.T, ctx context.Context, runs [2]api.RunContext, turnText [2]string, restartToolHub func()) {
	t.Helper()
	spoolRoot := filepath.Join(h.f.Root, "capture-producer")
	spool, err := communication.NewSpool(spoolRoot)
	m32Require(t, err)
	for i, user := range h.f.Users {
		for _, kind := range []string{"turn", "reply"} {
			event := api.CaptureEvent{Producer: "communication", EventID: kind + "-" + runs[i].JobID, Kind: kind, JobID: runs[i].JobID, UserID: user, PrincipalID: user, ScopeID: "user:" + user, Trigger: "message", Channel: "telegram_bot", Body: turnText[i], ObservedAt: time.Now().UTC()}
			if kind == "reply" {
				event.RunID, event.Body = runs[i].RunID, "M32 acceptance complete"
			}
			m32Require(t, spool.EnqueueCapture(event))
		}
	}
	// The trusted producer queue contains facts whose authority is checked
	// against actual supervisor records, never a hand-written run authority.
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			select {
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
		case strings.HasSuffix(r.URL.Path, "/setMyCommands"):
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			t.Error("capture-only producer attempted an external channel operation")
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer channel.Close()
	committed := make(chan struct{}, 4)
	var mu sync.Mutex
	seen := map[string]bool{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/memory-capture" {
			w.WriteHeader(404)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		var event api.CaptureEvent
		if err != nil || json.Unmarshal(body, &event) != nil {
			w.WriteHeader(400)
			return
		}
		request, err := http.NewRequestWithContext(r.Context(), "POST", h.toolURL+r.URL.Path, strings.NewReader(string(body)))
		if err != nil {
			w.WriteHeader(502)
			return
		}
		request.Header = r.Header.Clone()
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		if response.StatusCode != 200 {
			w.WriteHeader(response.StatusCode)
			return
		}
		mu.Lock()
		key := event.UserID + "/" + event.EventID
		if !seen[key] {
			seen[key] = true
			committed <- struct{}{}
		}
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable) // commit happened; the acknowledgement is lost
	}))
	defer proxy.Close()
	config := communication.Config{Supervised: true, Environment: h.environment, OrganizationID: "personal", SpoolDir: spoolRoot, TelegramToken: "synthetic-capture", APIBaseURL: channel.URL, RuntimeURL: h.supervisor.URL, RuntimeAuth: h.control, ToolHubURL: proxy.URL, PollTimeout: time.Second}
	for i, user := range h.f.Users {
		config.Users = append(config.Users, commodel.User{ID: user, RuntimeID: user, PolicyVersion: h.f.Auth[user].PolicyVersion, Enabled: true, TelegramIDs: []int64{int64(11 + i)}, Features: []string{"telegram", "workspace"}})
	}
	start := func() func() {
		gateway, err := communication.New(config)
		m32Require(t, err)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- gateway.Run(runCtx) }()
		var once sync.Once
		return func() {
			once.Do(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil && !errors.Is(err, context.Canceled) {
						t.Errorf("capture producer stopped: %v", err)
					}
				case <-time.After(15 * time.Second):
					t.Error("capture producer did not stop")
				}
			})
		}
	}
	stop := start()
	defer func() { stop() }()
	for range 4 {
		select {
		case <-committed:
		case <-time.After(20 * time.Second):
			t.Fatal("real capture did not commit before the injected lost acknowledgement")
		}
	}
	stop()
	pending, err := filepath.Glob(filepath.Join(spoolRoot, "capture", "*.json"))
	m32Require(t, err)
	if len(pending) != 4 {
		t.Fatal("lost acknowledgements removed durable producer events")
	}
	restartToolHub()
	config.ToolHubURL = h.toolURL
	stop = start()
	deadline := time.Now().Add(20 * time.Second)
	for {
		pending, err = filepath.Glob(filepath.Join(spoolRoot, "capture", "*.json"))
		m32Require(t, err)
		if len(pending) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capture outbox did not drain after both services restarted")
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("capture recovery cancelled")
		}
	}
	stop()
	for i, user := range h.f.Users {
		root := filepath.Join(h.f.Root, "vaults", user)
		leaves, err := filepath.Glob(filepath.Join(root, "memory", "daily", "*.md"))
		m32Require(t, err)
		blocks := 0
		for _, leaf := range leaves {
			body, err := os.ReadFile(leaf)
			m32Require(t, err)
			blocks += strings.Count(string(body), "mem:id=")
		}
		prov, err := memory.OpenProvDB(filepath.Join(root, "runtime", "memory"))
		m32Require(t, err)
		entries, err := prov.Entries()
		m32Require(t, err)
		if blocks != 2 || len(entries) != 2 {
			t.Fatal("capture retry duplicated or lost a turn/reply leaf or provenance row")
		}
		for _, entry := range entries {
			if entry.Audience != memory.AudiencePrivate || entry.SourceRef != "job:"+runs[i].JobID {
				t.Fatal("private capture crossed its audience or owner job")
			}
		}
	}
	t.Log("passed: actual producer pump and ToolHub restart over lost acknowledgements, one turn/reply and provenance row per user")
}
