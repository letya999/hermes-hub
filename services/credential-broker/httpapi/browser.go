package httpapi

import (
	"html/template"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/broker"
	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/provider"
)

var formPage = template.Must(template.New("form").Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Подключение учётных данных</title><link rel="stylesheet" href="/form.css"></head>
<body><main><p class="eyebrow">HERMES · CREDENTIAL BROKER</p><h1>{{.View.Title}}</h1>
{{if not .View.Approved}}<p>Подтвердите этот код в том же доверенном канале Communication Hub, где начали подключение. Одна ссылка сама по себе не даёт доступ к форме.</p><p class="code">{{.View.Code}}</p><p>Запрос: <code>{{.View.RequestID}}</code></p><p>После подтверждения обновите эту страницу.</p><a href="/connect/{{.View.RequestID}}">Проверить подтверждение</a>
{{else if eq .View.Status "pending"}}<p>Значения будут переданы только брокеру и настроенному хранилищу, не в чат или модель.</p>
<form method="post" enctype="multipart/form-data" action="/connect/{{.View.RequestID}}/submit"><input type="hidden" name="csrf" value="{{.CSRF}}">
{{range .View.Fields}}<label for="f_{{.ID}}">{{.Label}}</label>{{if or (eq .Kind "blob") (eq .Kind "json") (eq .Kind "pem")}}<input id="f_{{.ID}}" name="{{.ID}}" type="file" {{if .Required}}required{{end}}><small>До {{.MaxBytes}} байт. Имя загружаемого файла не используется как путь.</small>{{else}}<input id="f_{{.ID}}" name="{{.ID}}" type="{{if eq .Kind "secret"}}password{{else}}text{{end}}" autocomplete="off" spellcheck="false" maxlength="{{.MaxBytes}}" {{if .Required}}required{{end}}>{{end}}{{end}}
<button type="submit">Сохранить и продолжить</button></form>
{{else if eq .View.Status "authorizing"}}<p>Файлы приняты. Подтвердите доступ у провайдера.</p><form method="post" action="/connect/{{.View.RequestID}}/oauth"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">Перейти к авторизации</button></form>
{{else}}<p>Авторизация обрабатывается. Повторное использование кода запрещено.</p>{{end}}
<p class="foot">Ссылка ограничена по времени. Не отправляйте секреты через Telegram, Slack или обычные сообщения.</p></main></body></html>`))

const css = `body{font:16px/1.6 system-ui,sans-serif;background:#f4f5f7;color:#17212b;margin:0}main{max-width:620px;margin:48px auto;padding:32px;background:white;border:1px solid #d9e0e7;border-radius:12px}h1{font-size:28px;line-height:1.2}.eyebrow,.foot,small{color:#546476}.eyebrow{letter-spacing:.08em;font-size:12px}.code{font:700 25px monospace;padding:16px;background:#eef3f7;overflow-wrap:anywhere}label{display:block;font-weight:600;margin-top:20px}input{display:block;box-sizing:border-box;width:100%;padding:10px;margin:8px 0;border:1px solid #98a7b5;border-radius:6px}button{margin-top:24px;padding:12px 18px;border:0;border-radius:6px;background:#153a54;color:white;cursor:pointer}small{display:block;font-size:12px}.foot{font-size:13px;border-top:1px solid #ddd;margin-top:28px;padding-top:16px}a{color:#154d74}code{overflow-wrap:anywhere}@media(max-width:700px){main{margin:16px;padding:20px}}`

func (s *Server) cookieName() string {
	if s.cfg.DevHTTP {
		return "cb_dev_session"
	}
	return "__Host-cb_session"
}
func (s *Server) cookie(r *http.Request) string {
	c, e := r.Cookie(s.cookieName())
	if e != nil {
		return ""
	}
	return c.Value
}
func (s *Server) browser(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/form.css" && r.Method == "GET" {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = io.WriteString(w, css)
		return
	}
	if r.URL.Path == "/oauth/callback" && r.Method == "GET" {
		q, e := url.ParseQuery(r.URL.RawQuery)
		if e != nil || len(q["state"]) != 1 || len(q["code"]) != 1 || len(q["error"]) > 0 {
			fail(w, broker.ErrDenied)
			return
		}
		if e = s.b.CompleteOAuth(r.Context(), s.cookie(r), q.Get("state"), q.Get("code")); e != nil {
			fail(w, e)
			return
		}
		s.success(w)
		return
	}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(p) < 2 || p[0] != "connect" {
		http.NotFound(w, r)
		return
	}
	if len(p) == 2 && r.Method == "GET" {
		view, raw, e := s.b.OpenSession(p[1], s.cookie(r))
		if e != nil {
			fail(w, e)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: raw, Path: "/", HttpOnly: true, Secure: !s.cfg.DevHTTP, SameSite: http.SameSiteLaxMode, MaxAge: 900})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = formPage.Execute(w, struct {
			View v1.SessionView
			CSRF string
		}{view, s.b.CSRF(raw, p[1])})
		return
	}
	if len(p) != 3 || r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	// Host headers and CSRF tokens do not replace strict Origin validation.
	if r.Header.Get("Origin") != s.origin || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		fail(w, broker.ErrDenied)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, contract.MaxBundle+32<<10)
	if p[2] == "oauth" {
		mt, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || mt != "application/x-www-form-urlencoded" {
			fail(w, broker.ErrInvalid)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
		if e != nil || len(raw) > 4096 {
			fail(w, broker.ErrInvalid)
			return
		}
		form, e := url.ParseQuery(string(raw))
		if e != nil || len(form) != 1 || len(form["csrf"]) != 1 {
			fail(w, broker.ErrInvalid)
			return
		}
		target, e := s.b.StartOAuth(r.Context(), p[1], s.cookie(r), form.Get("csrf"))
		if e != nil {
			fail(w, e)
			return
		}
		// Cross-origin redirects are allowed only here, to the reviewed OAuth provider.
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	if p[2] != "submit" {
		http.NotFound(w, r)
		return
	}
	values, csrf, e := readForm(r)
	if values != nil {
		defer values.Wipe()
	}
	if e != nil {
		fail(w, e)
		return
	}
	if e = s.b.Submit(r.Context(), p[1], s.cookie(r), csrf, values); e != nil {
		fail(w, e)
		return
	}
	// Does not reveal credential IDs or values to a new/unapproved browser session.
	view, _, e := s.b.OpenSession(p[1], s.cookie(r))
	if e == nil && view.Status == "authorizing" {
		w.Header().Set("Location", "/connect/"+p[1])
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	s.success(w)
}
func readForm(r *http.Request) (provider.Bundle, string, error) {
	mt, params, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return nil, "", broker.ErrInvalid
	}
	// Stream parts into bounded memory, never multipart.ParseForm's disk spill path.
	rd := multipart.NewReader(r.Body, params["boundary"])
	values := provider.Bundle{}
	seen := map[string]bool{}
	csrf := ""
	total := 0
	for i := 0; ; i++ {
		part, e := rd.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil || i >= 33 {
			return values, "", broker.ErrInvalid
		}
		name := part.FormName()
		if name == "" || seen[name] || len(name) > 80 {
			_ = part.Close()
			return values, "", broker.ErrInvalid
		}
		seen[name] = true
		v, e := io.ReadAll(io.LimitReader(part, 65537))
		_ = part.Close()
		if e != nil || len(v) > 65536 {
			clear(v)
			return values, "", broker.ErrInvalid
		}
		total += len(v)
		if total > contract.MaxBundle {
			clear(v)
			return values, "", broker.ErrInvalid
		}
		if name == "csrf" {
			if len(v) > 128 {
				clear(v)
				return values, "", broker.ErrInvalid
			}
			csrf = string(v)
			clear(v)
		} else {
			values[name] = v
		}
	}
	if csrf == "" {
		return values, "", broker.ErrInvalid
	}
	return values, csrf, nil
}
func (s *Server) success(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", HttpOnly: true, Secure: !s.cfg.DevHTTP, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Готово</title><body><h1>Учётные данные приняты</h1><p>Вернитесь в чат. ToolHub может продолжить подключение автоматически. Сам MCP ещё должен пройти проверку готовности.</p></body></html>`)
}
