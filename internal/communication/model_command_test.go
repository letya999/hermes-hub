package communication

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// /model switches the owner's logical model through the space's own relay:
// the admin bearer proves the gateway — never the chat text — is the caller.
func TestModelCommandSwitchesThroughRelay(t *testing.T) {
	var gotAuth, gotModel string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"default"},{"id":"turbo"}]}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/model" {
			_, _ = io.WriteString(w, `{"model":"turbo"}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/registry" {
			_, _ = io.WriteString(w, `{"switch":"catalog","models":[{"name":"default","ladder":[{"provider":"cliproxy","model":"gemini-3.8-flash-n"}]},{"name":"turbo","ladder":[{"provider":"cliproxy","model":"gemini-3-flash"},{"provider":"openai","model":"gpt-5-mini","down":true}]}],"accounts":[{"provider":"cliproxy","catalog":["gemini-3.8-flash-n","gemini-3-flash","gemini-2.5-pro","veo-3"]},{"provider":"openai","down":true,"catalog":["gpt-5-mini","gpt-5"]}]}`)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		decodeBody(r, &body)
		gotModel = body.Model
		if body.Model == "turbo" {
			_, _ = io.WriteString(w, `{"model":"turbo"}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer relay.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", relay.URL)
	t.Setenv("HUB_MODEL_RELAY_ADMIN", "admintoken")

	out := modelCommand(context.Background(), User{ID: "alice"}, "/model")
	if strings.HasPrefix(out, "\n") || strings.HasPrefix(out, " ") {
		t.Fatalf("reply starts with whitespace: %q", out)
	}
	for _, want := range []string{
		"Аккаунты:", "cliproxy", "openai",
		"default → gemini-3.8-flash-n — основной",
		"turbo → gemini-3-flash — основной ← текущая",
		"turbo → gpt-5-mini — запасной",
		"недоступен",
		"ещё: cliproxy/gemini-2.5-pro, cliproxy/veo-3",
		"ещё: openai/gpt-5",
		"Переключить: /model <имя> или <аккаунт>/<модель>.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("account view missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "ещё: cliproxy/gemini-3-flash") || strings.Contains(out, "ещё: openai/gpt-5-mini") {
		t.Fatalf("wired models duplicated into the extra catalog: %q", out)
	}
	out = modelCommand(context.Background(), User{ID: "alice"}, "/model turbo")
	if gotAuth != "Bearer admintoken" || gotModel != "turbo" {
		t.Fatalf("switch call malformed: auth=%q model=%q", gotAuth, gotModel)
	}
	if !strings.Contains(out, "turbo") {
		t.Fatalf("switch not confirmed: %q", out)
	}
	out = modelCommand(context.Background(), User{ID: "alice"}, "/model secret-model")
	if !strings.Contains(strings.ToLower(out), "нет права") && !strings.Contains(out, "недоступн") {
		t.Fatalf("denied switch misreported: %q", out)
	}
}

// The relay's switch grant steers what /model advertises: catalog prints
// directly usable provider/model names, logical lists only registry names,
// none announces that switching is off. An ad-hoc selection marks the exact
// catalog entry as current.
func TestModelCommandSwitchLevelView(t *testing.T) {
	registry := func(level string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/v1/model" {
				_, _ = io.WriteString(w, `{"model":"gemini-2.5-pro","provider":"cliproxy"}`)
				return
			}
			if r.Method == http.MethodGet && r.URL.Path == "/v1/registry" {
				_, _ = io.WriteString(w, `{"switch":"`+level+`","models":[{"name":"default","ladder":[{"provider":"cliproxy","model":"gemini-3.8-flash-n"}]}],"accounts":[{"provider":"cliproxy","catalog":["gemini-3.8-flash-n","gemini-2.5-pro"]}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":[{"id":"default"}]}`)
		}
	}
	run := func(level string) string {
		relay := httptest.NewServer(registry(level))
		defer relay.Close()
		t.Setenv("HUB_MODEL_RELAY_URL", relay.URL)
		t.Setenv("HUB_MODEL_RELAY_ADMIN", "admintoken")
		t.Setenv("HUB_MODEL_RELAY_ADMINS_FILE", "")
		return modelCommand(context.Background(), User{ID: "alice"}, "/model")
	}
	out := run("catalog")
	if !strings.Contains(out, "ещё: cliproxy/gemini-2.5-pro ← текущая") {
		t.Fatalf("ad-hoc catalog selection not marked current: %q", out)
	}
	if !strings.Contains(out, "или <аккаунт>/<модель>") {
		t.Fatalf("catalog view misses qualified-name hint: %q", out)
	}
	out = run("logical")
	if strings.Contains(out, "ещё:") {
		t.Fatalf("logical view lists unusable catalog ids: %q", out)
	}
	if !strings.Contains(out, "Переключить: /model <имя>.") || strings.Contains(out, "аккаунт/") {
		t.Fatalf("logical view advertises provider/model switching: %q", out)
	}
	out = run("none")
	if !strings.Contains(out, "Переключение моделей отключено.") {
		t.Fatalf("none view does not announce the disabled switch: %q", out)
	}
}

func TestModelCommandWithoutRelay(t *testing.T) {
	t.Setenv("HUB_MODEL_RELAY_URL", "")
	t.Setenv("HUB_MODEL_RELAY_ADMINS_FILE", "")
	out := modelCommand(context.Background(), User{ID: "alice"}, "/model turbo")
	if !strings.Contains(out, "недоступн") {
		t.Fatalf("missing relay not reported: %q", out)
	}
}

// A relay without /v1/registry (env-mode or older build) still lists models —
// the account view degrades to the plain catalog, never an error.
func TestModelCommandListsWithoutRegistryEndpoint(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"default"},{"id":"turbo"}]}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/model" {
			_, _ = io.WriteString(w, `{"model":"default"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer relay.Close()
	t.Setenv("HUB_MODEL_RELAY_URL", relay.URL)
	t.Setenv("HUB_MODEL_RELAY_ADMIN", "admintoken")

	out := modelCommand(context.Background(), User{ID: "alice"}, "/model")
	if !strings.Contains(out, "default") || !strings.Contains(out, "turbo") || !strings.Contains(out, "Текущая") {
		t.Fatalf("plain catalog not shown: %q", out)
	}
}

// The shared gateway serves several managed principals: each /model call must
// resolve the caller's own relay URL + admin bearer from the enrolled file.
// The env-URL path stays the single-owner fallback.
func TestModelCommandResolvesCallerRelay(t *testing.T) {
	var gotAuth, gotModel string
	aliceRelay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"default"},{"id":"turbo"}]}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/model" {
			_, _ = io.WriteString(w, `{"model":"default"}`)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		decodeBody(r, &body)
		gotModel = body.Model
		_, _ = io.WriteString(w, `{"model":"`+body.Model+`"}`)
	}))
	defer aliceRelay.Close()
	bobHit := false
	bobRelay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bobHit = true
		w.WriteHeader(http.StatusForbidden)
	}))
	defer bobRelay.Close()

	dir := t.TempDir()
	admins := fmt.Sprintf(`{"alice":{"url":%q,"admin":"alice-admin"},"bob":{"url":%q,"admin":"bob-admin"}}`, aliceRelay.URL, bobRelay.URL)
	path := filepath.Join(dir, "model-relay-admins.json")
	if err := os.WriteFile(path, []byte(admins), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MODEL_RELAY_URL", "")
	t.Setenv("HUB_MODEL_RELAY_ADMIN", "")
	t.Setenv("HUB_MODEL_RELAY_ADMINS_FILE", path)

	out := modelCommand(context.Background(), User{ID: "alice"}, "/model")
	if !strings.Contains(out, "default") || !strings.Contains(out, "turbo") {
		t.Fatalf("alice's catalog not listed: %q", out)
	}
	if bobHit {
		t.Fatal("alice's /model reached bob's relay")
	}
	out = modelCommand(context.Background(), User{ID: "alice"}, "/model turbo")
	if gotAuth != "Bearer alice-admin" || gotModel != "turbo" {
		t.Fatalf("switch used wrong relay credentials: auth=%q model=%q", gotAuth, gotModel)
	}
	if !strings.Contains(out, "turbo") {
		t.Fatalf("switch not confirmed: %q", out)
	}

	// An unknown principal must not fall through to a sibling's relay.
	out = modelCommand(context.Background(), User{ID: "carol"}, "/model")
	if !strings.Contains(out, "недоступн") {
		t.Fatalf("unmapped principal not rejected: %q", out)
	}
	if bobHit {
		t.Fatal("unknown principal reached a sibling relay")
	}
}

// The mock channel is the real pipeline: a slash command injected for a
// principal must reach the command dispatcher, not the agent. /model is the
// covered surface — the reply lands in the durable mock outbox.
func TestMockChannelRunsModelCommand(t *testing.T) {
	var gotAuth, gotModel string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"default"},{"id":"turbo"}]}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v1/model" {
			_, _ = io.WriteString(w, `{"model":"default"}`)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		decodeBody(r, &body)
		gotModel = body.Model
		_, _ = io.WriteString(w, `{"model":"`+body.Model+`"}`)
	}))
	defer relay.Close()

	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	admins := fmt.Sprintf(`{"alice":{"url":%q,"admin":"alice-admin"}}`, relay.URL)
	adminsFile := filepath.Join(t.TempDir(), "model-relay-admins.json")
	if err := os.WriteFile(adminsFile, []byte(admins), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_MODEL_RELAY_URL", "")
	t.Setenv("HUB_MODEL_RELAY_ADMIN", "")
	t.Setenv("HUB_MODEL_RELAY_ADMINS_FILE", adminsFile)

	call := func(method, path string, body io.Reader) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+c.ControlAuth)
		req.Header.Set("X-Hub-Principal", "alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	if code, _ := call(http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":55,"text":"/model"}`)); code != http.StatusOK {
		t.Fatalf("mock /model inject: %d", code)
	}
	g.deliverOne(t.Context())
	if code, body := call(http.MethodGet, "/v1/mock/outbox", nil); code != http.StatusOK || !strings.Contains(body, "Модели") || !strings.Contains(body, "Текущая") {
		t.Fatalf("command reply not captured: %d %s", code, body)
	}
	if code, _ := call(http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":55,"text":"/model turbo"}`)); code != http.StatusOK {
		t.Fatalf("mock /model turbo inject: %d", code)
	}
	g.deliverOne(t.Context())
	if code, body := call(http.MethodGet, "/v1/mock/outbox", nil); code != http.StatusOK || !strings.Contains(body, "переключена") {
		t.Fatalf("switch reply not captured: %d %s", code, body)
	}
	if gotAuth != "Bearer alice-admin" || gotModel != "turbo" {
		t.Fatalf("switch call malformed: auth=%q model=%q", gotAuth, gotModel)
	}
	// Commands whose replies bypass the shared reply() helper — /delete's
	// topic rebound path and /usage's async measurement — still leave through
	// the injecting channel, never Telegram.
	if code, _ := call(http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":55,"text":"/new work"}`)); code != http.StatusOK {
		t.Fatalf("mock /new inject: %d", code)
	}
	g.deliverOne(t.Context())
	if code, _ := call(http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":55,"text":"/delete"}`)); code != http.StatusOK {
		t.Fatalf("mock /delete inject: %d", code)
	}
	g.deliverOne(t.Context())
	if code, body := call(http.MethodGet, "/v1/mock/outbox", nil); code != http.StatusOK || !strings.Contains(body, "удалена") {
		t.Fatalf("/delete reply not captured on mock channel: %d %s", code, body)
	}
	if code, _ := call(http.MethodPost, "/v1/mock/message", strings.NewReader(`{"chat_id":55,"text":"/usage"}`)); code != http.StatusOK {
		t.Fatalf("mock /usage inject: %d", code)
	}
	g.deliverOne(t.Context())
	g.usageWG.Wait()
	g.deliverOne(t.Context())
	// The async measurement result — not just the "measuring" ack — must land
	// on the mock channel too.
	if code, body := call(http.MethodGet, "/v1/mock/outbox", nil); code != http.StatusOK || !strings.Contains(body, "Журнал:") {
		t.Fatalf("async /usage result not captured on mock channel: %d %s", code, body)
	}
	// A command must not enqueue a message job for the agent.
	if job, err := g.spool.ClaimJob(); err != nil || job != nil {
		t.Fatalf("/model leaked to the agent queue: %+v %v", job, err)
	}
}

func decodeBody(r *http.Request, v any) {
	defer r.Body.Close()
	_ = json.NewDecoder(r.Body).Decode(v)
}
