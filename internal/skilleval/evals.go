package skilleval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EvalCase is one entry of evals/evals.json (R12.2): a prompt, the
// expected output, setup files, machine assertions and the evidence the
// grader must consult — a case without evidence cannot be graded (A12).
type EvalCase struct {
	Name           string            `json:"name"`
	Prompt         string            `json:"prompt"`
	ExpectedOutput string            `json:"expected_output"`
	Files          map[string]string `json:"files,omitempty"`
	Assertions     []string          `json:"assertions,omitempty"`
	Evidence       string            `json:"evidence"`
}

// EvalCases is the parsed evals.json document.
type EvalCases []EvalCase

// LoadEvals reads evals/evals.json under dir.
func LoadEvals(dir string) (EvalCases, error) {
	body, err := os.ReadFile(filepath.Join(dir, "evals", "evals.json")) // #nosec G304 -- operator-selected skill dir
	if err != nil {
		return nil, err
	}
	var cases EvalCases
	if err := json.Unmarshal(body, &cases); err != nil {
		return nil, fmt.Errorf("evals.json: %w", err)
	}
	return cases, nil
}

// Validate rejects evidence-less cases — the grader must not invent a pass.
func (cs EvalCases) Validate() error {
	for _, c := range cs {
		if strings.TrimSpace(c.Evidence) == "" {
			return fmt.Errorf("eval case %q lacks evidence", c.Name)
		}
	}
	return nil
}
