package agenttools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/locale"
)

// envUpdateNextStep reads the user language from the mounted workspace
// preferences — the runtime container resolves it without a gateway call.
func TestEnvUpdateNextStepLocalized(t *testing.T) {
	ws := t.TempDir()
	if err := locale.StorePrefs(ws, locale.Prefs{Lang: locale.LangES, Source: locale.SourceChosen}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_WORKSPACE", ws)
	dir := t.TempDir()
	envPath := filepath.Join(dir, envstore.FileName)
	for _, key := range []string{"tools.next-step.jira", "tools.next-step.generic"} {
		if _, ok := locale.Lookup(locale.LangES, key); !ok {
			t.Errorf("catalog missing es %q", key)
		}
	}
	if got := envUpdateNextStep(envPath); got != locale.T(locale.LangES, "tools.next-step.generic") {
		t.Errorf("generic %q", got)
	}
	if err := os.WriteFile(envPath, []byte(`{"JIRA_URL":"https://x","JIRA_USERNAME":"u","JIRA_API_TOKEN":"t"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got := envUpdateNextStep(envPath); got != locale.T(locale.LangES, "tools.next-step.jira") {
		t.Errorf("jira %q", got)
	}
}
