package communication

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

func (s *Spool) EnqueueDelivery(d Delivery) error {
	var err error
	if d, err = normalizeDelivery(d); err != nil {
		return err
	}
	s.mu.Lock()
	err = s.enqueueDeliveryLocked(d)
	s.mu.Unlock()
	if err == nil {
		s.signal()
	}
	return err
}

func normalizeDelivery(d Delivery) (Delivery, error) {
	if d.PrepareKey != "" {
		decoded, err := hex.DecodeString(d.PrepareKey)
		if err != nil || len(decoded) != 32 {
			return d, errors.New("invalid prepare delivery identity")
		}
	}
	if d.Format != "" && d.Format != formatMarkdown {
		return d, errors.New("invalid delivery identity")
	}
	if d.Channel == "" {
		d.Channel = "telegram_bot"
	}
	ch := channelFor(nil, d.Channel)
	if ch == nil {
		return d, errors.New("invalid delivery identity")
	}
	if err := ch.NormalizeDelivery(&d); err != nil {
		return d, err
	}
	// Approval prompts are control plane by contract: they unblock held work
	// and must never queue behind a flooded ordinary outbox.
	if d.ApprovalID != "" {
		d.Control = true
	}
	return d, nil
}

// enqueueDeliveryLocked is idempotent across the whole outbox lifecycle; s.mu
// must be held.
func (s *Spool) enqueueDeliveryLocked(d Delivery) error {
	if d.OccurrenceID == "" && (strings.HasPrefix(d.JobID, "routine-") || strings.HasPrefix(d.JobID, "batch2-")) {
		d.OccurrenceID = d.JobID
	}
	for _, dir := range []string{"pending", "sending", "done", "failed", "superseded"} {
		if _, err := s.fsys.Lstat(filepath.Join(s.root, "outbox", dir, spoolFileID(d.ID)+".json")); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if !d.Control && s.limits.MaxDeliveries > 0 {
		names, err := s.fsys.ReadDir(filepath.Join(s.root, "outbox", "pending"))
		if err != nil {
			return err
		}
		ordinary := 0
		for _, name := range names {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			body, err := s.fsys.ReadFile(filepath.Join(s.root, "outbox", "pending", name))
			if err != nil {
				return err
			}
			var pending Delivery
			if err := json.Unmarshal(body, &pending); err != nil {
				return err
			}
			if !pending.Control {
				ordinary++
			}
		}
		if ordinary >= s.limits.MaxDeliveries {
			return errors.New("outbox queue full")
		}
	}
	return s.writeJSONClass(filepath.Join(s.root, "outbox", "pending", spoolFileID(d.ID)+".json"), d, d.Control || d.JobID != "")
}

func (s *Spool) LoadOffset() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.fsys.ReadFile(filepath.Join(s.root, "offset.json"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var value struct {
		Offset int64 `json:"offset"`
	}
	if err := json.Unmarshal(b, &value); err != nil || value.Offset < 0 {
		return 0, errors.New("invalid Telegram update offset")
	}
	return value.Offset, nil
}

func (s *Spool) SaveOffset(offset int64) error {
	if offset < 0 {
		return errors.New("negative Telegram update offset")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeJSON(filepath.Join(s.root, "offset.json"), struct {
		Offset int64 `json:"offset"`
	}{offset})
}

// DeliverPolls reports how many outbox scans ran; it bounds the fallback
// poll rate for an idle control plane (SPEC-0053 R2.3).
func (s *Spool) DeliverPolls() int64 { return s.deliverPolls.Load() }

func (s *Spool) ClaimDelivery() (*Delivery, error) {
	s.deliverPolls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := filepath.Join(s.root, "outbox", "pending")
	entries, err := s.fsys.ReadDir(pending)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, name := range entries {
		if strings.HasSuffix(name, ".json") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	// Control-plane sends claim before ordinary replies so an outbox flood
	// cannot delay approvals or cancel-acks; the same bounded scan applies.
	for i, name := range names {
		if i >= 256 {
			break
		}
		b, err := s.fsys.ReadFile(filepath.Join(pending, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var d Delivery
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, err
		}
		if suppressed, err := s.suppressOldPrepareLocked(d, filepath.Join(pending, name)); err != nil {
			return nil, err
		} else if suppressed {
			continue
		}
		if !d.Control || d.NotBefore.After(time.Now().UTC()) {
			continue
		}
		if err := s.renameOwned(filepath.Join(pending, name), filepath.Join(s.root, "outbox", "sending", name)); err != nil {
			return nil, err
		}
		return &d, nil
	}
	// Quiet-window deliveries stay durable in pending until not_before; a
	// bounded scan keeps a deferred backlog from stalling the claim loop.
	for i, name := range names {
		if i >= 256 {
			break
		}
		b, err := s.fsys.ReadFile(filepath.Join(pending, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var d Delivery
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, err
		}
		if d.NotBefore.After(time.Now().UTC()) {
			continue
		}
		if err := s.renameOwned(filepath.Join(pending, name), filepath.Join(s.root, "outbox", "sending", name)); err != nil {
			return nil, err
		}
		return &d, nil
	}
	return nil, nil
}

func (s *Spool) CompleteDelivery(id string) error {
	if err := s.move("outbox/sending", "outbox/done", id); err != nil {
		return err
	}
	// Delete only blobs referenced by the durably completed delivery.
	body, err := s.fsys.ReadFile(filepath.Join(s.root, "outbox", "done", spoolFileID(id)+".json"))
	if err != nil {
		return err
	}
	var d Delivery
	if json.Unmarshal(body, &d) != nil || d.ID != id {
		return errors.New("invalid completed delivery")
	}
	dir := filepath.Join(s.root, "outbox", "blobs", spoolFileID(id))
	for _, artifact := range d.Artifacts {
		if artifact.Blob == "" {
			continue
		}
		if artifact.Blob == "." || artifact.Blob == ".." || !spoolIDPattern.MatchString(artifact.Blob) {
			return errors.New("invalid completed artifact")
		}
		if err := s.removeOwned(filepath.Join(dir, artifact.Blob)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return s.removeOwned(dir)
	}
	return nil
}

// WriteDeliveryBlob stages one fetched artifact for a not-yet-created delivery
// under a stable directory derived from the delivery ID.
func (s *Spool) WriteDeliveryBlob(deliveryID, name string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > videoSizeLimit {
		return "", errors.New("invalid artifact blob")
	}
	base := filepath.Base(name)
	if base == "" || base == "." || base == ".." || !spoolIDPattern.MatchString(base) {
		return "", errors.New("invalid artifact name")
	}
	dir := filepath.Join(s.root, "outbox", "blobs", spoolFileID(deliveryID))
	if err := s.mkdirOwned(dir); err != nil {
		return "", err
	}
	if err := s.writeBlob(filepath.Join(dir, base), data, true); err != nil {
		return "", err
	}
	return base, nil
}

func (s *Spool) ReadDeliveryBlob(deliveryID, blob string) ([]byte, error) {
	if blob == "" || blob == "." || blob == ".." || !spoolIDPattern.MatchString(blob) {
		return nil, errors.New("invalid artifact blob")
	}
	return os.ReadFile(filepath.Join(s.root, "outbox", "blobs", spoolFileID(deliveryID), blob))
}

// UpdateSending rewrites a claimed delivery's durable record so part progress
// survives a restart between parts of the same message.
func (s *Spool) UpdateSending(d Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, "outbox", "sending", spoolFileID(d.ID)+".json")
	if _, err := s.fsys.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return s.writeJSON(path, d)
}
func (s *Spool) UncertainDelivery(id string) error {
	return s.move("outbox/sending", "outbox/failed", id)
}

// RetryDelivery returns a claimed delivery to pending so a deferred send (for
// example a rate-limited status edit) is retried on a later pass.
func (s *Spool) RetryDelivery(id string) error {
	return s.move("outbox/sending", "outbox/pending", id)
}

func (s *Spool) move(from, to, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fileID := spoolFileID(id)
	return s.renameOwned(filepath.Join(s.root, from, fileID+".json"), filepath.Join(s.root, to, fileID+".json"))
}

func spoolFileID(id string) string { return commodel.FileID(id) }

func firstJSON(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", nil
	}
	return names[0], nil
}

func atomicJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicBytes(path, b)
}

func atomicBytes(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".queue-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
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
	if err := os.Rename(tmp, path); err != nil { // #nosec G703 -- path derived from the spool record id (IDPattern)
		return err
	}
	return syncSpoolDirectory(filepath.Dir(path))
}
