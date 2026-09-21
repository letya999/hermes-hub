package toolhub

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"regexp"
)

var artifactSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{20,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{32,}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
}

// artifactSecretPrefixLen is the fixed literal prefix length per detector.
// Index 0 (PEM header) has no token body and is never fixture-exempt.
var artifactSecretPrefixLen = map[int]int{1: 4, 2: 11, 3: 4, 4: 5, 5: 3, 6: 4}

// fixtureSecretBody reports whether a matched token is a placeholder: its
// secret body (after the fixed prefix, ignoring separators) is one repeated
// character. Real credentials are never uniform, so this cannot mask a usable
// secret; upstream test fixtures like ghp_xxxx... stay buildable.
func fixtureSecretBody(match []byte, detector int) bool {
	prefix, ok := artifactSecretPrefixLen[detector]
	if !ok || len(match) <= prefix {
		return false
	}
	var seen byte
	count := 0
	for _, b := range match[prefix:] {
		if (b < '0' || b > '9') && (b < 'a' || b > 'z') && (b < 'A' || b > 'Z') {
			continue
		}
		if count == 0 {
			seen = b
		} else if b != seen {
			return false
		}
		count++
	}
	return count >= 8
}

// VerifyArtifactContextNoSecrets is a deliberately high-confidence content
// gate. It complements filename exclusion and the empty Docker client/env; it
// does not claim to recognize every provider's credential format.
func VerifyArtifactContextNoSecrets(contextBytes []byte) error {
	r := tar.NewReader(bytes.NewReader(contextBytes))
	for {
		h, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > 64<<20 {
			return fmt.Errorf("%w: malformed artifact context", ErrInvalid)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("%w: unreadable artifact context", ErrInvalid)
		}
		for i, pattern := range artifactSecretPatterns {
			for _, match := range pattern.FindAllIndex(data, -1) {
				if fixtureSecretBody(data[match[0]:match[1]], i) {
					continue
				}
				// Report only the path and detector class; never include matched
				// bytes in an error, log or receipt.
				return fmt.Errorf("%w: secret-like content in artifact context file %q (detector %d)", ErrUnauthorized, h.Name, i)
			}
		}
	}
}
