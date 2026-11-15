package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
)

// Daily appends captured turns to memory/daily/<YYYY-MM-DD>.md leaves —
// every entry is provenance-stamped in the hub-owned prov.db sidecar before
// the leaf write is acknowledged (SPEC-0085 R2.2). Sidecar provenance is
// authoritative; the markdown comments are display-only.
type Daily struct {
	w    *Writer
	prov *memory.ProvDB
	tz   *time.Location
	mu   sync.Mutex // serializes appends inside this process
}

// OpenDaily binds the vault's governed writer and provenance sidecar; tz is
// the owner's timezone — leaf selection and timestamps are local-midnight
// correct, including DST.
func OpenDaily(root string, tz *time.Location) (*Daily, error) {
	if tz == nil {
		tz = time.UTC
	}
	prov, err := memory.OpenProvDB(filepath.Join(root, "runtime", "memory"))
	if err != nil {
		return nil, err
	}
	return &Daily{w: NewWriter(root), prov: prov, tz: tz}, nil
}

// Append stamps and fsyncs the entry into prov.db, then appends the fenced
// block to that day's leaf. A stale leaf hash (owner edit between read and
// write) retries on the fresh content rather than clobbering.
func (d *Daily) Append(e memory.Entry, channel string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	e.Body = memory.StripRecalled(e.Body)
	if strings.TrimSpace(e.Body) == "" {
		return nil // only recalled content — nothing new to capture (R2.5)
	}
	if err := d.prov.Append(&e); err != nil {
		return err
	}
	at := e.ObservedAt.In(d.tz)
	leaf := filepath.Join("memory", "daily", at.Format("2006-01-02")+".md")
	block := formatDailyBlock(at, e, channel)
	actor := actorFor(e.Provenance)
	for attempt := 0; attempt < 3; attempt++ {
		abs := filepath.Join(d.w.root, leaf)
		cur, err := os.ReadFile(abs)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		want := ""
		if err == nil {
			if strings.Contains(string(cur), "mem:id="+e.ID+" ") || strings.Contains(string(cur), "mem:id="+e.ID+" --") {
				return nil // block already landed — retry must not duplicate it
			}
			want = sha256Hex(cur)
		}
		content := append(append([]byte{}, cur...), []byte(block)...)
		_, applyErr := d.w.Apply(memory.WriteOp{Path: filepath.ToSlash(leaf), Content: content, WantSHA: want, Actor: actor})
		if errors.Is(applyErr, memory.ErrStaleHash) {
			continue
		}
		return applyErr
	}
	return memory.ErrStaleHash
}

func actorFor(p memory.Provenance) memory.Actor {
	switch p {
	case memory.ProvenanceOwner:
		return memory.ActorOwner
	case memory.ProvenanceAgent:
		return memory.ActorAgent
	default:
		return memory.ActorSystem
	}
}

// formatDailyBlock renders one captured entry. The fence is grown past any
// backtick run inside the body so content can never forge an entry boundary,
// and provenance metadata lives in one machine-readable comment the hub
// writes — a body line shaped like it stays inside the fence, inert.
func formatDailyBlock(at time.Time, e memory.Entry, channel string) string {
	fence := "```"
	for strings.Contains(e.Body, fence) {
		fence += "`"
	}
	var b strings.Builder
	speaker := speakerOf(e)
	fmt.Fprintf(&b, "### %s — %s\n", at.Format("15:04"), speaker)
	fmt.Fprintf(&b, "<!-- mem:id=%s channel=%s audience=%s kind=%s prov=%s", e.ID, channel, e.Audience, e.Kind, e.Provenance)
	if e.SourceRef != "" {
		fmt.Fprintf(&b, " ref=%s", e.SourceRef)
	}
	b.WriteString(" -->\n")
	if e.Provenance == memory.ProvenanceUntrusted {
		b.WriteString("> [!untrusted]\n")
	}
	b.WriteString(fence + "text\n")
	b.WriteString(e.Body)
	if !strings.HasSuffix(e.Body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(fence + "\n\n")
	return b.String()
}

func speakerOf(e memory.Entry) string {
	switch e.Provenance {
	case memory.ProvenanceOwner:
		return "owner"
	case memory.ProvenanceAgent:
		return "agent"
	default:
		return string(e.Provenance)
	}
}

// FlushFunc adapts a compaction Flush callback: each transcript message the
// summarization is about to drop becomes a compaction-kind audit entry —
// captured for the record, promotion-ineligible by kind (SPEC-0085 R2.6).
func (d *Daily) FlushFunc(sessionID string) func([]map[string]any) {
	return func(dropped []map[string]any) {
		for i, m := range dropped {
			body, _ := m["content"].(string)
			_ = d.Append(memory.Entry{
				ID:         fmt.Sprintf("compact-%s-%d", sessionID, i),
				Kind:       memory.KindCompaction,
				Provenance: memory.ProvenanceAgent,
				Audience:   memory.AudiencePrivate,
				SourceRef:  sessionID,
				Body:       body,
				ObservedAt: time.Now().UTC(),
			}, "compaction")
		}
	}
}

// OpenFlush returns a compaction Flush bound to the vault at root — nil when
// the vault cannot open, so a capture hiccup never fails the compact itself.
func OpenFlush(root string, tz *time.Location, sessionID string) func([]map[string]any) {
	d, err := OpenDaily(root, tz)
	if err != nil {
		return nil
	}
	return d.FlushFunc(sessionID)
}
