package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// The channel grammar mirrors upstream /compress: `here [N]`, `focus <text>`,
// `prune`, `preview` — anything else answers usage with zero side effects.
func TestCompactArgsParse(t *testing.T) {
	cases := []struct {
		text string
		want hubruntime.CompactRequest
		ok   bool
	}{
		{"/compact", hubruntime.CompactRequest{}, true},
		{"/compress", hubruntime.CompactRequest{}, true},
		{"/compact here", hubruntime.CompactRequest{Here: 2}, true},
		{"/compact here 3", hubruntime.CompactRequest{Here: 3}, true},
		{"/compress here 5", hubruntime.CompactRequest{Here: 5}, true},
		{"/compact focus the deploy steps", hubruntime.CompactRequest{Focus: "the deploy steps"}, true},
		{"/compact prune", hubruntime.CompactRequest{Prune: true}, true},
		{"/compact preview", hubruntime.CompactRequest{Preview: true}, true},
		{"/compact dry-run", hubruntime.CompactRequest{Preview: true}, true},
		{"/compact queue", hubruntime.CompactRequest{BusyPolicy: "queue"}, true},
		{"/compact here 2 focus deploy", hubruntime.CompactRequest{Here: 2, Focus: "deploy"}, true},
		{"/compact native", hubruntime.CompactRequest{Engine: "native"}, true},
		{"/compact engine native", hubruntime.CompactRequest{Engine: "native"}, true},
		{"/compact hub preview", hubruntime.CompactRequest{Engine: "hub", Preview: true}, true},
		{"/compact here x", hubruntime.CompactRequest{}, false},
		{"/compact here 0", hubruntime.CompactRequest{}, false},
		{"/compact here 101", hubruntime.CompactRequest{}, false},
		{"/compact bogus", hubruntime.CompactRequest{}, false},
		{"/compact prune prune", hubruntime.CompactRequest{}, false},
	}
	for _, tc := range cases {
		args, ok := parseCompactArgs(tc.text)
		if ok != tc.ok {
			t.Fatalf("%q: ok=%v want %v", tc.text, ok, tc.ok)
		}
		if !ok {
			continue
		}
		got := hubruntime.CompactRequest{
			Here: args.Here, Focus: args.Focus, Prune: args.Prune,
			Preview: args.Preview, Engine: args.Engine, BusyPolicy: args.BusyPolicy,
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: got %+v want %+v", tc.text, got, tc.want)
		}
	}
}

// /compact rides the verified envelope and the task's stable conversation to
// POST /v1/compact — the slash text never becomes a model run.
func TestCompactCommandRoutesToRuntimeNotRuns(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	var mu sync.Mutex
	var paths []string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/v1/compact" {
			t.Errorf("compact reached %s — slash commands must never hit run endpoints", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"applied": true, "method": "summary",
			"tokens_before_est": 1000, "tokens_after_est": 200,
			"messages_before": 20, "messages_after": 5,
		})
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}

	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/compact here 2", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for len(fake.sent) < 2 && time.Now().Before(deadline) {
		g.deliverOne(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	replies := strings.Join(fake.sent, "|")
	if len(fake.sent) != 2 || !strings.Contains(replies, "Сжимаю") ||
		!strings.Contains(replies, "Контекст сжат (summary): ~1000 → ~200") {
		t.Fatalf("replies: %v", fake.sent)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/v1/compact" {
		t.Fatalf("paths: %v", paths)
	}
	if body["here"] != float64(2) || body["conversation_id"] != task.ConversationID ||
		body["trigger"] != "compact" || body["user_id"] != "alice" {
		t.Fatalf("request body: %v", body)
	}
}

// Malformed args answer usage and never reach the runtime.
func TestCompactCommandBadArgs(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	if _, err := g.spool.ResolveTask(g.user("alice"), 11, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/compact here 0", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "Использование: /compact") {
		t.Fatalf("replies: %v", fake.sent)
	}
	if called {
		t.Fatal("runtime called for malformed args")
	}
}

// /compress is the upstream alias — same grammar, same endpoint.
func TestCompressAliasRoutesPreview(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/compact" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"would_method": "prune_only", "tokens_before_est": 900, "messages_before": 12,
		})
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	if _, err := g.spool.ResolveTask(g.user("alice"), 11, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/compress preview", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for len(fake.sent) < 2 && time.Now().Before(deadline) {
		g.deliverOne(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	if len(fake.sent) != 2 || !strings.Contains(strings.Join(fake.sent, "|"), "Предпросмотр: сработал бы prune_only") {
		t.Fatalf("replies: %v", fake.sent)
	}
	if body["preview"] != true {
		t.Fatalf("preview flag lost: %v", body)
	}
}

// /help advertises the compact command alongside the other session controls.
func TestHelpAdvertisesCompact(t *testing.T) {
	if !strings.Contains(commandHelp(locale.LangRU), "/compact — ") {
		t.Fatal("/compact missing from command help")
	}
}
