package communication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (s *Spool) trackApprovalMessage(d Delivery) error {
	if d.TelegramMessageID <= 0 {
		return errors.New("approval message receipt missing")
	}
	dir := filepath.Join(s.root, "approval-messages")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(dir, spoolFileID(d.ID)+".json"), d)
}

// Only receipts of messages this gateway sent can be removed. Decisions still
// use the existing ownership-checked control path; deleting UI never approves.
func (g *Gateway) closeApprovalMessages(ctx context.Context) {
	dir := filepath.Join(g.spool.root, "approval-messages")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return
	}
	defer root.Close()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		b, err := root.ReadFile(entry.Name())
		var d Delivery
		if err != nil || json.Unmarshal(b, &d) != nil || d.TelegramMessageID <= 0 {
			continue
		}
		m, found, err := g.spool.Mapping(d.ApprovalJobID)
		if err != nil || !found || m.ConversationID != d.ConversationID || m.DeliveryTargetID != d.DeliveryTargetID {
			continue
		}
		if !terminalStatus(m.Status) && (m.Control == nil || m.Control.Action != "approve" || m.Control.RequestID != d.ApprovalID || m.Control.State != "applied") {
			continue
		}
		if g.api.DeleteMessage(ctx, d.ChatID, d.TelegramMessageID) == nil {
			_ = os.Remove(path)
		}
	}
}
