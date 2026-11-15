package api

import "errors"

// Only trusted backend code may mark a failure before the business tools/call
// was sent. A provider error string or post-dispatch HTTP status is not proof.
var ErrBeforeEffect = errors.New("business tool call was not dispatched")
var ErrAuthBeforeEffect = errors.New("authentication rejected before business tool dispatch")
