package toolhub

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"sync"
)

// ReconnectController turns monotonic projection changes into at-most-once
// callbacks per revision. The callback is the transport-specific reconnect
// hook; this package never rebuilds Hermes homes or owns an MCP session.
type ReconnectController struct {
	Store    *Store
	Auth     identity.Envelope
	OnChange func(api.ProjectionChange) error
	mu       sync.Mutex
	last     uint64
}

func (c *ReconnectController) Reconcile() (api.ProjectionChange, bool, error) {
	if c == nil || c.Store == nil || c.OnChange == nil {
		return api.ProjectionChange{}, false, fmt.Errorf("%w: reconnect controller", api.ErrInvalid)
	}
	revision, err := c.Store.ProjectionRevision(c.Auth)
	if err != nil {
		return api.ProjectionChange{}, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if revision <= c.last {
		return api.ProjectionChange{}, false, nil
	}
	change := api.ProjectionChange{Revision: revision, Reason: "effective-binding-changed"}
	if err := c.OnChange(change); err != nil {
		return change, true, err
	}
	c.last = revision
	return change, true, nil
}
