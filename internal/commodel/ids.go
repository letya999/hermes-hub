package commodel

import "regexp"

// IDPattern bounds durable record ids (schedule ids, spool file names):
// filename-safe ASCII only — the charset is a model-level invariant.
var IDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ServiceNamePattern bounds the internal endpoint names a service probe may
// resolve (SPEC-0059 R5.5).
var ServiceNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
