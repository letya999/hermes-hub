package api

import (
	"slices"
	"testing"
)

func TestWorkspaceMCPHasReviewedOAuthRecipe(t *testing.T) {
	entry, matched, err := PreparedForRepository("https://github.com/taylorwilsdon/google_workspace_mcp", "")
	if err != nil || !matched || entry.OAuth == nil {
		t.Fatal("Workspace recipe missing", err)
	}
	if entry.Source.CommitSHA != "af866c34a1d5d9429b09d7fe5572b07be299b844" || entry.OAuth.AccountInput != "USER_GOOGLE_EMAIL" || entry.OAuth.ClientInput != "GOOGLE_CLIENT_SECRET_PATH" || entry.ProbeTool != "list_calendars" {
		t.Fatal("Workspace contract drift")
	}
	if !slices.Contains(entry.Entrypoint, "--permissions") || !slices.Contains(entry.Entrypoint, "calendar:readonly") || !slices.Contains(entry.Entrypoint, "gmail:readonly") {
		t.Fatal("read-only mode missing")
	}
	if _, err := entry.BrokerContract(); err != nil {
		t.Fatal(err)
	}
	blocked, err := BlockedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range blocked {
		if candidate.ID == entry.ID {
			t.Fatal("reviewed Workspace still blocked")
		}
	}
}
