package toolhub

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// adminAuth resolves a directly enrolled bearer carrying the "admin" scope.
// The control-plane bearer + X-Hub-Principal path deliberately does not
// reach this surface: it resolves another principal's envelope, which can
// never be the caller's own authority.
func (g *Gateway) adminAuth(r *http.Request) (identity.Envelope, bool, bool) {
	value := r.Header.Get("Authorization")
	token, ok := g.matchToken(value)
	if !ok {
		return identity.Envelope{}, false, value == "" || value == "Bearer"
	}
	g.mu.Lock()
	auth := g.Tokens[token]
	g.mu.Unlock()
	if !auth.HasScope("admin") {
		return auth, false, false
	}
	return auth, true, false
}

// adminField is a credential declaration's name — never its value.
type adminField struct {
	Name string `json:"name"`
}

// adminOnboardingView is the whitelist DTO for the admin surface (issue
// 141): ids, phases, finalizers and credential names — never nonces, wizard
// or device codes, authorization URLs or credential locators.
type adminOnboardingView struct {
	OnboardingID string          `json:"onboarding_id"`
	PrincipalID  string          `json:"principal_id"`
	Phase        string          `json:"phase"`
	Error        string          `json:"error,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	DefinitionID string          `json:"definition_id,omitempty"`
	SourceURL    string          `json:"source_url,omitempty"`
	CommitSHA    string          `json:"commit_sha,omitempty"`
	Required     []adminField    `json:"required,omitempty"`
	Finalizers   []api.Finalizer `json:"finalizers,omitempty"`
}

func adminOnboardingOf(o api.Onboarding) adminOnboardingView {
	code := o.ErrorCode
	switch code {
	case "", api.OpCodeAmbiguousTarget, api.OpCodeAmbiguousEntrypoint, api.OpCodeAmbiguousCredentialContract, api.OpCodeUnsupportedAuth, api.OpCodeProbeFailed, api.OpCodeCleanupIncomplete, api.OpCodeNotPermitted, api.OpCodeQuota, "unsupported_credential_contract":
	default:
		code = "unclassified_failure"
	}
	if code == "" && o.Error != "" {
		code = "unclassified_failure"
	}
	finalizers := make([]api.Finalizer, 0, len(o.Finalizers))
	for _, f := range o.Finalizers {
		if f.Error != "" {
			f.Error = "recorded failure"
		}
		finalizers = append(finalizers, f)
	}
	required := make([]adminField, 0, len(o.Required))
	for _, h := range o.Required {
		required = append(required, adminField{Name: h.Name})
	}
	return adminOnboardingView{
		OnboardingID: o.OnboardingID, PrincipalID: o.PrincipalID, Phase: o.Phase,
		Error: strings.ReplaceAll(code, "_", " "), ErrorCode: code, DefinitionID: o.DefinitionID,
		SourceURL: o.SourceURL, CommitSHA: o.CommitSHA,
		Required: required, Finalizers: finalizers,
	}
}

// adminMinInterval paces admin calls; adminMu serializes them (issue 141:
// deliberately slow by construction).
var adminMinInterval = time.Second

// adminGate holds one admin call at a time, at most one per interval.
// Callers must defer adminDone.
func (g *Gateway) adminGate() {
	g.adminMu.Lock()
	if since := time.Since(g.adminLast); since < adminMinInterval {
		time.Sleep(adminMinInterval - since)
	}
	g.adminLast = time.Now()
}

func (g *Gateway) adminDone() { g.adminMu.Unlock() }

// adminFetchSource is a seam so tests can replay a pinned context instead of
// hitting GitHub; production keeps the real fetch.
var adminFetchSource = api.FetchRepositoryArtifactContext

// adminInspectHit is one bounded grep snippet: the matched bytes onward,
// never a whole file body.
type adminInspectHit struct {
	Path    string `json:"path"`
	Snippet string `json:"snippet"`
}

func (g *Gateway) serveAdminInspectSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok, anonymous := g.adminAuth(r)
	if anonymous {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		OnboardingID string `json:"onboarding_id"`
		Grep         string `json:"grep"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil || req.OnboardingID == "" || strings.ContainsAny(req.OnboardingID, `/\`) || strings.ContainsAny(req.Grep, "\r\n") {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	g.adminGate()
	defer g.adminDone()
	if err := g.audit("admin.diagnostics", map[string]string{"admin": auth.PrincipalID, "target": "inspect:" + req.OnboardingID}); err != nil {
		http.Error(w, "audit unavailable", http.StatusServiceUnavailable)
		return
	}
	onboarding, err := g.Store.OnboardingRecord(req.OnboardingID)
	if errors.Is(err, api.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}
	data, err := adminFetchSource(r.Context(), api.ArtifactSource{
		Repository: onboarding.SourceURL, CommitSHA: onboarding.CommitSHA,
	}, 8<<20)
	if err != nil {
		http.Error(w, "source fetch failed", http.StatusBadGateway)
		return
	}
	files := make([]string, 0, 64)
	hits := make([]adminInspectHit, 0, 16)
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err != nil || hdr == nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if len(files) < 512 {
			files = append(files, hdr.Name)
		}
		if req.Grep != "" && len(hits) < 64 {
			if body, err := io.ReadAll(tr); err == nil {
				if at := bytes.Index(body, []byte(req.Grep)); at >= 0 {
					hits = append(hits, adminInspectHit{
						Path:    hdr.Name,
						Snippet: req.Grep,
					})
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"files": files, "hits": hits})
}

func (g *Gateway) serveAdminOnboardings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok, anonymous := g.adminAuth(r)
	if anonymous {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	g.adminGate()
	defer g.adminDone()
	if err := g.audit("admin.diagnostics", map[string]string{"admin": auth.PrincipalID, "target": "onboardings"}); err != nil {
		http.Error(w, "audit unavailable", http.StatusServiceUnavailable)
		return
	}
	list, err := g.Store.ListOnboardings()
	if err != nil {
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}
	views := make([]adminOnboardingView, 0, len(list))
	for _, o := range list {
		views = append(views, adminOnboardingView{OnboardingID: o.OnboardingID, PrincipalID: o.PrincipalID, Phase: o.Phase})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"onboardings": views})
}

func (g *Gateway) serveAdminDiagnose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok, anonymous := g.adminAuth(r)
	if anonymous {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/admin/diagnose/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	g.adminGate()
	defer g.adminDone()
	if err := g.audit("admin.diagnostics", map[string]string{"admin": auth.PrincipalID, "target": id}); err != nil {
		http.Error(w, "audit unavailable", http.StatusServiceUnavailable)
		return
	}
	onboarding, err := g.Store.OnboardingRecord(id)
	if errors.Is(err, api.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(adminOnboardingOf(onboarding))
}
