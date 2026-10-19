package commodel

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// IDPattern bounds durable record ids (schedule ids, spool file names):
// filename-safe ASCII only — the charset is a model-level invariant.
var IDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ServiceNamePattern bounds the internal endpoint names a service probe may
// resolve (SPEC-0059 R5.5).
var ServiceNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// FileID derives the durable filename form of a record id: clean ids pass
// through, anything else is replaced by a keyed hash so on-disk names stay
// filename-safe and collision-free.
func FileID(id string) string {
	if IDPattern.MatchString(id) {
		return id
	}
	hash := sha256.Sum256([]byte(id))
	return "id-" + hex.EncodeToString(hash[:])
}
