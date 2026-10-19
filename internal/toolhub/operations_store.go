package toolhub

import (
	"fmt"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// PutOperation validates and durably stores one journal record.
func (s *Store) PutOperation(op api.Operation) error {
	if err := op.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.operations[op.OperationID] = op
	s.mu.Unlock()
	return s.persist()
}

// OperationFor returns one operation for its exact owner only.
func (s *Store) OperationFor(auth identity.Envelope, id string) (api.Operation, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return api.Operation{}, fmt.Errorf("%w: %v", api.ErrUnauthorized, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	op, ok := s.operations[id]
	if !ok {
		return api.Operation{}, fmt.Errorf("%w: operation", api.ErrNotFound)
	}
	if op.PrincipalID != auth.PrincipalID || op.ContextID != auth.ContextID || op.RuntimeID != auth.RuntimeID || op.PolicyVersion != auth.PolicyVersion {
		return api.Operation{}, fmt.Errorf("%w: operation owner", api.ErrUnauthorized)
	}
	return op, nil
}

// OperationForRequestKey resolves the live operation for one
// (principal, intent, normalized target) tuple. Superseded records are
// never candidates — a retry opens a fresh operation.
func (s *Store) OperationForRequestKey(auth identity.Envelope, intent, target string) (api.Operation, bool) {
	key := api.OperationRequestKey(auth, intent, target)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, op := range s.operations {
		if op.RequestKey != key || op.PrincipalID != auth.PrincipalID || op.SupersededBy != "" {
			continue
		}
		return op, true
	}
	return api.Operation{}, false
}
