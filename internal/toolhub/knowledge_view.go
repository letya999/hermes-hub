package toolhub

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

const knowledgeLinkTTL = time.Hour

type knowledgeLink struct {
	Owner   identity.Envelope `json:"owner"`
	Issued  int64             `json:"issued"`
	Expires int64             `json:"expires"`
	Nonce   string            `json:"nonce"`
}

// knowledgeOpen is called only through the admitted private model tool. The
// signed link grants a human read view, with live rights checked per request.
func (g *Gateway) knowledgeOpen(ctx context.Context, auth identity.Envelope) (map[string]any, error) {
	if err := g.requireMemoryAdmission(ctx, auth, api.CallCorrelationFrom(ctx)); err != nil {
		return nil, err
	}
	if g.Control == nil || len(g.primaryToken) < 32 {
		return map[string]any{"status": "unavailable"}, nil
	}
	origin, err := knowledgeOrigin(g.Control.origin())
	if err != nil {
		return map[string]any{"status": "unavailable"}, nil
	}
	now := g.Control.now()
	link := knowledgeLink{Owner: auth, Issued: now.Unix(), Expires: now.Add(knowledgeLinkTTL).Unix(), Nonce: api.RandomNonce()}
	encoded, err := json.Marshal(link)
	if err != nil {
		return nil, err
	}
	payload := base64.RawURLEncoding.EncodeToString(encoded)
	mac := hmac.New(sha256.New, []byte(g.primaryToken))
	_, _ = mac.Write([]byte("knowledge-link-v1\x00" + payload))
	token := payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return map[string]any{"status": "ok", "url": origin + "/knowledge?t=" + url.QueryEscape(token), "expires_at": now.Add(knowledgeLinkTTL).UTC().Format(time.RFC3339)}, nil
}

func knowledgeOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", api.ErrInvalid
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return "", api.ErrInvalid
	}
	return strings.TrimRight(raw, "/"), nil
}

func (g *Gateway) verifyKnowledgeLink(token string, now time.Time) (identity.Envelope, error) {
	if len(g.primaryToken) < 32 || len(token) > 4096 {
		return identity.Envelope{}, api.ErrUnauthorized
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return identity.Envelope{}, api.ErrUnauthorized
	}
	mac := hmac.New(sha256.New, []byte(g.primaryToken))
	_, _ = mac.Write([]byte("knowledge-link-v1\x00" + parts[0]))
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		return identity.Envelope{}, api.ErrUnauthorized
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return identity.Envelope{}, api.ErrUnauthorized
	}
	var link knowledgeLink
	if json.Unmarshal(data, &link) != nil || link.Nonce == "" || link.Expires-link.Issued != int64(knowledgeLinkTTL.Seconds()) || link.Owner.Validate(link.Owner.PrincipalID, link.Owner.ContextID, link.Owner.RuntimeID, link.Owner.PolicyVersion) != nil {
		return identity.Envelope{}, api.ErrUnauthorized
	}
	if now.Unix() >= link.Expires {
		return identity.Envelope{}, api.ErrLinkGone
	}
	return link.Owner, nil
}

var knowledgePage = template.Must(template.New("knowledge").Parse(`<!doctype html><html lang="{{.Lang}}"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title><style>body{font:16px/1.5 system-ui,sans-serif;max-width:760px;margin:48px auto;padding:0 20px;color:#17202a}h1{font-size:28px}form{display:flex;gap:8px;margin:24px 0}input[type=search]{flex:1;padding:10px;border:1px solid #aab3bd;border-radius:8px}button{padding:10px 18px;border:0;border-radius:8px;background:#175cd3;color:white}article{border-top:1px solid #dfe4ea;padding:16px 0}small{color:#52606d}pre{white-space:pre-wrap;overflow-wrap:anywhere;font:inherit}a{color:#175cd3}</style><h1>{{.Title}}</h1><p>{{.Intro}}</p><form method="get" action="/knowledge"><input type="hidden" name="t" value="{{.Token}}"><input type="search" name="q" value="{{.Query}}" maxlength="256" aria-label="{{.Search}}"><button>{{.Search}}</button></form>{{if .Detail}}<p><a href="{{.Back}}">{{.BackLabel}}</a></p><article><small>{{.Detail.Date}} · {{.Detail.Kind}}</small><pre>{{.Detail.Text}}</pre></article>{{else}}{{range .Hits}}<article><small>{{.Date}} · {{.Kind}}</small><p><a href="{{.URL}}">{{.Excerpt}}</a></p></article>{{else}}{{if .Query}}<p>{{$.Empty}}</p>{{end}}{{end}}{{end}}</html>`))

func (g *Gateway) serveKnowledge(w http.ResponseWriter, r *http.Request) {
	fail := func(key string, status int) { http.Error(w, locale.T(pageLang(r), key), status) }
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	if !loopbackHTTP(r) {
		fail("page.knowledge-forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodGet {
		fail("page.knowledge-bad", http.StatusMethodNotAllowed)
		return
	}
	if g == nil || g.Store == nil || g.Vaults == nil {
		fail("page.knowledge-unavailable", http.StatusServiceUnavailable)
		return
	}
	now := time.Now()
	if g.Control != nil {
		now = g.Control.now()
	}
	token := r.URL.Query().Get("t")
	auth, err := g.verifyKnowledgeLink(token, now)
	if errors.Is(err, api.ErrLinkGone) {
		fail("page.knowledge-expired", http.StatusGone)
		return
	}
	if err != nil {
		fail("page.knowledge-forbidden", http.StatusForbidden)
		return
	}
	q, id := strings.TrimSpace(r.URL.Query().Get("q")), strings.TrimSpace(r.URL.Query().Get("id"))
	if len([]rune(q)) > 256 || len(id) > 256 || (q != "" && id != "") {
		fail("page.knowledge-bad", http.StatusBadRequest)
		return
	}
	if err := memoryEnrollment(g.PassportRoot, auth.PrincipalID, auth.Environment); err != nil {
		fail("page.knowledge-forbidden", http.StatusForbidden)
		return
	}
	if g.AuditWrite == nil {
		fail("page.knowledge-unavailable", http.StatusServiceUnavailable)
		return
	}
	activation, err := g.requireActivated(auth)
	if err != nil {
		fail("page.knowledge-forbidden", http.StatusForbidden)
		return
	}
	u, err := g.Vaults.Resolve(auth.PrincipalID, auth.Environment)
	if err != nil {
		fail("page.knowledge-unavailable", http.StatusServiceUnavailable)
		return
	}
	var result map[string]any
	err = u.Locked(func(root string) error {
		return g.Store.AuthorizeProjectedCall(auth, "memory_open", nil, func(projected api.ProjectedTool, effective api.EffectiveBinding) error {
			if effective.CapabilityProfileRevision != activation.profileRevision || g.checkActivePassport(auth, activation.revision) != nil || memoryEnrollment(g.PassportRoot, auth.PrincipalID, auth.Environment) != nil {
				return api.ErrUnauthorized
			}
			fields := mergeAudit(auditFields(auth, projected, effective, "admit"), api.CallCorrelation{ToolCallID: api.NewToolCallID()})
			if err := g.audit("admit", fields); err != nil {
				return err
			}
			ctx := context.WithValue(r.Context(), memoryVaultKey{}, memoryVaultPin{gateway: g, auth: auth, root: root})
			if id != "" {
				result, err = g.memoryRead(ctx, auth, map[string]any{"id": id})
			} else if q != "" {
				result, err = g.memorySearch(ctx, auth, map[string]any{"query": q, "limit": 25})
			}
			if err != nil {
				fields["outcome"] = "backend-error"
				_ = g.audit("deny", fields)
				return err
			}
			fields["outcome"] = "ok"
			fields["execution"] = "succeeded"
			return g.audit("allow", fields)
		})
	})
	if err != nil {
		fail("page.knowledge-forbidden", http.StatusForbidden)
		return
	}
	lang := pageLang(r)
	page := struct {
		Lang, Title, Intro, Search, BackLabel, Empty, Token, Query, Back string
		Hits                                                             []struct{ Date, Kind, Excerpt, URL string }
		Detail                                                           *struct{ Date, Kind, Text string }
	}{Lang: string(lang), Title: locale.T(lang, "page.knowledge-title"), Intro: locale.T(lang, "page.knowledge-intro"), Search: locale.T(lang, "page.knowledge-search"), BackLabel: locale.T(lang, "page.knowledge-back"), Empty: locale.T(lang, "page.knowledge-empty"), Token: token, Query: q, Back: "/knowledge?t=" + url.QueryEscape(token)}
	if id != "" {
		if result == nil || result["status"] == "not_found" {
			fail("page.knowledge-empty", http.StatusNotFound)
			return
		}
		hit, _ := result["hit"].(map[string]any)
		if hit == nil {
			fail("page.knowledge-unavailable", http.StatusServiceUnavailable)
			return
		}
		page.Detail = &struct{ Date, Kind, Text string }{api.ArgString(hit, "date"), api.ArgString(hit, "kind"), api.ArgString(hit, "text")}
	} else if result != nil {
		if result["status"] == "unavailable" {
			fail("page.knowledge-unavailable", http.StatusServiceUnavailable)
			return
		}
		hits, _ := result["hits"].([]map[string]any)
		for _, hit := range hits {
			page.Hits = append(page.Hits, struct{ Date, Kind, Excerpt, URL string }{
				api.ArgString(hit, "date"), api.ArgString(hit, "kind"), api.ArgString(hit, "excerpt"),
				"/knowledge?t=" + url.QueryEscape(token) + "&id=" + url.QueryEscape(api.ArgString(hit, "id")),
			})
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := knowledgePage.Execute(w, page); err != nil {
		return
	}
}
