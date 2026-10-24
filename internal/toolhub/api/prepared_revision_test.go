package api

import "testing"

func TestPreparedNotionUsesDistinctReviewedRevision(t *testing.T) {
	entry, ok, err := PreparedForRepository("https://github.com/makenotion/notion-mcp-server", "")
	if err != nil || !ok {
		t.Fatalf("prepared Notion missing: %v", err)
	}
	if entry.ContractRevision != 2 {
		t.Fatalf("prepared Notion reuses the Broker example revision: %d", entry.ContractRevision)
	}
	c, err := entry.BrokerContract()
	if err != nil || c.Revision != 2 || len(c.Deliveries) != 1 || c.Deliveries[0].Target != "NOTION_TOKEN" {
		t.Fatalf("invalid reviewed Notion contract: %v", err)
	}
}
