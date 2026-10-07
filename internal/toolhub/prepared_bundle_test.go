package toolhub

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPreparedBundleDiscoveryAndTelegramGate(t *testing.T) {
	ready, err := PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := BlockedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	var telegram PreparedEntry
	for _, entry := range ready {
		ids[entry.ID] = true
		for _, alias := range entry.Aliases {
			ids[alias] = true
		}
		if entry.ID == "telegram" {
			telegram = entry
		}
	}
	for _, entry := range blocked {
		ids[entry.ID] = true
	}
	for _, id := range []string{"google-workspace", "slack", "telegram", "github", "gitlab", "atlassian", "sql", "txttsql", "grafana", "prometheus", "datalens", "careergo"} {
		if !ids[id] {
			t.Fatalf("bundle ID %s missing", id)
		}
	}
	if telegram.Source.CommitSHA != "26f1632b2b07cca16fa8f172fe645477921db9ed" || !telegram.AuthBeforeToolsList || telegram.RuntimeEnvironment["TELEGRAM_EXPOSED_TOOLS"] != "read-only" || telegram.Stateful || !slices.Equal(telegram.PythonExtras, []string{"proxy"}) {
		t.Fatalf("unsafe Telegram entry: %+v", telegram)
	}
	if !slices.Equal(telegram.ProxyEnvironment, []string{"TELEGRAM_PROXY_HOST", "TELEGRAM_PROXY_PORT"}) {
		t.Fatal("Telegram proxy settings are not managed")
	}
	var gate *CredentialGate
	if err := preparedCredentialGate(telegram.Source); !errors.As(err, &gate) || len(gate.Names) != 4 {
		t.Fatalf("Telegram must collect protected credentials before tools/list: %v", err)
	}
	control := &ControlPlane{Now: time.Now}
	sql, err := control.discover(t.Context(), aliceAuth(), "sql")
	if err != nil || !slices.ContainsFunc(sql["candidates"].([]DiscoveryCandidate), func(candidate DiscoveryCandidate) bool {
		return candidate.PreparedID == "sql" && candidate.Status == "prepared"
	}) {
		t.Fatalf("stable SQL alias unavailable: %v %+v", err, sql)
	}
	for _, id := range []string{"google-workspace", "slack", "careergo"} {
		result, err := control.discover(t.Context(), aliceAuth(), id)
		if err != nil {
			t.Fatal(err)
		}
		candidates := result["candidates"].([]DiscoveryCandidate)
		if len(candidates) != 1 || candidates[0].Status != "blocked" || candidates[0].ID != "" || candidates[0].Source.Repository != "" {
			t.Fatalf("blocked slot %s selectable: %+v", id, candidates)
		}
	}
}

func TestPreparedProxyPartsAndPythonExtra(t *testing.T) {
	definition := statefulContainerDefinition()
	definition.ProxyEnvironment = []string{"TELEGRAM_PROXY_HOST", "TELEGRAM_PROXY_PORT"}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
	args := proxyEnvironmentArgs(definition, "http://10.0.0.7:3128")
	joined := strings.Join(args, " ")
	for _, expected := range []string{"TELEGRAM_PROXY_TYPE=http", "TELEGRAM_PROXY_HOST=10.0.0.7", "TELEGRAM_PROXY_PORT=3128"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %s: %v", expected, args)
		}
	}
	definition.ProxyEnvironment = []string{"TELEGRAM_PROXY_HOST"}
	if err := definition.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatal("unpaired proxy host accepted")
	}
	definition.ProxyEnvironment = nil
	definition.Credentials = append(definition.Credentials, CredentialInput{Name: "UPSTREAM_PROXY_HOST", Required: true})
	if err := definition.Validate(); err != nil {
		t.Fatalf("ordinary credential mistaken for managed proxy: %v", err)
	}
	context := languageContext(t, map[string]string{"pyproject.toml": "[project]\nname='telegram-mcp'\n[project.scripts]\ntelegram-mcp='main:main'\n[project.optional-dependencies]\nproxy = ['python-socks>=2.4.3']\n"})
	recipe, data, err := GenerateArtifactRecipe(context, "python", "", []string{"/opt/venv/bin/telegram-mcp"}, "proxy")
	if err != nil || recipe.VerifyContext(data) != nil || !strings.Contains(string(data), "\".[proxy]\"") {
		t.Fatalf("reviewed Python extra missing: %v", err)
	}
	if _, _, err := GenerateArtifactRecipe(context, "python", "", nil, "unknown"); !errors.Is(err, ErrInvalid) {
		t.Fatal("undeclared Python extra accepted")
	}
}
