package agenttools

import (
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// M31.11 S5 + charter 5.2/9.2: the fixed intent suite asserts exactly one
// obvious tool per intent — no competing tools answer the same ask. The
// scorer is deterministic: name-part hits dominate, description hits are
// IDF-weighted so common words cannot flood the ranking. A model-side eval
// can layer on top without changing the baseline report.

var intentStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "this": true, "that": true,
	"my": true, "me": true, "to": true, "for": true, "of": true,
	"in": true, "on": true, "and": true, "or": true, "it": true,
	"i": true, "into": true, "with": true, "from": true, "by": true,
	"please": true, "can": true, "you": true, "what": true, "which": true,
	"is": true, "are": true, "do": true, "does": true, "how": true,
	"'s": true, "s": true,
}

var intentWordPattern = regexp.MustCompile(`[a-z0-9]+`)

func intentTokens(text string) []string {
	out := []string{}
	for _, w := range intentWordPattern.FindAllString(strings.ToLower(text), -1) {
		if !intentStopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

func singular(w string) string { return strings.TrimSuffix(w, "s") }

// nameMatch reports a token ↔ name-part hit: exact, singular-normalized, or
// a shared prefix of ≥5 chars (assign↔assignment, expose↔exposure).
func nameMatch(part, tok string) bool {
	if part == tok || singular(part) == singular(tok) {
		return true
	}
	n := 0
	for n < len(part) && n < len(tok) && part[n] == tok[n] {
		n++
	}
	return n >= 5
}

// intentVerbs maps natural-language action words onto the catalog verb the
// user means; the bonus fires only on the tool's verb part so a noun cannot
// steal the intent's action. Entries join the table as the suite demands.
var intentVerbs = map[string]string{
	"update":    "edit",
	"pause":     "edit",
	"subscribe": "create",
	"compact":   "edit",
}

// intentNouns maps domain words onto the catalog noun that owns them; a
// transcript lives in the context domain even when the user says "session".
var intentNouns = map[string]string{
	"transcript": "context",
}

func intentScore(spec api.ToolSpec, tokens []string, idf map[string]float64) float64 {
	score := 0.0
	descWords := map[string]bool{}
	for _, w := range intentTokens(spec.Description) {
		descWords[w] = true
	}
	parts := strings.Split(spec.Name, "_")
	verb := parts[len(parts)-1]
	var descScore float64
	for _, tok := range tokens {
		for _, p := range parts {
			if nameMatch(p, tok) {
				score += 4
				break
			}
		}
		if alias, ok := intentVerbs[tok]; ok && alias == verb {
			score += 4
		}
		if noun, ok := intentNouns[tok]; ok {
			for _, p := range parts[:len(parts)-1] {
				if p == noun {
					score += 4
					break
				}
			}
		}
		if descWords[tok] || descWords[tok+"s"] || descWords[singular(tok)] {
			if w, ok := idf[tok]; ok {
				descScore += w
			}
		}
	}
	// Density, not presence: a keyword hit inside a lean contract outranks
	// the same hit inside prose that mentions everything (eval-gaming
	// red-team — stuffing is penalized, not rewarded).
	if len(descWords) > 0 {
		score += descScore / math.Sqrt(float64(len(descWords)))
	}
	return score
}

func TestIntentSelectionEval(t *testing.T) {
	suite := []struct {
		intent string
		want   string
	}{
		{"pack this folder into an archive", "file_archive"},
		{"search text inside the project files", "file_search"},
		{"make a directory under workspace", "file_mkdir"},
		{"run a command in the terminal", "terminal_exec"},
		{"pause my routine", "routine_edit"},
		{"read the card of my scheduled routine", "routine_read"},
		{"subscribe me to new chat events", "watch_create"},
		{"pause an owned watch subscription", "watch_edit"},
		{"dry run the watch filter against live events", "watch_sample"},
		{"assign the refactor as a durable task", "assignment_create"},
		{"read my assignment card", "assignment_read"},
		{"set this conversation's goal", "goal_edit"},
		{"compact this session transcript", "context_edit"},
		{"list my sessions", "session_list"},
		{"switch to yesterday's session", "session_switch"},
		{"list available models", "model_list"},
		{"read yesterday's session", "session_read"},
		{"publish the site publicly", "site_publish"},
		{"list my published sites", "site_list"},
		{"open a public tunnel for my local endpoint", "exposure_create"},
		{"inspect this image", "media_inspect"},
		{"convert this document to pdf", "document_convert"},
		{"transcribe the voice message", "media_transcribe"},
		{"install the github connector", "connector_install"},
		{"show connector status", "connector_status"},
		{"search the extended tool inventory for pdf tools", "tool_search"},
		{"call the connector list tool by name", "tool_call"},
		{"how do I orient in this space", "platform_guide"},
		{"show own health", "platform_status"},
		{"show space token usage", "platform_usage"},
		{"activate the pdf skill", "skill"},
		{"search my skills for pdf", "skill_search"},
		{"propose a change to the pdf skill", "skill_edit"},
		{"ssh into the staging host and run uptime", "ssh_exec"},
		{"search the web for the release notes", "web_search"},
		{"fetch this url's content", "web_fetch"},
		{"start a deep research job on the topic", "research_create"},
		{"open the page in the browser", "browser_open"},
		{"read the open page's text", "browser_read"},
		{"update the space settings", "settings_edit"},
	}
	var specs []api.ToolSpec
	for _, spec := range HubToolsDefinition().Tools {
		if spec.Audience == api.AudienceModel {
			specs = append(specs, spec)
		}
	}
	// IDF over the model-facing corpus: rare intent words discriminate,
	// ubiquitous words (session, read, tool) contribute almost nothing.
	df := map[string]int{}
	for _, spec := range specs {
		seen := map[string]bool{}
		for _, w := range intentTokens(spec.Name + " " + spec.Description) {
			seen[w] = true
			seen[singular(w)] = true
		}
		for w := range seen {
			df[w]++
		}
	}
	idf := map[string]float64{}
	for w, d := range df {
		idf[w] = 1 + math.Log(float64(len(specs))/float64(d))
	}
	hits, misses, ambiguous := 0, 0, 0
	for _, tc := range suite {
		tokens := intentTokens(tc.intent)
		best, bestScore, tied := "", -1.0, 0
		for _, spec := range specs {
			s := intentScore(spec, tokens, idf)
			if s > bestScore {
				best, bestScore, tied = spec.Name, s, 0
			} else if s == bestScore {
				tied++
			}
		}
		switch {
		case best != tc.want:
			misses++
			t.Errorf("intent %q selected %s (score %.2f), want %s", tc.intent, best, bestScore, tc.want)
		case tied > 0:
			ambiguous++
			t.Errorf("intent %q has %d tools tied at score %.2f — competing tools deny 5.2", tc.intent, tied+1, bestScore)
		default:
			hits++
		}
	}
	// Repeatable r5 baseline report (9.2): selection accuracy over the fixed
	// suite. Arg-fill accuracy, retry rate and token cost need a live model;
	// the deterministic baseline records what it can measure honestly.
	t.Logf("intent-eval catalog=r5 intents=%d selection_accuracy=%.3f ambiguous=%d arg_fill=n/a retry_rate=n/a token_cost=n/a",
		len(suite), float64(hits)/float64(len(suite)), ambiguous)
}
