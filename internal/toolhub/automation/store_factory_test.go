package automation_test

import (
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

func init() {
	automation.TestStoreFactory = func(root string, doc api.RightsDoc) (automation.DecisionStore, automation.TestGovernance) {
		store := toolhub.NewStore()
		store.RightsDoc = doc
		gov := toolhub.NewGovernanceStore(filepath.Join(root, "governance.json"))
		store.RightsLedger = gov.Load
		return store, gov
	}
}
