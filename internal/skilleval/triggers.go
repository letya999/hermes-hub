package skilleval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// TriggerQuery is one probe: the model should activate the skill iff Expect.
type TriggerQuery struct {
	Q      string `json:"q"`
	Expect bool   `json:"expect"`
}

// TriggerSuite is evals/triggers.json — the trigger-precision fixture
// (R12.2): queries, a train/validation split fraction, run count and the
// accuracy threshold on the validation side.
type TriggerSuite struct {
	Split     float64        `json:"split"`
	Runs      int            `json:"runs"`
	Threshold float64        `json:"threshold"`
	Queries   []TriggerQuery `json:"queries"`
}

// LoadTriggers reads evals/triggers.json under dir.
func LoadTriggers(dir string) (TriggerSuite, error) {
	body, err := os.ReadFile(filepath.Join(dir, "evals", "triggers.json")) // #nosec G304 -- operator-selected skill dir
	if err != nil {
		return TriggerSuite{}, err
	}
	var suite TriggerSuite
	if err := json.Unmarshal(body, &suite); err != nil {
		return TriggerSuite{}, fmt.Errorf("triggers.json: %w", err)
	}
	return suite, nil
}

// Partition splits queries deterministically into train/validation by the
// suite fraction — the same input always produces the same boundary (A12).
func (s TriggerSuite) Partition() (train, validation []TriggerQuery) {
	cut := int(float64(len(s.Queries)) * s.Split)
	return s.Queries[:cut], s.Queries[cut:]
}
