package hosting

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type routesFake struct{}

func (routesFake) Ensure(context.Context, string) error { return nil }
func (routesFake) Remove(context.Context, string) error { return nil }
func publishLink(t *testing.T, s *Service) Site {
	t.Helper()
	s.routes = routesFake{}
	r := request("link")
	r.Access = "link"
	out, err := s.Publish(context.Background(), s.Binding(), r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func serve(s *Service, method, host, p, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://"+host+p, strings.NewReader(body))
	req.Host = host
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if method == "POST" {
		req.Header.Set("Origin", "https://"+host)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}
func TestUnknownHostNoRead(t *testing.T) {
	s, _ := fixture(t)
	publishLink(t, s)
	w := serve(s, "GET", "bob.hub.example.test", "/", "", nil)
	if w.Code != 404 || strings.Contains(w.Body.String(), "alice-page") {
		t.Fatalf("unknown host %d", w.Code)
	}
}
// The anonymous bootstrap page negotiates Accept-Language like every other
// human surface (ADR-0084): no hardcoded English literals.
func TestShareBootstrapLocalized(t *testing.T) {
	s, _ := fixture(t)
	site := publishLink(t, s)
	u, err := url.Parse(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "https://"+u.Host+"/", nil)
	req.Header.Set("Accept-Language", "ru")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("bootstrap %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Открыть общий сайт") || strings.Contains(body, "Open shared site") {
		t.Fatalf("bootstrap not localized: %q", body[:120])
	}
}

func TestShareBootstrapAndEpoch(t *testing.T) {
	s, _ := fixture(t)
	site := publishLink(t, s)
	u, err := url.Parse(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := serve(s, "GET", u.Host, "/", "", nil)
	if bootstrap.Code != 200 || strings.Contains(bootstrap.Body.String(), "alice-page") {
		t.Fatal("app leaked before exchange")
	}
	bad := serve(s, "POST", u.Host, "/_hosting/exchange", "wrong", nil)
	if bad.Code != 404 {
		t.Fatal("bad token accepted")
	}
	exchange := serve(s, "POST", u.Host, "/_hosting/exchange", u.Fragment, nil)
	cookies := exchange.Result().Cookies()
	if exchange.Code != 204 || len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Domain != "" || cookies[0].Name != "__Host-hub-site" {
		t.Fatalf("bad exchange %d %#v", exchange.Code, cookies)
	}
	page := serve(s, "GET", u.Host, "/", "", cookies[0])
	if page.Code != 200 || page.Body.String() != "alice-page" {
		t.Fatalf("serve %d %q", page.Code, page.Body.String())
	}
	if err = s.Unpublish(context.Background(), s.Binding(), UnpublishRequest{"page", site.Revision, "remove"}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "https://"+u.Host+"/index.html", nil)
	req.AddCookie(cookies[0])
	req.Header.Set("Range", "bytes=0-2")
	req.Header.Set("If-None-Match", "anything")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("revoked range %d", w.Code)
	}
}
func TestHostingListenerSeparation(t *testing.T) {
	s, _ := fixture(t)
	publishLink(t, s)
	if w := serve(s, "POST", "page.alice.hub.example.test", "/v1/publish", "{}", nil); w.Code != 404 {
		t.Fatalf("control on data listener %d", w.Code)
	}
	handler := s.ControlHandler("abcdefghijklmnopqrstuvwx")
	req := httptest.NewRequest("POST", "http://control/v1/publish", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("control auth %d", w.Code)
	}
}
