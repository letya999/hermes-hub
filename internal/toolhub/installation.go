package toolhub

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// VerifyInstallationWitness checks this store's persisted installation
// identity against a witness file kept on an independent filesystem
// location. The in-volume `.initialized` marker cannot detect whole-volume
// loss — it disappears together with the data. A witnessed ID survives the
// volume, so a store carrying a different identity (a recreated empty
// volume's fresh install) is foreign and refused rather than silently
// adopted.
func (s *Store) VerifyInstallationWitness(witnessPath string) error {
	s.mu.Lock()
	if s.installationID == "" {
		id, err := newInstallationID()
		if err != nil {
			s.mu.Unlock()
			return err
		}
		s.installationID = id
	}
	id := s.installationID
	s.mu.Unlock()
	info, err := os.Lstat(witnessPath)
	switch {
	case err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0):
		return api.ErrInvalid
	case err == nil:
		b, err := os.ReadFile(witnessPath) // #nosec G304 -- operator-configured witness path
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(b)) != id {
			return fmt.Errorf("%w: installation witness identity", api.ErrConflict)
		}
		return nil
	case !os.IsNotExist(err):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(witnessPath), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(witnessPath), ".toolhub-installation-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.WriteString(id)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, witnessPath); err != nil {
		return err
	}
	syncDirectory := s.syncDirectory
	if syncDirectory == nil {
		syncDirectory = syncHostingFleetDirectory
	}
	return syncDirectory(filepath.Dir(witnessPath))
}

func newInstallationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
