package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/skillstore"
)

// The gateway reads its owner skill store path from the environment so
// skill-scoped credential forms can persist under .state (R8.2).
func TestConfigFromEnvSkillsStore(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11")
	t.Setenv("HUB_SKILLS_EFFECTIVE", "/skills-effective")
	t.Setenv("HUB_SKILLS_STORE", "/skills-store")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.SkillsDir != "/skills-effective" || config.SkillsStore != "/skills-store" {
		t.Fatalf("skill dirs not wired: %+v", config)
	}
}

// writeEffectiveSkill stages one skill the way the materialized read-only
// view presents it: <dir>/<name>/SKILL.md. sealEffectiveIndex then writes the
// matching index.json; eligible maps name → eligibility (missing map key
// means eligible).
func writeEffectiveSkill(t *testing.T, dir, name, desc, body, extraFM string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n" + extraFM + "---\n" + body
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(md), 0644); err != nil {
		t.Fatal(err)
	}
}

func sealEffectiveIndex(t *testing.T, dir string, ineligible map[string]bool) {
	t.Helper()
	type entry struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Source      string   `json:"source"`
		Revision    string   `json:"revision"`
		Hidden      bool     `json:"hidden,omitempty"`
		Eligible    bool     `json:"eligible"`
		Missing     []string `json:"missing,omitempty"`
	}
	idx := struct {
		Schema   int     `json:"schema"`
		Revision string  `json:"revision"`
		Skills   []entry `json:"skills"`
	}{Schema: 1, Revision: "rev-1"}
	dirs, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, d.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		hidden := strings.Contains(string(raw), "disable-model-invocation: true")
		ok := !ineligible[d.Name()]
		missing := []string(nil)
		if !ok {
			missing = []string{"connector:absent"}
		}
		idx.Skills = append(idx.Skills, entry{
			Name: d.Name(), Description: "d", Source: "user", Revision: "r-" + d.Name(),
			Hidden: hidden, Eligible: ok, Missing: missing,
		})
	}
	body, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
}

// R8.2: `service: "skill:<name>"` scopes the protected credential form to
// the skill's declared credentials — values persist into the owner store's
// .state/credentials, bound to (principal, skill); undeclared keys reject.
func TestSkillCredentialFormFlow(t *testing.T) {
	c := testConfig(t)
	skillsDir := t.TempDir()
	writeEffectiveSkill(t, skillsDir, "demo", "d", "b", "")
	body := `{"schema":1,"revision":"r1","skills":[{"name":"demo","description":"d","source":"user","revision":"r","eligible":true,"requires":{"credentials":[{"name":"API_KEY"}]}}]}`
	if err := os.WriteFile(filepath.Join(skillsDir, "index.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	c.SkillsDir = skillsDir
	c.SkillsStore = t.TempDir()
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	keys, err := g.formKeys(user, "skill:demo", nil)
	if err != nil || len(keys) != 1 || keys[0] != "API_KEY" {
		t.Fatalf("declared keys=%v err=%v", keys, err)
	}
	if _, err := g.formKeys(user, "skill:demo", []string{"UNDECLARED"}); err == nil {
		t.Fatal("undeclared credential name accepted")
	}
	if _, err := g.formKeys(user, "skill:absent", nil); err == nil {
		t.Fatal("unknown skill accepted")
	}
	form := credentialForm{OwnerID: "alice", Skill: "demo", Keys: []string{"API_KEY"}}
	request := httptest.NewRequest(http.MethodPost, "/credentials/x", nil)
	if err := g.persistCredentialForm(request, form, map[string]string{"API_KEY": "s3cr3t"}); err != nil {
		t.Fatal(err)
	}
	got, err := skillstore.ReadCredentials(c.SkillsStore, "demo")
	if err != nil || got["API_KEY"] != "s3cr3t" {
		t.Fatalf("stored credentials: %v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(c.Users[0].StateDir, "self-env.json")); err == nil {
		t.Fatal("skill credentials must never land in the runtime env store")
	}
}

// The skill-scoped form must carry the skill name through to persistence —
// a form minted for skill:demo persists under .state/credentials/demo.json,
// not the runtime env store.
func TestSkillCredentialFormCarriesSkillScope(t *testing.T) {
	c := testConfig(t)
	c.FormOrigin = "http://127.0.0.1:8081"
	skillsDir := t.TempDir()
	writeEffectiveSkill(t, skillsDir, "demo", "d", "b", "")
	body := `{"schema":1,"revision":"r1","skills":[{"name":"demo","description":"d","source":"user","revision":"r","eligible":true,"requires":{"credentials":[{"name":"API_KEY"}]}}]}`
	if err := os.WriteFile(filepath.Join(skillsDir, "index.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	c.SkillsDir = skillsDir
	c.SkillsStore = t.TempDir()
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	form, err := g.createCredentialForm(identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), []string{"API_KEY"}, "demo")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(form.FormURL)
	g.forms.mu.Lock()
	var stored credentialForm
	for _, f := range g.forms.m {
		stored = f
	}
	g.forms.mu.Unlock()
	if stored.Skill != "demo" {
		t.Fatalf("form lost skill scope: %+v", stored)
	}
	// The served POST persists to .state — prove the loop end to end.
	saved := httptest.NewRequest(http.MethodPost, parsed.RequestURI(), strings.NewReader("API_KEY=s3cr3t&nonce="+parsed.Query().Get("nonce")))
	saved.Host = "127.0.0.1:8081"
	saved.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, saved)
	if rec.Code != http.StatusOK {
		t.Fatalf("form save status=%d", rec.Code)
	}
	got, err := skillstore.ReadCredentials(c.SkillsStore, "demo")
	if err != nil || got["API_KEY"] != "s3cr3t" {
		t.Fatalf("end-to-end persist: %v %v", got, err)
	}
}

// `/name args` resolves harness-side: the skill body reaches the job wrapped
// in <skill_content> with $ARGUMENTS/$0..$n substituted (R6.5).
func TestSlashSkillExpandsIntoJob(t *testing.T) {
	c := testConfig(t)
	skillsDir := t.TempDir()
	writeEffectiveSkill(t, skillsDir, "deploy-app", "deploys", "Deploy $0 to $1. All args: $ARGUMENTS\n", "")
	sealEffectiveIndex(t, skillsDir, nil)
	c.SkillsDir = skillsDir
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	g.now = func() time.Time { return time.Unix(100, 0) }
	upd := Update{UpdateID: 1, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "/deploy-app staging blue"}}
	if err := g.handleUpdate(context.Background(), upd); err != nil {
		t.Fatal(err)
	}
	job, _ := g.spool.ClaimJob()
	if job == nil {
		t.Fatal("skill command must enqueue a job")
	}
	for _, want := range []string{"<skill_content", "deploy-app", "Deploy staging to blue", "All args: staging blue", "Re-load this skill with the skill tool"} {
		if !strings.Contains(job.Text, want) {
			t.Fatalf("expanded job missing %q: %q", want, job.Text)
		}
	}
	if strings.Contains(job.Text, "name: deploy-app") || strings.Contains(job.Text, "description:") {
		t.Fatalf("frontmatter must not leak into activation: %q", job.Text)
	}
}

// Hidden and ineligible skills never expand through the harness: `/name`
// produces a visible error instead of a job (R6.5).
func TestSlashSkillHiddenAndIneligibleGetVisibleError(t *testing.T) {
	c := testConfig(t)
	skillsDir := t.TempDir()
	writeEffectiveSkill(t, skillsDir, "quiet-one", "hidden", "body\n", "disable-model-invocation: true\n")
	writeEffectiveSkill(t, skillsDir, "needy", "dep", "body\n", "")
	sealEffectiveIndex(t, skillsDir, map[string]bool{"needy": true})
	c.SkillsDir = skillsDir
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	g.now = func() time.Time { return time.Unix(100, 0) }
	for i, text := range []string{"/quiet-one", "/needy"} {
		upd := Update{UpdateID: i + 1, Message: &Message{MessageID: i + 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: text}}
		if err := g.handleUpdate(context.Background(), upd); err != nil {
			t.Fatal(err)
		}
	}
	for range 4 {
		g.deliverOne(context.Background())
	}
	if job, _ := g.spool.ClaimJob(); job != nil {
		t.Fatalf("hidden/ineligible skill must not enqueue work: %q", job.Text)
	}
	joined := strings.Join(fake.sent, "\n")
	if !strings.Contains(joined, "quiet-one") || !strings.Contains(joined, "needy") {
		t.Fatalf("each bad reference needs a visible error naming the skill: %q", joined)
	}
}

// `$name` references inside free text expand the same way — at most once per
// session, stopping at the first token that names no skill (R6.5).
func TestDollarSkillRefsExpandOnceAndStopAtNonSkill(t *testing.T) {
	c := testConfig(t)
	skillsDir := t.TempDir()
	writeEffectiveSkill(t, skillsDir, "alpha", "first", "ALPHA BODY\n", "")
	writeEffectiveSkill(t, skillsDir, "beta", "second", "BETA BODY\n", "")
	sealEffectiveIndex(t, skillsDir, nil)
	c.SkillsDir = skillsDir
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	g.now = func() time.Time { return time.Unix(100, 0) }
	send := func(id int, text string) {
		upd := Update{UpdateID: id, Message: &Message{MessageID: id, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: text}}
		if err := g.handleUpdate(context.Background(), upd); err != nil {
			t.Fatal(err)
		}
	}
	send(1, "please use $alpha for this")
	job, _ := g.spool.ClaimJob()
	if job == nil || !strings.Contains(job.Text, "ALPHA BODY") || !strings.Contains(job.Text, "please use $alpha") {
		t.Fatalf("$name must expand alongside the user text: %+v", job)
	}
	_ = g.spool.CompleteJob(job.ID)

	// Same skill referenced again in a later message: already active, no
	// second body dump.
	send(2, "and again $alpha please")
	job, _ = g.spool.ClaimJob()
	if job == nil {
		t.Fatal("message still enqueues")
	}
	if strings.Contains(job.Text, "ALPHA BODY") || strings.Contains(job.Text, "<skill_content") {
		t.Fatalf("session dedupe must skip re-activation: %q", job.Text)
	}
	_ = g.spool.CompleteJob(job.ID)

	// A token naming no skill stops the scan — $beta must not expand.
	send(3, "env $HOME path and $beta")
	job, _ = g.spool.ClaimJob()
	if job == nil {
		t.Fatal("message still enqueues")
	}
	if strings.Contains(job.Text, "BETA BODY") {
		t.Fatalf("stop at first non-skill: %q", job.Text)
	}
}

// A catalog publish mid-session is delivered as a single note on the next
// job — the session prefix stays byte-stable otherwise (R6.3).
func TestSessionPinnedCatalogNotesChangeOnce(t *testing.T) {
	c := testConfig(t)
	skillsDir := t.TempDir()
	writeEffectiveSkill(t, skillsDir, "alpha", "first", "ALPHA BODY\n", "")
	sealEffectiveIndex(t, skillsDir, nil)
	c.SkillsDir = skillsDir
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	g.now = func() time.Time { return time.Unix(100, 0) }
	send := func(id int, text string) *Job {
		upd := Update{UpdateID: id, Message: &Message{MessageID: id, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: text}}
		if err := g.handleUpdate(context.Background(), upd); err != nil {
			t.Fatal(err)
		}
		job, _ := g.spool.ClaimJob()
		if job == nil {
			t.Fatal("message must enqueue")
		}
		return job
	}
	first := send(1, "hi")
	if strings.Contains(first.Text, "skills_catalog_updated") {
		t.Fatalf("first message pins silently: %q", first.Text)
	}
	_ = g.spool.CompleteJob(first.ID)

	// Simulate a publish: index revision moves.
	idxBody, _ := os.ReadFile(filepath.Join(skillsDir, "index.json"))
	var idx map[string]any
	if err := json.Unmarshal(idxBody, &idx); err != nil {
		t.Fatal(err)
	}
	idx["revision"] = "rev-2"
	body, _ := json.Marshal(idx)
	if err := os.WriteFile(filepath.Join(skillsDir, "index.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	second := send(2, "another message")
	if !strings.Contains(second.Text, "skills_catalog_updated") {
		t.Fatalf("changed revision must arrive as one note: %q", second.Text)
	}
	_ = g.spool.CompleteJob(second.ID)

	third := send(3, "and another")
	if strings.Contains(third.Text, "skills_catalog_updated") {
		t.Fatalf("the note fires once per change: %q", third.Text)
	}
}

// `/name` joins the same per-session dedupe: a second explicit invocation
// gets a visible "already active" reply instead of a duplicated body (R6.5).
func TestSlashSkillDedupesWithinSession(t *testing.T) {
	c := testConfig(t)
	skillsDir := t.TempDir()
	writeEffectiveSkill(t, skillsDir, "alpha", "first", "ALPHA BODY\n", "")
	sealEffectiveIndex(t, skillsDir, nil)
	c.SkillsDir = skillsDir
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	g.now = func() time.Time { return time.Unix(100, 0) }
	send := func(id int, text string) {
		upd := Update{UpdateID: id, Message: &Message{MessageID: id, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: text}}
		if err := g.handleUpdate(context.Background(), upd); err != nil {
			t.Fatal(err)
		}
	}
	send(1, "/alpha")
	job, _ := g.spool.ClaimJob()
	if job == nil || !strings.Contains(job.Text, "ALPHA BODY") {
		t.Fatalf("first /alpha must activate: %+v", job)
	}
	_ = g.spool.CompleteJob(job.ID)
	send(2, "/alpha")
	for range 4 {
		g.deliverOne(context.Background())
	}
	if job, _ := g.spool.ClaimJob(); job != nil && strings.Contains(job.Text, "ALPHA BODY") {
		t.Fatalf("re-invocation must not re-dump the body: %q", job.Text)
	}
	if !strings.Contains(strings.Join(fake.sent, "\n"), "alpha") {
		t.Fatal("deduped /name needs a visible reply")
	}
}

// `$name` expansion is bounded: a message carrying more than five skill
// references expands only the first five (R6.5).
func TestDollarSkillRefsCapAtFive(t *testing.T) {
	c := testConfig(t)
	skillsDir := t.TempDir()
	var names []string
	for i := 0; i < 7; i++ {
		name := "sk" + string(rune('a'+i))
		names = append(names, name)
		writeEffectiveSkill(t, skillsDir, name, "d", "BODY-"+name+"\n", "")
	}
	sealEffectiveIndex(t, skillsDir, nil)
	c.SkillsDir = skillsDir
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	g.now = func() time.Time { return time.Unix(100, 0) }
	refs := ""
	for _, n := range names {
		refs += "$" + n + " "
	}
	upd := Update{UpdateID: 1, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: refs}}
	if err := g.handleUpdate(context.Background(), upd); err != nil {
		t.Fatal(err)
	}
	job, _ := g.spool.ClaimJob()
	if job == nil {
		t.Fatal("message must enqueue")
	}
	got := strings.Count(job.Text, "<skill_content")
	if got != 5 {
		t.Fatalf("expansion caps at 5, got %d: %q", got, job.Text)
	}
}

// A `$name` token hitting a hidden or ineligible skill surfaces a visible
// unavailability marker in the job text instead of silently dropping (R6.5).
func TestDollarSkillRefsFlagUnavailable(t *testing.T) {
	c := testConfig(t)
	skillsDir := t.TempDir()
	writeEffectiveSkill(t, skillsDir, "quiet", "hidden", "body\n", "disable-model-invocation: true\n")
	sealEffectiveIndex(t, skillsDir, nil)
	c.SkillsDir = skillsDir
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	g.now = func() time.Time { return time.Unix(100, 0) }
	upd := Update{UpdateID: 1, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "run $quiet please"}}
	if err := g.handleUpdate(context.Background(), upd); err != nil {
		t.Fatal(err)
	}
	job, _ := g.spool.ClaimJob()
	if job == nil {
		t.Fatal("message must enqueue")
	}
	if !strings.Contains(job.Text, "quiet") || !strings.Contains(strings.ToLower(job.Text), "unavailable") {
		t.Fatalf("hidden $ref needs a visible marker: %q", job.Text)
	}
}

// `$$name` is a literal escape — the scan skips the doubled marker instead
// of re-entering at the second dollar (R6.5 red-team).
func TestDollarSkillRefsDoubleDollarStaysLiteral(t *testing.T) {
	c := testConfig(t)
	skillsDir := t.TempDir()
	writeEffectiveSkill(t, skillsDir, "alpha", "first", "ALPHA BODY\n", "")
	sealEffectiveIndex(t, skillsDir, nil)
	c.SkillsDir = skillsDir
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	g.now = func() time.Time { return time.Unix(100, 0) }
	upd := Update{UpdateID: 1, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "write $$alpha literally"}}
	if err := g.handleUpdate(context.Background(), upd); err != nil {
		t.Fatal(err)
	}
	job, _ := g.spool.ClaimJob()
	if job == nil {
		t.Fatal("message must enqueue")
	}
	if strings.Contains(job.Text, "ALPHA BODY") {
		t.Fatalf("$$ escape must not expand: %q", job.Text)
	}
}

// The gateway learns the mounted effective view from HUB_SKILLS_EFFECTIVE —
// the same env the runtime and ToolHub containers use.
func TestConfigFromEnvSkillsDir(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11")
	t.Setenv("HUB_SKILLS_EFFECTIVE", "/skills-effective")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.SkillsDir != "/skills-effective" {
		t.Fatalf("HUB_SKILLS_EFFECTIVE not mapped: %q", config.SkillsDir)
	}
}
