package skilleval

import "context"

// ToolCall is one tool invocation recorded in a run receipt.
type ToolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

// Receipt is what one isolated run produced — the tool calls the agent made.
type Receipt struct {
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Runner executes one prompt inside the prepared eval space and returns
// the run receipt.
type Runner interface {
	Run(ctx context.Context, spaceDir, prompt string) (Receipt, error)
}

// TriggerReport is the scored outcome of one trigger leg (R12.3).
type TriggerReport struct {
	Status   string  `json:"status"` // done | model_unavailable
	Accuracy float64 `json:"accuracy,omitempty"`
	Pass     bool    `json:"pass"`
	Queries  int     `json:"queries"`
}

// RunTriggers scores validation-set accuracy over suite.Runs attempts per
// query. A query activates when any run calls `skill` with the skill's
// name; accuracy below Threshold fails the leg. A runner error reports the
// leg unavailable — never a fabricated score.
func RunTriggers(ctx context.Context, suite TriggerSuite, skill string, run Runner, spaceDir string) TriggerReport {
	_, validation := suite.Partition()
	rep := TriggerReport{Queries: len(validation)}
	correct := 0
	for _, q := range validation {
		hits := 0
		for i := 0; i < suite.Runs; i++ {
			rc, err := run.Run(ctx, spaceDir, q.Q)
			if err != nil {
				rep.Status = "model_unavailable"
				return rep
			}
			if receiptActivated(rc, skill) {
				hits++
			}
		}
		if (hits > 0) == q.Expect {
			correct++
		}
	}
	rep.Status = "done"
	if len(validation) > 0 {
		rep.Accuracy = float64(correct) / float64(len(validation))
		rep.Pass = rep.Accuracy >= suite.Threshold
	}
	return rep
}

func receiptActivated(rc Receipt, skill string) bool {
	for _, call := range rc.ToolCalls {
		name, _ := call.Args["name"].(string)
		if call.Name == "skill" && name == skill {
			return true
		}
	}
	return false
}
