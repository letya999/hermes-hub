package diagnostics

import (
	"regexp"
	"strings"
)

// Redact removes credential-shaped material from a log line. It runs at
// collection time so the combined diagnostics file never stores secrets in
// plaintext, and again at projection time as a second layer for files written
// before collection-time redaction existed.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)\b(token|api[-_]?key|api[-_]?secret|secret|password|passwd|authorization|cookie|set-cookie|session|credential|private[-_]?key)\b["'\s]*[:=]["'\s]*\S+`),
	regexp.MustCompile(`://[^/\s:@]+:[^/\s:@]+@`),                                          // user:pass@ URLs
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}\b`), // JWT
	regexp.MustCompile(`\b\d{8,12}:[A-Za-z0-9_-]{30,}\b`),                                  // Telegram bot token
	// Provider-prefixed keys. Real formats use both "_" and "-" separators
	// (xoxb-, sk-proj-, lin_api_) and some have no separator at all (ATATT).
	regexp.MustCompile(`\b(sk|pk|xox[a-z]|ghp|gho|glpat|hf|lin_api|npm|dop_v1)[-_.][A-Za-z0-9._-]{10,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{10,}\b`),
	regexp.MustCompile(`\bATATT[A-Za-z0-9]{20,}\b`),                      // Atlassian API token
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}\b`),                     // Google API key
	regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), // SendGrid
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                           // AWS access key
}

// looksLikeSecret requires length >= 32 plus upper, lower and digit classes so
// hex digests, container hashes and commit SHAs (all-lowercase) stay visible.
func looksLikeSecret(word string) bool {
	if len(word) < 32 {
		return false
	}
	var upper, lower, digit bool
	for _, c := range word {
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= '0' && c <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}

var highEntropyPattern = regexp.MustCompile(`\b[A-Za-z0-9+/_-]{32,}\b`)

func Redact(line string) string {
	for _, pattern := range secretPatterns {
		line = pattern.ReplaceAllStringFunc(line, func(match string) string {
			if idx := strings.IndexAny(match, "=:"); idx >= 0 && !strings.HasPrefix(match, "://") {
				if sep := strings.IndexAny(match[idx+1:], "\"'"); sep == 0 {
					return match[:idx+2] + "<redacted>"
				}
				return match[:idx+1] + "<redacted>"
			}
			return "<redacted>"
		})
	}
	return highEntropyPattern.ReplaceAllStringFunc(line, func(match string) string {
		if looksLikeSecret(match) {
			return "<redacted>"
		}
		return match
	})
}
