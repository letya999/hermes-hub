package communication

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"
)

// InboundRecord is the durable staging record for one file a user sent through
// a channel. The blob sits in the spool (not the workspace — the gateway has
// no workspace mount) until the owning tools plane fetches and consumes it.
type InboundRecord struct {
	ID             string `json:"id"`
	UserID         string `json:"user_id"`
	ConversationID string `json:"conversation_id"`
	Name           string `json:"name"`
	// Source routes the materialized copy: "people" (default) for
	// person-sent files, "services" for service originals (R4.2).
	Source    string    `json:"source,omitempty"`
	Mime      string    `json:"mime"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

func validInboundID(id string) bool {
	return id != "" && spoolIDPattern.MatchString(id)
}

func (s *Spool) StageInbound(rec InboundRecord, src io.Reader) error {
	if !validInboundID(rec.ID) {
		return errors.New("invalid inbound id")
	}
	data, err := io.ReadAll(io.LimitReader(src, videoSizeLimit+1))
	if err != nil {
		return err
	}
	if len(data) > videoSizeLimit {
		return errors.New("inbound exceeds storage write limit")
	}
	rec.Size = int64(len(data))
	dir := filepath.Join(s.root, "inbound")
	if err := s.writeBlob(filepath.Join(dir, rec.ID+".bin"), data, false); err != nil {
		return err
	}
	return s.writeJSONClass(filepath.Join(dir, rec.ID+".json"), rec, false)
}

func (s *Spool) ReadInbound(id string) (InboundRecord, []byte, error) {
	var rec InboundRecord
	if !validInboundID(id) {
		return rec, nil, errors.New("invalid inbound id")
	}
	b, err := s.fsys.ReadFile(filepath.Join(s.root, "inbound", id+".json")) // #nosec G703 -- id is validated by spoolIDPattern
	if err != nil {
		return rec, nil, errors.New("inbound record not found")
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return rec, nil, err
	}
	data, err := s.fsys.ReadFile(filepath.Join(s.root, "inbound", id+".bin")) // #nosec G703 -- id is validated by spoolIDPattern
	if err != nil {
		return rec, nil, errors.New("inbound payload missing")
	}
	return rec, data, nil
}

func (s *Spool) ConsumeInbound(id string) error {
	if !validInboundID(id) {
		return errors.New("invalid inbound id")
	}
	err := s.removeOwned(filepath.Join(s.root, "inbound", id+".json")) // #nosec G703 -- id is validated by spoolIDPattern
	_ = s.removeOwned(filepath.Join(s.root, "inbound", id+".bin"))     // #nosec G703 -- id is validated by spoolIDPattern
	return err
}
