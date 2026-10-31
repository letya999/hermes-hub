package communication

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const eventLayoutVersion = 2

type eventMove struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Digest    string `json:"digest"`
}
type eventLayout struct {
	Version  int         `json:"version"`
	Complete bool        `json:"complete"`
	Files    []eventMove `json:"files,omitempty"`
}

func strictEventJSON(body []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid event record")
	}
	return nil
}

// Classify only known schemas and canonical identities; unrecognized bytes stay put.
func eventRecordDirectory(name string, body []byte) (string, error) {
	var discriminator struct {
		ScheduleID string `json:"schedule_id"`
		JobID      string `json:"job_id"`
		Provider   string `json:"provider"`
	}
	if err := json.Unmarshal(body, &discriminator); err != nil {
		return "", errors.New("invalid legacy event record")
	}
	kinds := 0
	if discriminator.ScheduleID != "" {
		kinds++
	}
	if discriminator.JobID != "" {
		kinds++
	}
	if discriminator.Provider != "" {
		kinds++
	}
	if kinds != 1 {
		return "", errors.New("unknown legacy event schema")
	}
	switch {
	case discriminator.ScheduleID != "":
		var st EventState
		if strictEventJSON(body, &st) != nil || name != spoolFileID(st.ScheduleID)+".json" {
			break
		}
		return "event-state", nil
	case discriminator.JobID != "":
		var r streamReceipt
		if strictEventJSON(body, &r) != nil || r.Position == 0 || r.Event.JobID != r.JobID || r.Event.EventID == "" {
			break
		}
		sum := sha256.Sum256([]byte(r.JobID + "\x00" + r.Event.EventID))
		if name != hex.EncodeToString(sum[:])+".json" {
			break
		}
		return "run-events", nil
	case discriminator.Provider != "":
		var marker struct {
			Provider string `json:"provider"`
			EventID  string `json:"event_id"`
		}
		if strictEventJSON(body, &marker) != nil || marker.EventID == "" {
			break
		}
		sum := sha256.Sum256([]byte(marker.Provider + "\x00" + marker.EventID))
		if name != hex.EncodeToString(sum[:])+".json" {
			break
		}
		return "provider-dedup", nil
	}
	return "", errors.New("event schema or identity mismatch")
}

func readEventRecord(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, errors.New("unsafe event record")
	}
	return os.ReadFile(path)
}

// migrateEventLayout runs only under the existing exclusive spool writer fence.
// The manifest precedes every move; an events file fences the old binary's MkdirAll.
func (s *Spool) migrateEventLayout() error {
	manifestPath := filepath.Join(s.root, "event-layout.json")
	legacy := filepath.Join(s.root, "events")
	var plan eventLayout
	body, err := readEventRecord(manifestPath)
	switch {
	case err == nil:
		if strictEventJSON(body, &plan) != nil || plan.Version != eventLayoutVersion {
			return errors.New("unknown event layout")
		}
		if plan.Complete {
			marker, err := readEventRecord(legacy)
			var fence struct {
				Version int `json:"version"`
			}
			if err != nil || strictEventJSON(marker, &fence) != nil || fence.Version != eventLayoutVersion {
				return errors.New("event layout recovery required")
			}
			return nil
		}
	case errors.Is(err, os.ErrNotExist):
		plan.Version = eventLayoutVersion
		entries, err := os.ReadDir(legacy)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, entry := range entries {
			data, err := readEventRecord(filepath.Join(legacy, entry.Name()))
			if err != nil {
				return err
			}
			dir, err := eventRecordDirectory(entry.Name(), data)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			plan.Files = append(plan.Files, eventMove{entry.Name(), dir, hex.EncodeToString(sum[:])})
		}
	default:
		return err
	}
	hasLegacyDir := false
	if info, err := os.Lstat(legacy); err == nil {
		if info.IsDir() {
			hasLegacyDir = true
		} else {
			var fence struct {
				Version int `json:"version"`
			}
			body, err := readEventRecord(legacy)
			if err != nil || strictEventJSON(body, &fence) != nil || fence.Version != eventLayoutVersion {
				return errors.New("unknown event layout fence")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Validate the entire source/destination set before the first mutation, also on resume.
	for _, m := range plan.Files {
		if filepath.Base(m.Name) != m.Name || m.Directory != "event-state" && m.Directory != "run-events" && m.Directory != "provider-dedup" {
			return errors.New("invalid event migration manifest")
		}
		found := false
		dirs := []string{filepath.Join(s.root, m.Directory)}
		if hasLegacyDir {
			dirs = append(dirs, legacy)
		}
		for _, dir := range dirs {
			data, err := readEventRecord(filepath.Join(dir, m.Name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			kind, err := eventRecordDirectory(m.Name, data)
			if err != nil || kind != m.Directory || hex.EncodeToString(sum[:]) != m.Digest {
				return errors.New("conflicting event migration record")
			}
			found = true
		}
		if !found {
			return errors.New("event migration source missing")
		}
	}
	if data, err := json.Marshal(plan); err != nil || len(data) > 4<<20 {
		return errors.New("event migration manifest exceeds safe bound")
	}
	if err := s.writeJSON(manifestPath, plan); err != nil {
		return err
	}
	for _, m := range plan.Files {
		src := filepath.Join(legacy, m.Name)
		dst := filepath.Join(s.root, m.Directory, m.Name)
		if m.Directory == "event-state" {
			if err := s.markEventStateInitializedPath(dst); err != nil {
				return err
			}
		}
		if !hasLegacyDir {
			continue
		}
		if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if _, err := os.Lstat(dst); err == nil {
			// A resumed duplicate copy needs file durability before removing source.
			targetRoot, err := os.OpenRoot(filepath.Dir(dst))
			if err != nil {
				return err
			}
			f, err := targetRoot.OpenFile(m.Name, os.O_RDWR, 0600)
			_ = targetRoot.Close()
			if err != nil {
				return err
			}
			syncErr := f.Sync()
			closeErr := f.Close()
			if syncErr != nil {
				return syncErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := syncSpoolDirectory(filepath.Dir(dst)); err != nil {
				return err
			}
			if err := s.removeOwned(src); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else if err := s.renameOwned(src, dst); err != nil {
			return err
		}
		if err := syncSpoolDirectory(filepath.Dir(dst)); err != nil {
			return err
		}
		if err := syncSpoolDirectory(legacy); err != nil {
			return err
		}
	}
	if info, err := os.Lstat(legacy); err == nil && info.IsDir() {
		if err := s.removeOwned(legacy); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.writeJSON(legacy, struct {
		Version int `json:"version"`
	}{eventLayoutVersion}); err != nil {
		return err
	}

	// Retain only the layout version after completion; no lifetime list of cursor IDs.
	return s.writeJSON(manifestPath, eventLayout{Version: eventLayoutVersion, Complete: true})
}

func (s *Spool) markEventStateInitializedPath(path string) error {
	if data, err := readEventRecord(path + ".initialized"); err == nil {
		var marker struct {
			Version int `json:"version"`
		}
		if strictEventJSON(data, &marker) != nil || marker.Version != eventLayoutVersion {
			return errors.New("invalid cursor initialization evidence")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.writeJSON(path+".initialized", struct {
		Version int `json:"version"`
	}{eventLayoutVersion})
}

func (s *Spool) writeEventStateLocked(id string, st EventState) error {
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if len(body) > 4<<20 {
		return errors.New("source state exceeds record limit")
	}
	path := s.eventPath(id)
	if err := s.markEventStateInitializedPath(path); err != nil {
		return err
	}
	return s.writeJSON(path, st)
}
