//go:build telegramauth

package communication

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"html"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/letya999/hermes-hub/internal/identity"
	"rsc.io/qr"
)

const telegramAuthTTL = 10 * time.Minute

type telegramAuthState struct {
	mu       sync.Mutex
	attempts map[string]*telegramAuthAttempt
}

type telegramAuthAttempt struct {
	mu          sync.Mutex
	owner       identity.Envelope
	requestID   string
	expires     time.Time
	browser     string
	csrf        string
	status      string
	qr          []byte
	apiID       int
	apiHash     string
	session     string
	accountID   int64
	accountName string
	password    chan string
	cancel      context.CancelFunc
}

func newTelegramAuthState(config Config) (*telegramAuthState, error) {
	if !config.BrokerApprove.Enabled() || strings.TrimSpace(config.FormOrigin) == "" {
		return nil, errors.New("telegram authentication requires Broker and the local Communication Hub form origin")
	}
	u, err := url.Parse(config.FormOrigin)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, errors.New("telegram authentication origin must be local HTTP")
	}
	return &telegramAuthState{attempts: map[string]*telegramAuthAttempt{}}, nil
}

func (g *Gateway) registerTelegramAuth(mux *http.ServeMux) {
	if g.telegramAuth != nil {
		mux.HandleFunc("/telegram-auth/", g.serveTelegramAuth)
	}
}

func (g *Gateway) telegramAuthInvite(owner identity.Envelope, request prepareOutcomeRequest) string {
	if g.telegramAuth == nil || request.Phase != "awaiting-credentials" || request.ContractID != "telegram-session" || !identity.ValidID(request.BrokerRequestID) || request.BrokerRequestID == "" || owner.PrincipalID == "" {
		return ""
	}
	id, err := randomFormToken()
	if err != nil {
		return ""
	}
	csrf, err := randomFormToken()
	if err != nil {
		return ""
	}
	origin, err := g.formOrigin()
	if err != nil {
		return ""
	}
	g.telegramAuth.mu.Lock()
	defer g.telegramAuth.mu.Unlock()
	for key, attempt := range g.telegramAuth.attempts {
		if attempt.owner.PrincipalID == owner.PrincipalID && attempt.owner.ContextID == owner.ContextID && attempt.requestID == request.BrokerRequestID && g.now().Before(attempt.expires) {
			attempt.mu.Lock()
			reusable := attempt.status != "failed" && attempt.status != "done"
			attempt.mu.Unlock()
			if reusable {
				return origin + "/telegram-auth/" + key
			}
			delete(g.telegramAuth.attempts, key)
		}
		if g.now().After(attempt.expires) {
			attempt.mu.Lock()
			if attempt.cancel != nil {
				attempt.cancel()
			}
			attempt.session, attempt.apiHash = "", ""
			attempt.mu.Unlock()
			delete(g.telegramAuth.attempts, key)
		}
	}
	if len(g.telegramAuth.attempts) >= 8 {
		return ""
	}
	attempt := &telegramAuthAttempt{owner: owner, requestID: request.BrokerRequestID, expires: g.now().Add(telegramAuthTTL), csrf: csrf, status: "new"}
	g.telegramAuth.attempts[id] = attempt
	time.AfterFunc(telegramAuthTTL, func() { g.expireTelegramAuth(id, attempt) })
	return origin + "/telegram-auth/" + id
}

func (g *Gateway) expireTelegramAuth(id string, attempt *telegramAuthAttempt) {
	g.telegramAuth.mu.Lock()
	if g.telegramAuth.attempts[id] == attempt {
		delete(g.telegramAuth.attempts, id)
	}
	g.telegramAuth.mu.Unlock()
	attempt.mu.Lock()
	if attempt.cancel != nil {
		attempt.cancel()
	}
	attempt.status = "failed"
	attempt.session, attempt.apiHash, attempt.qr, attempt.accountName = "", "", nil, ""
	select {
	case <-attempt.password:
	default:
	}
	attempt.mu.Unlock()
}

func (g *Gateway) authAttempt(id string) *telegramAuthAttempt {
	if g.telegramAuth == nil || len(id) != 48 {
		return nil
	}
	g.telegramAuth.mu.Lock()
	a := g.telegramAuth.attempts[id]
	g.telegramAuth.mu.Unlock()
	if a == nil || !g.now().Before(a.expires) {
		return nil
	}
	return a
}

func (g *Gateway) serveTelegramAuth(w http.ResponseWriter, r *http.Request) {
	if !communicationLoopbackHTTP(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	path := strings.TrimPrefix(r.URL.Path, "/telegram-auth/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	a := g.authAttempt(parts[0])
	if a == nil {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	if a.browser == "" && r.Method == http.MethodGet && len(parts) == 1 {
		csrf := a.csrf
		a.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html lang="ru"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Telegram QR вход</title><main><h1>Telegram QR вход</h1><p>Продолжайте только если сами запросили подключение Telegram.</p><form method="post" action="/telegram-auth/`+parts[0]+`/claim"><input type="hidden" name="csrf" value="`+html.EscapeString(csrf)+`"><button>Начать</button></form></main></html>`)
		return
	}
	if a.browser == "" && r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "claim" {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(a.csrf)) != 1 {
			a.mu.Unlock()
			http.Error(w, "invalid form", http.StatusForbidden)
			return
		}
		browser, tokenErr := randomFormToken()
		if tokenErr != nil {
			a.mu.Unlock()
			http.Error(w, "login unavailable", http.StatusServiceUnavailable)
			return
		}
		a.browser = browser
		http.SetCookie(w, &http.Cookie{Name: "tg_auth_browser", Value: a.browser, Path: "/telegram-auth/" + parts[0], HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 600})
		a.mu.Unlock()
		http.Redirect(w, r, "/telegram-auth/"+parts[0], http.StatusSeeOther)
		return
	}
	cookie, err := r.Cookie("tg_auth_browser")
	valid := err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(a.browser)) == 1 && a.browser != ""
	a.mu.Unlock()
	if !valid {
		http.Error(w, "browser session unavailable", http.StatusForbidden)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		g.telegramAuthPage(w, parts[0], a)
		return
	}
	if len(parts) == 2 && parts[1] == "qr.png" && r.Method == http.MethodGet {
		a.mu.Lock()
		image := bytes.Clone(a.qr)
		a.mu.Unlock()
		if len(image) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(image)
		clear(image)
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	csrf := a.csrf
	a.mu.Unlock()
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(csrf)) != 1 {
		http.Error(w, "invalid form", http.StatusForbidden)
		return
	}
	switch parts[1] {
	case "start":
		g.telegramAuthStart(w, r, parts[0], a)
	case "password":
		g.telegramAuthPassword(w, r, parts[0], a)
	case "save":
		g.telegramAuthSave(w, r, parts[0], a)
	case "cancel":
		a.mu.Lock()
		if a.cancel != nil {
			a.cancel()
		}
		a.status = "failed"
		a.session, a.apiHash = "", ""
		a.mu.Unlock()
		http.Redirect(w, r, "/telegram-auth/"+parts[0], http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

func (g *Gateway) telegramAuthPage(w http.ResponseWriter, id string, a *telegramAuthAttempt) {
	a.mu.Lock()
	status, csrf, accountID, accountName := a.status, a.csrf, a.accountID, a.accountName
	a.mu.Unlock()
	if status == "connecting" || status == "qr" || status == "checking-password" || status == "saving" {
		w.Header().Set("Refresh", "3")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="ru"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Telegram QR вход</title><style>body{font:16px system-ui;margin:3rem auto;max-width:620px;padding:0 1rem;color:#17212b}input,button{display:block;margin:.75rem 0;padding:.7rem;font:inherit;max-width:100%;box-sizing:border-box}input{width:100%}img{width:min(320px,100%);image-rendering:pixelated}p{line-height:1.5}</style><main><h1>Telegram QR вход</h1>`)
	form := func(action string) {
		b.WriteString(`<form method="post" action="/telegram-auth/` + id + `/` + action + `"><input type="hidden" name="csrf" value="` + html.EscapeString(csrf) + `">`)
	}
	switch status {
	case "new":
		b.WriteString(`<p>Введите API ID и API hash от my.telegram.org/apps. Они остаются только в памяти этого процесса до завершения входа.</p>`)
		form("start")
		b.WriteString(`<label>API ID<input name="api_id" inputmode="numeric" required></label><label>API hash<input name="api_hash" type="password" autocomplete="off" required></label><button>Создать QR</button></form>`)
	case "connecting", "qr":
		b.WriteString(`<p>Откройте Telegram → Настройки → Устройства → Подключить устройство и отсканируйте QR.</p>`)
		if status == "qr" {
			b.WriteString(`<img alt="QR код входа Telegram" src="/telegram-auth/` + id + `/qr.png">`)
		}
		form("cancel")
		b.WriteString(`<button>Отменить</button></form>`)
	case "password":
		b.WriteString(`<p>Telegram запросил пароль двухэтапной проверки.</p>`)
		form("password")
		b.WriteString(`<label>Пароль Telegram<input name="password" type="password" autocomplete="current-password" required></label><button>Продолжить</button></form>`)
	case "ready":
		b.WriteString(`<p>Вход выполнен в аккаунт ` + html.EscapeString(accountName) + ` (ID ` + strconv.FormatInt(accountID, 10) + `). Подтвердите передачу новой сессии в Credential Broker.</p>`)
		form("save")
		b.WriteString(`<button>Подключить аккаунт</button></form>`)
	case "checking-password":
		b.WriteString(`<p>Проверяю пароль…</p>`)
	case "saving":
		b.WriteString(`<p>Передаю сессию в Broker…</p>`)
	case "done":
		b.WriteString(`<p>Сессия передана. Вернитесь в чат и напишите боту «продолжи подключение Telegram».</p>`)
	default:
		b.WriteString(`<p>Вход не завершён. Запросите новую ссылку подключения.</p>`)
	}
	b.WriteString(`</main></html>`)
	_, _ = io.WriteString(w, b.String())
}

func (g *Gateway) telegramAuthStart(w http.ResponseWriter, r *http.Request, id string, a *telegramAuthAttempt) {
	apiID, err := strconv.Atoi(r.PostForm.Get("api_id"))
	apiHash := strings.TrimSpace(r.PostForm.Get("api_hash"))
	if err != nil || apiID <= 0 || apiID >= 1<<31 || len(apiHash) != 32 || !allHex(apiHash) {
		http.Error(w, "invalid Telegram API credentials", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	if a.status != "new" {
		a.mu.Unlock()
		http.Error(w, "login already started", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), a.expires)
	a.apiID, a.apiHash, a.status, a.cancel, a.password = apiID, apiHash, "connecting", cancel, make(chan string, 1)
	a.mu.Unlock()
	go g.runTelegramAuth(ctx, a)
	http.Redirect(w, r, "/telegram-auth/"+id, http.StatusSeeOther)
}

func allHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

func (g *Gateway) telegramAuthPassword(w http.ResponseWriter, r *http.Request, id string, a *telegramAuthAttempt) {
	password := r.PostForm.Get("password")
	if password == "" || len(password) > 1024 {
		http.Error(w, "invalid password", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	if a.status != "password" {
		a.mu.Unlock()
		http.Error(w, "password not expected", http.StatusConflict)
		return
	}
	a.status = "checking-password"
	select {
	case a.password <- password:
	default:
	}
	a.mu.Unlock()
	http.Redirect(w, r, "/telegram-auth/"+id, http.StatusSeeOther)
}

func (g *Gateway) telegramAuthSave(w http.ResponseWriter, r *http.Request, id string, a *telegramAuthAttempt) {
	a.mu.Lock()
	if a.status != "ready" {
		a.mu.Unlock()
		http.Error(w, "session not ready", http.StatusConflict)
		return
	}
	a.status = "saving"
	values := map[string]string{"api_id": strconv.Itoa(a.apiID), "api_hash": a.apiHash, "session": a.session, "expected_user_id": strconv.FormatInt(a.accountID, 10)}
	owner, requestID := a.owner, a.requestID
	a.session, a.apiHash = "", ""
	a.mu.Unlock()
	defer func() {
		for k := range values {
			values[k] = ""
		}
	}()
	client, err := g.config.BrokerApprove.New(owner, "broker:approve")
	if err == nil {
		err = client.Do(r.Context(), http.MethodPost, "/v1/requests/"+url.PathEscape(requestID)+"/telegram-submit", values, nil)
	}
	a.mu.Lock()
	if err == nil {
		a.status = "done"
	} else {
		a.status = "failed"
	}
	a.mu.Unlock()
	http.Redirect(w, r, "/telegram-auth/"+id, http.StatusSeeOther)
}

func (g *Gateway) runTelegramAuth(ctx context.Context, a *telegramAuthAttempt) {
	defer func() {
		a.mu.Lock()
		if a.status != "ready" && a.status != "saving" && a.status != "done" {
			a.status = "failed"
			a.apiHash = ""
		}
		a.qr = nil
		a.mu.Unlock()
	}()
	a.mu.Lock()
	apiID, apiHash := a.apiID, a.apiHash
	a.mu.Unlock()
	storage := &session.StorageMemory{}
	dispatcher := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(dispatcher)
	client := telegram.NewClient(apiID, apiHash, telegram.Options{SessionStorage: storage, UpdateHandler: dispatcher})
	var accountID int64
	var accountName string
	err := client.Run(ctx, func(ctx context.Context) error {
		_, err := client.QR().Auth(ctx, loggedIn, func(_ context.Context, token qrlogin.Token) error {
			im, err := token.Image(qr.M)
			if err != nil {
				return err
			}
			var b bytes.Buffer
			if err = png.Encode(&b, im); err != nil {
				return err
			}
			a.mu.Lock()
			a.qr = bytes.Clone(b.Bytes())
			a.status = "qr"
			a.mu.Unlock()
			return nil
		})
		if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
			for tries := 0; tries < 3; tries++ {
				a.mu.Lock()
				a.status = "password"
				a.qr = nil
				a.mu.Unlock()
				var password string
				select {
				case password = <-a.password:
				case <-ctx.Done():
					return ctx.Err()
				}
				_, err = client.Auth().Password(ctx, password)
				password = ""
				if err == nil {
					break
				}
				if !tgerr.Is(err, "PASSWORD_HASH_INVALID") {
					return err
				}
			}
		}
		if err != nil {
			return err
		}
		me, err := client.Self(ctx)
		if err != nil || me == nil || me.Bot {
			return errors.New("personal Telegram account required")
		}
		accountID = me.ID
		accountName = strings.TrimSpace(me.FirstName + " " + me.LastName)
		if me.Username != "" {
			accountName += " @" + me.Username
		}
		accountName = strings.TrimSpace(accountName)
		if accountName == "" {
			accountName = "Telegram"
		}
		return nil
	})
	if err != nil {
		return
	}
	data, err := (&session.Loader{Storage: storage}).Load(context.Background())
	if err != nil {
		return
	}
	defer clear(data.AuthKey)
	stringSession, err := telethonStringSession(data)
	if err != nil {
		return
	}
	a.mu.Lock()
	if a.status != "failed" && ctx.Err() == nil {
		a.session, a.accountID, a.accountName, a.status = stringSession, accountID, accountName, "ready"
	}
	a.mu.Unlock()
}

func telethonStringSession(data *session.Data) (string, error) {
	if data == nil || data.DC < 1 || data.DC > 255 || len(data.AuthKey) != 256 {
		return "", errors.New("invalid MTProto session")
	}
	if data.Config.TestMode {
		return "", errors.New("telegram test DC session is not supported")
	}
	address := data.Addr
	if address == "" {
		for _, option := range data.Config.DCOptions {
			if option.ID == data.DC && !option.MediaOnly && !option.CDN && !option.TCPObfuscatedOnly && !option.Ipv6 && net.ParseIP(option.IPAddress) != nil && option.Port > 0 {
				address = net.JoinHostPort(option.IPAddress, strconv.Itoa(option.Port))
				break
			}
		}
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", errors.New("invalid MTProto address")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", errors.New("MTProto address must be an IP")
	}
	packed := ip.To4()
	if packed == nil {
		packed = ip.To16()
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("invalid MTProto port")
	}
	buf := make([]byte, 1+len(packed)+2+256)
	buf[0] = byte(data.DC)
	copy(buf[1:], packed)
	binary.BigEndian.PutUint16(buf[1+len(packed):], uint16(port))
	copy(buf[3+len(packed):], data.AuthKey)
	out := "1" + base64.URLEncoding.EncodeToString(buf)
	clear(buf)
	return out, nil
}
