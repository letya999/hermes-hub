package api

import (
	"encoding/json"
	"fmt"
	"testing"
)

// The manifest freezes each reviewed payload under its id@revision.
func TestPreparedContractDigests(t *testing.T) {
	var pinned map[string]string
	if err := json.Unmarshal(preparedDigestsJSON, &pinned); err != nil {
		t.Fatal(err)
	}
	entries, err := PreparedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(pinned) != len(entries) {
		t.Fatalf("manifest has %d contracts, catalog has %d", len(pinned), len(entries))
	}
	for _, entry := range entries {
		c, err := entry.BrokerContract()
		if err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("%s@%d", c.ID, c.Revision)
		if pinned[key] != c.Digest() {
			t.Fatalf("reviewed contract %s digest changed without a new revision", key)
		}
	}
}
