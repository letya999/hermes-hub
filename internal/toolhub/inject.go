package toolhub

import "github.com/letya999/hermes-hub/internal/toolhub/api"

import ()

// ConnectionsForSecret returns active connections owned by owner that already
// point at locator or declare any of names. Callers must not hold Store.mu.
func (s *Store) ConnectionsForSecret(locator, owner string, names []string) []api.CredentialReference {
	if s == nil {
		return nil
	}
	named := map[string]bool{}
	for _, name := range names {
		named[name] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	out := make([]api.CredentialReference, 0)
	for _, cred := range s.credentials {
		if cred.Status != api.ActiveStatus || seen[cred.ConnectionID] {
			continue
		}
		connection, ok := s.connections[cred.ConnectionID]
		if !ok || connection.Owner.ID != owner {
			continue
		}
		match := locator != "" && cred.Locator == locator
		if !match {
			for _, key := range cred.Keys {
				if named[key] {
					match = true
					break
				}
			}
		}
		if !match {
			continue
		}
		seen[cred.ConnectionID] = true
		copyKeys := append([]string(nil), cred.Keys...)
		out = append(out, api.CredentialReference{CredentialRefID: cred.CredentialRefID, ConnectionID: cred.ConnectionID, Locator: cred.Locator, Keys: copyKeys, Revision: cred.Revision, Backend: cred.Backend, Status: cred.Status})
	}
	return out
}
