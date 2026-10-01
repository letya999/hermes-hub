package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func boolPtr(v bool) *bool { return &v }

func webSettings(base Settings) Settings {
	base.Features = append(append([]string{}, base.Features...), "web")
	base.Web = WebSettings{
		SearchBackend:    "searxng",
		ExtractBackend:   "firecrawl",
		ExtractCharLimit: 12000,
		ProviderTier:     map[string]string{"exa": "paid"},
		CacheTTLMinutes:  30,
		CacheExemptHosts: []string{"internal.example.com", "*.preview.dev"},
		Research:         WebResearchBounds{MaxRounds: 3, QueriesPerRound: 4, PagesPerRound: 5, MaxPages: 16, MaxPagesPerHost: 3, DeadlineMinutes: 15},
	}
	return base
}

func validSettings() Settings {
	return Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
}

func TestWebConfigRendersOnlySetFields(t *testing.T) {
	s := validSettings()
	if _, ok := Config(s)["web"]; ok {
		t.Fatal("empty web settings rendered a web section")
	}
	s.Features = []string{"web"}
	s.Web = WebSettings{SearchBackend: "SearXNG", KeylessFallback: boolPtr(false), Research: WebResearchBounds{MaxRounds: 3, MaxPages: 16}}
	web, ok := Config(s)["web"].(M)
	if !ok {
		t.Fatalf("web section missing: %#v", Config(s))
	}
	if web["search_backend"] != "searxng" || web["keyless_fallback"] != false {
		t.Fatalf("web fields not rendered: %#v", web)
	}
	research, ok := web["research"].(M)
	if !ok || research["max_rounds"] != 3 || research["max_pages"] != 16 {
		t.Fatalf("research bounds not rendered: %#v", web)
	}
	for _, absent := range []string{"backend", "extract_backend", "keyless_rescue", "extract_char_limit", "provider_tier", "cache_enabled", "cache_ttl_minutes", "cache_exempt_hosts"} {
		if _, ok := web[absent]; ok {
			t.Fatalf("unset web field %s emitted", absent)
		}
	}
	if _, ok := research["queries_per_round"]; ok {
		t.Fatal("unset research bound emitted")
	}
}

func TestWebValidationRejectsBadInput(t *testing.T) {
	for _, modify := range []func(*WebSettings){
		func(w *WebSettings) { w.Backend = "guessed-provider" },
		func(w *WebSettings) { w.ExtractBackend = "ddgs" },
		func(w *WebSettings) { w.ExtractBackend = "searxng" },
		func(w *WebSettings) { w.Backend = "ddgs"; w.ExtractBackend = "" },
		func(w *WebSettings) { w.ProviderTier = map[string]string{"searxng": "paid"} },
		func(w *WebSettings) { w.ProviderTier = map[string]string{"exa": "enterprise"} },
		func(w *WebSettings) { w.ExtractCharLimit = -1 },
		func(w *WebSettings) { w.ExtractCharLimit = 200001 },
		func(w *WebSettings) { w.CacheTTLMinutes = 1441 },
		func(w *WebSettings) { w.CacheExemptHosts = []string{"https://evil.example", "ok.example"} },
		func(w *WebSettings) { w.Research.MaxRounds = 17 },
		func(w *WebSettings) { w.Research.MaxPages = 101 },
		func(w *WebSettings) { w.Research.MaxPagesPerHost = 11 },
		func(w *WebSettings) { w.Research.DeadlineMinutes = 241 },
		func(w *WebSettings) { w.Research.DeadlineMinutes = -1 },
	} {
		s := webSettings(validSettings())
		modify(&s.Web)
		if err := s.Validate(); err == nil {
			t.Fatalf("invalid web settings accepted: %#v", s.Web)
		}
	}
	s := webSettings(validSettings())
	if err := s.Validate(); err != nil {
		t.Fatalf("valid web settings rejected: %v", err)
	}
	// A shared search-only backend is fine once extract_backend is explicit.
	s = validSettings()
	s.Features = []string{"web"}
	s.Web = WebSettings{Backend: "ddgs", ExtractBackend: "tavily"}
	if err := s.Validate(); err != nil {
		t.Fatalf("ddgs backend with extract override rejected: %v", err)
	}
}

func TestWebFeatureGatesCapability(t *testing.T) {
	// web: settings without the feature are rejected outright.
	s := validSettings()
	s.Web = WebSettings{Backend: "ddgs"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("web settings without web feature must fail: %v", err)
	}
	// deep_research cannot stand without web.
	s = validSettings()
	s.Features = []string{"deep_research"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "deep_research requires web") {
		t.Fatalf("deep_research without web must fail: %v", err)
	}
	// No feature: no web toolset, no web config, gated skill disabled.
	cfg := Config(validSettings())
	cli := cfg["platform_toolsets"].(M)["cli"].([]string)
	if slices.Contains(cli, "web") {
		t.Fatal("web toolset present without web feature")
	}
	if _, ok := cfg["web"]; ok {
		t.Fatal("web section rendered without web feature")
	}
	disabled := cfg["skills"].(M)["disabled"].([]string)
	if !slices.Contains(disabled, "deep-research-embedded") {
		t.Fatal("deep-research-embedded not disabled without deep_research feature")
	}
	// Feature on: toolset present, gated skill enabled (no disabled entry).
	s = validSettings()
	s.Features = []string{"web", "deep_research"}
	cfg = Config(s)
	cli = cfg["platform_toolsets"].(M)["cli"].([]string)
	if !slices.Contains(cli, "web") {
		t.Fatal("web toolset missing with web feature")
	}
	if disabled, ok := cfg["skills"].(M)["disabled"]; ok && slices.Contains(disabled.([]string), "deep-research-embedded") {
		t.Fatal("deep-research-embedded disabled despite deep_research feature")
	}
}

func TestGatedSkillExcludedFromMaterializedMount(t *testing.T) {
	src := t.TempDir()
	for _, name := range []string{"deep-research-embedded", "other-skill"} {
		dir := filepath.Join(src, name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("name: "+name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	space := t.TempDir()
	s := validSettings()
	s.GlobalSkillsDir = src
	if err := materializeGlobalSkills(space, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(space, "generated", "skills", "deep-research-embedded")); !os.IsNotExist(err) {
		t.Fatal("gated skill materialized without deep_research feature")
	}
	if _, err := os.Stat(filepath.Join(space, "generated", "skills", "other-skill", "SKILL.md")); err != nil {
		t.Fatalf("ungated skill not copied: %v", err)
	}
	s.Features = []string{"web", "deep_research"}
	if err := materializeGlobalSkills(space, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(space, "generated", "skills", "deep-research-embedded", "SKILL.md")); err != nil {
		t.Fatalf("gated skill missing with deep_research enabled: %v", err)
	}
}

func TestWebProviderEnvKeysReachSelfEnvAllowlist(t *testing.T) {
	s := validSettings()
	before := selfEnvKeys(s)
	for _, key := range []string{"EXA_API_KEY", "SEARXNG_URL"} {
		if slices.Contains(before, key) {
			t.Fatalf("%s leaked into self-env without configuration", key)
		}
	}
	s.Features = []string{"web"}
	s.Web = WebSettings{SearchBackend: "searxng", ExtractBackend: "exa"}
	keys := selfEnvKeys(s)
	for _, key := range []string{"SEARXNG_URL", "EXA_API_KEY"} {
		if !slices.Contains(keys, key) {
			t.Fatalf("configured provider key %s missing from self-env allowlist", key)
		}
	}
}

func TestWebDoctorWarnsOnlyKeyedProviders(t *testing.T) {
	s := validSettings()
	s.Features = []string{"web"}
	s.Web = WebSettings{Backend: "searxng"}
	issues := Doctor(s, map[string]string{"OPENAI_API_KEY": "x", "model": "y"})
	joined := strings.Join(issues, " ")
	if !strings.Contains(joined, "SEARXNG_URL") {
		t.Fatalf("missing searxng endpoint not reported: %v", issues)
	}
	if strings.Contains(joined, "TAVILY") {
		t.Fatal("keyless-capable provider must not produce a doctor warning")
	}
	s.Web = WebSettings{Backend: "searxng"}
	issues = Doctor(s, map[string]string{"OPENAI_API_KEY": "x", "SEARXNG_URL": "https://search.example.org"})
	for _, issue := range issues {
		if strings.Contains(issue, "SEARXNG_URL") {
			t.Fatalf("provided searxng endpoint still warned: %v", issues)
		}
	}
	s.Web = WebSettings{Backend: "firecrawl"}
	issues = Doctor(s, map[string]string{"OPENAI_API_KEY": "x"})
	if !strings.Contains(strings.Join(issues, " "), "FIRECRAWL_API_KEY") {
		t.Fatalf("missing firecrawl credentials not reported: %v", issues)
	}
	issues = Doctor(s, map[string]string{"OPENAI_API_KEY": "x", "FIRECRAWL_API_URL": "https://firecrawl.internal"})
	for _, issue := range issues {
		if strings.Contains(issue, "FIRECRAWL") {
			t.Fatalf("self-hosted firecrawl URL satisfies the credential: %v", issues)
		}
	}
	s.Web = WebSettings{Backend: "perplexity"}
	issues = Doctor(s, map[string]string{"OPENAI_API_KEY": "x"})
	if !strings.Contains(strings.Join(issues, " "), "PERPLEXITY_API_KEY") {
		t.Fatalf("missing perplexity key not reported: %v", issues)
	}
	s.Web = WebSettings{Backend: "exa"}
	for _, issue := range Doctor(s, map[string]string{"OPENAI_API_KEY": "x"}) {
		if strings.Contains(issue, "EXA_API_KEY") {
			t.Fatalf("keyless-ring provider must not produce a doctor warning: %v", issues)
		}
	}
}

func TestWebSettingsRoundTripThroughSettingsYAML(t *testing.T) {
	d := t.TempDir()
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	s, err := Read(filepath.Join(d, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s.Web = WebSettings{Backend: "tavily", Research: WebResearchBounds{MaxRounds: 2}}
	body, err := yaml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "settings.yaml"), body, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Read(filepath.Join(d, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Web.Backend != "tavily" || reloaded.Web.Research.MaxRounds != 2 {
		t.Fatalf("web settings did not round-trip: %#v", reloaded.Web)
	}
}
