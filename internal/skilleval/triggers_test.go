package skilleval

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFixtures(t *testing.T, dir, triggers, evals string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "evals"), 0755); err != nil {
		t.Fatal(err)
	}
	if triggers != "" {
		if err := os.WriteFile(filepath.Join(dir, "evals", "triggers.json"), []byte(triggers), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if evals != "" {
		if err := os.WriteFile(filepath.Join(dir, "evals", "evals.json"), []byte(evals), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// R12.2: a trigger suite validates shape and splits deterministically —
// the same queries always land on the same side of the 60/40 boundary.
func TestLoadTriggersValidatesAndSplits(t *testing.T) {
	dir := t.TempDir()
	qs := `{"split":0.6,"runs":3,"threshold":0.5,"queries":[`
	for i := 0; i < 20; i++ {
		if i > 0 {
			qs += ","
		}
		pos := "true"
		if i%2 == 1 {
			pos = "false"
		}
		qs += `{"q":"query ` + string(rune('a'+i)) + `","expect":` + pos + `}`
	}
	qs += `]}`
	writeFixtures(t, dir, qs, "")
	suite, err := LoadTriggers(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Queries) != 20 || suite.Runs != 3 || suite.Threshold != 0.5 {
		t.Fatalf("suite %+v", suite)
	}
	train1, val1 := suite.Partition()
	train2, val2 := suite.Partition()
	if len(train1)+len(val1) != 20 || len(train1) != 12 || len(val1) != 8 {
		t.Fatalf("split %d/%d must be 12/8", len(train1), len(val1))
	}
	for i := range train1 {
		if train1[i].Q != train2[i].Q {
			t.Fatal("split is not deterministic")
		}
	}
	for i := range val1 {
		if val1[i].Q != val2[i].Q {
			t.Fatal("validation side is not deterministic")
		}
	}
}

// R12.2: evals.json carries prompt, expected output, setup files and
// assertions; every case must name its grading evidence (A12).
func TestLoadEvalsRequiresEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFixtures(t, dir, "", `[
		{"name":"extract","prompt":"merge these","expected_output":"merged.pdf",
		 "files":{"in/a.pdf":"A"},"assertions":["exit==0"],"evidence":"transcript+files"},
		{"name":"no-evidence","prompt":"x","expected_output":"y","assertions":["z"]}
	]`)
	cases, err := LoadEvals(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 || cases[0].Name != "extract" || cases[0].Files["in/a.pdf"] != "A" {
		t.Fatalf("cases %+v", cases)
	}
	if cases[0].Evidence == "" {
		t.Fatal("evidence must be carried through")
	}
	// A case without evidence is rejected by validation.
	if err := cases.Validate(); err == nil {
		t.Fatal("evidence-less case passed validation")
	}
	if err := cases[:1].Validate(); err != nil {
		t.Fatalf("evidence-graded case must pass: %v", err)
	}
}
