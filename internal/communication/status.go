package communication

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// Gateway status contract (issue #201): a bounded control-plane document
// answering "how deep is the queue, what's stuck in the outbox, are
// routines healthy". Everything is counts and bounded timestamps — never
// job content, never secrets. Read through authorizeControl so only the
// enrolled control token or an enrolled sibling can see it.

type SpoolQueueStatus struct {
	Pending int `json:"pending"`
	Running int `json:"running"`
	Failed  int `json:"failed"`
}

type OutboxStatus struct {
	Pending              int   `json:"pending"`
	Sending              int   `json:"sending"`
	Failed               int   `json:"failed"`
	OldestPendingSeconds int64 `json:"oldest_pending_seconds,omitempty"`
}

type RoutinesStatus struct {
	State   string     `json:"state"` // ok | unavailable
	Total   int        `json:"total"`
	Enabled int        `json:"enabled"`
	Overdue int        `json:"overdue"`
	NextDue *time.Time `json:"next_due,omitempty"`
}

type GatewayStatusView struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Queue       SpoolQueueStatus `json:"queue"`
	Outbox      OutboxStatus     `json:"outbox"`
	Routines    RoutinesStatus   `json:"routines"`
}

func (s *Spool) countDir(rel string) int {
	names, err := s.fsys.ReadDir(rel)
	if err != nil {
		return 0
	}
	return len(names)
}

// QueueStatus counts spool records by lifecycle directory. The oldest
// outbox-pending age is read from entry modtimes; the scan is bounded to
// the first 64 names so a flooded queue cannot pin the status call.
func (s *Spool) QueueStatus(now time.Time) (SpoolQueueStatus, OutboxStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	queue := SpoolQueueStatus{
		Pending: s.countDir(filepath.Join(s.root, "pending")),
		Running: s.countDir(filepath.Join(s.root, "running")),
		Failed:  s.countDir(filepath.Join(s.root, "failed")),
	}
	outbox := OutboxStatus{
		Pending: s.countDir(filepath.Join(s.root, "outbox", "pending")),
		Sending: s.countDir(filepath.Join(s.root, "outbox", "sending")),
		Failed:  s.countDir(filepath.Join(s.root, "outbox", "failed")),
	}
	names, err := s.fsys.ReadDir(filepath.Join(s.root, "outbox", "pending"))
	if err == nil {
		var oldest time.Time
		for i, name := range names {
			if i >= 64 {
				break
			}
			info, err := s.fsys.Lstat(filepath.Join(s.root, "outbox", "pending", name))
			if err != nil {
				continue
			}
			if oldest.IsZero() || info.ModTime().Before(oldest) {
				oldest = info.ModTime()
			}
		}
		if !oldest.IsZero() && oldest.Before(now) {
			outbox.OldestPendingSeconds = int64(now.Sub(oldest).Seconds())
		}
	}
	return queue, outbox
}

// statusView assembles the bounded gateway projection. Routines degrade to
// state=unavailable when the automation call fails — status must still
// answer while ToolHub is down.
func (g *Gateway) statusView(ctx context.Context, caller identity.Envelope) GatewayStatusView {
	now := time.Now().UTC()
	queue, outbox := g.spool.QueueStatus(now)
	view := GatewayStatusView{GeneratedAt: now, Queue: queue, Outbox: outbox}

	var items []Schedule
	if err := g.automationCall(ctx, caller, "schedule.list", nil, &items); err != nil {
		view.Routines.State = "unavailable"
		return view
	}
	view.Routines.State = "ok"
	view.Routines.Total = len(items)
	for _, item := range items {
		if item.Paused {
			continue
		}
		view.Routines.Enabled++
		if item.NextDue.IsZero() {
			continue
		}
		if item.NextDue.Before(now) {
			view.Routines.Overdue++
		}
		if view.Routines.NextDue == nil || item.NextDue.Before(*view.Routines.NextDue) {
			due := item.NextDue
			view.Routines.NextDue = &due
		}
	}
	return view
}

func (g *Gateway) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		writeJSON(w, map[string]string{"error": "method not allowed"})
		return
	}
	caller, ok := g.authorizeControl(r)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, g.statusView(r.Context(), caller))
}
