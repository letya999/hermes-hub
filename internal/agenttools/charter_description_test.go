package agenttools

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Eval-gaming guard (M31.11 mandatory red-team): a description stuffed with
// every keyword would inflate selection scores dishonestly, so the contract
// caps description length mechanically. Catalog r5 baseline peaks at ~1021
// bytes on parameter-dense create descriptions; 8.2's 2–3-sentence rule is
// the editorial guideline — this byte ceiling is the hard anti-stuffing
// bound. New tools should land far below it.
const descriptionByteBudget = 1024

func TestDescriptionBudgetLint(t *testing.T) {
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Audience != api.AudienceModel {
			continue
		}
		if spec.Description == "" {
			t.Errorf("%s: empty description — the contract cannot select on nothing", spec.Name)
			continue
		}
		if len(spec.Description) > descriptionByteBudget {
			t.Errorf("%s: description %d bytes exceeds the %d-byte budget — trim, do not keyword-stuff", spec.Name, len(spec.Description), descriptionByteBudget)
		}
	}
}
