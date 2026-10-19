package toolhub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// viewerLink is the durable one-time owner-viewer grant for a browser
// connection: nonce-bound, owner-bound, operation-bound, short-TTL, spent on
// first use. The nonce never leaves the store — only the channel notice
// carries it, so no tool result ever exposes link material.
type viewerLink struct {
	ConnectionID string    `json:"connection_id"`
	Nonce        string    `json:"nonce"`
	PrincipalID  string    `json:"principal_id"`
	OperationID  string    `json:"operation_id"`
	ExpiresAt    time.Time `json:"expires_at"`
	Used         bool      `json:"used,omitempty"`
}

// MintViewerLink arms a one-time viewer nonce on an owner-scoped browser
// connection. Only the connection's principal owner may mint — team members
// and other principals are denied; the link binds caller, connection and
// the operation that requested it. A second mint replaces the nonce.
func (s *Store) MintViewerLink(auth identity.Envelope, connectionID, operationID string, now time.Time, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 || ttl > time.Hour {
		return "", time.Time{}, fmt.Errorf("%w: viewer link ttl", api.ErrInvalid)
	}
	s.mu.RLock()
	connection, ok := s.connections[connectionID]
	definition, defErr := s.definitionByID(connection.DefinitionID)
	s.mu.RUnlock()
	if !ok {
		return "", time.Time{}, fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if defErr != nil || definition.Execution.EgressClass != api.EgressWeb {
		return "", time.Time{}, fmt.Errorf("%w: viewer links exist only for browser connections", api.ErrInvalid)
	}
	if connection.Status != api.ActiveStatus || connection.Owner.Type != api.PrincipalOwner || connection.Owner.ID != auth.PrincipalID {
		return "", time.Time{}, api.ErrUnauthorized
	}
	link := viewerLink{
		ConnectionID: connectionID,
		Nonce:        api.RandomNonce(),
		PrincipalID:  auth.PrincipalID,
		OperationID:  operationID,
		ExpiresAt:    now.Add(ttl),
	}
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if s.viewerLinks == nil {
		s.viewerLinks = map[string]viewerLink{}
	}
	if err := s.loadViewerLinksLocked(); err != nil {
		return "", time.Time{}, err
	}
	s.viewerLinks[connectionID] = link
	if err := s.persistViewerLinksLocked(); err != nil {
		delete(s.viewerLinks, connectionID)
		if path := s.viewerLinksPath(); path != "" {
			_ = os.Remove(path)
		}
		return "", time.Time{}, err
	}
	return link.Nonce, link.ExpiresAt, nil
}

// SpendViewerLink consumes the one-time nonce: a forged or absent nonce is
// unauthorized, a spent or expired one is gone, a live one marks itself used
// and returns the owning connection.
func (s *Store) SpendViewerLink(connectionID, nonce string, now time.Time) (api.Connection, error) {
	// Lock ordering is s.mu then leaseMu — never the reverse.
	s.mu.RLock()
	connection, found := s.connections[connectionID]
	s.mu.RUnlock()
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if err := s.loadViewerLinksLocked(); err != nil {
		return api.Connection{}, err
	}
	link, ok := s.viewerLinks[connectionID]
	if !ok || nonce == "" || nonce != link.Nonce {
		return api.Connection{}, api.ErrUnauthorized
	}
	if link.Used || !now.Before(link.ExpiresAt) {
		return api.Connection{}, api.ErrLinkGone
	}
	if !found || connection.Status != api.ActiveStatus {
		return api.Connection{}, api.ErrUnauthorized
	}
	link.Used = true
	s.viewerLinks[connectionID] = link
	if err := s.persistViewerLinksLocked(); err != nil {
		// Fail closed: the in-memory mark denies re-use while running, and the
		// stale ledger would leave the nonce spendable after a restart — drop
		// it so a failed spend can never replay (unspent links expire anyway).
		if path := s.viewerLinksPath(); path != "" {
			_ = os.Remove(path)
		}
		return api.Connection{}, err
	}
	return connection, nil
}

func (s *Store) viewerLinksPath() string {
	if s.path == "" {
		return ""
	}
	return s.path + ".viewer-links"
}

func (s *Store) loadViewerLinksLocked() error {
	if s.viewerLinksLoaded {
		return nil
	}
	s.viewerLinksLoaded = true
	path := s.viewerLinksPath()
	if path == "" {
		return nil
	}
	body, err := os.ReadFile(path) // #nosec G304 -- registry-adjacent link ledger under the same trusted path rules.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var links []viewerLink
	if err := json.Unmarshal(body, &links); err != nil {
		return fmt.Errorf("%w: viewer link ledger", api.ErrInvalid)
	}
	for _, link := range links {
		if link.ConnectionID == "" || link.Nonce == "" || link.PrincipalID == "" {
			return fmt.Errorf("%w: viewer link ledger record", api.ErrInvalid)
		}
		s.viewerLinks[link.ConnectionID] = link
	}
	return nil
}

func (s *Store) persistViewerLinksLocked() error {
	path := s.viewerLinksPath()
	if path == "" {
		return nil
	}
	if err := api.SafeStorePath(path); err != nil {
		return err
	}
	links := make([]viewerLink, 0, len(s.viewerLinks))
	for _, link := range s.viewerLinks {
		links = append(links, link)
	}
	slices.SortFunc(links, func(a, b viewerLink) int { return strings.Compare(a.ConnectionID, b.ConnectionID) })
	body, err := json.Marshal(links)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".viewer-links-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
