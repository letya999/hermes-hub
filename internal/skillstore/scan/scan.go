// Package scan is the deterministic skill-content scanner (SPEC-0083 R4.1/
// R4.2). Every tree that enters a skill store — published or drafted —
// passes Scan once; findings carry file/line/rule only, never content.
package scan

import (
	"bytes"
	"path"
	"regexp"
	"strings"
)

// Severity orders findings; Critical blocks publication outright.
type Severity string

const (
	Info     Severity = "info"
	Low      Severity = "low"
	Medium   Severity = "medium"
	High     Severity = "high"
	Critical Severity = "critical"
)

// Finding is one deterministic hit: file, line and rule id. No content is
// ever copied into a finding, so reports are safe to show approvers.
type Finding struct {
	RuleID   string   `json:"rule"`
	Severity Severity `json:"severity"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
}

// Input is one file to scan.
type Input struct {
	Path string
	Data []byte
	Exec bool
}

// Report is the bounded scan result for one tree.
type Report struct {
	Findings []Finding `json:"findings,omitempty"`
	Scanned  int       `json:"scanned"`
}

// Verdict is "critical" when any critical finding exists, else "pass".
func (r Report) Verdict() string {
	for _, f := range r.Findings {
		if f.Severity == Critical {
			return "critical"
		}
	}
	return "pass"
}

// lineRule matches a regular expression against each line of a file.
type lineRule struct {
	id       string
	severity Severity
	re       *regexp.Regexp
}

// rules apply to every text file in a tree.
var rules = []lineRule{
	// R4.1: download-and-execute pipelines.
	{"shell.download_exec", Critical, regexp.MustCompile(`(?i)\b(curl|wget)\b[^\n|]*\|\s*(sudo\s+)?(ba|z|da)?sh\b|base64\s+(-d|--decode)\b[^\n|]*\|\s*(ba)?sh\b|\beval\s+\$\(|\biex\b|Invoke-Expression|powershell[^\n]*\s-enc\b`)},
	// R4.1: hidden unicode — zero-width, bidi and BOM characters outside
	// file start.
	{"unicode.hidden", High, regexp.MustCompile(`[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{2064}\x{2066}-\x{2069}\x{FEFF}]`)},
	// R4.1: prompt-injection / instruction hijack, including payloads
	// hidden inside HTML or markdown comments.
	{"inject.hijack", Critical, regexp.MustCompile(`(?i)ignore\s+(all\s+|any\s+|the\s+)?(previous|prior|above)\s+instructions|do\s+not\s+(mention|tell)|don'?t\s+(mention|tell)|skip\s+(the\s+)?(approval|review)|bypass\s+(the\s+)?(approval|review)|new\s+instructions\s*:|system\s+prompt`)},
	// R4.1: literal secrets — PEM blocks, KEY=VALUE assignments with a
	// real-looking value, bearer tokens. Placeholders (<...>, ${...})
	// contain chars outside the value class, so docs stay clean.
	{"secret.pem", Critical, regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"secret.keyvalue", Critical, regexp.MustCompile(`(?i)\w*(api[_-]?key|secret|token)s?\w*\s*[:=]\s*["']?[A-Za-z0-9_/+\-.]{16,}`)},
	{"secret.bearer", Critical, regexp.MustCompile(`Bearer\s+[A-Za-z0-9_\-]{20,}`)},
	// R4.1: exfiltration hints, destructive shell, privilege escalation,
	// encoded payloads, hub-internal references, homoglyphs.
	{"exfil.webhook", High, regexp.MustCompile(`(?i)webhook\.site|/api/webhooks/`)},
	{"exfil.paths", High, regexp.MustCompile(`(?i)~/\.ssh|\.ssh/id_|\.env\b|runtime/credentials|runtime/broker|store\.enc`)},
	{"hub.internal", High, regexp.MustCompile(`(?i)HUB_TOOLHUB|\borg_actions\b`)},
	{"shell.destructive", Critical, regexp.MustCompile(`rm\s+-rf?\s+[/~*]|:\(\)\s*\{`)},
	{"shell.privilege", High, regexp.MustCompile(`\bsudo\b|chmod\s+\S*\+s|--privileged`)},
	{"enc.base64", Medium, regexp.MustCompile(`[A-Za-z0-9+/]{120,}={0,2}`)},
	{"enc.data_uri", High, regexp.MustCompile(`(?i)data:(text|image|application)/[a-z]+;base64`)},
	{"unicode.homoglyph", High, regexp.MustCompile(`[a-zA-Z][а-яёА-ЯЁ]|[а-яёА-ЯЁ][a-zA-Z]`)},
	// R7.6: Claude-style !`command` load-time execution markers. The hub
	// never executes them — the finding makes the foreign intent visible.
	{"load.exec", High, regexp.MustCompile("!`[^`\\n]+`")},
}

// shebangInterpreter returns the lowercased interpreter of a #! line:
// the basename of the program, or — for env shebangs — the first
// non-flag argument after env. Empty when the line is not a shebang.
func shebangInterpreter(line string) string {
	if !strings.HasPrefix(line, "#!") {
		return ""
	}
	fields := strings.Fields(line[2:])
	if len(fields) == 0 {
		return ""
	}
	interp := path.Base(fields[0])
	if interp == "env" {
		interp = ""
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "-") {
				interp = f
				break
			}
		}
	}
	return strings.ToLower(interp)
}

// scriptInterpreters is the R4.2 interpreter allowlist.
var scriptInterpreters = map[string]bool{
	"sh": true, "bash": true, "dash": true, "zsh": true,
	"python": true, "python3": true, "node": true, "perl": true, "ruby": true,
}

// Scan runs every rule over every input; findings come out in the
// deterministic order input -> line -> rule declaration.
func Scan(inputs []Input) Report {
	r := Report{Scanned: len(inputs)}
	for _, in := range inputs {
		lines := strings.Split(string(in.Data), "\n")
		for i, line := range lines {
			for _, rule := range rules {
				if rule.re.MatchString(line) {
					r.Findings = append(r.Findings, Finding{
						RuleID: rule.id, Severity: rule.severity,
						File: in.Path, Line: i + 1,
					})
				}
			}
		}
		if strings.HasPrefix(in.Path, "scripts/") && bytes.IndexByte(in.Data, 0) >= 0 {
			r.Findings = append(r.Findings, Finding{
				RuleID: "script.binary", Severity: Critical, File: in.Path,
			})
		}
		if strings.HasPrefix(in.Path, "scripts/") && len(lines) > 0 {
			interp := shebangInterpreter(lines[0])
			switch {
			case interp == "" && in.Exec:
				r.Findings = append(r.Findings, Finding{
					RuleID: "script.shebang", Severity: Medium,
					File: in.Path, Line: 1,
				})
			case interp != "" && !scriptInterpreters[interp]:
				r.Findings = append(r.Findings, Finding{
					RuleID: "script.interpreter", Severity: Critical,
					File: in.Path, Line: 1,
				})
			}
			for i, line := range lines {
				if scriptNetworkRE.MatchString(line) {
					r.Findings = append(r.Findings, Finding{
						RuleID: "script.network", Severity: Medium,
						File: in.Path, Line: i + 1,
					})
				}
			}
		}
	}
	return r
}

// scriptNetworkRE flags outbound-call APIs inside scripts/ (R4.2).
var scriptNetworkRE = regexp.MustCompile(`urllib`)
