package supervisor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestM10ScriptIsolationHTTPContract(t *testing.T) {
	var commands [][]string
	output, runErr := []byte(`["hello"]`), error(nil)
	m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if args[0] == "run" {
			return output, runErr
		}
		return nil, nil
	}, nil)
	request := hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron", Text: `emit("hello")`}, DueAt: time.Now()}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	call := func(body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/script", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		return w
	}
	if got := call(string(body), "wrong"); got.Code != 401 || len(commands) != 0 {
		t.Fatalf("unauthorized executed: %d %v", got.Code, commands)
	}
	if got := call(string(body), "secret"); got.Code != 200 || got.Body.String() != "[\"hello\"]\n" {
		t.Fatalf("script=%d %s", got.Code, got.Body.String())
	}
	if len(commands) != 2 || commands[0][0] != "run" || commands[1][0] != "rm" {
		t.Fatalf("cleanup missing: %v", commands)
	}
	args := commands[0]
	options := map[string]string{}
	for i := 1; i+1 < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			options[args[i]] = args[i+1]
		}
	}
	for key, want := range map[string]string{"--network": "none", "--user": "10001:10001", "--cap-drop": "ALL", "--security-opt": "no-new-privileges:true", "--memory": "128m", "--memory-swap": "128m", "--pids-limit": "16", "--log-driver": "none", "--entrypoint": "communication-hub"} {
		if options[key] != want {
			t.Errorf("unbounded option %s=%q", key, options[key])
		}
	}
	for _, forbidden := range []string{"--mount", "--volume", "-v", "--env", "-e", "--privileged"} {
		for _, arg := range args {
			if arg == forbidden {
				t.Errorf("script has access through %s", arg)
			}
		}
	}
	if !reflect.DeepEqual(commands[1], []string{"rm", "-f", options["--name"]}) {
		t.Fatalf("cleanup targeted another container: %v", commands[1])
	}
	source, err := base64.StdEncoding.DecodeString(args[len(args)-4])
	if err != nil || string(source) != request.Text {
		t.Fatalf("source transport=%q %v", source, err)
	}
	for _, bad := range []string{`{}`, string(body) + `{}`, strings.Replace(string(body), `"trigger":"cron"`, `"trigger":"message"`, 1), strings.Replace(string(body), `"actor_id":"alice"`, `"actor_id":""`, 1)} {
		commands = nil
		if got := call(bad, "secret"); got.Code/100 == 2 || len(commands) != 0 {
			t.Fatalf("bad script accepted: %d %v", got.Code, commands)
		}
	}
	for _, badOut := range []string{`invalid`, `["a","b","c","d","e"]`, `["` + strings.Repeat("x", 4097) + `"]`, strings.Repeat("x", 128*1024+1)} {
		output = []byte(badOut)
		commands = nil
		if got := call(string(body), "secret"); got.Code != 502 || len(commands) != 2 {
			t.Fatalf("unbounded output or missing cleanup: %d %v", got.Code, len(commands))
		}
	}
	output = nil
	runErr = errors.New("killed")
	if got := call(string(body), "secret"); got.Code != 502 {
		t.Fatalf("failure=%d", got.Code)
	}
}

func TestScriptProbeInputAndWatchMount(t *testing.T) {
	var commands [][]string
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if args[0] == "run" {
			return []byte(`["value"]`), nil
		}
		return nil, nil
	}, nil)
	request := hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron", Text: `emit(input())`}, DueAt: time.Now(), Input: "fetched-body", MountSpace: true}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/script", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("script=%d %s", w.Code, w.Body.String())
	}
	args := commands[0]
	decoded, err := base64.StdEncoding.DecodeString(args[len(args)-2])
	if err != nil || string(decoded) != "fetched-body" {
		t.Fatalf("input not transported: %q %v", decoded, err)
	}
	var mount string
	for i, arg := range args {
		if arg == "--mount" && i+1 < len(args) {
			mount = args[i+1]
		}
	}
	wantSrc := filepath.Join(root, "watch")
	if !strings.Contains(mount, "dst=/watch") || !strings.Contains(mount, "readonly") || !strings.Contains(mount, "src="+wantSrc) {
		t.Fatalf("watch mount missing or wrong: %q", mount)
	}
	for _, arg := range args {
		if strings.Contains(arg, "workspace") {
			t.Fatalf("mount leaks workspace: %q", arg)
		}
	}
}

func TestScriptProbeInputBounds(t *testing.T) {
	m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			return []byte(`["ok"]`), nil
		}
		return nil, nil
	}, nil)
	call := func(input string) *httptest.ResponseRecorder {
		request := hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron", Text: `emit(input())`}, DueAt: time.Now(), Input: input}
		body, _ := json.Marshal(request)
		r := httptest.NewRequest("POST", "/v1/script", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		return w
	}
	if got := call(strings.Repeat("x", 63*1024)); got.Code != 200 {
		t.Fatalf("bounded input rejected: %d", got.Code)
	}
	if got := call(strings.Repeat("x", 64*1024+1)); got.Code != 400 {
		t.Fatalf("oversized input accepted: %d", got.Code)
	}
}

func TestScriptProbeNeedsTransportAndBounds(t *testing.T) {
	var commands [][]string
	m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if args[0] == "run" {
			return []byte(`["ok"]`), nil
		}
		return nil, nil
	}, nil)
	call := func(needs []string) *httptest.ResponseRecorder {
		request := hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron", Text: `emit("ok")`}, DueAt: time.Now(), Needs: needs}
		body, _ := json.Marshal(request)
		r := httptest.NewRequest("POST", "/v1/script", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		return w
	}
	if got := call([]string{"alpha", "beta"}); got.Code != 200 {
		t.Fatalf("needs request rejected: %d %s", got.Code, got.Body.String())
	}
	args := commands[0]
	tail := args[len(args)-2:]
	got := []string{}
	for _, encoded := range tail {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("need not base64: %v", err)
		}
		got = append(got, string(raw))
	}
	if !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("needs argv=%v", got)
	}
	if got := call([]string{"a", "b", "c", "d", "e"}); got.Code != 400 {
		t.Fatalf("five needs accepted: %d", got.Code)
	}
	large := strings.Repeat("x", 32*1024)
	if got := call([]string{large, large, large, large}); got.Code != 200 {
		t.Fatalf("large bounded needs rejected: %d", got.Code)
	}
	if got := call([]string{strings.Repeat("x", 64*1024+1)}); got.Code != 400 {
		t.Fatalf("oversized need accepted: %d", got.Code)
	}
}

func TestM10UsageLookupDoesNotWakeStoppedRuntime(t *testing.T) {
	commands := 0
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { commands++; return nil, nil }, nil)
	request := hubruntime.UsageRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot"}, LookupOnly: true}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/usage", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, r)
	if w.Code != 503 || commands != 0 {
		t.Fatalf("meter woke stopped runtime: %d commands=%d", w.Code, commands)
	}
}

func TestM10IdleDeadlineStartsAfterActivityFinishes(t *testing.T) {
	removes := 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		if args[0] == "rm" {
			removes++
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	now := time.Unix(100, 0)
	m.cfg.Now = func() time.Time { return now }
	lease, _, err := m.Acquire(context.Background(), binding(root), LeaseJob)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(m.cfg.WarmTTL + time.Second)
	if err = m.Reap(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if removes != 0 {
		t.Fatal("active runtime reaped")
	}
	if err = m.ReleaseLease(lease.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.Reap(context.Background(), now.Add(m.cfg.WarmTTL/2)); err != nil {
		t.Fatal(err)
	}
	if removes != 0 {
		t.Fatal("runtime did not get fresh idle interval")
	}
	if err = m.Reap(context.Background(), now.Add(m.cfg.WarmTTL)); err != nil {
		t.Fatal(err)
	}
	if removes != 1 {
		t.Fatalf("idle runtime not reaped: %d", removes)
	}
}

func TestM10UsageLookupActiveAndMalformedRequests(t *testing.T) {
	commands := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("wrong upstream authority")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_found":false}`))
	}))
	defer api.Close()
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { commands++; return nil, nil }, nil)
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	m.items[runtimeKey(normalized)] = &runtimeEntry{Runtime: Runtime{Address: api.URL, State: Idle}, binding: normalized, leases: map[string]Lease{}}
	request := hubruntime.UsageRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot"}, LookupOnly: true}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/v1/usage", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		return w
	}
	if got := call("POST", string(body)); got.Code != 200 || got.Body.String() != `{"session_found":false}` || commands != 0 {
		t.Fatalf("lookup=%d %s commands=%d", got.Code, got.Body.String(), commands)
	}
	for _, bad := range []struct {
		method, body string
		status       int
	}{{"GET", "", 405}, {"POST", "{", 400}, {"POST", strings.Repeat("x", 128*1024+1), 400}, {"POST", "{}", 409}} {
		if got := call(bad.method, bad.body); got.Code != bad.status {
			t.Fatalf("malformed lookup=%d want=%d", got.Code, bad.status)
		}
	}
	m.items[runtimeKey(normalized)].Address = "://bad"
	if got := call("POST", string(body)); got.Code != 502 {
		t.Fatalf("bad runtime address=%d", got.Code)
	}
}
