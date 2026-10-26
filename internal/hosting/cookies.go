package hosting

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

// Namespacing prevents parent-domain credentials from reaching user code.
func appCookiePrefix(w Workload) string {
	return "__Host-site-" + digest(w.Binding.Owner + "/" + w.Binding.Environment + "/" + w.Name + "/" + strconv.FormatUint(w.Revision, 10) + "/" + w.Release)[:16] + "-"
}
func appRequestCookies(w Workload, req *http.Request) string {
	out := []string{}
	prefix := appCookiePrefix(w)
	for _, c := range req.Cookies() {
		if !strings.HasPrefix(c.Name, prefix) {
			continue
		}
		name, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(c.Name, prefix))
		c.Name = string(name)
		if err == nil && len(name) <= 80 && len(c.Value) <= 2048 && c.Name != cookieName && c.Valid() == nil {
			out = append(out, c.String())
		}
	}
	return strings.Join(out, "; ")
}
func appResponseCookie(w Workload, c *http.Cookie) *http.Cookie {
	if c.Domain != "" || c.Name == cookieName || len(c.Name) > 80 || len(c.Value) > 2048 || c.Valid() != nil {
		return nil
	}
	copy := *c // #nosec G124 -- Secure, HttpOnly and SameSite are forced below.
	copy.Name = appCookiePrefix(w) + base64.RawURLEncoding.EncodeToString([]byte(c.Name))
	copy.Path = "/"
	copy.Secure = true
	copy.HttpOnly = true
	copy.SameSite = http.SameSiteStrictMode
	return &copy
}
