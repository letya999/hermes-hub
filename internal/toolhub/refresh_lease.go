package toolhub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Refresh leases deliberately never touch s.mu: a refresh can run inside the
// admission callback, which already holds the registry read lock — taking
// s.mu there self-deadlocks. leaseMu guards refreshLeases, the connStatus
// mirror and the durable lease file instead.
func refreshLeaseKey(connectionID, provider string) string {
	return connectionID + "\x00" + provider
}

// connectionStatusFor returns the mirrored connection status without taking
// the registry lock. The mirror is fed by every connection-mutation site.
func (s *Store) connectionStatusFor(connectionID string) (api.Status, bool) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	status, ok := s.connStatus[connectionID]
	return status, ok
}

// noteConnectionStatusLocked mirrors a connection's status for the lease
// path. Callers hold s.mu; the lease mutex is taken briefly inside — the
// ordering s.mu → leaseMu is the only direction ever taken.
func (s *Store) noteConnectionStatusLocked(connection api.Connection) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	s.connStatus[connection.ConnectionID] = connection.Status
}

func (s *Store) forgetConnectionStatusLocked(connectionID string) {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	delete(s.connStatus, connectionID)
}

// AcquireRefreshLease takes the durable (connection, provider) refresh
// claim. An unexpired record returns acquired=false so a concurrent caller
// joins on the holder's result instead of double-spending a rotating
// refresh token. A non-active connection can never take the lease — revoke
// wins over in-flight refresh.
func (s *Store) AcquireRefreshLease(connectionID, provider string, ttl time.Duration, now time.Time) (api.RefreshLease, bool, error) {
	if !identity.ValidID(connectionID) || provider == "" || ttl <= 0 {
		return api.RefreshLease{}, false, fmt.Errorf("%w: refresh lease", api.ErrInvalid)
	}
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	unlock, err := s.lockLeaseFile()
	if err != nil {
		return api.RefreshLease{}, false, err
	}
	defer unlock()
	if err := s.loadLeasesLocked(); err != nil {
		return api.RefreshLease{}, false, err
	}
	status, ok := s.connStatus[connectionID]
	if !ok {
		return api.RefreshLease{}, false, fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if status != api.ActiveStatus {
		return api.RefreshLease{}, false, fmt.Errorf("%w: connection is not active", api.ErrRevoked)
	}
	key := refreshLeaseKey(connectionID, provider)
	if lease, held := s.refreshLeases[key]; held && now.Before(lease.ExpiresAt) {
		return lease, false, nil
	}
	holder := make([]byte, 16)
	if _, err := rand.Read(holder); err != nil {
		return api.RefreshLease{}, false, err
	}
	lease := api.RefreshLease{ConnectionID: connectionID, Provider: provider, Holder: hex.EncodeToString(holder), ExpiresAt: now.Add(ttl)}
	s.refreshLeases[key] = lease
	if err := s.persistLeasesLocked(); err != nil {
		delete(s.refreshLeases, key)
		return api.RefreshLease{}, false, err
	}
	return lease, true, nil
}

// ReleaseRefreshLease drops the claim only when the holder matches — an
// expired-then-retaken lease is never released by the stale holder.
func (s *Store) ReleaseRefreshLease(connectionID, provider, holder string) error {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	unlock, err := s.lockLeaseFile()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.loadLeasesLocked(); err != nil {
		return err
	}
	key := refreshLeaseKey(connectionID, provider)
	lease, held := s.refreshLeases[key]
	if !held || lease.Holder != holder {
		return nil
	}
	delete(s.refreshLeases, key)
	return s.persistLeasesLocked()
}

// refreshLeaseHeld reports the live lease for joiner polling — a record past
// its expiry no longer counts as held even before a takeover removes it.
func (s *Store) refreshLeaseHeld(connectionID, provider string, now time.Time) bool {
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	unlock, err := s.lockLeaseFile()
	if err != nil {
		return true
	}
	defer unlock()
	if err := s.loadLeasesLocked(); err != nil {
		return true
	}
	lease, held := s.refreshLeases[refreshLeaseKey(connectionID, provider)]
	return held && now.Before(lease.ExpiresAt)
}

// leasesPath is the durable lease ledger beside the registry file; in-memory
// stores carry leases in memory only.
func (s *Store) leasesPath() string {
	if s.path == "" {
		return ""
	}
	return s.path + ".refresh-leases"
}

// The file fence covers reload through rename across independent processes.
func (s *Store) lockLeaseFile() (func(), error) {
	path := s.leasesPath()
	if path == "" {
		return func() {}, nil
	}
	if err := api.SafeStorePath(path + ".lock"); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	s.leasesLoaded = false
	s.refreshLeases = map[string]api.RefreshLease{}
	return func() { _ = lock.Unlock() }, nil
}

func (s *Store) loadLeasesLocked() error {
	if s.leasesLoaded {
		return nil
	}
	s.leasesLoaded = true
	path := s.leasesPath()
	if path == "" {
		return nil
	}
	body, err := os.ReadFile(path) // #nosec G304 -- registry-adjacent lease ledger under the same trusted path rules.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var leases []api.RefreshLease
	if err := json.Unmarshal(body, &leases); err != nil {
		return fmt.Errorf("%w: refresh lease ledger", api.ErrInvalid)
	}
	for _, lease := range leases {
		if err := lease.Validate(); err != nil {
			return err
		}
		s.refreshLeases[refreshLeaseKey(lease.ConnectionID, lease.Provider)] = lease
	}
	return nil
}

func (s *Store) persistLeasesLocked() error {
	path := s.leasesPath()
	if path == "" {
		return nil
	}
	if err := api.SafeStorePath(path); err != nil {
		return err
	}
	leases := make([]api.RefreshLease, 0, len(s.refreshLeases))
	for _, lease := range s.refreshLeases {
		leases = append(leases, lease)
	}
	slices.SortFunc(leases, func(a, b api.RefreshLease) int {
		return strings.Compare(refreshLeaseKey(a.ConnectionID, a.Provider), refreshLeaseKey(b.ConnectionID, b.Provider))
	})
	body, err := json.Marshal(leases)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".refresh-leases-*")
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
