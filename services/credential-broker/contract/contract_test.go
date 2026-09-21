package contract

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

func sample() Contract {
	return Contract{ID: "github-pat", Revision: 1, Title: "GitHub", Storage: "local", Fields: []Field{{ID: "token", Label: "Token", Kind: "secret", Required: true, MaxBytes: 4096}}, Deliveries: []Delivery{{Type: "env", Field: "token", Target: "GITHUB_TOKEN"}}}
}
func clone(c Contract) Contract {
	r, _ := json.Marshal(c)
	var out Contract
	_ = json.Unmarshal(r, &out)
	return out
}
func TestContractAdmission(t *testing.T) {
	c := sample()
	if c.Validate() != nil || c.Digest() != clone(c).Digest() {
		t.Fatal("valid contract")
	}
	cases := []struct {
		name   string
		mutate func(*Contract)
	}{
		{"id", func(c *Contract) { c.ID = "../other" }}, {"revision", func(c *Contract) { c.Revision = 0 }},
		{"title", func(c *Contract) { c.Title = "" }}, {"storage", func(c *Contract) { c.Storage = "" }},
		{"unknown field kind", func(c *Contract) { c.Fields[0].Kind = "shell-command" }},
		{"field limit", func(c *Contract) { c.Fields[0].MaxBytes = 999999 }},
		{"duplicate fields", func(c *Contract) { c.Fields = append(c.Fields, c.Fields[0]) }},
		{"field path", func(c *Contract) { c.Fields[0].ID = "/tmp/x" }},
		{"duplicate delivery", func(c *Contract) { c.Deliveries = append(c.Deliveries, c.Deliveries[0]) }},
		{"invalid env", func(c *Contract) { c.Deliveries[0].Target = "PATH=bad" }},
		{"unknown field", func(c *Contract) { c.Deliveries[0].Field = "other" }},
		{"unknown delivery", func(c *Contract) { c.Deliveries[0].Type = "argv" }},
		{"file target", func(c *Contract) { c.Deliveries[0] = Delivery{Type: "file", Field: "token", Target: "/etc/passwd"} }},
		{"json mapping absent", func(c *Contract) { c.Deliveries[0] = Delivery{Type: "json_file", Target: "/run/secrets/a.json"} }},
		{"json mapping unknown", func(c *Contract) {
			c.Deliveries[0] = Delivery{Type: "json_file", Target: "/run/secrets/a.json", Mapping: map[string]string{"token": "absent"}}
		}},
		{"state file traversal", func(c *Contract) {
			c.State = &State{Target: "/app/state", Files: []StateFile{{Name: "../token", MaxBytes: 100}}}
		}},
		{"state shared", func(c *Contract) {
			c.State = &State{Target: "/app/state", Files: []StateFile{{Name: "session.json", MaxBytes: 100}}}
			c.AllowShared = true
		}},
		{"state target overlap", func(c *Contract) {
			c.Deliveries = []Delivery{{Type: "file", Field: "token", Target: "/app/state/token"}}
			c.State = &State{Target: "/app/state", Files: []StateFile{{Name: "session.json", MaxBytes: 100}}}
		}},
		{"empty input", func(c *Contract) { c.Fields = nil; c.Deliveries = nil }},
		{"oauth invalid", func(c *Contract) { c.OAuthProvider = "../provider" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			cc := clone(c)
			tt.mutate(&cc)
			if cc.Validate() == nil {
				t.Fatal("accepted")
			}
		})
	}
	c.Deliveries = []Delivery{{Type: "file", Field: "token", Target: "/run/secrets/token", EnvName: "TOKEN_FILE"}, {Type: "json_file", Target: "/app/auth.json", Mapping: map[string]string{"api_key": "token"}}}
	c.State = &State{Target: "/app/state", Files: []StateFile{{Name: "runtime_session.json", MaxBytes: 1000, JSON: true}}}
	if c.Validate() != nil {
		t.Fatal("file/state")
	}
	c.Deliveries[0].EnvName = "bad-value"
	if c.Validate() == nil {
		t.Fatal("env name")
	}
}
func TestValues(t *testing.T) {
	c := sample()
	for _, v := range []map[string][]byte{nil, {"token": {}}, {"token": []byte("x\ny")}, {"token": []byte("x\x00y")}, {"token": []byte(strings.Repeat("a", 4097))}, {"other": []byte("x")}, {"token": []byte{0xff}}} {
		if c.ValidateValues(v) == nil {
			t.Fatal("invalid value")
		}
	}
	if c.ValidateValues(map[string][]byte{"token": []byte("fake-value")}) != nil {
		t.Fatal("valid")
	}
	c.Fields[0].Required = false
	if c.ValidateValues(nil) != nil {
		t.Fatal("optional")
	}
	c.Fields[0].Kind = "json"
	for _, s := range []string{`null`, `{"x":1,"x":2}`, `bad`} {
		if c.ValidateValues(map[string][]byte{"token": []byte(s)}) == nil {
			t.Fatal(s)
		}
	}
	if c.ValidateValues(map[string][]byte{"token": []byte(`{"a":1}`)}) != nil {
		t.Fatal("json")
	}
	c.Fields[0].Kind = "blob"
	if c.ValidateValues(map[string][]byte{"token": {0, 1}}) == nil {
		t.Fatal("env NUL")
	}
	c.Deliveries = nil
	if c.ValidateValues(map[string][]byte{"token": {0, 1}}) != nil {
		t.Fatal("blob")
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	c.Fields[0].Kind = "pem"
	if c.ValidateValues(map[string][]byte{"token": encoded}) != nil {
		t.Fatal("pem")
	}
	for _, bad := range [][]byte{[]byte("not-pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("bad")}), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), append(encoded, encoded...)} {
		if c.ValidateValues(map[string][]byte{"token": bad}) == nil {
			t.Fatal("invalid PEM")
		}
	}
	c.Fields = nil
	values := map[string][]byte{}
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		c.Fields = append(c.Fields, Field{ID: id, Label: id, Kind: "blob", MaxBytes: 65536})
		values[id] = make([]byte, 65536)
	}
	if c.ValidateValues(values) == nil {
		t.Fatal("bundle limit")
	}
}
func TestProxyContractAndCompatibility(t *testing.T) {
	c := sample()
	c.Routes = []Route{{ID: "github", BaseURL: "https://api.github.com", PathPrefixes: []string{"/repos"}, Methods: []string{"GET", "POST"}, Auth: "bearer", Field: "token"}}
	if c.Validate() != nil {
		t.Fatal("route")
	}
	r := c.Routes[0]
	if !r.Allows("GET", "/repos/a") || !r.Allows("POST", "/repos") || r.Allows("DELETE", "/repos") || r.Allows("GET", "/reposx") || r.Allows("GET", "/repos/%2e%2e/secret") {
		t.Fatal("route policy")
	}
	r.PathPrefixes = []string{"/"}
	if !r.Allows("GET", "/anything") {
		t.Fatal("root route")
	}
	mutations := []func(*Route){
		func(r *Route) { r.BaseURL = "http://api.github.com" }, func(r *Route) { r.BaseURL = "https://user:pass@api.github.com" }, func(r *Route) { r.BaseURL = "https://api.github.com?secret=a" },
		func(r *Route) { r.BaseURL = "https://api.github.com/redirect" }, func(r *Route) { r.BaseURL = "https://api.github.com." },
		func(r *Route) { r.PathPrefixes = nil }, func(r *Route) { r.PathPrefixes = []string{"/a/../b"} }, func(r *Route) { r.Methods = []string{"CONNECT"} },
		func(r *Route) { r.Auth = "anything" }, func(r *Route) { r.Auth = "oauth2" }, func(r *Route) { r.Auth = "basic"; r.PasswordField = "missing" },
		func(r *Route) { r.Auth = "header"; r.Header = "Host" }, func(r *Route) { r.Auth = "header"; r.Header = "Bad\r\nHeader" },
		func(r *Route) { r.StaticHeaders = map[string]string{"Cookie": "evil"} }, func(r *Route) { r.StaticHeaders = map[string]string{"X-Test": "a\r\nb"} },
	}
	for i, mutate := range mutations {
		cc := clone(c)
		mutate(&cc.Routes[0])
		if cc.Validate() == nil {
			t.Fatal("route mutation", i)
		}
	}
	c.Routes[0].Auth = "header"
	c.Routes[0].Header = "x-api-key"
	if c.Validate() != nil || c.Routes[0].HeaderName() != "X-Api-Key" {
		t.Fatal("header")
	}
	other := sample()
	other.ID = "other"
	other.Deliveries[0].Target = "OTHER_TOKEN"
	if !Compatible(sample(), other) {
		t.Fatal("delivery-independent compatibility")
	}
	other.Fields[0].Kind = "json"
	if Compatible(sample(), other) {
		t.Fatal("incompatible schema")
	}
	if HeaderName("") || HeaderName("a_b") || HeaderName(strings.Repeat("a", 81)) {
		t.Fatal("header token")
	}
	for _, p := range []string{"/", "../x", "/a/../b", "/proc/x", "/sys/x", "/dev/a", "/etc/a", "/usr/bin/x", "/run/x\x00"} {
		if SafeTarget(p) {
			t.Fatal(p)
		}
	}
}
func FuzzPaths(f *testing.F) {
	for _, s := range []string{"/repos/a", "/a/../b", "/%2fetc", "//evil", "/safe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 65536 {
			return
		}
		if SafeRelative(s) && strings.ContainsAny(s, "\\%\x00") {
			t.Fatal("unsafe accepted")
		}
		_ = SafeTarget(s)
	})
}
