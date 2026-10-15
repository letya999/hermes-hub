package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Config validation guards identity/path collisions; the env path builds the
// single-space deployment; HTTP STT/TTS sidecars exchange bounded payloads.
func TestConfigValidateAndMediaServices(t *testing.T) {
	dir := t.TempDir()
	valid := func(mut func(*Config)) Config {
		c := Config{
			OrganizationID: "acme",
			SpoolDir:       filepath.Join(dir, "spool"),
			Users:          []User{{ID: "me", Enabled: true, StateDir: filepath.Join(dir, "state"), WorkspaceDir: filepath.Join(dir, "ws"), TelegramIDs: []int64{42}}},
		}
		if mut != nil {
			mut(&c)
		}
		return c
	}
	if err := valid(nil).Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cases := []struct {
		name string
		cfg  Config
	}{
		{"supervised-no-url", valid(func(c *Config) { c.Supervised = true })},
		{"bad-org", valid(func(c *Config) { c.OrganizationID = "Bad Org" })},
		{"no-spool", valid(func(c *Config) { c.SpoolDir = "" })},
		{"runtime-url-query", valid(func(c *Config) { c.RuntimeURL = "http://127.0.0.1:9/x?y=1"; c.RuntimeAuth = "t" })},
		{"runtime-url-creds", valid(func(c *Config) { c.RuntimeURL = "http://u:p@127.0.0.1:9" })},
		{"runtime-no-auth", valid(func(c *Config) { c.RuntimeURL = "http://127.0.0.1:9" })},
		{"bad-user", valid(func(c *Config) { c.Users[0].ID = "Me!" })},
		{"dup-user", valid(func(c *Config) { c.Users = append(c.Users, c.Users[0]) })},
		{"bad-runtime-id", valid(func(c *Config) { c.Users[0].RuntimeID = "RUN!" })},
		{"bad-policy-version", valid(func(c *Config) { c.Users[0].PolicyVersion = "V 1" })},
		{"state-eq-workspace", valid(func(c *Config) { c.Users[0].WorkspaceDir = c.Users[0].StateDir })},
		{"root-overlap", valid(func(c *Config) {
			c.Users = append(c.Users, User{ID: "two", Enabled: true, StateDir: filepath.Join(dir, "state2"), WorkspaceDir: filepath.Join(dir, "state", "inner")})
		})},
		{"dup-telegram", valid(func(c *Config) { c.Users[0].TelegramIDs = []int64{7, 7} })},
		{"bad-telegram", valid(func(c *Config) { c.Users[0].TelegramIDs = []int64{0} })},
		{"bad-slack", valid(func(c *Config) { c.Users[0].SlackIDs = []SlackLink{{TeamID: "T1"}} })},
		{"dup-policy-disabled", valid(func(c *Config) { c.Users[0].PolicyDisabled = []string{"telegram", "telegram"} })},
		{"unknown-policy-disabled", valid(func(c *Config) { c.Users[0].PolicyDisabled = []string{"not-a-service"} })},
		{"no-users", valid(func(c *Config) { c.Users = nil })},
	}
	for _, tc := range cases {
		if err := tc.cfg.Validate(); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
	// Disabled users skip root ownership entirely.
	disabled := valid(func(c *Config) { c.Users[0].Enabled = false; c.Users[0].TelegramIDs = nil })
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled user config rejected: %v", err)
	}

	yamlPath := filepath.Join(dir, "gw.yaml")
	body := "organization_id: acme\nspool_dir: " + filepath.ToSlash(filepath.Join(dir, "s2")) + "\nusers:\n  - id: me\n    enabled: true\n    state_dir: " + filepath.ToSlash(filepath.Join(dir, "st2")) + "\n    workspace_dir: " + filepath.ToSlash(filepath.Join(dir, "ws2")) + "\n"
	if err := os.WriteFile(yamlPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadYAML(yamlPath)
	if err != nil || cfg.OrganizationID != "acme" {
		t.Fatalf("LoadYAML: %v %+v", err, cfg)
	}
	if _, err := LoadYAML(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing YAML accepted")
	}
	if err := os.WriteFile(yamlPath, []byte(body+"---\nsecond: doc\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadYAML(yamlPath); err == nil {
		t.Fatal("second YAML document accepted")
	}

	t.Setenv("HUB_COMMUNICATION_CONFIG", yamlPath)
	if err := os.WriteFile(yamlPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("HUB_COMMUNICATION_SPOOL", filepath.Join(dir, "envspool"))
	t.Setenv("HUB_COMMUNICATION_WORKERS", "7")
	t.Setenv("HUB_SPOOL_RETENTION", "48h")
	t.Setenv("HUB_STT_URL", "http://stt.local")
	t.Setenv("HUB_SUPERVISOR_AUTH", "suptok")
	cfg, err = ConfigFromEnv()
	if err != nil || cfg.Workers != 7 || cfg.SpoolRetention != 48*time.Hour || cfg.STTURL != "http://stt.local" || cfg.TelegramToken != "tok" {
		t.Fatalf("ConfigFromEnv yaml path: %v %+v", err, cfg)
	}

	os.Unsetenv("HUB_COMMUNICATION_CONFIG")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "42, 43")
	t.Setenv("HUB_USER_ID", "me")
	t.Setenv("HUB_ORGANIZATION_ID", "acme")
	cfg, err = ConfigFromEnv()
	if err != nil || len(cfg.Users) != 1 || len(cfg.Users[0].TelegramIDs) != 2 || cfg.OrganizationID != "acme" {
		t.Fatalf("ConfigFromEnv env path: %v %+v", err, cfg)
	}
	t.Setenv("TELEGRAM_ALLOWED_USERS", "nan")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("non-numeric TELEGRAM_ALLOWED_USERS accepted")
	}
	os.Unsetenv("TELEGRAM_ALLOWED_USERS")

	// HTTP STT: multipart upload returns transcript text.
	var gotAuth string
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "  hello world  "})
	}))
	defer stt.Close()
	audio := filepath.Join(dir, "voice.ogg")
	if err := os.WriteFile(audio, []byte("ogg"), 0600); err != nil {
		t.Fatal(err)
	}
	tr := HTTPTranscriber{Base: stt.URL, Auth: "bear", Timeout: 5 * time.Second}
	text, err := tr.Transcribe(context.Background(), audio, "audio/ogg")
	if err != nil || text != "hello world" {
		t.Fatalf("transcribe: %v %q", err, text)
	}
	if gotAuth != "Bearer bear" {
		t.Fatalf("auth header: %q", gotAuth)
	}
	if !tr.Healthy(context.Background()) {
		t.Fatal("healthy sidecar reported down")
	}
	if _, err := tr.Transcribe(context.Background(), filepath.Join(dir, "missing.ogg"), "audio/ogg"); err == nil {
		t.Fatal("missing file transcribed")
	}
	if _, err := (HTTPTranscriber{}).Transcribe(context.Background(), audio, "audio/ogg"); err == nil {
		t.Fatal("unconfigured STT transcribed")
	}

	tts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["voice"] != "nova" || req["input"] == "" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Write([]byte("AUDIO"))
	}))
	defer tts.Close()
	syn := HTTPSynthesizer{Base: tts.URL, Voice: "nova", Timeout: 5 * time.Second}
	out, mime, err := syn.Synthesize(context.Background(), "say hi")
	if err != nil || string(out) != "AUDIO" || mime != "audio/ogg" {
		t.Fatalf("synthesize: %v %q %q", err, out, mime)
	}
	if !syn.Healthy(context.Background()) {
		t.Fatal("healthy tts reported down")
	}
	if _, _, err := syn.Synthesize(context.Background(), "   "); err == nil {
		t.Fatal("empty text synthesized")
	}
	if _, _, err := (HTTPSynthesizer{}).Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("unconfigured TTS synthesized")
	}
	if !allowedAudioMIME("audio/ogg;codecs=opus") || allowedAudioMIME("text/plain") {
		t.Fatal("audio mime gate wrong")
	}
}
