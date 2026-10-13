package e2e

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestCoreScopeKeepsLocalToolsAndCredentialFreeInstalls(t *testing.T) {
	cases, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, c := range CoreCases(cases) {
		found[c.ID] = true
	}
	for _, id := range []string{"E2E-001", "E2E-013", "E2E-047", "E2E-065", "E2E-066", "E2E-079", "E2E-081", "E2E-136", "E2E-137"} {
		if !found[id] {
			t.Fatalf("core tool %s excluded", id)
		}
	}
	for _, id := range []string{"E2E-032", "E2E-037", "E2E-110", "E2E-122", "E2E-125", "E2E-127"} {
		if found[id] {
			t.Fatalf("account-dependent connector %s remains mandatory", id)
		}
	}
}

func TestRegistryCoversReviewedToolInventory(t *testing.T) {
	cases, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, c := range cases {
		found[c.Surface+":"+c.Name] = true
	}
	for _, tool := range agenttools.HubToolsDefinition().Tools {
		if !found["hub:"+tool.Name] {
			t.Errorf("reviewed tool has no acceptance case: %s", tool.Name)
		}
	}
	for _, op := range append(append([]string{}, toolhub.ControlOperations...), "invoke") {
		if !found["toolhub:"+op] {
			t.Errorf("control operation has no acceptance case: %s", op)
		}
	}
	for _, section := range toolhub.SeedInventory() {
		if strings.HasPrefix(section.ID, "native:") && !found[section.ID] {
			t.Errorf("native capability has no acceptance case: %s", section.ID)
		}
	}
	// Selectors must name an actual test declaration, not a green empty run.
	for _, c := range cases {
		if c.Test == "" {
			continue
		}
		files, err := filepath.Glob(filepath.Join("..", c.Package, "*_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		declared := false
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			declared = declared || bytes.Contains(data, []byte("func "+c.Test+"("))
		}
		if !declared {
			t.Errorf("%s selects an absent test: %s", c.ID, c.Test)
		}
	}
}

func TestVerdictCannotPassUnmatchedSkippedOrTruncatedTests(t *testing.T) {
	for _, tc := range []struct{ events, status string }{
		{`{"Action":"run","Test":"TestA"}` + "\n" + `{"Action":"pass","Test":"TestA"}`, "PASS"},
		{`{"Action":"run","Test":"TestA"}` + "\n" + `{"Action":"fail","Test":"TestA"}`, "FAIL"},
		{`{"Action":"run","Test":"TestA"}` + "\n" + `{"Action":"skip","Test":"TestA"}`, "BLOCKED"},
		{`{"Action":"run","Test":"TestA"}` + "\n" + `{"Action":"skip","Test":"TestA/needs-account"}` + "\n" + `{"Action":"pass","Test":"TestA"}`, "BLOCKED"},
		{`{"Action":"pass","Test":"TestOther"}`, "FAIL"},
		{`{"Action":"run","Test":"TestA"}`, "FAIL"},
		{"invalid-json", "FAIL"},
	} {
		status, _ := TestVerdict([]byte(tc.events), "TestA")
		if status != tc.status {
			t.Fatalf("events=%s got=%s want=%s", tc.events, status, tc.status)
		}
	}
}

func TestMockPreservesOrderReusesOnlyBinaryAndPropagatesFailure(t *testing.T) {
	cases := []Case{{ID: "E2E-001", Package: "agenttools", Test: "TestA", Level: "contract"}, {ID: "E2E-002", Package: "agenttools", Test: "TestB", Level: "contract"}, {ID: "E2E-003", Level: "agent-e2e"}}
	compiled, selected := 0, []string{}
	execute := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "go" {
			t.Fatal(name)
		}
		if args[0] == "test" {
			compiled++
			return nil, nil
		}
		sel := args[len(args)-1]
		selected = append(selected, sel)
		if !strings.Contains(strings.Join(args, " "), "-test.count=1") {
			t.Fatal("cached result allowed")
		}
		test, action := "TestA", "pass"
		if strings.Contains(sel, "TestB") {
			test, action = "TestB", "fail"
		}
		body := []byte("{\"Action\":\"run\",\"Test\":\"" + test + "\"}\n{\"Action\":\"" + action + "\",\"Test\":\"" + test + "\"}\n")
		if action == "fail" {
			return body, errors.New("exit 1")
		}
		return body, nil
	}
	report, err := Mock(context.Background(), cases, execute, &bytes.Buffer{})
	if err != nil || compiled != 1 || strings.Join(selected, ",") != "-test.run=^TestA$,-test.run=^TestB$" {
		t.Fatal(report, compiled, selected, err)
	}
	if len(report.Results) != 3 || report.Results[0].Status != "PASS" || report.Results[1].Status != "FAIL" || report.Results[2].Status != "BLOCKED" || Complete(report) {
		t.Fatal(report)
	}
	_, err = Mock(context.Background(), cases, func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("compiler failed") }, &bytes.Buffer{})
	if err == nil {
		t.Fatal("compile failure ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Mock(ctx, []Case{{ID: "E2E-001"}}, execute, &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTelegramNeverPromotesManualOrMockEvidence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "receipt.txt")
	if err := os.WriteFile(file, []byte("synthetic operator receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"PASS", "MANUAL_PASS", "MANUAL_PASS missing", "MANUAL_PASS " + t.TempDir(), "BLOCKED"} {
		if got := ManualResult("E2E-001", line); got.Status != "BLOCKED" {
			t.Fatal(got)
		}
	}
	r := ManualResult("E2E-001", "MANUAL_PASS "+file)
	if r.Status != "MANUAL_PASS" || len(r.EvidenceSHA256) != 64 {
		t.Fatal(r)
	}
	if got := ManualResult("E2E-001", "FAIL"); got.Status != "FAIL" {
		t.Fatal(got)
	}
	cases := []Case{{ID: "E2E-001"}, {ID: "E2E-002"}}
	blocked := Telegram(cases, strings.NewReader("MANUAL_PASS "+file), &bytes.Buffer{}, false)
	if Complete(blocked) || blocked.Results[0].Status != "BLOCKED" {
		t.Fatal(blocked)
	}
	live := Telegram(cases, strings.NewReader("MANUAL_PASS "+file+"\nFAIL\n"), &bytes.Buffer{}, true)
	if live.Results[0].Status != "MANUAL_PASS" || live.Results[1].Status != "FAIL" || Complete(live) {
		t.Fatal(live)
	}
	if !Complete(Report{Results: []Result{r}}) || Complete(Report{}) {
		t.Fatal("completion check")
	}
}
