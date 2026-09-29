package sshcap

import "strings"

// match reports whether s satisfies an allowlist pattern. '*' matches any
// (possibly empty) substring; all other characters are literal. A pattern is
// anchored at both ends unless it starts or ends with '*'.
func match(pattern, s string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}
	parts := strings.Split(pattern, "*")
	pos := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(s[pos:], part)
		if idx < 0 || (i == 0 && idx != 0) {
			return false
		}
		pos += idx + len(part)
	}
	if last := parts[len(parts)-1]; last != "" && !strings.HasSuffix(s, last) {
		return false
	}
	return true
}

func matchAny(patterns []string, s string) bool {
	for _, pattern := range patterns {
		if match(pattern, s) {
			return true
		}
	}
	return false
}
