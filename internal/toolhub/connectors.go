package toolhub

import (
	"fmt"
	"slices"

	"github.com/letya999/hermes-hub/internal/identity"
)

// ConnectorManifestVersion is bumped with every reviewed manifest change; a
// profile publication can pin the exact reviewed recommendation set.
const ConnectorManifestVersion = "1.0.0"

// AdmissionOptIn is the only admission state a recommendation may carry: the
// manifest recommends, never grants. Actual admission still requires a
// reviewed definition, connection, binding and confirmed capability profile.
const AdmissionOptIn = "opt_in"

// ConnectorRecommendation is one capability family the organization bundle
// review may admit later. A recommendation names the reviewed source and the
// capability family, not a grant; mutually exclusive alternatives are marked
// so a profile never selects both.
type ConnectorRecommendation struct {
	Family       string   `json:"family"`
	Source       string   `json:"source"`
	Admission    string   `json:"admission"`
	Status       string   `json:"status"` // implemented | recommended | deferred
	Alternatives []string `json:"alternatives,omitempty"`
	Notes        string   `json:"notes,omitempty"`
}

// ConnectorManifest is the versioned recommendation set from SPEC-0041 CP-07.
// HH stays an optional separately reviewed connector — it exists as the
// agenttools "hh" capability family behind explicit grants, never as a core
// or default feature.
type ConnectorManifest struct {
	Schema  int                       `json:"schema"`
	Version string                    `json:"version"`
	Entries []ConnectorRecommendation `json:"entries"`
}

// RecommendedConnectorManifest returns the reviewed connector recommendation
// set. Adding or changing an entry is a reviewed source change that bumps
// ConnectorManifestVersion.
func RecommendedConnectorManifest() ConnectorManifest {
	return ConnectorManifest{Schema: SchemaVersion, Version: ConnectorManifestVersion, Entries: []ConnectorRecommendation{
		{Family: "hh", Source: "https://api.hh.ru/openapi/redoc", Admission: AdmissionOptIn, Status: "implemented",
			Notes: "HeadHunter vacancies/resumes/negotiations via the reviewed agenttools hh capability; no embedded business service, explicit grant required"},
		{Family: "github", Source: "https://api.githubcopilot.com/mcp/ (github/github-mcp-server)", Admission: AdmissionOptIn, Status: "recommended"},
		{Family: "gitlab", Source: "Debian glab package (pinned runtime)", Admission: AdmissionOptIn, Status: "recommended"},
		{Family: "atlassian", Source: "sooperset/mcp-atlassian 74bdaa8f1d28783cccfe99f7b4d75e6dc947cf76", Admission: AdmissionOptIn, Status: "recommended"},
		{Family: "slack", Source: "korotovsky/slack-mcp-server b88c0de3f706f4f07337c9eda7133c736d1c9524", Admission: AdmissionOptIn, Status: "recommended"},
		{Family: "telegram", Source: "chigwell/telegram-mcp c9460f8ded6e2457bd70ebabfad840b58d23645d", Admission: AdmissionOptIn, Status: "recommended",
			Notes: "personal-account connector only; the bot channel adapter is a separate grant"},
		{Family: "sql", Source: "txttsql or dbhub, one only", Admission: AdmissionOptIn, Status: "recommended",
			Alternatives: []string{"txttsql", "dbhub"}, Notes: "a profile must never select both alternatives"},
		{Family: "grafana", Source: "Grafana API", Admission: AdmissionOptIn, Status: "recommended"},
		{Family: "prometheus", Source: "Prometheus HTTP API", Admission: AdmissionOptIn, Status: "recommended"},
		{Family: "datalens", Source: "Yandex DataLens API", Admission: AdmissionOptIn, Status: "recommended"},
		{Family: "google_calendar", Source: "https://calendarmcp.googleapis.com/mcp/v1 (ADR-0019)", Admission: AdmissionOptIn, Status: "recommended"},
		{Family: "notion", Source: "Notion API", Admission: AdmissionOptIn, Status: "recommended"},
		{Family: "metabase", Source: "Metabase API", Admission: AdmissionOptIn, Status: "deferred"},
		{Family: "sentry", Source: "Sentry API", Admission: AdmissionOptIn, Status: "deferred"},
		{Family: "google_analytics", Source: "Google Analytics Data API", Admission: AdmissionOptIn, Status: "deferred"},
		{Family: "appmetrica", Source: "Yandex AppMetrica API", Admission: AdmissionOptIn, Status: "deferred"},
		{Family: "gmail", Source: "https://gmailmcp.googleapis.com/mcp/v1 (ADR-0019)", Admission: AdmissionOptIn, Status: "deferred"},
		{Family: "drive", Source: "https://drivemcp.googleapis.com/mcp/v1 (ADR-0019)", Admission: AdmissionOptIn, Status: "deferred"},
		{Family: "docs", Source: "https://docsmcp.googleapis.com/mcp/v1 (ADR-0019)", Admission: AdmissionOptIn, Status: "deferred"},
		{Family: "figma", Source: "Figma API", Admission: AdmissionOptIn, Status: "deferred"},
	}}
}

// Validate rejects a manifest that could be mistaken for authority: duplicate
// families, a non-opt-in admission, unknown status or an alternatives list on
// a family that would otherwise look selectable twice.
func (m ConnectorManifest) Validate() error {
	if m.Schema != SchemaVersion || !versionPattern.MatchString(m.Version) || len(m.Entries) == 0 || len(m.Entries) > 64 {
		return fmt.Errorf("%w: connector manifest identity", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, entry := range m.Entries {
		if !identity.ValidID(entry.Family) || seen[entry.Family] || entry.Source == "" || len(entry.Source) > 512 || len(entry.Notes) > 1024 {
			return fmt.Errorf("%w: connector entry %q", ErrInvalid, entry.Family)
		}
		if entry.Admission != AdmissionOptIn {
			return fmt.Errorf("%w: connector %q admission must stay opt-in", ErrUnauthorized, entry.Family)
		}
		if entry.Status != "implemented" && entry.Status != "recommended" && entry.Status != "deferred" {
			return fmt.Errorf("%w: connector %q status", ErrInvalid, entry.Family)
		}
		if len(entry.Alternatives) > 8 || slices.Contains(entry.Alternatives, "") {
			return fmt.Errorf("%w: connector %q alternatives", ErrInvalid, entry.Family)
		}
		seen[entry.Family] = true
	}
	return nil
}
