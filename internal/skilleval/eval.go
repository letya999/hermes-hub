package skilleval

import (
	"context"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// RunnerFunc adapts a function to Runner (test seams and simple closures).
type RunnerFunc func(ctx context.Context, spaceDir, prompt string) (Receipt, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, spaceDir, prompt string) (Receipt, error) {
	return f(ctx, spaceDir, prompt)
}

// Report is the full eval outcome for one skill (R12.3).
type Report struct {
	Skill    string        `json:"skill"`
	Triggers TriggerReport `json:"triggers"`
	Evals    EvalsReport   `json:"evals"`
}

// EvalsReport summarizes the evals.json leg: cases execute their prompts
// but grading is evidence review (model or human) — reported honestly.
type EvalsReport struct {
	Status string `json:"status"` // ungraded | model_unavailable | absent
	Cases  int    `json:"cases"`
}

// Eval materializes an isolated temporary space holding only the named
// skill, then scores every leg through the injected runner.
func Eval(ctx context.Context, skillDir, name string, run Runner) (Report, error) {
	tree, err := skillTreeDir(skillDir)
	if err != nil {
		return Report{}, err
	}
	space, err := os.MkdirTemp("", "skill-eval-*")
	if err != nil {
		return Report{}, err
	}
	st, err := skillstore.Open(filepath.Join(space, "store"), skillstore.ScopeUser)
	if err != nil {
		return Report{}, err
	}
	if _, err := st.Publish(name, tree, skillstore.IndexMeta{Author: "eval", Source: "eval"}); err != nil {
		return Report{}, err
	}
	if _, err := skillstore.Build(filepath.Join(space, "effective"), []skillstore.Source{{Name: "user", Dir: st.Root()}}); err != nil {
		return Report{}, err
	}
	rep := Report{Skill: name}
	if suite, err := LoadTriggers(skillDir); err == nil {
		rep.Triggers = RunTriggers(ctx, suite, name, run, space)
	}
	if cases, err := LoadEvals(skillDir); err == nil && len(cases) > 0 {
		rep.Evals.Cases = len(cases)
		rep.Evals.Status = "ungraded"
	}
	return rep, nil
}

// skillTreeDir loads a skill checkout from disk as a Tree.
func skillTreeDir(dir string) (skillstore.Tree, error) {
	return skillstore.LoadTree(dir)
}
