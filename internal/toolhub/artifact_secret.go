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
			if pattern.Match(data) {
				// Report only the path and detector class; never include matched
				// bytes in an error, log or receipt.
				return fmt.Errorf("%w: secret-like content in artifact context file %q (detector %d)", ErrUnauthorized, h.Name, i)
			}
		}
	}
}
