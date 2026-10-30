package communication

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// skillDraft is one pending skill-store mutation awaiting the owner's click on
// a nonce-protected card (SPEC-0083 R5.3). The tool never publishes; this
// record is the single-use bridge between the agent's draft and the runtime's
// /v1/skill-apply.
type skillDraft struct {
	OwnerID      string
	Envelope     identity.Envelope
	DraftID      string
	SkillName    string
	Scope        string
	Summary      string
	Diff         string
	Findings     string
	Provenance   string
	BaseRevision string
	NonceHash    string
	State        string
	ExpiresAt    time.Time
}

type skillDraftRegistry struct {
	mu   sync.Mutex
	m    map[string]skillDraft
	path string // empty: memory-only (tests)
}

// The registry persists beside the spool — a gateway restart used to murder
// every pending approval card and the owner's click landed on a bare 401.
// Only nonce hashes are stored; the nonce itself stays in the link.
func (r *skillDraftRegistry) load(path string, now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.path = path
	r.m = map[string]skillDraft{}
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if json.Unmarshal(body, &r.m) != nil || r.m == nil {
		r.m = map[string]skillDraft{}
		return
	}
	for id, d := range r.m {
		if now().After(d.ExpiresAt) {
			delete(r.m, id)
		}
	}
}

// persistLocked snapshots the registry atomically; callers hold r.mu. A
// write failure degrades to pre-persistence behavior (cards die on
// restart) rather than failing the owner's action, so it only logs.
func (r *skillDraftRegistry) persistLocked() {
	if r.path == "" {
		return
	}
	body, err := json.Marshal(r.m)
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		log.Printf("skill draft registry persist: %v", err)
		return
	}
	_ = os.Rename(tmp, r.path)
}

type skillDraftRequest struct {
	DraftID      string `json:"draft_id"`
	SkillName    string `json:"skill_name"`
	Scope        string `json:"scope"`
	Summary      string `json:"summary"`
	Diff         string `json:"diff"`
	Findings     string `json:"findings"`
	Provenance   string `json:"provenance"`
	BaseRevision string `json:"base_revision"`
}

// Cards are the only path that turns a draft into a published skill, and
// drafts on disk never expire — a 30-minute link window just made owners
// re-issue installs. 72h bounds a leaked bearer link's window while letting
// the owner click whenever the card actually reaches them.
const skillDraftTTL = 72 * time.Hour

func (g *Gateway) handleSkillDraftRequest(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var request skillDraftRequest
	r.Body = http.MaxBytesReader(w, r.Body, 512*1024)
	if decodeJSON(r, &request) != nil {
		http.Error(w, "invalid draft request", http.StatusBadRequest)
		return
	}
	origin, err := g.formOrigin()
	if err != nil {
		http.Error(w, "draft form unavailable", http.StatusServiceUnavailable)
		return
	}
	nonce, err := randomFormToken()
	if err != nil {
		http.Error(w, "draft form unavailable", http.StatusServiceUnavailable)
		return
	}
	id, err := randomFormToken()
	if err != nil {
		http.Error(w, "draft form unavailable", http.StatusServiceUnavailable)
		return
	}
	hash := sha256.Sum256([]byte(nonce))
	g.drafts.mu.Lock()
	if g.drafts.m == nil {
		g.drafts.m = map[string]skillDraft{}
	}
	g.drafts.m[id] = skillDraft{
		OwnerID:      caller.PrincipalID,
		Envelope:     caller,
		DraftID:      request.DraftID,
		SkillName:    request.SkillName,
		Scope:        request.Scope,
		Summary:      request.Summary,
		Diff:         request.Diff,
		Findings:     request.Findings,
		Provenance:   request.Provenance,
		BaseRevision: request.BaseRevision,
		NonceHash:    hex.EncodeToString(hash[:]),
		State:        "pending",
		ExpiresAt:    g.now().Add(skillDraftTTL),
	}
	g.drafts.persistLocked()
	g.drafts.mu.Unlock()
	writeJSON(w, map[string]string{"input": "protected-form", "form_url": origin + "/skill-drafts/" + id + "?nonce=" + url.QueryEscape(nonce)})
}

func (g *Gateway) serveSkillDraft(w http.ResponseWriter, r *http.Request) {
	if !communicationLoopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/skill-drafts/"), "/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	nonce := r.URL.Query().Get("nonce")
	draft, err := g.skillDraftAttempt(id, nonce)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	lang := locale.LangOf(g.user(draft.OwnerID).WorkspaceDir)

	if r.Method == http.MethodPost {
		switch r.URL.Query().Get("action") {
		case "publish":
			g.skillDraftPublish(w, r, id, draft, lang)
			return
		case "reject":
			if draft.State != "pending" {
				http.Error(w, "draft already handled", http.StatusConflict)
				return
			}
			g.setDraftState(id, "rejected")
			_, _ = io.WriteString(w, "<!doctype html><html lang=\""+string(lang)+"\"><title>"+html.EscapeString(locale.T(lang, "draft.rejected-title"))+"</title><p>"+html.EscapeString(locale.T(lang, "draft.rejected-body", draft.SkillName))+"</p>")
			return
		case "undo":
			g.skillDraftUndo(w, r, id, draft, lang)
			return
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
	}
	if draft.State != "pending" {
		_, _ = io.WriteString(w, "<!doctype html><html lang=\""+string(lang)+"\"><title>"+html.EscapeString(locale.T(lang, "draft.handled-title"))+"</title><p>"+html.EscapeString(locale.T(lang, "draft.handled-body", draft.State))+"</p>")
		return
	}
	var body strings.Builder
	body.WriteString("<!doctype html><html lang=\"" + string(lang) + "\"><meta name=\"referrer\" content=\"no-referrer\"><title>" + html.EscapeString(locale.T(lang, "draft.card-title")) + "</title><h1>" + html.EscapeString(locale.T(lang, "draft.card-heading")) + "</h1>")
	body.WriteString("<p><b>" + html.EscapeString(locale.T(lang, "draft.field-skill")) + "</b> " + html.EscapeString(draft.SkillName) + " &nbsp; <b>" + html.EscapeString(locale.T(lang, "draft.field-scope")) + "</b> " + html.EscapeString(draft.Scope) + "</p>")
	if draft.Summary != "" {
		body.WriteString("<p><b>" + html.EscapeString(locale.T(lang, "draft.field-change")) + "</b> " + html.EscapeString(draft.Summary) + "</p>")
	}
	if draft.Diff != "" {
		body.WriteString("<h2>Diff</h2><pre>" + html.EscapeString(draft.Diff) + "</pre>")
	}
	body.WriteString("<p><b>" + html.EscapeString(locale.T(lang, "draft.field-findings")) + "</b> " + html.EscapeString(draft.Findings) + "</p>")
	body.WriteString("<p><b>" + html.EscapeString(locale.T(lang, "draft.field-provenance")) + "</b> <span class=\"provenance\">" + html.EscapeString(draft.Provenance) + "</span></p>")
	query := "?nonce=" + url.QueryEscape(nonce)
	body.WriteString("<form method=post action=\"" + query + "&action=publish\"><button type=submit>" + html.EscapeString(locale.T(lang, "draft.publish")) + "</button></form>")
	body.WriteString("<form method=post action=\"" + query + "&action=reject\"><button type=submit>" + html.EscapeString(locale.T(lang, "draft.reject")) + "</button></form>")
	_, _ = io.WriteString(w, body.String())
}

// skillDraftAttempt resolves id+nonce to a live attempt; the nonce is the
// bearer secret, so a wrong nonce is indistinguishable from a missing draft —
// replayed or cross-user links get the same unauthorized.
func (g *Gateway) skillDraftAttempt(id, nonce string) (skillDraft, error) {
	g.drafts.mu.Lock()
	defer g.drafts.mu.Unlock()
	draft, ok := g.drafts.m[id]
	if !ok || g.now().After(draft.ExpiresAt) {
		return skillDraft{}, errors.New("draft unavailable")
	}
	hash := sha256.Sum256([]byte(nonce))
	if subtle.ConstantTimeCompare([]byte(draft.NonceHash), []byte(hex.EncodeToString(hash[:]))) != 1 {
		return skillDraft{}, errors.New("draft unavailable")
	}
	return draft, nil
}

// rotateNonce installs a fresh nonce on the attempt and returns it; after a
// publish the undo link gets a new single-use secret and the spent publish
// nonce dies immediately.
func (g *Gateway) rotateNonce(id string) (string, error) {
	nonce, err := randomFormToken()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(nonce))
	g.drafts.mu.Lock()
	defer g.drafts.mu.Unlock()
	draft, ok := g.drafts.m[id]
	if !ok {
		return "", errors.New("draft unavailable")
	}
	draft.NonceHash = hex.EncodeToString(hash[:])
	g.drafts.m[id] = draft
	g.drafts.persistLocked()
	return nonce, nil
}

func (g *Gateway) setDraftState(id, state string) {
	g.drafts.mu.Lock()
	defer g.drafts.mu.Unlock()
	if draft, ok := g.drafts.m[id]; ok {
		draft.State = state
		g.drafts.m[id] = draft
		g.drafts.persistLocked()
	}
}

// skillDraftPublish relays the confirmed apply to the runtime container — the
// only place /skills-store is mounted writable — then re-arms exactly one
// undo under a fresh nonce.
func (g *Gateway) skillDraftPublish(w http.ResponseWriter, r *http.Request, id string, draft skillDraft, lang locale.Lang) {
	if draft.State != "pending" {
		http.Error(w, "draft already handled", http.StatusConflict)
		return
	}
	if err := g.runtimeSkillApply(r, draft, "publish"); err != nil {
		http.Error(w, "apply failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	g.setDraftState(id, "published")
	fresh, err := g.rotateNonce(id)
	if err != nil {
		http.Error(w, "draft unavailable", http.StatusInternalServerError)
		return
	}
	var body strings.Builder
	body.WriteString("<!doctype html><html lang=\"" + string(lang) + "\"><title>" + html.EscapeString(locale.T(lang, "draft.published-title")) + "</title><p>" + html.EscapeString(locale.T(lang, "draft.published-body", draft.SkillName)) + "</p>")
	body.WriteString("<form method=post action=\"?nonce=" + url.QueryEscape(fresh) + "&action=undo\"><button type=submit>" + html.EscapeString(locale.T(lang, "draft.undo-button")) + "</button></form>")
	_, _ = io.WriteString(w, body.String())
}

func (g *Gateway) skillDraftUndo(w http.ResponseWriter, r *http.Request, id string, draft skillDraft, lang locale.Lang) {
	if draft.State != "published" {
		http.Error(w, "nothing to undo", http.StatusConflict)
		return
	}
	if err := g.runtimeSkillApply(r, draft, "rollback"); err != nil {
		http.Error(w, "rollback failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	g.setDraftState(id, "undone")
	_, _ = io.WriteString(w, "<!doctype html><html lang=\""+string(lang)+"\"><title>"+html.EscapeString(locale.T(lang, "draft.undone-title"))+"</title><p>"+html.EscapeString(locale.T(lang, "draft.undone-body", draft.SkillName))+"</p>")
}

// runtimeSkillApply is the control call into the runtime container's
// /v1/skill-apply; communication itself never touches the store.
// skillApplyTimeout bounds the apply/rollback relay into the supervisor.
// It must cover a cold runtime spawn, which takes far longer than a warm
// publish.
var skillApplyTimeout = 2 * time.Minute

func (g *Gateway) runtimeSkillApply(r *http.Request, draft skillDraft, action string) error {
	if g.config.RuntimeURL == "" {
		return errors.New("runtime not configured")
	}
	payload, err := json.Marshal(hubruntime.SkillApplyRequest{
		Envelope: draft.Envelope, OrganizationID: g.config.OrganizationID,
		UserID: draft.OwnerID, ActorID: draft.OwnerID, ScopeID: "user:" + draft.OwnerID,
		Action: action, DraftID: draft.DraftID, SkillName: draft.SkillName,
		Scope: draft.Scope, BaseRevision: draft.BaseRevision, Approver: draft.OwnerID,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/skill-apply", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: skillApplyTimeout}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		// The runtime's 409 body carries the real reason (frontmatter
		// mismatch, consumed draft, base conflict); a bare "apply failed"
		// leaves the owner with no way to fix the draft.
		var detail struct {
			Error string `json:"error"`
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		if json.Unmarshal(body, &detail) == nil && detail.Error != "" {
			return fmt.Errorf("runtime rejected skill apply: %s", detail.Error)
		}
		return fmt.Errorf("runtime rejected skill apply (status %d)", response.StatusCode)
	}
	return nil
}
