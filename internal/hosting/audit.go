package hosting

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
)

type auditEvent struct {
	At          time.Time `json:"at"`
	Owner       string    `json:"owner"`
	Environment string    `json:"environment"`
	Site        string    `json:"site"`
	Revision    uint64    `json:"revision"`
	Operation   string    `json:"operation"`
	Kind        string    `json:"kind"`
	Outcome     string    `json:"outcome"`
	Bytes       int64     `json:"bytes"`
	DurationMS  int64     `json:"duration_ms"`
}

func (s *Service) auditFiles() ([]string, int64, error) {
	entries, err := fs.ReadDir(s.root.FS(), ".")
	if err != nil {
		return nil, 0, ErrUnavailable
	}
	var files []string
	var total int64
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "audit-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil, 0, ErrUnavailable
		}
		if _, err = time.Parse("audit-2006-01-02.jsonl", entry.Name()); err != nil {
			return nil, 0, ErrUnavailable
		}
		total += info.Size()
		files = append(files, entry.Name())
	}
	if total > s.cfg.AuditBytes {
		return nil, total, ErrUnavailable
	}
	return files, total, nil
}
func (s *Service) appendAudit(e auditEvent, reserve int64) error {
	if s.auditFault {
		return ErrUnavailable
	}
	data, err := json.Marshal(e)
	if err != nil || len(data) > 1023 {
		return ErrUnavailable
	}
	_, total, err := s.auditFiles()
	lifecycleReserve := int64(s.cfg.MaxSites * 2048)
	if e.Kind == "unpublish" || e.Kind == "expire" {
		lifecycleReserve = 0
	}
	if err != nil || total+int64(len(data)+1)+s.auditReserved+reserve+lifecycleReserve > s.cfg.AuditBytes {
		return ErrUnavailable
	}
	filename := s.now().UTC().Format("audit-2006-01-02.jsonl")
	f, err := s.root.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		s.auditFault = true
		return ErrUnavailable
	}
	defer f.Close()
	if _, err = f.Write(append(data, '\n')); err != nil {
		s.auditFault = true
		return ErrUnavailable
	}
	if f.Sync() != nil || syncDirectory(s.cfg.Store) != nil {
		s.auditFault = true
		return ErrUnavailable
	}
	return nil
}
func (s *Service) audit(kind, name string, revision uint64, outcome string, bytes int64) error {
	op, err := randomID()
	if err != nil {
		return err
	}
	return s.appendAudit(auditEvent{At: s.now(), Owner: s.cfg.Owner, Environment: s.cfg.Environment, Site: name, Revision: revision, Operation: op, Kind: kind, Outcome: outcome, Bytes: bytes}, 0)
}

// beginAudit reserves the completion record before any admitted work.
func (s *Service) beginAudit(kind, name string, revision uint64) (func(string, int64) error, error) {
	op, err := randomID()
	if err != nil {
		return nil, err
	}
	e := auditEvent{At: s.now(), Owner: s.cfg.Owner, Environment: s.cfg.Environment, Site: name, Revision: revision, Operation: op, Kind: kind, Outcome: "admitted"}
	if err = s.appendAudit(e, 1024); err != nil {
		return nil, err
	}
	s.auditReserved += 1024
	started := time.Now()
	completed := false
	return func(outcome string, n int64) error {
		if completed {
			return ErrConflict
		}
		s.auditReserved -= 1024
		e.At = s.now()
		e.Outcome = outcome
		e.Bytes = n
		e.DurationMS = time.Since(started).Milliseconds()
		if err := s.appendAudit(e, 0); err != nil {
			s.auditReserved += 1024
			return err
		}
		completed = true
		return nil
	}, nil
}
func (s *Service) recoverAudit() error {
	files, _, err := s.auditFiles()
	if err != nil {
		return err
	}
	pending := map[string]auditEvent{}
	for _, name := range files {
		f, err := s.root.Open(name)
		if err != nil {
			return ErrUnavailable
		}
		scanner := bufio.NewScanner(io.LimitReader(f, s.cfg.AuditBytes+1))
		scanner.Buffer(make([]byte, 1024), 2048)
		for scanner.Scan() {
			var e auditEvent
			if json.Unmarshal(scanner.Bytes(), &e) != nil || e.Owner != s.cfg.Owner || e.Environment != s.cfg.Environment || !id.MatchString(e.Operation) || e.Site != "" && !id.MatchString(e.Site) {
				_ = f.Close()
				return ErrUnavailable
			}
			if e.Outcome == "admitted" {
				pending[e.Operation] = e
			} else {
				delete(pending, e.Operation)
			}
			if len(pending) > 2048 {
				_ = f.Close()
				return ErrUnavailable
			}
		}
		err = scanner.Err()
		_ = f.Close()
		if err != nil {
			return ErrUnavailable
		}
	}
	for _, e := range pending {
		e.At = s.now()
		e.Outcome = "interrupted"
		if err = s.appendAudit(e, 0); err != nil {
			return err
		}
	}
	return s.expireAudit()
}
func (s *Service) expireAudit() error {
	files, _, err := s.auditFiles()
	if err != nil {
		return err
	}
	cutoff := s.now().UTC().Add(-7 * 24 * time.Hour)
	for _, name := range files {
		date, _ := time.Parse("audit-2006-01-02.jsonl", name)
		if !date.Add(24 * time.Hour).After(cutoff) {
			if s.root.Remove(name) != nil {
				return ErrUnavailable
			}
		}
	}
	return nil
}
