package scan

import "regexp"

// secretAssignRE matches KEY=VALUE assignments where the key names a
// secret; group 2 is the value to redact.
var secretAssignRE = regexp.MustCompile(`(?i)(\w*(?:api[_-]?key|secret|token|password|passwd|credential)s?\w*\s*[:=]\s*["']?)[A-Za-z0-9_/+\-.]{8,}`)

// RedactSecrets replaces literal secret values with <REDACTED> and
// returns the rewritten content with a redaction count (R4.7).
func RedactSecrets(data []byte) ([]byte, int) {
	n := 0
	out := secretAssignRE.ReplaceAllFunc(data, func(m []byte) []byte {
		n++
		return []byte(string(secretAssignRE.FindSubmatch(m)[1]) + "<REDACTED>")
	})
	return out, n
}
