package toolhub

import (
	"github.com/letya999/hermes-hub/internal/identity"
	"maps"
)

// LoadStaged is the protected connect path: verify the provider before publishing
// any binding. Save retains the loaded digest's stale-writer check, while Enable
// and Resolve cannot auto-persist/reload a half-finished account connection.
func LoadStaged(path string) (*Store, error) {
	s, err := Load(path)
	if err != nil {
		return nil, err
	}
	s.path = ""
	return s, nil
}

// OwnedConnection is the protected host lifecycle lookup. Model calls must
// instead use AuthorizeProjected and cannot supply a credential selector.
func (s *Store) OwnedConnection(auth identity.Envelope, id string) (Connection, CredentialReference, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return Connection{}, CredentialReference{}, ErrUnauthorized
	}
	if err := s.Reload(); err != nil {
		return Connection{}, CredentialReference{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.connections[id]
	if !ok || !c.Owner.Matches(auth) {
		return Connection{}, CredentialReference{}, ErrUnauthorized
	}
	r, ok := s.credentials[c.CredentialRefID]
	if !ok || r.ConnectionID != id {
		return Connection{}, CredentialReference{}, ErrInvalid
	}
	c.Metadata = maps.Clone(c.Metadata)
	r.Keys = append([]string(nil), r.Keys...)
	return c, r, nil
}
