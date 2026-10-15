package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/identity"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	if d.Format != "" && d.Format != formatMarkdown {
		return d, errors.New("invalid delivery identity")
	}
	if d.Channel == "" {
		d.Channel = "telegram_bot"
	}
	switch d.Channel {
	case "telegram_bot":
		transportID := "telegram-" + strconv.FormatInt(d.ChatID, 10)
		if d.ConversationID == "" {
			d.ConversationID = transportID
		}
		if d.DeliveryTargetID == "" {
			d.DeliveryTargetID = transportID
		}
		// Group forum chats carry negative ids; zero stays invalid.
		if d.ChatID == 0 || !identity.ValidID(d.ConversationID) || d.DeliveryTargetID != transportID {
			return d, errors.New("invalid delivery identity")
		}
	case "slack_app":
		if !identity.ValidID(d.ConversationID) || !identity.ValidID(d.DeliveryTargetID) || d.DeliveryTargetID != d.ConversationID || !validSlackIMChannel(d.SlackChannel) || !validSlackThread(d.SlackThread) {
			return d, errors.New("invalid delivery identity")
		}
	case "mock":
		if d.ChatID == 0 {
			return d, errors.New("invalid delivery identity")
		}
	default:
		return d, errors.New("invalid delivery identity")
	}
	return d, nil
}

// enqueueDeliveryLocked is idempotent across the whole outbox lifecycle; s.mu
// must be held.
func (s *Spool) enqueueDeliveryLocked(d Delivery) error {
	if d.OccurrenceID == "" && strings.HasPrefix(d.JobID, "routine-") {
		d.OccurrenceID = d.JobID
	}
	for _, dir := range []string{"pending", "sending", "done", "failed"} {
		if _, err := os.Stat(filepath.Join(s.root, "outbox", dir, spoolFileID(d.ID)+".json")); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return atomicJSON(filepath.Join(s.root, "outbox", "pending", spoolFileID(d.ID)+".json"), d)
}

func (s *Spool) LoadOffset() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.root, "offset.json"))
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
	return atomicJSON(filepath.Join(s.root, "offset.json"), struct {
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
	name, err := firstJSON(filepath.Join(s.root, "outbox", "pending"))
	if err != nil || name == "" {
		return nil, err
	}
	from, to := filepath.Join(s.root, "outbox", "pending", name), filepath.Join(s.root, "outbox", "sending", name)
	if err := os.Rename(from, to); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(to)
	if err != nil {
		return nil, err
	}
	var d Delivery
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Spool) CompleteDelivery(id string) error {
	err := s.move("outbox/sending", "outbox/done", id)
	// Artifact bytes have reached the channel by now; the workspace original
	// remains authoritative, so the spool copy is dropped.
	_ = os.RemoveAll(filepath.Join(s.root, "outbox", "blobs", spoolFileID(id)))
	return err
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
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, base), data, 0600); err != nil {
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
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return atomicJSON(path, d)
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
	return os.Rename(filepath.Join(s.root, from, fileID+".json"), filepath.Join(s.root, to, fileID+".json"))
}

func spoolFileID(id string) string {
	if spoolIDPattern.MatchString(id) {
		return id
	}
	hash := sha256.Sum256([]byte(id))
	return "id-" + hex.EncodeToString(hash[:])
}

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
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncSpoolDirectory(filepath.Dir(path))
}
