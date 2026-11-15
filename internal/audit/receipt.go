package audit

import (
	"strings"
	"unicode"
)

// ValidReceipt checks finite control metadata. Producers must still supply
// reviewed identifiers or counts, never arguments, output or credential maps.
func ValidReceipt(value string) bool {
	if len(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	lower := strings.ToLower(value)
	for _, needle := range []string{"http:", "https:", "data:", "file:", "javascript:", "bearer ", "bearer:", "authorization:", "access_token", "refresh_token", "{", "}", "[", "]"} {
		if strings.Contains(lower, needle) {
			return false
		}
	}
	return true
}
