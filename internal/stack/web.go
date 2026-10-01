package stack

import (
	"fmt"
	"regexp"
	"strings"
)

// WebSettings mirrors the upstream Hermes `web:` config section (pinned
// hermes-agent 869228c). Empty fields are omitted at render so upstream
// defaults apply; names that upstream does not know are rejected here instead
// of failing at the first web_search/web_extract call.
type WebSettings struct {
	Backend          string            `yaml:"backend,omitempty"`
	SearchBackend    string            `yaml:"search_backend,omitempty"`
	ExtractBackend   string            `yaml:"extract_backend,omitempty"`
	KeylessFallback  *bool             `yaml:"keyless_fallback,omitempty"`
	KeylessRescue    *bool             `yaml:"keyless_rescue,omitempty"`
	ExtractCharLimit int               `yaml:"extract_char_limit,omitempty"`
	ProviderTier     map[string]string `yaml:"provider_tier,omitempty"`
	CacheEnabled     *bool             `yaml:"cache_enabled,omitempty"`
	CacheTTLMinutes  int               `yaml:"cache_ttl_minutes,omitempty"`
	CacheExemptHosts []string          `yaml:"cache_exempt_hosts,omitempty"`
	Research         WebResearchBounds `yaml:"research,omitempty"`
}

// webGatedSkills maps bundled skills in config/skills to the feature that
// mounts them; a space without the feature gets a skills directory where the
// skill physically does not exist (and `skills.disabled` blocks it too).
var webGatedSkills = map[string]string{"deep-research-embedded": "deep_research"}

// empty reports whether no web field is set, so validation can reject a
// `web:` block on a space whose `web` feature is disabled.
func (w WebSettings) empty() bool {
	return w.Backend == "" && w.SearchBackend == "" && w.ExtractBackend == "" &&
		w.KeylessFallback == nil && w.KeylessRescue == nil && w.ExtractCharLimit == 0 &&
		len(w.ProviderTier) == 0 && w.CacheEnabled == nil && w.CacheTTLMinutes == 0 &&
		len(w.CacheExemptHosts) == 0 && w.Research == (WebResearchBounds{})
}

// WebResearchBounds are hub policy for the deep-research-embedded skill, read from
// the mounted read-only config; upstream Hermes ignores this sub-map. Zero
// values fall back to the skill defaults; configured values may only tighten
// the ceilings below.
type WebResearchBounds struct {
	MaxRounds       int `yaml:"max_rounds,omitempty"`
	QueriesPerRound int `yaml:"queries_per_round,omitempty"`
	PagesPerRound   int `yaml:"pages_per_round,omitempty"`
	MaxPages        int `yaml:"max_pages,omitempty"`
	MaxPagesPerHost int `yaml:"max_pages_per_host,omitempty"`
	DeadlineMinutes int `yaml:"deadline_minutes,omitempty"`
}

// Hard ceilings the operator config can only tighten. They keep a deep
// research run a bounded workflow rather than an autonomous crawler: at most
// ~4 hours, 100 extracted pages and 16 search rounds even when the owner asks
// for a long deep-research run.
const (
	webMaxRoundsCeiling       = 16
	webQueriesPerRoundCeiling = 5
	webPagesPerRoundCeiling   = 8
	webMaxPagesCeiling        = 100
	webMaxPagesPerHostCeiling = 10
	webDeadlineMinutesCeiling = 240
	webExtractCharLimitMax    = 200000
	webCacheTTLMax            = 1440
)

// webProviderEnv lists the environment variables each pinned upstream web
// provider reads. Credential names are delivered to the runtime through
// secrets.<env>.env or the protected self-env form; they never appear in
// prompts or tool arguments.
var webProviderEnv = map[string][]string{
	"tavily":     {"TAVILY_API_KEY", "TAVILY_BASE_URL"},
	"exa":        {"EXA_API_KEY"},
	"parallel":   {"PARALLEL_API_KEY"},
	"perplexity": {"PERPLEXITY_API_KEY", "PERPLEXITY_BASE_URL"},
	"firecrawl":  {"FIRECRAWL_API_KEY", "FIRECRAWL_API_URL"},
	"searxng":    {"SEARXNG_URL"},
	"brave-free": {"BRAVE_SEARCH_API_KEY"},
	"keenable":   {"KEENABLE_API_KEY"},
	"xai":        {"XAI_API_KEY"},
	"ddgs":       {},
	// The managed Nous subscription alias resolves to firecrawl upstream and
	// uses the Nous Tool Gateway session, not a local key.
	"nous": {},
}

// Upstream capability split: search-only providers are honest errors for
// web_extract, and vice versa. Kept in sync with the pinned provider registry.
var webExtractProviders = map[string]bool{
	"tavily": true, "exa": true, "parallel": true, "perplexity": true,
	"firecrawl": true, "keenable": true,
}

var webTieredProviders = map[string]bool{
	"exa": true, "parallel": true, "firecrawl": true, "keenable": true,
}

// webRequiredEnv names the value a configured backend needs before its keyed
// path can authenticate. Keyless-ring providers (exa, parallel, firecrawl
// has a URL-only self-hosted path) and the managed alias return nothing or an
// either-list: absence degrades to the anonymous tier, never a hard failure.
func webRequiredEnv(backend string) []string {
	switch backend {
	case "searxng":
		return []string{"SEARXNG_URL"}
	case "perplexity":
		return []string{"PERPLEXITY_API_KEY"}
	case "brave-free":
		return []string{"BRAVE_SEARCH_API_KEY"}
	case "xai":
		return []string{"XAI_API_KEY"}
	case "firecrawl":
		return []string{"FIRECRAWL_API_KEY", "FIRECRAWL_API_URL"} // either
	default:
		return nil
	}
}

var webExemptHostPattern = regexp.MustCompile(`^(\*\.)?[a-z0-9][a-z0-9.-]{0,252}$`)

func (w WebSettings) backends() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range []string{w.Backend, w.SearchBackend, w.ExtractBackend} {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func (w WebSettings) validate() error {
	for capability, name := range map[string]string{"backend": w.Backend, "search_backend": w.SearchBackend, "extract_backend": w.ExtractBackend} {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := webProviderEnv[name]; !ok {
			return fmt.Errorf("web.%s: unknown provider %q", capability, name)
		}
		if capability == "extract_backend" && !webExtractProviders[name] {
			return fmt.Errorf("web.extract_backend: provider %q is search-only", name)
		}
		// A shared search-only backend would silently fall through on every
		// extract call; require an explicit extract_backend alongside it.
		if capability == "backend" && !webExtractProviders[name] && strings.ToLower(strings.TrimSpace(w.ExtractBackend)) == "" {
			return fmt.Errorf("web.backend: provider %q is search-only; set web.extract_backend too", name)
		}
	}
	for provider, tier := range w.ProviderTier {
		if !webTieredProviders[strings.ToLower(strings.TrimSpace(provider))] || (tier != "free" && tier != "paid") {
			return fmt.Errorf("web.provider_tier: %q must be a tiered provider (exa, parallel, firecrawl, keenable) with tier free or paid", provider)
		}
	}
	if w.ExtractCharLimit < 0 || w.ExtractCharLimit > webExtractCharLimitMax {
		return fmt.Errorf("web.extract_char_limit must be 0..%d", webExtractCharLimitMax)
	}
	if w.CacheTTLMinutes < 0 || w.CacheTTLMinutes > webCacheTTLMax {
		return fmt.Errorf("web.cache_ttl_minutes must be 0..%d", webCacheTTLMax)
	}
	if len(w.CacheExemptHosts) > 64 {
		return fmt.Errorf("web.cache_exempt_hosts exceeds 64 entries")
	}
	for _, host := range w.CacheExemptHosts {
		if !webExemptHostPattern.MatchString(host) {
			return fmt.Errorf("web.cache_exempt_hosts: invalid host %q", host)
		}
	}
	r := w.Research
	for _, bound := range []struct {
		name    string
		value   int
		ceiling int
	}{
		{"max_rounds", r.MaxRounds, webMaxRoundsCeiling},
		{"queries_per_round", r.QueriesPerRound, webQueriesPerRoundCeiling},
		{"pages_per_round", r.PagesPerRound, webPagesPerRoundCeiling},
		{"max_pages", r.MaxPages, webMaxPagesCeiling},
		{"max_pages_per_host", r.MaxPagesPerHost, webMaxPagesPerHostCeiling},
		{"deadline_minutes", r.DeadlineMinutes, webDeadlineMinutesCeiling},
	} {
		if bound.value < 0 || bound.value > bound.ceiling {
			return fmt.Errorf("web.research.%s must be 0..%d", bound.name, bound.ceiling)
		}
	}
	return nil
}

// envKeys returns the credential/endpoint env names the configured providers
// read, so the protected self-env form can deliver them.
func (w WebSettings) envKeys() []string {
	var keys []string
	for _, backend := range w.backends() {
		keys = append(keys, webProviderEnv[backend]...)
	}
	return keys
}

// config renders the Hermes `web:` section; only explicitly set fields are
// emitted so upstream defaults own everything else.
func (w WebSettings) config() M {
	out := M{}
	if w.Backend != "" {
		out["backend"] = strings.ToLower(strings.TrimSpace(w.Backend))
	}
	if w.SearchBackend != "" {
		out["search_backend"] = strings.ToLower(strings.TrimSpace(w.SearchBackend))
	}
	if w.ExtractBackend != "" {
		out["extract_backend"] = strings.ToLower(strings.TrimSpace(w.ExtractBackend))
	}
	if w.KeylessFallback != nil {
		out["keyless_fallback"] = *w.KeylessFallback
	}
	if w.KeylessRescue != nil {
		out["keyless_rescue"] = *w.KeylessRescue
	}
	if w.ExtractCharLimit > 0 {
		out["extract_char_limit"] = w.ExtractCharLimit
	}
	if len(w.ProviderTier) > 0 {
		tiers := M{}
		for provider, tier := range w.ProviderTier {
			tiers[strings.ToLower(strings.TrimSpace(provider))] = tier
		}
		out["provider_tier"] = tiers
	}
	if w.CacheEnabled != nil {
		out["cache_enabled"] = *w.CacheEnabled
	}
	if w.CacheTTLMinutes > 0 {
		out["cache_ttl_minutes"] = w.CacheTTLMinutes
	}
	if len(w.CacheExemptHosts) > 0 {
		out["cache_exempt_hosts"] = append([]string(nil), w.CacheExemptHosts...)
	}
	if w.Research != (WebResearchBounds{}) {
		research := M{}
		for _, field := range []struct {
			key   string
			value int
		}{
			{"max_rounds", w.Research.MaxRounds},
			{"queries_per_round", w.Research.QueriesPerRound},
			{"pages_per_round", w.Research.PagesPerRound},
			{"max_pages", w.Research.MaxPages},
			{"max_pages_per_host", w.Research.MaxPagesPerHost},
			{"deadline_minutes", w.Research.DeadlineMinutes},
		} {
			if field.value > 0 {
				research[field.key] = field.value
			}
		}
		if len(research) > 0 {
			out["research"] = research
		}
	}
	return out
}

// doctorIssues warns when a configured provider cannot authenticate. The
// anonymous tiers (ddgs, the keyless ring) keep web_search/web_extract usable
// without any key, so a missing credential is a hint, never a hard failure.
func (w WebSettings) doctorIssues(secrets map[string]string) []string {
	var issues []string
	for _, backend := range w.backends() {
		required := webRequiredEnv(backend)
		if len(required) == 0 {
			continue
		}
		missing := true
		for _, name := range required {
			if secrets[name] != "" {
				missing = false
			}
		}
		if missing {
			issues = append(issues, fmt.Sprintf("set %s for web provider %s", strings.Join(required, " or "), backend))
		}
	}
	return issues
}
