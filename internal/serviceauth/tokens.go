// Package serviceauth owns the inter-service authentication contract
// shared by the hub's control planes: the enrolled-tokens file that maps
// bearer tokens to identity envelopes. Authorization decisions stay local
// to each service — this package fixes only the file format and its
// validation so a malformed entry cannot silently differ between readers.
package serviceauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

// MaxTokensFile bounds the enrolled-tokens file: it is rewritten in place
// by enrollment and read on control-plane calls, so it stays small.
const MaxTokensFile = 1 << 20

// ErrInvalid marks a tokens file that cannot be trusted at all: readers
// must fail closed rather than skip the bad entry.
var ErrInvalid = errors.New("invalid tokens file")

// ParseTokenEnvelopes decodes {"<token>": <envelope>} and validates every
// entry — token shape and the envelope's own binding. Any invalid entry
// rejects the whole file: a partially trusted file would let a bad writer
// pick which readers see which tokens.
func ParseTokenEnvelopes(body []byte) (map[string]identity.Envelope, error) {
	var entries map[string]identity.Envelope
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	for token, env := range entries {
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return nil, fmt.Errorf("%w: token", ErrInvalid)
		}
		if err := env.Validate(env.PrincipalID, env.ContextID, env.RuntimeID, env.PolicyVersion); err != nil {
			return nil, fmt.Errorf("%w: identity: %v", ErrInvalid, err)
		}
	}
	return entries, nil
}

// ReadTokenEnvelopes loads and validates the file at path, bounded by
// MaxTokensFile.
func ReadTokenEnvelopes(path string) (map[string]identity.Envelope, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if info.Size() > MaxTokensFile {
		return nil, fmt.Errorf("%w: oversized", ErrInvalid)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return ParseTokenEnvelopes(body)
}
