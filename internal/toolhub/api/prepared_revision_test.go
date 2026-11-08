package api

import "testing"

func TestPreparedNotionUsesDistinctReviewedRevision(t *testing.T) {
	entry, ok, err := PreparedForRepository("https://github.com/makenotion/notion-mcp-server", "")
	if err != nil || !ok {
		t.Fatalf("prepared Notion missing: %v", err)
	}
	if entry.ContractRevision != 3 {
		t.Fatalf("prepared Notion reuses the Broker example revision: %d", entry.ContractRevision)
	}
	c, err := entry.BrokerContract()
	if err != nil || c.Revision != 3 || len(c.Deliveries) != 1 || c.Deliveries[0].Target != "NOTION_TOKEN" {
		t.Fatalf("invalid reviewed Notion contract: %v", err)
	}
}

func TestPreparedGitHubProjectsNewImmutableToolContractVersion(t *testing.T) {
	entry, ok, err := PreparedForRepository("https://github.com/github/github-mcp-server", "")
	if err != nil || !ok {
		t.Fatalf("prepared GitHub missing: %v", err)
	}
	if got := entry.Apply(DefaultSelfInstallConfig(entry.Source)).Version; got != "0.0.3" {
		t.Fatalf("GitHub reused pre-capability version: %s", got)
	}
}
