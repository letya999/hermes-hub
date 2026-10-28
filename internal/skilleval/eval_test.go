package skilleval

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// R12.3: Eval materializes an isolated temp space containing only the
// evaluated skill, runs every leg through the injected runner and reports
// each leg's status — model_unavailable is an honest outcome, not a fail.
func TestEvalPreparesIsolatedSpace(t *testing.T) {
	skillDir := t.TempDir()
	md := "---\nname: demo\ndescription: Use when testing.\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(md), 0644); err != nil {
		t.Fatal(err)
	}
	writeFixtures(t, skillDir, `{"split":0.5,"runs":1,"threshold":0.5,"queries":[
		{"q":"train","expect":true},{"q":"val","expect":true}
	]}`, "")
	var spaceDir string
	runner := &fakeRunner{activated: map[int]bool{0: true}}
	probe := RunnerFunc(func(ctx context.Context, dir, prompt string) (Receipt, error) {
		spaceDir = dir
		return runner.Run(ctx, dir, prompt)
	})
	rep, err := Eval(context.Background(), skillDir, "demo", probe)
	if err != nil {
		t.Fatal(err)
	}
	if spaceDir == "" {
		t.Fatal("runner never saw the eval space")
	}
	// The prepared space holds an effective index listing only this skill.
	idxBody, err := os.ReadFile(filepath.Join(spaceDir, "effective", "index.json"))
	if err != nil {
		t.Fatalf("effective index missing in %s: %v", spaceDir, err)
	}
	var idx struct {
		Skills []struct {
			Name string `json:"name"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(idxBody, &idx); err != nil || len(idx.Skills) != 1 || idx.Skills[0].Name != "demo" {
		t.Fatalf("isolated index %+v err %v", idx.Skills, err)
	}
	if rep.Triggers.Status != "done" || !rep.Triggers.Pass {
		t.Fatalf("trigger leg %+v", rep.Triggers)
	}
}

// R12.2/R12.3: evals.json cases run their prompts in the prepared space;
// grading is evidence work, not a machine guess — a working runner yields
// ungraded receipts, a dead one yields model_unavailable.
func TestEvalRunsEvalCasesUngraded(t *testing.T) {
	skillDir := t.TempDir()
	md := "---\nname: demo\ndescription: Use when testing.\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(md), 0644); err != nil {
		t.Fatal(err)
	}
	writeFixtures(t, skillDir, "", `[
		{"name":"extract","prompt":"merge these","expected_output":"ok","evidence":"transcript"}
	]`)
	rep, err := Eval(context.Background(), skillDir, "demo", &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Evals.Status != "ungraded" || rep.Evals.Cases != 1 {
		t.Fatalf("evals leg %+v", rep.Evals)
	}
}
