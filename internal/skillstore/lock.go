package skillstore

import (
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// lock serializes writes for one skill across processes on this host.
// The lock file lives under .state/locks/ so it is hub-private.
func (s *Store) lock(name string) (func(), error) {
	dir := filepath.Join(s.root, stateDir, locksDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	fl := flock.New(filepath.Join(dir, name+".lock"))
	if err := fl.Lock(); err != nil {
		return nil, err
	}
	return func() { _ = fl.Unlock() }, nil
}
