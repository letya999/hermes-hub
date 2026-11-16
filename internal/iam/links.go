package iam

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrLinkConflict denies a binding that would give two principals one
// channel identity, or one verified account two transports.
var ErrLinkConflict = errors.New("iam link conflict")

// Link is one verified binding: an issuer+subject pair proven at the
// IdP bound to a channel transport id and the principal it resolves to.
// A single verified account may hold several transports (telegram and
// slack) but never two principals.
type Link struct {
	Issuer      string    `json:"issuer"`
	Subject     string    `json:"subject"`
	Principal   string    `json:"principal"`
	TransportID string    `json:"transport_id"`
	LinkedAt    time.Time `json:"linked_at"`
}

// LinkStore persists verified bindings beside the spool — a plain JSON
// file replaced atomically like supervisor.json, plus an append-only
// audit journal. Subjects never reach the audit file raw; a hash
// prefix correlates events without storing the corporate account id.
type LinkStore struct {
	mu    sync.Mutex
	path  string
	audit *os.File
	links map[string]Link // transport id -> link
	Now   func() time.Time
}

func OpenLinkStore(dir string) (*LinkStore, error) {
	s := &LinkStore{path: filepath.Join(dir, "iam-links.json"), links: map[string]Link{}}
	if data, err := os.ReadFile(s.path); err == nil && len(data) > 0 {
		var list []Link
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("iam link store corrupt: %w", err)
		}
		for _, l := range list {
			s.links[l.TransportID] = l
		}
	}
	audit, err := os.OpenFile(filepath.Join(dir, "iam-links-audit.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	s.audit = audit
	return s, nil
}

// Close releases the audit journal handle.
func (s *LinkStore) Close() error {
	if s.audit == nil {
		return nil
	}
	return s.audit.Close()
}

func (s *LinkStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func subjectHash(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "|" + subject))
	return hex.EncodeToString(sum[:8])
}

func (s *LinkStore) record(event, transportID, principal, issuer, subject, detail string) {
	if s.audit == nil {
		return
	}
	entry, _ := json.Marshal(map[string]string{
		"at": s.now().UTC().Format(time.RFC3339), "event": event,
		"transport_id": transportID, "principal": principal,
		"issuer": issuer, "subject_hash": subjectHash(issuer, subject), "detail": detail,
	})
	_, _ = s.audit.Write(append(entry, '\n'))
}

func (s *LinkStore) persistLocked() error {
	list := make([]Link, 0, len(s.links))
	for _, l := range s.links {
		list = append(list, l)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Link binds a verified account to a transport id. Conflict classes:
// the transport is already bound to another principal, or the same
// verified account already claims a different principal. Re-linking the
// identical triple is idempotent.
func (s *LinkStore) Link(issuer, subject, principal, transportID string) error {
	if issuer == "" || subject == "" || principal == "" || transportID == "" {
		return errors.New("link requires issuer, subject, principal and transport")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.links[transportID]; ok {
		if existing.Issuer == issuer && existing.Subject == subject && existing.Principal == principal {
			return nil
		}
		s.record("link_denied", transportID, principal, issuer, subject, "transport already bound")
		return ErrLinkConflict
	}
	for _, l := range s.links {
		if l.Issuer == issuer && l.Subject == subject && l.Principal != principal {
			s.record("link_denied", transportID, principal, issuer, subject, "subject bound to another principal")
			return ErrLinkConflict
		}
	}
	s.links[transportID] = Link{Issuer: issuer, Subject: subject, Principal: principal, TransportID: transportID, LinkedAt: s.now().UTC()}
	if err := s.persistLocked(); err != nil {
		return err
	}
	s.record("link", transportID, principal, issuer, subject, "")
	return nil
}

// Resolve answers which principal a channel transport resolves to via
// a verified link — the second mapping source behind the static
// users.yaml binding.
func (s *LinkStore) Resolve(transportID string) (Link, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.links[transportID]
	return l, ok
}

// Unlink removes a transport binding (account recovery path). Returns
// false when nothing was bound.
func (s *LinkStore) Unlink(transportID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.links[transportID]
	if !ok {
		return false
	}
	delete(s.links, transportID)
	if err := s.persistLocked(); err != nil {
		return false
	}
	s.record("unlink", transportID, l.Principal, l.Issuer, l.Subject, "")
	return true
}

// LinksForPrincipal lists a principal's verified bindings — the
// operator's recovery view.
func (s *LinkStore) LinksForPrincipal(principal string) []Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Link
	for _, l := range s.links {
		if l.Principal == principal {
			out = append(out, l)
		}
	}
	return out
}
