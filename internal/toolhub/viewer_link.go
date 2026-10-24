package toolhub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// viewerLink is the durable one-time owner-viewer grant for a browser
// connection: nonce-bound, owner-bound, operation-bound, short-TTL, spent on
// first use. The nonce never leaves the store — only the channel notice
// carries it, so no tool result ever exposes link material.
type viewerLink struct {
	ConnectionID string    `json:"connection_id"`
	Nonce        string    `json:"nonce"`
	PrincipalID  string    `json:"principal_id"`
	OperationID  string    `json:"operation_id"`
	ExpiresAt    time.Time `json:"expires_at"`
	Used         bool      `json:"used,omitempty"`
}

// MintViewerLink arms a one-time viewer nonce on an owner-scoped browser
// connection. Only the connection's principal owner may mint — team members
// and other principals are denied; the link binds caller, connection and
// the operation that requested it. A second mint replaces the nonce.
func (s *Store) MintViewerLink(auth identity.Envelope, connectionID, operationID string, now time.Time, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 || ttl > time.Hour {
		return "", time.Time{}, fmt.Errorf("%w: viewer link ttl", api.ErrInvalid)
	}
	s.mu.RLock()
	connection, ok := s.connections[connectionID]
	definition, defErr := s.definitionForConnection(connection)
	s.mu.RUnlock()
	if !ok {
		return "", time.Time{}, fmt.Errorf("%w: connection", api.ErrNotFound)
	}
	if defErr != nil || definition.Execution.EgressClass != api.EgressWeb {
		return "", time.Time{}, fmt.Errorf("%w: viewer links exist only for browser connections", api.ErrInvalid)
	}
	if connection.Status != api.ActiveStatus || connection.Owner.Type != api.PrincipalOwner || connection.Owner.ID != auth.PrincipalID {
		return "", time.Time{}, api.ErrUnauthorized
	}
	link := viewerLink{
		ConnectionID: connectionID,
		Nonce:        api.RandomNonce(),
		PrincipalID:  auth.PrincipalID,
		OperationID:  operationID,
		ExpiresAt:    now.Add(ttl),
	}
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if s.viewerLinks == nil {
		s.viewerLinks = map[string]viewerLink{}
	}
	if err := s.loadViewerLinksLocked(); err != nil {
		return "", time.Time{}, err
	}
	s.viewerLinks[connectionID] = link
	if err := s.persistViewerLinksLocked(); err != nil {
		delete(s.viewerLinks, connectionID)
		if path := s.viewerLinksPath(); path != "" {
			_ = os.Remove(path)
		}
		return "", time.Time{}, err
	}
	return link.Nonce, link.ExpiresAt, nil
}

// SpendViewerLink consumes the one-time nonce: a forged or absent nonce is
// unauthorized, a spent or expired one is gone, a live one marks itself used
// and returns the owning connection.
func (s *Store) SpendViewerLink(connectionID, nonce string, now time.Time) (api.Connection, error) {
	// Lock ordering is s.mu then leaseMu — never the reverse.
	s.mu.RLock()
	connection, found := s.connections[connectionID]
	s.mu.RUnlock()
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if err := s.loadViewerLinksLocked(); err != nil {
		return api.Connection{}, err
	}
	link, ok := s.viewerLinks[connectionID]
	if !ok || nonce == "" || nonce != link.Nonce {
		return api.Connection{}, api.ErrUnauthorized
	}
	if link.Used || !now.Before(link.ExpiresAt) {
		return api.Connection{}, api.ErrLinkGone
	}
	if !found || connection.Status != api.ActiveStatus {
		return api.Connection{}, api.ErrUnauthorized
	}
	link.Used = true
	s.viewerLinks[connectionID] = link
	if err := s.persistViewerLinksLocked(); err != nil {
		// Fail closed: the in-memory mark denies re-use while running, and the
		// stale ledger would leave the nonce spendable after a restart — drop
		// it so a failed spend can never replay (unspent links expire anyway).
		if path := s.viewerLinksPath(); path != "" {
			_ = os.Remove(path)
		}
		return api.Connection{}, err
	}
	return connection, nil
}

// viewerSession is the post-spend handle the rendered page polls with. It is
// memory-only on purpose: a restart drops it and forces a fresh one-time link
// instead of reviving an aged remote-control grant. ExpiresAt slides on use;
// NotAfter caps the whole session.
type viewerSession struct {
	SessionID    string
	ConnectionID string
	PrincipalID  string
	ExpiresAt    time.Time
	NotAfter     time.Time
}

const (
	viewerSessionSlide = 15 * time.Minute
	viewerSessionMax   = 2 * time.Hour
)

// OpenViewerSession spends the one-time link and pins a polling session to
// the connection's owning principal. The page gets a session id, never the
// link nonce again; every later forward re-resolves the owner's binding so a
// mid-session disable or revoke closes the channel.
func (s *Store) OpenViewerSession(connectionID, nonce string, now time.Time) (string, error) {
	connection, err := s.SpendViewerLink(connectionID, nonce, now)
	if err != nil {
		return "", err
	}
	if connection.Owner.Type != api.PrincipalOwner {
		return "", api.ErrUnauthorized
	}
	s.mu.RLock()
	hasBinding := false
	for _, binding := range s.bindings {
		if binding.ConnectionID == connectionID && binding.PrincipalID == connection.Owner.ID && binding.Status == api.ActiveStatus {
			hasBinding = true
			break
		}
	}
	s.mu.RUnlock()
	if !hasBinding {
		return "", fmt.Errorf("%w: owner binding", api.ErrNotFound)
	}
	session := viewerSession{
		SessionID: api.RandomNonce(), ConnectionID: connectionID,
		PrincipalID: connection.Owner.ID,
		ExpiresAt:   now.Add(viewerSessionSlide), NotAfter: now.Add(viewerSessionMax),
	}
	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()
	if s.viewerSessions == nil {
		s.viewerSessions = map[string]viewerSession{}
	}
	s.viewerSessions[session.SessionID] = session
	return session.SessionID, nil
}

// viewerToolAllowlist is the bounded forward surface for the owner viewer:
// navigation, snapshot/screenshot reads and the coordinate/element input the
// manual-login flow needs. eval, network, file, pdf and close tools stay out —
// a spent link must never become a scriptable workload proxy.
var viewerToolAllowlist = map[string]bool{
	"browser_navigate": true, "browser_navigate_back": true,
	"browser_tabs": true, "browser_snapshot": true, "browser_find": true,
	"browser_take_screenshot": true, "browser_wait_for": true,
	"browser_console_messages": true, "browser_resize": true,
	"browser_click": true, "browser_hover": true,
	"browser_type": true, "browser_fill_form": true,
	"browser_press_key": true, "browser_select_option": true,
	"browser_handle_dialog":  true,
	"browser_mouse_click_xy": true, "browser_mouse_down": true,
	"browser_mouse_up": true, "browser_mouse_move_xy": true,
	"browser_mouse_wheel": true, "browser_mouse_drag_xy": true,
}

// CallViewerTool resolves the session's owner binding and runs admit under
// the registry read lock, exactly like the projected-call path. The tool must
// be on the viewer allowlist and exist in the admitted definition — the
// session can reach nothing else, regardless of the principal's projection.
func (s *Store) CallViewerTool(sessionID, tool string, now time.Time, admit func(api.ToolSpec, api.EffectiveBinding) error) error {
	if admit == nil {
		return fmt.Errorf("%w: nil admission callback", api.ErrInvalid)
	}
	if !viewerToolAllowlist[tool] {
		return fmt.Errorf("%w: tool is outside the viewer surface", api.ErrUnauthorized)
	}
	s.leaseMu.Lock()
	session, ok := s.viewerSessions[sessionID]
	if !ok {
		s.leaseMu.Unlock()
		return fmt.Errorf("%w: viewer session", api.ErrUnauthorized)
	}
	if !now.Before(session.ExpiresAt) || !now.Before(session.NotAfter) {
		delete(s.viewerSessions, sessionID)
		s.leaseMu.Unlock()
		return api.ErrLinkGone
	}
	session.ExpiresAt = now.Add(viewerSessionSlide)
	s.viewerSessions[sessionID] = session
	s.leaseMu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var candidates []api.ToolBinding
	for _, binding := range s.bindings {
		if binding.ConnectionID == session.ConnectionID && binding.PrincipalID == session.PrincipalID && binding.Status == api.ActiveStatus {
			candidates = append(candidates, binding)
		}
	}
	slices.SortFunc(candidates, func(a, b api.ToolBinding) int { return strings.Compare(a.ToolBindingID, b.ToolBindingID) })
	for _, binding := range candidates {
		auth := identity.Envelope{
			Schema: identity.Schema, PrincipalID: binding.PrincipalID, ExternalIdentityID: "viewer",
			ContextID: binding.ContextID, RuntimeID: binding.RuntimeID,
			ConversationID: "viewer", DeliveryTargetID: "viewer",
			PolicyVersion: binding.PolicyVersion,
		}
		effective, err := s.resolveLocked(auth, binding.ToolBindingID)
		if err != nil {
			continue
		}
		for _, spec := range effective.Definition.Tools {
			if spec.Name == tool {
				return admit(spec, effective)
			}
		}
		return fmt.Errorf("%w: viewer tool not admitted", api.ErrNotFound)
	}
	return fmt.Errorf("%w: owner binding", api.ErrNotFound)
}

func (s *Store) viewerLinksPath() string {
	if s.path == "" {
		return ""
	}
	return s.path + ".viewer-links"
}

func (s *Store) loadViewerLinksLocked() error {
	if s.viewerLinksLoaded {
		return nil
	}
	s.viewerLinksLoaded = true
	path := s.viewerLinksPath()
	if path == "" {
		return nil
	}
	body, err := os.ReadFile(path) // #nosec G304 -- registry-adjacent link ledger under the same trusted path rules.
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var links []viewerLink
	if err := json.Unmarshal(body, &links); err != nil {
		return fmt.Errorf("%w: viewer link ledger", api.ErrInvalid)
	}
	// SpendViewerLink reaches this on a cold process where only
	// MintViewerLink otherwise creates the map — a restart with a live
	// ledger would panic on the nil-map write and the owner could never
	// open a session again.
	if s.viewerLinks == nil {
		s.viewerLinks = map[string]viewerLink{}
	}
	for _, link := range links {
		if link.ConnectionID == "" || link.Nonce == "" || link.PrincipalID == "" {
			return fmt.Errorf("%w: viewer link ledger record", api.ErrInvalid)
		}
		s.viewerLinks[link.ConnectionID] = link
	}
	return nil
}

func (s *Store) persistViewerLinksLocked() error {
	path := s.viewerLinksPath()
	if path == "" {
		return nil
	}
	if err := api.SafeStorePath(path); err != nil {
		return err
	}
	links := make([]viewerLink, 0, len(s.viewerLinks))
	for _, link := range s.viewerLinks {
		links = append(links, link)
	}
	slices.SortFunc(links, func(a, b viewerLink) int { return strings.Compare(a.ConnectionID, b.ConnectionID) })
	body, err := json.Marshal(links)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".viewer-links-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
