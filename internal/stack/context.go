package stack

import (
	"fmt"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/selfsettings"
)

// Context control (SPEC-0072): the operator may declare compression, engine
// and goal policy in settings.yaml. The normalized ContextConfig rides
// HUB_MANAGED_CONTEXT into the runtime; materialization merges it into the
// effective Hermes config and attestation reconstructs the identical bytes,
// so a hand-edited config.yaml can never drop or widen the declaration.

// ContextConfig is the normalized context-control section. It crosses the
// render boundary as JSON (HUB_MANAGED_CONTEXT) — the effective config
// derives from it on both sides of the attestation.
type ContextConfig struct {
	// Compression carries validated compression.* leaves (nested maps under
	// the section, e.g. hub_sweep). Only selfsettings-allowlisted paths can
	// arrive here; the merge cannot widen authority.
	Compression    map[string]any `json:"compression,omitempty"`
	CompactEngine  string         `json:"compact_engine,omitempty"`
	CompactEngines []string       `json:"compact_engines,omitempty"`
	GoalsEngine    string         `json:"goals_engine,omitempty"`
	GoalsJudge     string         `json:"goals_judge,omitempty"`
	GoalsMaxTurns  int            `json:"goals_max_turns,omitempty"`
	SummaryModel   string         `json:"summary_model,omitempty"`
	JudgeModel     string         `json:"judge_model,omitempty"`
	// ModelURL is the relay base URL the auxiliary side paths call; inside
	// the container it is always the rendered managed model endpoint.
	ModelURL string `json:"model_url,omitempty"`
}

// ContextSection mirrors the declared context: block in settings.yaml. The
// yaml key is `context` (not `features`) because schema-1 files already own
// `features:` as a legacy string list; this block renders to the
// features.compact / selfsettings vocabulary upstream of the merge.
type ContextSection struct {
	Compact ContextCompact `yaml:"compact,omitempty"`
}

// ContextCompact selects the compaction engine policy. Engine is the bounded
// preference (auto|hub|native); Engines is the operator grant listing which
// engines the space may resolve at all.
type ContextCompact struct {
	Engine  string   `yaml:"engine,omitempty"`
	Engines []string `yaml:"engines,omitempty"`
}

// ContextGoals mirrors the declared goals: block.
type ContextGoals struct {
	Engine   string `yaml:"engine,omitempty"`
	Judge    string `yaml:"judge,omitempty"`
	MaxTurns int    `yaml:"max_turns,omitempty"`
}

var contextEngines = []string{"hub", "native"}
var contextJudges = []string{"auto", "aux", "main", "self"}

func contextEngineChoice(value string) bool {
	return value == "auto" || slices.Contains(contextEngines, value)
}

// validateContext enforces the declared context sections. Every leaf under
// compression: must be a reviewed selfsettings path — the declared block is
// the same authority as the runtime overlay, only provisioned at render.
func (s Settings) validateContext() error {
	if err := flattenCompressionLeaves(s.Compression, "compression"); err != nil {
		return err
	}
	compact := s.Context.Compact
	if compact.Engine != "" && !contextEngineChoice(compact.Engine) {
		return fmt.Errorf("context.compact.engine must be auto, hub or native")
	}
	if len(compact.Engines) > 8 {
		return fmt.Errorf("context.compact.engines: too many engines")
	}
	seen := map[string]bool{}
	for _, name := range compact.Engines {
		if !slices.Contains(contextEngines, name) {
			return fmt.Errorf("context.compact.engines: unknown engine %q", name)
		}
		if seen[name] {
			return fmt.Errorf("context.compact.engines: duplicate engine %q", name)
		}
		seen[name] = true
	}
	if compact.Engine != "" && compact.Engine != "auto" && len(compact.Engines) > 0 && !seen[compact.Engine] {
		return fmt.Errorf("context.compact.engine %q is outside the granted engines", compact.Engine)
	}
	if s.Goals.Engine != "" && !contextEngineChoice(s.Goals.Engine) {
		return fmt.Errorf("goals.engine must be auto, hub or native")
	}
	if s.Goals.Judge != "" && !slices.Contains(contextJudges, s.Goals.Judge) {
		return fmt.Errorf("goals.judge must be auto, aux, main or self")
	}
	if s.Goals.MaxTurns < 0 || s.Goals.MaxTurns > 50 {
		return fmt.Errorf("goals.max_turns must be 1..50")
	}
	for name, model := range map[string]string{"summary_model": s.SummaryModel, "judge_model": s.JudgeModel} {
		if model != "" && !idPattern.MatchString(model) {
			return fmt.Errorf("%s must be a logical model identifier", name)
		}
	}
	return nil
}

// flattenCompressionLeaves walks the declared compression: block and
// validates every leaf against the selfsettings allowlist — unknown paths or
// out-of-range values fail the whole load with a named error.
func flattenCompressionLeaves(section map[string]any, prefix string) error {
	for key, value := range section {
		if strings.ContainsAny(key, ".\x00\r\n") {
			return fmt.Errorf("%s: invalid key %q", prefix, key)
		}
		path := prefix + "." + key
		if nested, ok := value.(map[string]any); ok {
			if err := flattenCompressionLeaves(nested, path); err != nil {
				return err
			}
			continue
		}
		if _, err := selfsettings.ValidatePath(path, value); err != nil {
			return err
		}
	}
	return nil
}

// canonicalCompression re-validates the declared compression leaves and
// returns the normalized nested map that merges into the effective config.
// Validate() runs first, so an error here marks a programming slip and the
// leaf is dropped rather than passed through unchecked.
func canonicalCompression(section map[string]any, prefix string) map[string]any {
	out := map[string]any{}
	for key, value := range section {
		if nested, ok := value.(map[string]any); ok {
			out[key] = canonicalCompression(nested, prefix+"."+key)
			continue
		}
		canonical, err := selfsettings.ValidatePath(prefix+"."+key, value)
		if err != nil {
			continue
		}
		out[key] = canonical
	}
	return out
}

// managedContextConfig normalizes the declared context sections into the
// env-carried ContextConfig. Nil when the space declares nothing — the
// effective config then matches the reviewed zero template exactly.
func (s Settings) managedContextConfig() *ContextConfig {
	if s.Compression == nil && s.Context.Compact.Engine == "" && len(s.Context.Compact.Engines) == 0 &&
		s.Goals == (ContextGoals{}) && s.SummaryModel == "" && s.JudgeModel == "" {
		return nil
	}
	cfg := &ContextConfig{
		CompactEngine:  s.Context.Compact.Engine,
		CompactEngines: slices.Clone(s.Context.Compact.Engines),
		GoalsEngine:    s.Goals.Engine,
		GoalsJudge:     s.Goals.Judge,
		GoalsMaxTurns:  s.Goals.MaxTurns,
		SummaryModel:   s.SummaryModel,
		JudgeModel:     s.JudgeModel,
		ModelURL:       s.ModelURL,
	}
	if len(s.Compression) > 0 {
		cfg.Compression = canonicalCompression(s.Compression, "compression")
	}
	return cfg
}

// ManagedContextConfig exposes the normalized declaration to the supervisor
// materialize path; it is nil for spaces that declare no context policy.
func (s Settings) ManagedContextConfig() *ContextConfig { return s.managedContextConfig() }

// mergeContextConfig projects the declared context onto an effective config
// map. The merge only writes bounded scalar leaves: compression.* reuses the
// selfsettings-validated values, and features/goals/auxiliary carry no
// authority-bearing fields (engine choices reference reviewed backends).
func mergeContextConfig(config M, cfg *ContextConfig) {
	if cfg == nil {
		return
	}
	if len(cfg.Compression) > 0 {
		mergeDeep(config, "compression", cfg.Compression)
	}
	if cfg.CompactEngine != "" || len(cfg.CompactEngines) > 0 {
		compact := M{}
		if cfg.CompactEngine != "" {
			compact["engine"] = cfg.CompactEngine
		}
		if len(cfg.CompactEngines) > 0 {
			compact["engines"] = cfg.CompactEngines
		}
		mergeDeep(config, "features", M{"compact": compact})
	}
	if cfg.GoalsEngine != "" || cfg.GoalsJudge != "" || cfg.GoalsMaxTurns > 0 {
		goals := M{}
		if cfg.GoalsEngine != "" {
			goals["engine"] = cfg.GoalsEngine
		}
		if cfg.GoalsJudge != "" {
			goals["judge"] = cfg.GoalsJudge
		}
		if cfg.GoalsMaxTurns > 0 {
			goals["max_turns"] = cfg.GoalsMaxTurns
		}
		mergeDeep(config, "goals", goals)
	}
	// Auxiliary side paths route through the owner's model relay: a granted
	// logical model renders the custom block; without a grant the side path
	// stays on the main provider, which managed runtimes already pin to the
	// relay. goal_judge renders only with a dedicated grant.
	aux := M{}
	if cfg.SummaryModel != "" {
		aux["compression"] = M{"provider": "custom", "base_url": cfg.ModelURL, "api_key": "${OPENAI_API_KEY}", "model": cfg.SummaryModel}
	} else {
		aux["compression"] = M{"provider": "main"}
	}
	if cfg.JudgeModel != "" {
		aux["goal_judge"] = M{"provider": "custom", "base_url": cfg.ModelURL, "api_key": "${OPENAI_API_KEY}", "model": cfg.JudgeModel}
	}
	mergeDeep(config, "auxiliary", aux)
}

// mergeDeep overlays values under config[section] recursively. Sibling keys
// the template or other merges own (auxiliary.vision, compression defaults)
// survive; only declared leaves are written.
func mergeDeep(config M, section string, values map[string]any) {
	target, _ := config[section].(M)
	if target == nil {
		target = M{}
		config[section] = target
	}
	for key, value := range values {
		if nested, ok := value.(map[string]any); ok {
			mergeDeep(target, key, nested)
			continue
		}
		target[key] = value
	}
}
