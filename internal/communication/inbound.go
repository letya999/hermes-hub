package communication

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// InboundRecord is the durable staging record for one file a user sent through
// a channel. The blob sits in the spool (not the workspace — the gateway has
// no workspace mount) until the owning tools plane fetches and consumes it.
type InboundRecord struct {
	ID             string    `json:"id"`
	UserID         string    `json:"user_id"`
	ConversationID string    `json:"conversation_id"`
	Name           string    `json:"name"`
	Mime           string    `json:"mime"`
	Size           int64     `json:"size"`
	CreatedAt      time.Time `json:"created_at"`
}

func validInboundID(id string) bool {
	return id != "" && spoolIDPattern.MatchString(id)
}

func (s *Spool) StageInbound(rec InboundRecord, src io.Reader) error {
	if !validInboundID(rec.ID) {
		return errors.New("invalid inbound id")
	}
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	rec.Size = int64(len(data))
	dir := filepath.Join(s.root, "inbound")
	if err := os.WriteFile(filepath.Join(dir, rec.ID+".bin"), data, 0600); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(dir, rec.ID+".json"), rec)
}

func (s *Spool) ReadInbound(id string) (InboundRecord, []byte, error) {
	var rec InboundRecord
	if !validInboundID(id) {
		return rec, nil, errors.New("invalid inbound id")
	}
	b, err := os.ReadFile(filepath.Join(s.root, "inbound", id+".json")) // #nosec G703 -- id is validated by spoolIDPattern
	if err != nil {
		return rec, nil, errors.New("inbound record not found")
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.root, "inbound", id+".bin")) // #nosec G703 -- id is validated by spoolIDPattern
	if err != nil {
		return rec, nil, errors.New("inbound payload missing")
	}
	return rec, data, nil
}

func (s *Spool) ConsumeInbound(id string) error {
	if !validInboundID(id) {
		return errors.New("invalid inbound id")
	}
	err := os.Remove(filepath.Join(s.root, "inbound", id+".json")) // #nosec G703 -- id is validated by spoolIDPattern
	_ = os.Remove(filepath.Join(s.root, "inbound", id+".bin"))     // #nosec G703 -- id is validated by spoolIDPattern
	return err
}
