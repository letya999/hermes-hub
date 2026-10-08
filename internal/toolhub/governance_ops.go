package toolhub

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// grant_request is the in-conversation half of the unlock ceremony. The host
// created the pending grant out-of-band; this op stamps the fresh
// single-use request nonce and returns the protected form URL. The form is
// the acknowledge step: it lives on the loopback listener, so the model can
// carry the link but only a human at the console can complete it — the
// nonce alone over MCP cannot activate anything.
func (c *ControlPlane) grantRequest(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	if c.Governance == nil {
		return nil, fmt.Errorf("%w: tool governance is not configured", ErrInvalid)
	}
	section := argString(args, "section")
	if section == "" {
		section = SectionUserMCP
	}
	if !sectionIDPattern.MatchString(section) {
		return nil, fmt.Errorf("%w: section", ErrInvalid)
	}
	grantID := argString(args, "grant_id")
	var granted ToolGrant
	err := c.Governance.Update(c.now(), func(doc *Governance) error {
		pending := doc.PendingToolGrants(auth.PrincipalID, section, c.now())
		if grantID != "" {
			pending = nil
			for _, g := range doc.Grants {
				if g.GrantID == grantID {
					if g.PrincipalID != auth.PrincipalID || g.Section != section {
						return fmt.Errorf("%w: grant belongs to another principal or section", ErrUnauthorized)
					}
					pending = append(pending, g)
				}
			}
		}
		if len(pending) == 0 {
			return fmt.Errorf("%w: no pending grant for section %s — the host creates grants with hubctl governance", ErrNotFound, section)
		}
		stamped, err := doc.RequestToolGrant(auth.PrincipalID, pending[0].GrantID, c.now())
		if err != nil {
			return err
		}
		granted = stamped
		return nil
	})
	if err != nil {
		return nil, err
	}
	acknowledgeURL := c.origin() + "/grants/" + url.PathEscape(granted.GrantID) + "?nonce=" + url.QueryEscape(granted.RequestNonce)
	return map[string]any{
		"grant_id":            granted.GrantID,
		"section":             granted.Section,
		"principal_id":        granted.PrincipalID,
		"reason":              granted.Reason,
		"expires_at":          granted.ExpiresAt.Format(time.RFC3339),
		"acknowledge_url":     acknowledgeURL,
		"acknowledge_expires": granted.RequestExpires.Format(time.RFC3339),
		"note":                "Ask the user to open the acknowledge URL and confirm. The grant activates only through that form; repeat the request if the link expires.",
	}, nil
}

// serveGrantAck is the protected-form acknowledge step: GET renders what is
// being granted (section, expiry, reason), POST with the matching request
// nonce activates it. Loopback-only and nonce-bound, same discipline as the
// credential form.
func (g *Gateway) serveGrantAck(w http.ResponseWriter, r *http.Request) {
	if !loopbackHTTP(r) {
		http.Error(w, "loopback only", http.StatusForbidden)
		return
	}
	if g == nil || g.Control == nil || g.Control.Governance == nil {
		http.NotFound(w, r)
		return
	}
	grantID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/grants/"), "/")
	if grantID == "" || strings.Contains(grantID, "/") || !identity.ValidID(grantID) {
		http.NotFound(w, r)
		return
	}
	doc, err := g.Control.Governance.Load()
	if err != nil {
		http.Error(w, "governance unavailable", http.StatusServiceUnavailable)
		return
	}
	var grant *ToolGrant
	for i := range doc.Grants {
		if doc.Grants[i].GrantID == grantID {
			grant = &doc.Grants[i]
			break
		}
	}
	if grant == nil {
		http.NotFound(w, r)
		return
	}
	nonce := r.URL.Query().Get("nonce")
	if r.Method == http.MethodGet {
		if grant.Status != GrantPending || grant.RequestNonce == "" || nonce != grant.RequestNonce ||
			grant.RequestExpires.IsZero() || g.Control.now().After(grant.RequestExpires) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		_, _ = io.WriteString(w, grantAckPage(grant, nonce))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if nonce == "" {
		nonce = r.Form.Get("nonce")
	}
	var acknowledged bool
	updateErr := g.Control.Governance.Update(g.Control.now(), func(doc *Governance) error {
		current, err := doc.AcknowledgeToolGrant(grant.PrincipalID, grantID, nonce, g.Control.now())
		if err != nil {
			return err
		}
		acknowledged = current.Status == GrantActive
		return nil
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if updateErr != nil || !acknowledged {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, credentialResultPage("Grant not activated", "The request nonce is missing, expired or already rotated. Return to the conversation and request the grant again."))
		return
	}
	_, _ = io.WriteString(w, credentialResultPage("Grant activated", "The tool access grant is active until its expiry. Return to the conversation."))
}

func grantAckPage(grant *ToolGrant, nonce string) string {
	return `<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Tool access grant</title><style>
:root{font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;background:#f4f6f8;color:#17202a}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px}main{width:min(620px,100%);background:#fff;border:1px solid #dfe4ea;border-radius:16px;padding:32px;box-shadow:0 16px 42px rgba(23,32,42,.10)}h1{margin:0 0 12px;font-size:26px}p,dl{color:#52606d;line-height:1.6}dt{font-weight:650;color:#17202a}dd{margin:0 0 12px}button{width:100%;border:0;border-radius:10px;padding:13px 18px;background:#175cd3;color:#fff;font:inherit;font-weight:700;cursor:pointer}
</style><main><h1>Tool access grant</h1><dl>` +
		`<dt>Section</dt><dd>` + html.EscapeString(grant.Section) + `</dd>` +
		`<dt>Principal</dt><dd>` + html.EscapeString(grant.PrincipalID) + `</dd>` +
		`<dt>Reason</dt><dd>` + html.EscapeString(grant.Reason) + `</dd>` +
		`<dt>Expires</dt><dd>` + html.EscapeString(grant.ExpiresAt.Format("2006-01-02 15:04:05 UTC")) + `</dd></dl>` +
		`<form method="post"><input type="hidden" name="nonce" value="` + html.EscapeString(nonce) + `">` +
		`<button type="submit">Grant access</button></form>` +
		`<p>This activates the host-approved grant for this principal only. The agent cannot complete this step itself.</p></main></html>`
}
