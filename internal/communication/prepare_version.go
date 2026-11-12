package communication

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func (s *Spool) prepareVersionLocked(key string) (uint64, error) {
	body, err := s.fsys.ReadFile(filepath.Join(s.root, "prepares", key+"-version.json"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var version uint64
	if err := json.Unmarshal(body, &version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Spool) enqueuePrepareDelivery(d Delivery, legacyPrefix ...string) (bool, error) {
	var err error
	if d, err = normalizeDelivery(d); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.PrepareKey != "" {
		version, err := s.prepareVersionLocked(d.PrepareKey)
		if err != nil {
			return false, err
		}
		if d.PrepareVersion < version {
			return false, nil
		}
		if d.PrepareVersion > version {
			path := filepath.Join(s.root, "prepares", d.PrepareKey+"-version.json")
			if err := s.mkdirOwned(filepath.Dir(path)); err != nil {
				return false, err
			}
			// Persist the fence first. The parent outbox retries this same
			// version if enqueue fails or the gateway crashes before enqueue.
			if err := s.writeJSON(path, d.PrepareVersion); err != nil {
				return false, err
			}
		}
		if d.PrepareVersion > 0 && len(legacyPrefix) == 1 {
			pending := filepath.Join(s.root, "outbox", "pending")
			names, err := s.fsys.ReadDir(pending)
			if err != nil {
				return false, err
			}
			for _, name := range names {
				if !strings.HasPrefix(name, legacyPrefix[0]) {
					continue
				}
				path := filepath.Join(pending, name)
				body, err := s.fsys.ReadFile(path)
				if err != nil {
					return false, err
				}
				var old Delivery
				if json.Unmarshal(body, &old) != nil {
					return false, errors.New("invalid prepare delivery")
				}
				if old.PrepareKey != "" || old.ChatID != d.ChatID || old.Channel != d.Channel || old.TaskID != d.TaskID {
					continue
				}
				old.PrepareKey = d.PrepareKey
				if _, err := s.suppressOldPrepareLocked(old, path); err != nil {
					return false, err
				}
			}
		}
	}
	if err := s.enqueueDeliveryLocked(d); err != nil {
		return false, err
	}
	s.signal()
	return true, nil
}

func (s *Spool) suppressOldPrepareSending(d Delivery) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.suppressOldPrepareLocked(d, filepath.Join(s.root, "outbox", "sending", spoolFileID(d.ID)+".json"))
}

func (s *Spool) suppressOldPrepareLocked(d Delivery, from string) (bool, error) {
	if d.PrepareKey == "" {
		return false, nil
	}
	version, err := s.prepareVersionLocked(d.PrepareKey)
	if err != nil {
		return false, err
	}
	if d.PrepareVersion >= version {
		return false, nil
	}
	d.Kind = "superseded_prepare"
	if err := s.writeJSON(from, d); err != nil {
		return false, err
	}
	return true, s.renameOwned(from, filepath.Join(s.root, "outbox", "superseded", spoolFileID(d.ID)+".json"))
}
