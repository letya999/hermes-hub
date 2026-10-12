// Package e2e maintains sequential acceptance cases and separates live evidence
// from deterministic boundary tests. It never accesses an account implicitly.
package e2e

import (
	"bufio"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed registry.json
var registry []byte

type Case struct {
	ID, Surface, Name, Title, Action, Expected   string
	Package, Test, Prerequisites, Level, Cleanup string
}

type Result struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	Level          string  `json:"level"`
	Reason         string  `json:"reason,omitempty"`
	EvidenceSHA256 string  `json:"evidence_sha256,omitempty"`
	Seconds        float64 `json:"seconds"`
}

type Report struct {
	Schema  int       `json:"schema"`
	Mode    string    `json:"mode"`
	Started time.Time `json:"started"`
	Results []Result  `json:"results"`
}

// CoreCases follows the owner's scope: local tools and credential-free installs,
// excluding connector accounts rather than treating missing accounts as failures.
func CoreCases(cases []Case) []Case {
	excluded := map[string]bool{
		"hh_search": true, "hh_vacancy": true, "hh_resumes": true, "hh_apply": true,
		"x_search": true, "spotify": true, "homeassistant": true, "feishu_doc": true, "feishu_drive": true,
	}
	for _, name := range []string{"google", "github", "gitlab", "slack", "atlassian", "telegram_user", "desktop", "meet", "web_providers", "web_search_multi", "deep_research"} {
		excluded["integration/"+name] = true
	}
	selected := []Case{}
	for _, c := range cases {
		if excluded[c.Name] || strings.HasPrefix(c.Name, "ssh_") || excluded[c.Surface+"/"+c.Name] {
			continue
		}
		selected = append(selected, c)
	}
	return selected
}

func Registry() ([]Case, error) {
	var cases []Case
	if err := json.Unmarshal(registry, &cases); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if !regexp.MustCompile(`^E2E-[0-9]{3}$`).MatchString(c.ID) || seen[c.ID] || c.Action == "" || c.Expected == "" || c.Prerequisites == "" || c.Cleanup == "" {
			return nil, fmt.Errorf("incomplete or duplicate acceptance case %q", c.ID)
		}
		if (c.Package == "") != (c.Test == "") || (c.Package != "" && !regexp.MustCompile(`^[a-z]+$`).MatchString(c.Package)) || (c.Test != "" && !regexp.MustCompile(`^Test[A-Za-z0-9]+$`).MatchString(c.Test)) {
			return nil, fmt.Errorf("unsafe test selector in %s", c.ID)
		}
		seen[c.ID] = true
	}
	return cases, nil
}

// TestVerdict refuses false green from an unmatched selector or a skipped test.
func TestVerdict(data []byte, test string) (string, string) {
	scan := bufio.NewScanner(strings.NewReader(string(data)))
	scan.Buffer(make([]byte, 4096), 4<<20)
	started, ended, skipped := false, false, false
	status := "FAIL"
	for scan.Scan() {
		var event struct{ Action, Test string }
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			return "FAIL", "invalid test event stream"
		}
		if strings.HasPrefix(event.Test, test+"/") && event.Action == "skip" {
			skipped = true
		}
		if event.Test != test {
			continue
		}
		switch event.Action {
		case "run":
			started = true
		case "pass":
			status, ended = "PASS", true
		case "fail":
			status, ended = "FAIL", true
		case "skip":
			status, ended = "BLOCKED", true
		}
	}
	if scan.Err() != nil || !started || !ended {
		return "FAIL", "selected test did not run to completion"
	}
	if status == "BLOCKED" || skipped {
		return "BLOCKED", "selected test skipped a prerequisite"
	}
	return status, ""
}

type Execute func(context.Context, string, ...string) ([]byte, error)

func Command(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Mock checks run serially and share compiled race-enabled binaries, not results.
// Logs are intentionally omitted from reports: provider/test outputs may be private.
func Mock(ctx context.Context, cases []Case, execute Execute, progress io.Writer) (Report, error) {
	report := Report{Schema: 1, Mode: "mock", Started: time.Now().UTC()}
	dir, err := os.MkdirTemp("", "hermes-e2e-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(dir)
	binaries := map[string]string{}
	for _, c := range cases {
		started := time.Now()
		r := Result{ID: c.ID, Level: c.Level, Status: "BLOCKED", Reason: "no automated functional fixture for this capability"}
		if c.Test != "" {
			binary := binaries[c.Package]
			if binary == "" {
				binary = filepath.Join(dir, c.Package+".test.exe")
				_, err := execute(ctx, "go", "test", "-c", "-race", "-o", binary, "./internal/"+c.Package)
				if err != nil {
					return report, fmt.Errorf("compile %s test fixture: %w", c.Package, err)
				}
				binaries[c.Package] = binary
			}
			data, err := execute(ctx, "go", "tool", "test2json", "-t", binary, "-test.v=test2json", "-test.count=1", "-test.timeout=3m", "-test.run=^"+c.Test+"$")
			r.Status, r.Reason = TestVerdict(data, c.Test)
			if err != nil {
				r.Status, r.Reason = "FAIL", "test process failed; inspect the selected check separately"
			}
		}
		r.Seconds = time.Since(started).Seconds()
		report.Results = append(report.Results, r)
		fmt.Fprintf(progress, "%s %s (%s)\n", c.ID, r.Status, c.Level)
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
	}
	return report, nil
}

// Telegram is a guided real-bot mode. A human sends the specified action through
// the actual account and verifies the independent oracle. MANUAL_PASS never
// becomes automated PASS, even when an evidence file is provided.
func Telegram(cases []Case, input io.Reader, progress io.Writer, interactive bool) Report {
	report := Report{Schema: 1, Mode: "telegram", Started: time.Now().UTC()}
	scan := bufio.NewScanner(input)
	for _, c := range cases {
		r := Result{ID: c.ID, Status: "BLOCKED", Level: "live-telegram", Reason: "sender account/test chat and live observation required"}
		if interactive {
			fmt.Fprintf(progress, "\n%s %s\nPrerequisites: %s\nAction: %s\nExpect: %s\nCleanup: %s\nEnter FAIL, BLOCKED or MANUAL_PASS <private evidence file>: ", c.ID, c.Title, c.Prerequisites, c.Action, c.Expected, c.Cleanup)
			if scan.Scan() {
				r = ManualResult(c.ID, scan.Text())
			}
		}
		report.Results = append(report.Results, r)
	}
	return report
}

func ManualResult(id, line string) Result {
	r := Result{ID: id, Level: "live-telegram", Status: "BLOCKED", Reason: "no verified live evidence"}
	if line == "FAIL" {
		r.Status, r.Reason = "FAIL", "operator observed a discrepancy"
		return r
	}
	status, path, ok := strings.Cut(line, " ")
	if status != "MANUAL_PASS" || !ok {
		return r
	}
	f, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return r
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() == 0 || stat.Size() > 16<<20 {
		return r
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (16<<20)+1))
	if err != nil || n != stat.Size() {
		return r
	}
	r.Status, r.Reason = "MANUAL_PASS", "operator verification; not an automated assertion"
	r.EvidenceSHA256 = hex.EncodeToString(hash.Sum(nil))
	return r
}

func Complete(report Report) bool {
	if len(report.Results) == 0 {
		return false
	}
	for _, r := range report.Results {
		if r.Status != "PASS" && r.Status != "MANUAL_PASS" {
			return false
		}
	}
	return true
}
