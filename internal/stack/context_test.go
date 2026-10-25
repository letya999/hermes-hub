package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func contextTestSettings() Settings {
	return Settings{Schema: 1, Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1, User: "alice", Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
}

func TestSettingsContextDeclarationAccepted(t *testing.T) {
	s := contextTestSettings()
	s.Compression = map[string]any{
		"threshold_tokens": 180000,
		"hub_sweep":        map[string]any{"enabled": true, "threshold_tokens": 220000, "cooldown_seconds": 600, "precompute": true},
	}
	s.Context = ContextSection{Compact: ContextCompact{Engine: "auto", Engines: []string{"hub", "native"}}}
	s.Goals = ContextGoals{Engine: "hub", Judge: "self", MaxTurns: 12}
	s.SummaryModel = "ctx-summary"
	s.JudgeModel = "ctx-judge"
	if err := s.Validate(); err != nil {
		t.Fatalf("declared context config rejected: %v", err)
	}
	cfg := s.managedContextConfig()
	if cfg == nil {
		t.Fatal("declared context config produced no ContextConfig")
	}
	if cfg.Compression["threshold_tokens"] != int64(180000) {
		t.Fatalf("compression canonical: %v", cfg.Compression)
	}
	sweep, ok := cfg.Compression["hub_sweep"].(map[string]any)
	if !ok || sweep["threshold_tokens"] != int64(220000) || sweep["enabled"] != true {
		t.Fatalf("hub_sweep canonical: %v", cfg.Compression)
	}
	if cfg.CompactEngine != "auto" || len(cfg.CompactEngines) != 2 || cfg.GoalsEngine != "hub" || cfg.GoalsJudge != "self" || cfg.GoalsMaxTurns != 12 {
		t.Fatalf("context config: %+v", cfg)
	}
	if cfg.SummaryModel != "ctx-summary" || cfg.JudgeModel != "ctx-judge" || cfg.ModelURL != s.ModelURL {
		t.Fatalf("aux models: %+v", cfg)
	}
	// The env round-trip is the transport between render and the container.
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back ContextConfig
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if back.GoalsJudge != "self" || back.Compression["threshold_tokens"] != float64(180000) {
		t.Fatalf("env round-trip: %+v", back)
	}
}

func TestSettingsContextDefaultsAbsent(t *testing.T) {
	s := contextTestSettings()
	if cfg := s.managedContextConfig(); cfg != nil {
		t.Fatalf("undeclared context produced %+v", cfg)
	}
}

func TestSettingsContextValidateRejects(t *testing.T) {
	for name, mutate := range map[string]func(*Settings){
		"unknown-compression":   func(s *Settings) { s.Compression = map[string]any{"evil_key": 1} },
		"authority-compression": func(s *Settings) { s.Compression = map[string]any{"mcp_servers": map[string]any{"x": 1}} },
		"low-threshold":         func(s *Settings) { s.Compression = map[string]any{"threshold_tokens": 5} },
		"bad-engine":            func(s *Settings) { s.Context.Compact.Engine = "cloud" },
		"bad-engines-entry":     func(s *Settings) { s.Context.Compact.Engines = []string{"hub", "moon"} },
		"dup-engines":           func(s *Settings) { s.Context.Compact.Engines = []string{"hub", "hub"} },
		"engine-not-granted": func(s *Settings) {
			s.Context.Compact.Engine = "native"
			s.Context.Compact.Engines = []string{"hub"}
		},
		"bad-goal-engine": func(s *Settings) { s.Goals.Engine = "cron" },
		"bad-judge":       func(s *Settings) { s.Goals.Judge = "frontier" },
		"bad-max-turns":   func(s *Settings) { s.Goals.MaxTurns = 500 },
		"bad-summary":     func(s *Settings) { s.SummaryModel = "not a model!" },
	} {
		t.Run(name, func(t *testing.T) {
			s := contextTestSettings()
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("invalid context declaration accepted")
			}
		})
	}
}

func TestContextConfigMergesIntoEffectiveConfig(t *testing.T) {
	base := M{
		"compression": M{"enabled": true, "in_place": true, "micro_compact": false, "threshold_tokens": 200000},
		"auxiliary":   M{"vision": M{"provider": "main"}},
		"model":       M{"default": "synthetic"},
	}
	cfg := &ContextConfig{
		Compression:    map[string]any{"threshold_tokens": int64(180000), "hub_sweep": map[string]any{"enabled": true, "threshold_tokens": int64(220000)}},
		CompactEngine:  "hub",
		CompactEngines: []string{"hub", "native"},
		GoalsEngine:    "native",
		GoalsJudge:     "aux",
		GoalsMaxTurns:  10,
		SummaryModel:   "ctx-summary",
		JudgeModel:     "ctx-judge",
		ModelURL:       "http://model-relay:8318/v1",
	}
	mergeContextConfig(base, cfg)
	compression := base["compression"].(M)
	if compression["threshold_tokens"] != int64(180000) || compression["enabled"] != true {
		t.Fatalf("compression merge: %v", compression)
	}
	if compression["hub_sweep"].(map[string]any)["threshold_tokens"] != int64(220000) {
		t.Fatalf("hub_sweep merge: %v", compression)
	}
	compact := base["features"].(M)["compact"].(M)
	if compact["engine"] != "hub" || len(compact["engines"].([]string)) != 2 {
		t.Fatalf("features merge: %v", base["features"])
	}
	goals := base["goals"].(M)
	if goals["engine"] != "native" || goals["judge"] != "aux" || goals["max_turns"] != 10 {
		t.Fatalf("goals merge: %v", goals)
	}
	aux := base["auxiliary"].(M)
	if aux["vision"].(M)["provider"] != "main" {
		t.Fatal("auxiliary merge dropped vision")
	}
	auxCompression := aux["compression"].(M)
	if auxCompression["provider"] != "custom" || auxCompression["model"] != "ctx-summary" ||
		auxCompression["base_url"] != "http://model-relay:8318/v1" || auxCompression["api_key"] != "${OPENAI_API_KEY}" {
		t.Fatalf("auxiliary.compression: %v", auxCompression)
	}
	if aux["goal_judge"].(M)["model"] != "ctx-judge" {
		t.Fatalf("auxiliary.goal_judge: %v", aux["goal_judge"])
	}
}

func TestContextConfigAuxDefaultsToMain(t *testing.T) {
	base := M{"compression": M{"enabled": true}}
	mergeContextConfig(base, &ContextConfig{CompactEngine: "hub"})
	aux := base["auxiliary"].(M)["compression"].(M)
	if aux["provider"] != "main" {
		t.Fatalf("ungranted summary path: %v", aux)
	}
	if _, present := base["auxiliary"].(M)["goal_judge"]; present {
		t.Fatal("ungranted judge rendered a goal_judge block")
	}
}

func TestManagedContextMaterializeAndAttest(t *testing.T) {
	s := contextTestSettings()
	zero := Config(s)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.yaml")
	dest := filepath.Join(dir, "effective.yaml")
	body, err := yaml.Marshal(zero)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, body, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := &ContextConfig{
		Compression:   map[string]any{"hub_sweep": map[string]any{"enabled": true, "threshold_tokens": int64(220000)}},
		CompactEngine: "auto", CompactEngines: []string{"hub", "native"},
		GoalsEngine: "hub", GoalsJudge: "self", GoalsMaxTurns: 12,
		ModelURL: "http://model-relay:8318/v1",
	}
	opts := MaterializeOptions{Managed: true, ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true, Context: ctx}
	if err := MaterializeHermesConfig(source, dest, opts); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedEffectiveConfig(dest, s, opts); err != nil {
		t.Fatalf("context materialization failed attestation: %v", err)
	}
	// An attestation that forgets the declared context must fail, and so must
	// a hand-edit removing the merged keys.
	dropped := opts
	dropped.Context = nil
	if err := ValidateManagedEffectiveConfig(dest, s, dropped); err == nil {
		t.Fatal("attestation accepted config without the declared context")
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	var decoded M
	if err := yaml.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	delete(decoded, "goals")
	out, _ := yaml.Marshal(decoded)
	if err := os.WriteFile(dest, out, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedEffectiveConfig(dest, s, opts); err == nil {
		t.Fatal("tampered context section accepted")
	}
}

func TestRuntimeEnvCarriesManagedContext(t *testing.T) {
	s := contextTestSettings()
	s.Goals = ContextGoals{Engine: "hub", MaxTurns: 10}
	dir := t.TempDir()
	service := RuntimeService(s, dir, dir)
	if service == nil {
		t.Fatal("no runtime service")
	}
	envMap, _ := service["environment"].(M)
	raw, _ := envMap["HUB_MANAGED_CONTEXT"].(string)
	if raw == "" {
		t.Fatal("declared context did not render into runtime env")
	}
	var cfg ContextConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.GoalsEngine != "hub" || cfg.GoalsMaxTurns != 10 {
		t.Fatalf("env context: %+v", cfg)
	}
	// No declaration → no env key: the container then attests the zero
	// template unchanged.
	plain := RuntimeService(contextTestSettings(), dir, dir)
	plainEnv, _ := plain["environment"].(M)
	if _, present := plainEnv["HUB_MANAGED_CONTEXT"]; present {
		t.Fatal("undeclared context rendered an env key")
	}
}
