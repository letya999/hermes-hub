package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// R12.3: `hubctl skill eval` scores the stored skill's trigger suite
// through the configured eval endpoint and reports the result.
func TestSkillEvalRunsTriggersAgainstEndpoint(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "skills")
	st, err := skillstore.Open(store, skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	md := "---\nname: demo\ndescription: Use when testing.\n---\nbody\n"
	triggers := `{"split":0.5,"runs":1,"threshold":0.5,"queries":[{"q":"train","expect":true},{"q":"val","expect":true}]}`
	tree := skillstore.Tree{
		skillstore.SkillFile:  skillstore.File{Data: []byte(md)},
		"evals/triggers.json": skillstore.File{Data: []byte(triggers)},
	}
	if _, err := st.Publish("demo", tree, skillstore.IndexMeta{Author: "u", Source: "user"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tool_calls": []map[string]any{{"name": "skill", "args": map[string]any{"name": "demo"}}},
		})
	}))
	defer srv.Close()
	t.Setenv("HUB_SKILL_EVAL_URL", srv.URL)
	t.Chdir(dir)
	out, err := captureStdout(t, func() error {
		return runSkill(context.Background(), []string{"eval", "--dir", dir, "--name", "demo"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Skill    string `json:"skill"`
		Triggers struct {
			Status   string  `json:"status"`
			Accuracy float64 `json:"accuracy"`
			Pass     bool    `json:"pass"`
		} `json:"triggers"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("eval output %q: %v", out, err)
	}
	if rep.Skill != "demo" || rep.Triggers.Status != "done" || !rep.Triggers.Pass {
		t.Fatalf("report %+v", rep)
	}
	// R12.3: the report is also persisted under ignored .work/tmp/.
	matches, err := filepath.Glob(filepath.Join(dir, ".work", "tmp", "skill-eval-demo-*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no eval report written under .work/tmp: %v %v", matches, err)
	}
}

// An unpublished draft must be evaluable too — that is the point of the
// approval gate: check the skill before it goes live, not after.
func TestSkillEvalRunsAgainstDraft(t *testing.T) {
	dir := t.TempDir()
	st, err := skillstore.Open(filepath.Join(dir, "skills"), skillstore.ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	md := "---\nname: drafty\ndescription: Use when drafting.\n---\nbody\n"
	triggers := `{"split":0.5,"runs":1,"threshold":0.5,"queries":[{"q":"train","expect":true}]}`
	draft, err := st.CreateDraft("drafty", skillstore.Tree{
		skillstore.SkillFile:  skillstore.File{Data: []byte(md)},
		"evals/triggers.json": skillstore.File{Data: []byte(triggers)},
	}, skillstore.DraftMeta{Author: "agent", Reason: "eval me"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tool_calls": []map[string]any{{"name": "skill", "args": map[string]any{"name": "drafty"}}},
		})
	}))
	defer srv.Close()
	t.Setenv("HUB_SKILL_EVAL_URL", srv.URL)
	t.Chdir(dir)
	out, err := captureStdout(t, func() error {
		return runSkill(context.Background(), []string{"eval", "--dir", dir, "--id", draft.ID})
	})
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Skill    string `json:"skill"`
		Triggers struct {
			Pass bool `json:"pass"`
		} `json:"triggers"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("eval output %q: %v", out, err)
	}
	if rep.Skill != "drafty" || !rep.Triggers.Pass {
		t.Fatalf("draft eval report %+v", rep)
	}
	// The persisted report carries the draft's skill name, not an empty one.
	matches, err := filepath.Glob(filepath.Join(dir, ".work", "tmp", "skill-eval-drafty-*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no draft eval report under .work/tmp: %v %v", matches, err)
	}
}
